//! Read-only scan resolution. Scanning never reserves a port or creates an order.
//!
//! P3:SQL 已下沉到 `DeviceService`,本文件只留入参校验与编排。
use crate::AppState;
use api_contracts::{ScanPortDetail, ScanPortRequest, ScanResolveRequest, ScanResolveResponse};
use axum::{extract::State, Json};
use common_error::{ApiEnvelope, AppError, AppResult};

fn validate(code: &str) -> AppResult<()> {
    if code.is_empty() || code.len() > 64 || code.chars().any(|c| c.is_control() || c.is_whitespace()) {
        return Err(AppError::BadRequest("二维码内容无效".into()));
    }
    Ok(())
}

pub async fn scan_resolve(State(st): State<AppState>, Json(req): Json<ScanResolveRequest>)
    -> AppResult<Json<ApiEnvelope<ScanResolveResponse>>> {
    validate(&req.code)?;
    let data = st.device.scan_resolve(&req.code).await?;
    Ok(Json(ApiEnvelope::ok(data, common_error::current_request_id())))
}

pub async fn scan_port(State(st): State<AppState>, Json(req): Json<ScanPortRequest>)
    -> AppResult<Json<ApiEnvelope<ScanPortDetail>>> {
    validate(&req.port_id)?;
    let data = st.device.scan_port(&req.port_id).await?;
    Ok(Json(ApiEnvelope::ok(data, common_error::current_request_id())))
}
