//! admin OTA 固件管理

use crate::AppState;
use axum::{extract::{Path, State}, Json};
use common_auth::AdminClaims;
use common_db::IdGen;
use common_error::{AppError, AppResult};
use common_redis::StreamEnvelope;
use serde::Deserialize;
use serde_json::{json, Value};

pub async fn packages_list(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query(
        "SELECT id, code, vendor_id, version, size_bytes, checksum_sha256, status, created_at
         FROM ota_package WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "code": sqlx::Row::try_get::<String, _>(r, "code").unwrap_or_default(),
        "version": sqlx::Row::try_get::<String, _>(r, "version").unwrap_or_default(),
        "size_bytes": sqlx::Row::try_get::<u64, _>(r, "size_bytes").unwrap_or(0),
        "status": sqlx::Row::try_get::<String, _>(r, "status").unwrap_or_default(),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
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

pub async fn packages_create(State(st): State<AppState>, _c: AdminClaims, Json(req): Json<PackageCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let id: u64 = sqlx::query_scalar(
        "INSERT INTO ota_package (code, vendor_id, version, storage_url, size_bytes, checksum_sha256, sign, release_notes, status)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'draft')"
    )
    .bind(&req.code).bind(req.vendor_id).bind(&req.version).bind(&req.storage_url)
    .bind(req.size_bytes).bind(&req.checksum_sha256).bind(req.sign.as_deref()).bind(req.release_notes.as_deref())
    .fetch_one(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": id}), common_error::current_request_id())))
}

pub async fn packages_get(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r: Option<(u64, String, String, String, String, u64)> = sqlx::query_as(
        "SELECT id, code, version, storage_url, checksum_sha256, size_bytes FROM ota_package WHERE id = ? AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("ota package".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "id": r.0, "code": r.1, "version": r.2, "storage_url": r.3, "checksum_sha256": r.4, "size_bytes": r.5,
    }), common_error::current_request_id())))
}

pub async fn packages_delete(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let n = sqlx::query("UPDATE ota_package SET deleted_at = NOW(3) WHERE id = ? AND deleted_at IS NULL")
        .bind(id).execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("ota package".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"deleted": true}), common_error::current_request_id())))
}

pub async fn schedules_list(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query(
        "SELECT id, package_id, rollout_strategy, batch_size, status, scheduled_at, started_at, completed_at, created_at
         FROM ota_schedule ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "package_id": sqlx::Row::try_get::<u64, _>(r, "package_id").unwrap_or(0),
        "rollout_strategy": sqlx::Row::try_get::<String, _>(r, "rollout_strategy").unwrap_or_default(),
        "status": sqlx::Row::try_get::<String, _>(r, "status").unwrap_or_default(),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct ScheduleCreateReq {
    pub package_id: u64,
    pub rollout_strategy: String,
    pub batch_size: Option<u32>,
    pub target_filter_json: Option<Value>,
    pub scheduled_at: Option<chrono::DateTime<chrono::Utc>>,
}

pub async fn schedules_create(State(st): State<AppState>, c: AdminClaims, Json(req): Json<ScheduleCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let id: u64 = sqlx::query_scalar(
        "INSERT INTO ota_schedule (package_id, target_filter_json, rollout_strategy, batch_size, status, scheduled_at, created_by)
         VALUES (?, ?, ?, ?, 'pending', ?, ?)"
    )
    .bind(req.package_id).bind(req.target_filter_json).bind(&req.rollout_strategy).bind(req.batch_size)
    .bind(req.scheduled_at).bind(c.admin_user_id)
    .fetch_one(st.db.pool()).await?;

    // 发 ota_schedule_stream 通知 worker 调度
    let env = StreamEnvelope::new("ota_scheduled", "admin", json!({"schedule_id": id, "package_id": req.package_id}));
    let _ = st.redis_stream.xadd_envelope(common_redis::streams::OTA_SCHEDULE, &env).await;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": id}), common_error::current_request_id())))
}

pub async fn schedules_get(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r: Option<(u64, u64, String, String)> = sqlx::query_as(
        "SELECT id, package_id, rollout_strategy, status FROM ota_schedule WHERE id = ?"
    ).bind(id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("ota schedule".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "id": r.0, "package_id": r.1, "rollout_strategy": r.2, "status": r.3,
    }), common_error::current_request_id())))
}

pub async fn schedules_trigger(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let env = StreamEnvelope::new("ota_schedule_trigger", "admin", json!({"schedule_id": id, "idempotency_key": IdGen::new("TRI").next()}));
    let _ = st.redis_stream.xadd_envelope(common_redis::streams::OTA_SCHEDULE, &env).await;
    sqlx::query("UPDATE ota_schedule SET status = 'running', started_at = NOW(3) WHERE id = ? AND status = 'pending'")
        .bind(id).execute(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"triggered": true}), common_error::current_request_id())))
}