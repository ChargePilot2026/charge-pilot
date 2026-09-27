//! admin OTA 固件管理

use crate::AppState;
use axum::{extract::{Path, State}, Json};
use crate::auth::ActiveAdmin;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use serde_json::{json, Value};

pub async fn packages_list(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::OtaPackage>>> >{
    let rows = sqlx::query(
        "SELECT id, code, vendor_id, version, size_bytes, checksum_sha256, status, created_at
         FROM ota_package WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.device.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::OtaPackage> {
        Ok(api_contracts::admin::OtaPackage {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            code: sqlx::Row::try_get::<String, _>(r, "code")?,
            vendor_id: sqlx::Row::try_get::<Option<u64>, _>(r, "vendor_id")?,
            version: sqlx::Row::try_get::<String, _>(r, "version")?,
            size_bytes: sqlx::Row::try_get::<u64, _>(r, "size_bytes")?,
            checksum_sha256: sqlx::Row::try_get::<String, _>(r, "checksum_sha256")?,
            status: sqlx::Row::try_get::<String, _>(r, "status")?,
            created_at: sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "created_at")?.to_rfc3339(),
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::common::ListResponse::new(items),
        common_error::current_request_id(),
    )))
}

#[derive(Debug, Deserialize)]
pub struct PackageCreateReq {
    pub code: String,
    pub vendor_id: Option<u64>,
    pub version: String,
    pub storage_url: String,
    pub size_bytes: u64,
    pub checksum_sha256: String,
    pub sign: Option<String>,
    pub release_notes: Option<String>,
}

pub async fn packages_create(State(st): State<AppState>, _c: ActiveAdmin, Json(req): Json<PackageCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::CreatedResponse>>> {
    crate::auth::require_permission(&st,&_c,"ota.package.create").await?;
    let result = sqlx::query(
        "INSERT INTO ota_package (code, vendor_id, version, storage_url, size_bytes, checksum_sha256, sign, release_notes, status)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'draft')"
    )
    .bind(&req.code).bind(req.vendor_id).bind(&req.version).bind(&req.storage_url)
    .bind(req.size_bytes).bind(&req.checksum_sha256).bind(req.sign.as_deref()).bind(req.release_notes.as_deref())
    .execute(st.device.pool()).await?;
    let id = result.last_insert_id();
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::CreatedResponse { id: id }, common_error::current_request_id())))
}

pub async fn packages_get(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::OtaPackageDetail>>> {
    let r: Option<(u64, String, String, String, String, u64)> = sqlx::query_as(
        "SELECT id, code, version, storage_url, checksum_sha256, size_bytes FROM ota_package WHERE id = ? AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.device.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("ota package".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::admin::OtaPackageDetail {
            id: r.0, code: r.1, version: r.2, storage_url: r.3, checksum_sha256: r.4, size_bytes: r.5,
        },
        common_error::current_request_id(),
    )))
}

pub async fn packages_delete(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::DeletedResponse>>> {
    crate::auth::require_permission(&st,&_c,"ota.package.delete").await?;
    let n = sqlx::query("UPDATE ota_package SET deleted_at = NOW(3) WHERE id = ? AND deleted_at IS NULL")
        .bind(id).execute(st.device.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("ota package".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::DeletedResponse::new(), common_error::current_request_id())))
}

pub async fn schedules_list(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::OtaSchedule>>> >{
    let rows = sqlx::query(
        "SELECT id, package_id, rollout_strategy, batch_size, status, scheduled_at, started_at, completed_at, created_at
         FROM ota_schedule ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.device.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::OtaSchedule> {
        Ok(api_contracts::admin::OtaSchedule {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            package_id: sqlx::Row::try_get::<u64, _>(r, "package_id")?,
            rollout_strategy: sqlx::Row::try_get::<String, _>(r, "rollout_strategy")?,
            batch_size: sqlx::Row::try_get::<Option<u32>, _>(r, "batch_size")?,
            status: sqlx::Row::try_get::<String, _>(r, "status")?,
            scheduled_at: sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(r, "scheduled_at")?.map(|t| t.to_rfc3339()),
            started_at: sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(r, "started_at")?.map(|t| t.to_rfc3339()),
            completed_at: sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(r, "completed_at")?.map(|t| t.to_rfc3339()),
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::common::ListResponse::new(items),
        common_error::current_request_id(),
    )))
}

#[derive(Debug, Deserialize)]
pub struct ScheduleCreateReq {
    pub package_id: u64,
    pub rollout_strategy: String,
    pub batch_size: Option<u32>,
    pub target_filter_json: Option<Value>,
    pub scheduled_at: Option<chrono::DateTime<chrono::Utc>>,
}

pub async fn schedules_create(State(st): State<AppState>, c: ActiveAdmin, Json(req): Json<ScheduleCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::gateway_devices::NotImplementedResponse>>> {
    crate::auth::require_permission(&st,&c,"ota.schedule.create").await?;
    let _ = (st, c, req);
    Err(AppError::ServiceUnavailable(
        "OTA 调度器尚未接入设备筛选、传输与 ACK 确认，未创建调度".into(),
    ))
}

pub async fn schedules_get(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::OtaScheduleDetail>>> {
    let r: Option<(u64, u64, String, String)> = sqlx::query_as(
        "SELECT id, package_id, rollout_strategy, status FROM ota_schedule WHERE id = ?"
    ).bind(id).fetch_optional(st.device.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("ota schedule".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::admin::OtaScheduleDetail {
            id: r.0, package_id: r.1, rollout_strategy: r.2, status: r.3,
        },
        common_error::current_request_id(),
    )))
}

pub async fn schedules_trigger(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::gateway_devices::NotImplementedResponse>>> {
    crate::auth::require_permission(&st,&_c,"ota.schedule.trigger").await?;
    let _ = (st, id);
    Err(AppError::ServiceUnavailable(
        "OTA 调度器尚未接入设备筛选、传输与 ACK 确认，未触发升级".into(),
    ))
}
