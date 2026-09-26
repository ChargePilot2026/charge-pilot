//! admin Webhook 订阅 + 投递日志

use crate::AppState;
use axum::{extract::{Path, State}, Json};
use common_auth::AdminClaims;
use common_db::IdGen;
use common_error::{AppError, AppResult};
use common_redis::StreamEnvelope;
use serde::Deserialize;
use serde_json::{json, Value};

pub async fn list(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query(
        "SELECT id, name, url, event_types, enabled, created_at FROM webhook_subscription WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "name": sqlx::Row::try_get::<String, _>(r, "name").unwrap_or_default(),
        "url": sqlx::Row::try_get::<String, _>(r, "url").unwrap_or_default(),
        "event_types": sqlx::Row::try_get::<serde_json::Value, _>(r, "event_types").ok(),
        "enabled": sqlx::Row::try_get::<i8, _>(r, "enabled").unwrap_or(1) != 0,
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct WebhookCreateReq {
    pub name: String,
    pub url: String,
    pub secret: Option<String>,
    pub event_types: Vec<String>,
    pub headers_json: Option<Value>,
}

pub async fn create(State(st): State<AppState>, _c: AdminClaims, Json(req): Json<WebhookCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let secret = req.secret.unwrap_or_else(|| IdGen::new("WHK").next());
    let event_types = serde_json::to_value(&req.event_types).unwrap_or(serde_json::Value::Null);
    let id: u64 = sqlx::query_scalar(
        "INSERT INTO webhook_subscription (name, url, secret, event_types, headers_json, enabled) VALUES (?, ?, ?, ?, ?, 1)"
    )
    .bind(&req.name).bind(&req.url).bind(&secret).bind(event_types).bind(req.headers_json)
    .fetch_one(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": id, "secret": secret}), common_error::current_request_id())))
}

pub async fn get(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r: Option<(u64, String, String, String, serde_json::Value)> = sqlx::query_as(
        "SELECT id, name, url, secret, event_types FROM webhook_subscription WHERE id = ? AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("webhook".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "id": r.0, "name": r.1, "url": r.2, "secret": r.3, "event_types": r.4,
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
    let event_types = req.event_types.as_ref().and_then(|v| serde_json::to_value(v).ok());
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
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "event_type": sqlx::Row::try_get::<String, _>(r, "event_type").unwrap_or_default(),
        "response_status": sqlx::Row::try_get::<Option<i32>, _>(r, "response_status").ok().flatten(),
        "attempt_count": sqlx::Row::try_get::<u32, _>(r, "attempt_count").unwrap_or(1),
        "duration_ms": sqlx::Row::try_get::<Option<u32>, _>(r, "duration_ms").ok().flatten(),
        "delivered_at": sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "delivered_at").ok().map(|t| t.to_rfc3339()),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[allow(dead_code)]
fn _trigger_via_stream(env: StreamEnvelope) {
    let _ = env;
}