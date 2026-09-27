//! admin Webhook 订阅 + 投递日志

use crate::AppState;
use axum::{extract::{Path, State}, Json};
use common_auth::AdminClaims;
use common_error::{AppError, AppResult};
use common_redis::StreamEnvelope;
use serde::Deserialize;
use serde_json::{json, Value};

pub async fn list(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query(
        "SELECT id, name, url, secret, event_types, enabled, created_at FROM webhook_subscription WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| -> AppResult<Value> { Ok(json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id")?,
        "name": sqlx::Row::try_get::<String, _>(r, "name")?,
        "url": sqlx::Row::try_get::<String, _>(r, "url")?,
        "secret_prefix": sqlx::Row::try_get::<String, _>(r, "secret")?.chars().take(8).collect::<String>(),
        "event_types": sqlx::Row::try_get::<serde_json::Value, _>(r, "event_types")?,
        "enabled": sqlx::Row::try_get::<i8, _>(r, "enabled")? != 0,
    })) }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct WebhookCreateReq {
    pub name: String,
    pub url: String,
    pub event_types: Vec<String>,
    pub headers_json: Option<Value>,
}

pub async fn create(State(st): State<AppState>, _c: AdminClaims, Json(req): Json<WebhookCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let secret = format!("whsec_{}", uuid::Uuid::new_v4().simple());
    let event_types = serde_json::to_value(&req.event_types)?;
    let result = sqlx::query(
        "INSERT INTO webhook_subscription (name, url, secret, event_types, headers_json, enabled) VALUES (?, ?, ?, ?, ?, 1)"
    )
    .bind(&req.name).bind(&req.url).bind(&secret).bind(event_types).bind(req.headers_json)
    .execute(st.db.pool()).await?;
    let id = result.last_insert_id();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": id, "secret": secret}), common_error::current_request_id())))
}

pub async fn get(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r: Option<(u64, String, String, String, serde_json::Value)> = sqlx::query_as(
        "SELECT id, name, url, secret, event_types FROM webhook_subscription WHERE id = ? AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("webhook".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "id": r.0, "name": r.1, "url": r.2,
        "secret_prefix": r.3.chars().take(8).collect::<String>(), "event_types": r.4,
    }), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct WebhookUpdateReq {
    pub name: Option<String>,
    pub url: Option<String>,
    pub event_types: Option<Vec<String>>,
    pub enabled: Option<bool>,
}

pub async fn update(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>, Json(req): Json<WebhookUpdateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let event_types = req.event_types.as_ref().map(serde_json::to_value).transpose()?;
    let n = sqlx::query(
        "UPDATE webhook_subscription
         SET name = COALESCE(?, name), url = COALESCE(?, url),
             event_types = COALESCE(?, event_types), enabled = COALESCE(?, enabled)
         WHERE id = ? AND deleted_at IS NULL"
    )
    .bind(req.name.as_deref()).bind(req.url.as_deref()).bind(event_types).bind(req.enabled).bind(id)
    .execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("webhook".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"updated": true}), common_error::current_request_id())))
}

pub async fn delete(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let n = sqlx::query("UPDATE webhook_subscription SET deleted_at = NOW(3) WHERE id = ? AND deleted_at IS NULL")
        .bind(id).execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("webhook".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"deleted": true}), common_error::current_request_id())))
}

pub async fn deliveries(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query(
        "SELECT id, event_type, response_status, attempt_count, duration_ms, delivered_at
         FROM webhook_delivery_log WHERE subscription_id = ? ORDER BY id DESC LIMIT 100"
    ).bind(id).fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| -> AppResult<Value> { Ok(json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id")?,
        "event_type": sqlx::Row::try_get::<String, _>(r, "event_type")?,
        "response_status": sqlx::Row::try_get::<Option<i32>, _>(r, "response_status")?,
        "attempt_count": sqlx::Row::try_get::<u32, _>(r, "attempt_count")?,
        "duration_ms": sqlx::Row::try_get::<Option<u32>, _>(r, "duration_ms")?,
        "delivered_at": sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(r, "delivered_at")?.map(|t| t.to_rfc3339()),
    })) }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[allow(dead_code)]
fn _trigger_via_stream(env: StreamEnvelope) {
    let _ = env;
}
