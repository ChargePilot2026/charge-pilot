//! admin 告警:列表 / ACK / 规则 CRUD / 订阅 / 风控配置

use crate::AppState;
use axum::{extract::{Path, State}, Json};
use common_auth::AdminClaims;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use serde_json::{json, Value};

pub async fn list(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query(
        "SELECT id, device_id, rule_id, severity, metric, value, threshold, status, acked_by, acked_at, created_at
         FROM alert_event ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "device_id": sqlx::Row::try_get::<String, _>(r, "device_id").unwrap_or_default(),
        "rule_id": sqlx::Row::try_get::<Option<u64>, _>(r, "rule_id").ok().flatten(),
        "severity": sqlx::Row::try_get::<String, _>(r, "severity").unwrap_or_default(),
        "metric": sqlx::Row::try_get::<String, _>(r, "metric").unwrap_or_default(),
        "value": sqlx::Row::try_get::<Option<f64>, _>(r, "value").ok().flatten(),
        "status": sqlx::Row::try_get::<String, _>(r, "status").unwrap_or_default(),
        "acked_by": sqlx::Row::try_get::<Option<u64>, _>(r, "acked_by").ok().flatten(),
        "created_at": sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "created_at").ok().map(|t| t.to_rfc3339()),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

pub async fn ack(State(st): State<AppState>, c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let n = sqlx::query("UPDATE alert_event SET status = 'acknowledged', acked_by = ?, acked_at = NOW(3) WHERE id = ? AND status = 'active'")
        .bind(c.admin_user_id).bind(id).execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::Conflict("not active".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"acked": true}), common_error::current_request_id())))
}

pub async fn rules_list(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query("SELECT id, name, device_id_pattern, metric, op, threshold, window_seconds, severity, enabled FROM alert_rule WHERE deleted_at IS NULL")
        .fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "name": sqlx::Row::try_get::<String, _>(r, "name").unwrap_or_default(),
        "metric": sqlx::Row::try_get::<String, _>(r, "metric").unwrap_or_default(),
        "op": sqlx::Row::try_get::<String, _>(r, "op").unwrap_or_default(),
        "severity": sqlx::Row::try_get::<String, _>(r, "severity").unwrap_or_default(),
        "enabled": sqlx::Row::try_get::<i8, _>(r, "enabled").unwrap_or(1) != 0,
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct AlertRuleCreateReq {
    pub name: String,
    pub device_id_pattern: Option<String>,
    pub metric: String,
    pub op: String,
    pub threshold: serde_json::Value,
    pub window_seconds: Option<u32>,
    pub severity: String,
}

pub async fn rules_create(State(st): State<AppState>, _c: AdminClaims, Json(req): Json<AlertRuleCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let id: u64 = sqlx::query_scalar(
        "INSERT INTO alert_rule (name, device_id_pattern, metric, op, threshold, window_seconds, severity, enabled)
         VALUES (?, COALESCE(?, '*'), ?, ?, ?, COALESCE(?, 60), ?, 1)"
    )
    .bind(&req.name).bind(req.device_id_pattern.as_deref()).bind(&req.metric).bind(&req.op)
    .bind(req.threshold).bind(req.window_seconds).bind(&req.severity)
    .fetch_one(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": id}), common_error::current_request_id())))
}

pub async fn rules_get(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r: Option<(u64, String, String, String, String, serde_json::Value, u32, String, i8)> = sqlx::query_as(
        "SELECT id, name, device_id_pattern, metric, op, threshold, window_seconds, severity, enabled FROM alert_rule WHERE id = ? AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("rule".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "id": r.0, "name": r.1, "device_id_pattern": r.2,
        "metric": r.3, "op": r.4, "threshold": r.5,
        "window_seconds": r.6, "severity": r.7, "enabled": r.8 != 0,
    }), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct AlertRuleUpdateReq {
    pub name: Option<String>,
    pub severity: Option<String>,
    pub enabled: Option<bool>,
}

pub async fn rules_update(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>, Json(req): Json<AlertRuleUpdateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let n = sqlx::query(
        "UPDATE alert_rule SET name = COALESCE(?, name), severity = COALESCE(?, severity),
                                enabled = COALESCE(?, enabled)
         WHERE id = ? AND deleted_at IS NULL"
    )
    .bind(req.name.as_deref()).bind(req.severity.as_deref()).bind(req.enabled).bind(id)
    .execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("rule".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"updated": true}), common_error::current_request_id())))
}

pub async fn rules_delete(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let n = sqlx::query("UPDATE alert_rule SET deleted_at = NOW(3) WHERE id = ? AND deleted_at IS NULL")
        .bind(id).execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("rule".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"deleted": true}), common_error::current_request_id())))
}

pub async fn subs_list(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query("SELECT id, rule_id, severity, webhook_subscription_id, admin_user_id, enabled FROM alert_subscription")
        .fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "rule_id": sqlx::Row::try_get::<Option<u64>, _>(r, "rule_id").ok().flatten(),
        "severity": sqlx::Row::try_get::<Option<String>, _>(r, "severity").ok().flatten(),
        "webhook_subscription_id": sqlx::Row::try_get::<Option<u64>, _>(r, "webhook_subscription_id").ok().flatten(),
        "admin_user_id": sqlx::Row::try_get::<Option<u64>, _>(r, "admin_user_id").ok().flatten(),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct SubCreateReq {
    pub rule_id: Option<u64>,
    pub severity: Option<String>,
    pub webhook_subscription_id: Option<u64>,
    pub admin_user_id: Option<u64>,
}

pub async fn subs_create(State(st): State<AppState>, _c: AdminClaims, Json(req): Json<SubCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let id: u64 = sqlx::query_scalar(
        "INSERT INTO alert_subscription (rule_id, severity, webhook_subscription_id, admin_user_id, enabled) VALUES (?, ?, ?, ?, 1)"
    )
    .bind(req.rule_id).bind(req.severity.as_deref()).bind(req.webhook_subscription_id).bind(req.admin_user_id)
    .fetch_one(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": id}), common_error::current_request_id())))
}

pub async fn risk_config_get(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query("SELECT `key`, value, description FROM risk_config").fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "key": sqlx::Row::try_get::<String, _>(r, "key").unwrap_or_default(),
        "value": sqlx::Row::try_get::<serde_json::Value, _>(r, "value").ok(),
        "description": sqlx::Row::try_get::<Option<String>, _>(r, "description").ok().flatten(),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

pub async fn risk_config_put(State(st): State<AppState>, c: AdminClaims, Json(req): Json<Value>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let arr = req.as_array().cloned().unwrap_or_default();
    for item in arr {
        let key = item.get("key").and_then(|v| v.as_str()).unwrap_or("");
        let value = item.get("value").cloned().unwrap_or(serde_json::Value::Null);
        let desc = item.get("description").and_then(|v| v.as_str());
        sqlx::query(
            "INSERT INTO risk_config (`key`, value, description, updated_by) VALUES (?, ?, ?, ?)
             ON DUPLICATE KEY UPDATE value = VALUES(value), description = VALUES(description), updated_by = VALUES(updated_by)"
        )
        .bind(key).bind(value).bind(desc).bind(c.admin_user_id)
        .execute(st.db.pool()).await?;
    }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"updated": true}), common_error::current_request_id())))
}