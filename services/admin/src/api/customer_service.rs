//! 客服坐席 CRUD

use crate::AppState;
use axum::{extract::{Path, State}, Json};
use common_auth::AdminClaims;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use serde_json::{json, Value};

pub async fn list(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query("SELECT id, agent_wechat, agent_name, path, priority, enabled FROM customer_service_config ORDER BY priority DESC, id ASC")
        .fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| -> AppResult<Value> { Ok(json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id")?,
        "agent_wechat": sqlx::Row::try_get::<String, _>(r, "agent_wechat")?,
        "agent_name": sqlx::Row::try_get::<Option<String>, _>(r, "agent_name")?,
        "path": sqlx::Row::try_get::<Option<String>, _>(r, "path")?,
        "priority": sqlx::Row::try_get::<u32, _>(r, "priority")?,
        "enabled": sqlx::Row::try_get::<i8, _>(r, "enabled")? != 0,
    })) }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct CSCreateReq {
    pub agent_wechat: String,
    pub agent_name: Option<String>,
    pub path: Option<String>,
    pub priority: Option<u32>,
    pub enabled: Option<bool>,
    pub working_hours_json: Option<serde_json::Value>,
}

fn validate(req: &CSCreateReq) -> AppResult<()> {
    if req.agent_wechat.trim().is_empty() || req.agent_wechat.len() > 64 || req.agent_wechat.chars().any(char::is_control) {
        return Err(AppError::BadRequest("客服微信号无效".into()));
    }
    if req.agent_name.as_deref().is_some_and(|name| name.trim().is_empty() || name.len() > 64 || name.chars().any(char::is_control)) {
        return Err(AppError::BadRequest("客服名称无效".into()));
    }
    if req.path.as_deref().is_some_and(|path| path.len() > 512 || !path.starts_with("https://") || path.chars().any(char::is_control)) {
        return Err(AppError::BadRequest("客服入口必须是有效 HTTPS URL".into()));
    }
    Ok(())
}

pub async fn create(State(st): State<AppState>, _c: AdminClaims, Json(req): Json<CSCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    validate(&req)?;
    let result = sqlx::query(
        "INSERT INTO customer_service_config (agent_wechat, agent_name, path, priority, enabled, working_hours_json)
         VALUES (?, ?, ?, COALESCE(?, 0), COALESCE(?, 1), ?)"
    )
    .bind(&req.agent_wechat).bind(req.agent_name.as_deref()).bind(req.path.as_deref())
    .bind(req.priority).bind(req.enabled).bind(req.working_hours_json)
    .execute(st.db.pool()).await?;
    let id = result.last_insert_id();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": id}), common_error::current_request_id())))
}

pub async fn get(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r = sqlx::query("SELECT id, agent_wechat, agent_name, path, priority, enabled, working_hours_json FROM customer_service_config WHERE id = ?")
        .bind(id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("cs".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "id": sqlx::Row::try_get::<u64, _>(&r, "id")?,
        "agent_wechat": sqlx::Row::try_get::<String, _>(&r, "agent_wechat")?,
        "agent_name": sqlx::Row::try_get::<Option<String>, _>(&r, "agent_name")?,
        "path": sqlx::Row::try_get::<Option<String>, _>(&r, "path")?,
        "priority": sqlx::Row::try_get::<u32, _>(&r, "priority")?,
        "enabled": sqlx::Row::try_get::<i8, _>(&r, "enabled")? != 0,
        "working_hours_json": sqlx::Row::try_get::<Option<serde_json::Value>, _>(&r, "working_hours_json")?,
    }), common_error::current_request_id())))
}

pub async fn update(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>, Json(req): Json<CSCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    validate(&req)?;
    let exists: bool = sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM customer_service_config WHERE id = ?)")
        .bind(id).fetch_one(st.db.pool()).await?;
    if !exists { return Err(AppError::NotFound("cs".into())); }
    sqlx::query(
        "UPDATE customer_service_config
         SET agent_wechat = ?, agent_name = ?, path = ?, priority = ?, enabled = ?, working_hours_json = ?
         WHERE id = ?"
    )
    .bind(&req.agent_wechat).bind(req.agent_name.as_deref()).bind(req.path.as_deref())
    .bind(req.priority.unwrap_or_default()).bind(req.enabled.unwrap_or(true)).bind(req.working_hours_json).bind(id)
    .execute(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"updated": true}), common_error::current_request_id())))
}

pub async fn delete(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let exists: bool = sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM customer_service_config WHERE id = ?)")
        .bind(id).fetch_one(st.db.pool()).await?;
    if !exists { return Err(AppError::NotFound("cs".into())); }
    sqlx::query("UPDATE customer_service_config SET enabled = 0 WHERE id = ?")
        .bind(id).execute(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"deleted": true}), common_error::current_request_id())))
}
