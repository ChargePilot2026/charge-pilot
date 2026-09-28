//! Device onboarding is distinct from a physical device opening a session.
//!
//! P5:全部 SQL 已下沉到 `DeviceService::provision`(设备域 repository 层),
//! 本文件只做入参校验、锁顺序编排与响应包装 —— handler/usecase 层不碰 SQL。
use crate::AppState;
use api_contracts::devices::{DeviceProvisionBatch, DeviceProvisionResult};
use axum::{extract::State, Json};
use common_error::{ApiEnvelope, AppError, AppResult};

pub async fn provision(
    State(state): State<AppState>,
    Json(mut batch): Json<DeviceProvisionBatch>,
) -> AppResult<Json<ApiEnvelope<DeviceProvisionResult>>> {
    batch.validate().map_err(AppError::BadRequest)?;
    // All requests acquire reservation locks in the same order.
    batch
        .devices
        .sort_by_key(|d| d.device_id.to_ascii_lowercase());
    let items = state.device.provision(&batch.devices).await?;
    Ok(Json(ApiEnvelope::ok(
        DeviceProvisionResult { items },
        common_error::current_request_id(),
    )))
}
