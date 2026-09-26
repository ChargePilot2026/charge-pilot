//! Device onboarding is distinct from a physical device opening a session.
use crate::AppState;
use api_contracts::devices::{
    DeviceProvisionBatch, DeviceProvisionResult, ProvisionedDevice, ProvisionedPort,
};
use axum::{extract::State, Json};
use common_error::{ApiEnvelope, AppError, AppResult};
use sqlx::Row;

pub async fn provision(
    State(state): State<AppState>,
    Json(mut batch): Json<DeviceProvisionBatch>,
) -> AppResult<Json<ApiEnvelope<DeviceProvisionResult>>> {
    batch.validate().map_err(AppError::BadRequest)?;
    // All requests acquire reservation locks in the same order.
    batch
        .devices
        .sort_by_key(|d| d.device_id.to_ascii_lowercase());
    let mut tx = state.db.pool().begin().await?;
    let mut items = Vec::with_capacity(batch.devices.len());
    for device in &batch.devices {
        let request = serde_json::to_value(device)?;
        sqlx::query("INSERT INTO device_provision (device_id,request_json) VALUES (?,?) ON DUPLICATE KEY UPDATE device_id=device_provision.device_id")
            .bind(&device.device_id).bind(&request).execute(&mut *tx).await?;
        let stored: serde_json::Value = sqlx::query_scalar(
            "SELECT request_json FROM device_provision WHERE device_id=? FOR UPDATE",
        )
        .bind(&device.device_id)
        .fetch_one(&mut *tx)
        .await?;
        if stored != request {
            return Err(AppError::Conflict(format!(
                "设备 {} 已使用不同参数导入",
                device.device_id
            )));
        }
        let vendor: Option<u64> = sqlx::query_scalar(
            "SELECT id FROM vendor WHERE id=? AND status='enabled' AND deleted_at IS NULL",
        )
        .bind(device.vendor_id)
        .fetch_optional(&mut *tx)
        .await?;
        if vendor.is_none() {
            return Err(AppError::BadRequest(format!(
                "设备 {} 的厂商不存在或未启用",
                device.device_id
            )));
        }
        let rows = sqlx::query("SELECT vendor_id,station_id,port_count,model,status,deleted_at FROM device WHERE device_id=? FOR UPDATE")
            .bind(&device.device_id).fetch_all(&mut *tx).await?;
        let created = rows.is_empty();
        if !created {
            if rows.len() != 1 {
                return Err(AppError::Conflict(format!(
                    "设备 {} 存在重复记录，需先清理",
                    device.device_id
                )));
            }
            let row = &rows[0];
            if row.try_get::<u64, _>("vendor_id")? != device.vendor_id
                || row.try_get::<Option<u64>, _>("station_id")? != Some(device.station_id)
                || row.try_get::<u8, _>("port_count")? != device.port_count
                || row.try_get::<Option<String>, _>("model")? != device.model
                || row.try_get::<String, _>("status")? != "enabled"
                || row
                    .try_get::<Option<chrono::NaiveDateTime>, _>("deleted_at")?
                    .is_some()
            {
                return Err(AppError::Conflict(format!(
                    "设备 {} 已存在且配置不同或已停用",
                    device.device_id
                )));
            }
        } else {
            sqlx::query("INSERT INTO device (device_id,vendor_id,station_id,port_count,model) VALUES (?,?,?,?,?)")
                .bind(&device.device_id).bind(device.vendor_id).bind(device.station_id).bind(device.port_count).bind(&device.model)
                .execute(&mut *tx).await?;
            for port_no in 1..=device.port_count {
                let port_code = format!("{}:{port_no}", device.device_id);
                let existing: i64 = sqlx::query_scalar(
                    "SELECT COUNT(*) FROM device_port WHERE port_code=? AND deleted_at IS NULL",
                )
                .bind(&port_code)
                .fetch_one(&mut *tx)
                .await?;
                if existing > 0 {
                    return Err(AppError::Conflict(format!("端口码 {port_code} 已存在")));
                }
                sqlx::query("INSERT INTO device_port (device_id,port_no,port_code) VALUES (?,?,?)")
                    .bind(&device.device_id)
                    .bind(port_no)
                    .bind(port_code)
                    .execute(&mut *tx)
                    .await?;
            }
        }
        let rows = sqlx::query("SELECT id,port_no,port_code FROM device_port WHERE device_id=? AND deleted_at IS NULL ORDER BY port_no,id")
            .bind(&device.device_id).fetch_all(&mut *tx).await?;
        if rows.len() != usize::from(device.port_count) {
            return Err(AppError::Conflict(format!(
                "设备 {} 的端口记录不完整",
                device.device_id
            )));
        }
        let mut ports = Vec::with_capacity(rows.len());
        for (index, row) in rows.iter().enumerate() {
            let port_no: u8 = row.try_get("port_no")?;
            if usize::from(port_no) != index + 1 {
                return Err(AppError::Conflict("设备端口编号重复或缺失".into()));
            }
            ports.push(ProvisionedPort {
                port_id: row.try_get("id")?,
                port_no,
                port_code: row.try_get("port_code")?,
            });
        }
        items.push(ProvisionedDevice {
            device_id: device.device_id.clone(),
            created,
            ports,
        });
    }
    tx.commit().await?;
    Ok(Json(ApiEnvelope::ok(
        DeviceProvisionResult { items },
        common_error::current_request_id(),
    )))
}
