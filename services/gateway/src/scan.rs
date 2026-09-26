//! Read-only scan resolution. Scanning never reserves a port or creates an order.
use crate::AppState;
use api_contracts::{ScanPortDetail, ScanPortRequest, ScanResolveRequest, ScanResolveResponse};
use axum::{extract::State, Json};
use common_error::{ApiEnvelope, AppError, AppResult};
use sqlx::{MySql, Row, Transaction};

fn validate(code: &str) -> AppResult<()> {
    if code.is_empty() || code.len() > 64 || code.chars().any(|c| c.is_control() || c.is_whitespace()) {
        return Err(AppError::BadRequest("二维码内容无效".into()));
    }
    Ok(())
}

async fn ports(tx: &mut Transaction<'_, MySql>, code: &str, by_device: bool) -> AppResult<Vec<ScanPortDetail>> {
    let filter = if by_device { "p.device_id" } else { "p.port_code" };
    let rows = sqlx::query(&format!(
        "SELECT p.device_id,p.port_no,p.port_code,IF(p.status='idle' AND p.current_order_id IS NOT NULL,'reserved',p.status) AS status FROM device_port p \
         WHERE {filter}=? AND p.deleted_at IS NULL AND EXISTS \
         (SELECT 1 FROM device d JOIN vendor v ON v.id=d.vendor_id \
          WHERE d.device_id=p.device_id AND d.deleted_at IS NULL AND d.status='enabled' \
          AND v.deleted_at IS NULL AND v.status='enabled') ORDER BY p.port_no,p.id"
    )).bind(code).fetch_all(&mut **tx).await?;
    if !by_device && rows.len() > 1 {
        return Err(AppError::Conflict("端口数据重复，请联系运营人员".into()));
    }
    let mut result = Vec::with_capacity(rows.len());
    for row in rows {
        let port_code: String = row.try_get("port_code")?;
        let port_no = row.try_get("port_no")?;
        if result.iter().any(|p: &ScanPortDetail| p.port_code == port_code || (by_device && p.port_no == port_no)) {
            return Err(AppError::Conflict("端口数据重复，请联系运营人员".into()));
        }
        result.push(ScanPortDetail { port_id: port_code.clone(), port_code,
            device_id: row.try_get("device_id")?, port_no, status: row.try_get("status")? });
    }
    Ok(result)
}

pub async fn scan_resolve(State(st): State<AppState>, Json(req): Json<ScanResolveRequest>)
    -> AppResult<Json<ApiEnvelope<ScanResolveResponse>>> {
    validate(&req.code)?;
    let mut tx = st.db.pool().begin().await?;
    let mut found = ports(&mut tx, &req.code, false).await?;
    let data = if let Some(port) = found.pop() {
        ScanResolveResponse::Port { port }
    } else {
        let devices: Vec<(String, String)> = sqlx::query_as(
            "SELECT d.device_id,d.status FROM device d JOIN vendor v ON v.id=d.vendor_id \
             WHERE d.device_id=? AND d.deleted_at IS NULL AND d.status='enabled' \
             AND v.deleted_at IS NULL AND v.status='enabled'"
        ).bind(&req.code).fetch_all(&mut *tx).await?;
        if devices.len() > 1 { return Err(AppError::Conflict("设备数据重复，请联系运营人员".into())); }
        let (device_id, status) = devices.into_iter().next().ok_or_else(|| AppError::NotFound("设备或端口不存在或已停用".into()))?;
        let ports = ports(&mut tx, &device_id, true).await?;
        ScanResolveResponse::Device { device_id, status, ports }
    };
    tx.commit().await?;
    Ok(Json(ApiEnvelope::ok(data, common_error::current_request_id())))
}

pub async fn scan_port(State(st): State<AppState>, Json(req): Json<ScanPortRequest>)
    -> AppResult<Json<ApiEnvelope<ScanPortDetail>>> {
    validate(&req.port_id)?;
    let mut tx = st.db.pool().begin().await?;
    let port = ports(&mut tx, &req.port_id, false).await?.pop()
        .ok_or_else(|| AppError::NotFound("端口不存在或设备已停用".into()))?;
    tx.commit().await?;
    Ok(Json(ApiEnvelope::ok(port, common_error::current_request_id())))
}
