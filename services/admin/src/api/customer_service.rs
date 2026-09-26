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
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "agent_wechat": sqlx::Row::try_get::<String, _>(r, "agent_wechat").unwrap_or_default(),
        "agent_name": sqlx::Row::try_get::<Option<String>, _>(r, "agent_name").ok().flatten(),
        "path": sqlx::Row::try_get::<Option<String>, _>(r, "path").ok().flatten(),
        "priority": sqlx::Row::try_get::<i32, _>(r, "priority").unwrap_or(0),
        "enabled": sqlx::Row::try_get::<i8, _>(r, "enabled").unwrap_or(1) != 0,
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct CSCreateReq {
    pub agent_wechat: String,
    pub agent_name: Option<String>,
    pub path: Option<String>,
    pub priority: Option<i32>,
    pub enabled: Option<bool>,
    pub working_hours_json: Option<serde_json::Value>,
}

pub async fn create(State(st): State<AppState>, _c: AdminClaims, Json(req): Json<CSCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let id: u64 = sqlx::query_scalar(
        "INSERT INTO customer_service_config (agent_wechat, agent_name, path, priority, enabled, working_hours_json)
         VALUES (?, ?, ?, COALESCE(?, 0), COALESCE(?, 1), ?)"
    )
    .bind(&req.agent_wechat).bind(req.agent_name.as_deref()).bind(req.path.as_deref())
    .bind(req.priority).bind(req.enabled).bind(req.working_hours_json)
    .fetch_one(st.db.pool()).await?;
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
        "priority": sqlx::Row::try_get::<i32, _>(&r, "priority").unwrap_or(0),
        "enabled": sqlx::Row::try_get::<i8, _>(&r, "enabled").unwrap_or(1) != 0,
    }), common_error::current_request_id())))
}

pub async fn update(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>, Json(req): Json<CSCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let n = sqlx::query(
        "UPDATE customer_service_config
         SET agent_wechat = ?, agent_name = ?, path = ?, priority = ?, enabled = ?, working_hours_json = ?
         WHERE id = ?"
    )
    .bind(&req.agent_wechat).bind(req.agent_name.as_deref()).bind(req.path.as_deref())
    .bind(req.priority).bind(req.enabled).bind(req.working_hours_json).bind(id)
    .execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("cs".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"updated": true}), common_error::current_request_id())))
}

pub async fn delete(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let n = sqlx::query("DELETE FROM customer_service_config WHERE id = ?").bind(id).execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("cs".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"deleted": true}), common_error::current_request_id())))
}