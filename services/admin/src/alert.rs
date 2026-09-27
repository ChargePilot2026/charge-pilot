//! admin 告警:列表 / ACK / 规则 CRUD / 订阅 / 风控配置

use crate::AppState;
use axum::{extract::{Path, State}, Json};
use crate::auth::ActiveAdmin;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use serde_json::{json, Value};

pub async fn list(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::AlertEvent>>>> {
    let rows = sqlx::query(
        "SELECT id, device_id, rule_id, severity, metric, value, threshold, status, acked_by, acked_at, created_at
         FROM alert_event ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.db.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::AlertEvent> {
        Ok(api_contracts::admin::AlertEvent {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            device_id: sqlx::Row::try_get::<String, _>(r, "device_id")?,
            rule_id: sqlx::Row::try_get::<Option<u64>, _>(r, "rule_id")?,
            severity: sqlx::Row::try_get::<String, _>(r, "severity")?,
            metric: sqlx::Row::try_get::<String, _>(r, "metric")?,
            value: sqlx::Row::try_get::<Option<f64>, _>(r, "value")?,
            status: sqlx::Row::try_get::<String, _>(r, "status")?,
            acked_by: sqlx::Row::try_get::<Option<u64>, _>(r, "acked_by")?,
            created_at: sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "created_at")?.to_rfc3339(),
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::common::ListResponse::new(items),
        common_error::current_request_id(),
    )))
}

pub async fn ack(State(st): State<AppState>, c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    crate::auth::require_permission(&st,&c,"alert.ack").await?;
    let n = sqlx::query("UPDATE alert_event SET status = 'acknowledged', acked_by = ?, acked_at = NOW(3) WHERE id = ? AND status = 'active'")
        .bind(c.admin_user_id).bind(id).execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::Conflict("not active".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"acked": true}), common_error::current_request_id())))
}

pub async fn rules_list(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::AlertRule>>>> {
    let rows = sqlx::query("SELECT id, name, device_id_pattern, metric, op, threshold, window_seconds, severity, enabled FROM alert_rule WHERE deleted_at IS NULL")
        .fetch_all(st.db.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::AlertRule> {
        Ok(api_contracts::admin::AlertRule {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            name: sqlx::Row::try_get::<String, _>(r, "name")?,
            metric: sqlx::Row::try_get::<String, _>(r, "metric")?,
            op: sqlx::Row::try_get::<String, _>(r, "op")?,
            severity: sqlx::Row::try_get::<String, _>(r, "severity")?,
            enabled: sqlx::Row::try_get::<i8, _>(r, "enabled")? != 0,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::common::ListResponse::new(items),
        common_error::current_request_id(),
    )))
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

pub async fn rules_create(State(st): State<AppState>, _c: ActiveAdmin, Json(req): Json<AlertRuleCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::CreatedResponse>>> {
    crate::auth::require_permission(&st,&_c,"alert.rule.create").await?;
    let result = sqlx::query(
        "INSERT INTO alert_rule (name, device_id_pattern, metric, op, threshold, window_seconds, severity, enabled)
         VALUES (?, COALESCE(?, '*'), ?, ?, ?, COALESCE(?, 60), ?, 1)"
    )
    .bind(&req.name).bind(req.device_id_pattern.as_deref()).bind(&req.metric).bind(&req.op)
    .bind(req.threshold).bind(req.window_seconds).bind(&req.severity)
    .execute(st.db.pool()).await?;
    let id = result.last_insert_id();
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::CreatedResponse { id: id }, common_error::current_request_id())))
}

pub async fn rules_get(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
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

pub async fn rules_update(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>, Json(req): Json<AlertRuleUpdateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::UpdatedResponse>>> {
    crate::auth::require_permission(&st,&_c,"alert.rule.update").await?;
    let n = sqlx::query(
        "UPDATE alert_rule SET name = COALESCE(?, name), severity = COALESCE(?, severity),
                                enabled = COALESCE(?, enabled)
         WHERE id = ? AND deleted_at IS NULL"
    )
    .bind(req.name.as_deref()).bind(req.severity.as_deref()).bind(req.enabled).bind(id)
    .execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("rule".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::UpdatedResponse::new(), common_error::current_request_id())))
}

pub async fn rules_delete(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::DeletedResponse>>> {
    crate::auth::require_permission(&st,&_c,"alert.rule.delete").await?;
    let n = sqlx::query("UPDATE alert_rule SET deleted_at = NOW(3) WHERE id = ? AND deleted_at IS NULL")
        .bind(id).execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("rule".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::DeletedResponse::new(), common_error::current_request_id())))
}

pub async fn subs_list(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::AlertSubscription>>>> {
    let rows = sqlx::query("SELECT id, rule_id, severity, webhook_subscription_id, admin_user_id, enabled FROM alert_subscription")
        .fetch_all(st.db.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::AlertSubscription> {
        Ok(api_contracts::admin::AlertSubscription {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            rule_id: sqlx::Row::try_get::<Option<u64>, _>(r, "rule_id")?,
            severity: sqlx::Row::try_get::<Option<String>, _>(r, "severity")?,
            webhook_subscription_id: sqlx::Row::try_get::<Option<u64>, _>(r, "webhook_subscription_id")?,
            admin_user_id: sqlx::Row::try_get::<Option<u64>, _>(r, "admin_user_id")?,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::common::ListResponse::new(items),
        common_error::current_request_id(),
    )))
}

#[derive(Debug, Deserialize)]
pub struct SubCreateReq {
    pub rule_id: Option<u64>,
    pub severity: Option<String>,
    pub webhook_subscription_id: Option<u64>,
    pub admin_user_id: Option<u64>,
}

pub async fn subs_create(State(st): State<AppState>, _c: ActiveAdmin, Json(req): Json<SubCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::CreatedResponse>>> {
    crate::auth::require_permission(&st,&_c,"alert.subscription.create").await?;
    let result = sqlx::query(
        "INSERT INTO alert_subscription (rule_id, severity, webhook_subscription_id, admin_user_id, enabled) VALUES (?, ?, ?, ?, 1)"
    )
    .bind(req.rule_id).bind(req.severity.as_deref()).bind(req.webhook_subscription_id).bind(req.admin_user_id)
    .execute(st.db.pool()).await?;
    let id = result.last_insert_id();
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::CreatedResponse { id: id }, common_error::current_request_id())))
}

pub async fn risk_config_get(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::RiskConfigItem>>>> {
    let rows = sqlx::query("SELECT `key`, value, description FROM risk_config").fetch_all(st.db.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::RiskConfigItem> {
        Ok(api_contracts::admin::RiskConfigItem {
            key: sqlx::Row::try_get::<String, _>(r, "key")?,
            value: sqlx::Row::try_get::<serde_json::Value, _>(r, "value")?,
            description: sqlx::Row::try_get::<Option<String>, _>(r, "description")?,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::common::ListResponse::new(items),
        common_error::current_request_id(),
    )))
}

pub async fn risk_config_put(State(st): State<AppState>, c: ActiveAdmin, Json(req): Json<Value>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::UpdatedResponse>>> {
    crate::auth::require_permission(&st,&c,"alert.risk_config.update").await?;
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
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::UpdatedResponse::new(), common_error::current_request_id())))
}
