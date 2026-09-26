//! 公告 CRUD

use crate::AppState;
use axum::{extract::{Path, State}, Json};
use common_auth::AdminClaims;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use serde_json::{json, Value};

pub async fn list(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query(
        "SELECT id, title, content, scope, priority, start_at, end_at, status, created_at
         FROM announcement WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "title": sqlx::Row::try_get::<String, _>(r, "title").unwrap_or_default(),
        "content": sqlx::Row::try_get::<String, _>(r, "content").unwrap_or_default(),
        "scope": sqlx::Row::try_get::<String, _>(r, "scope").unwrap_or_default(),
        "priority": sqlx::Row::try_get::<u8, _>(r, "priority").unwrap_or(0),
        "start_at": sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "start_at").ok().map(|t| t.to_rfc3339()),
        "end_at": sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(r, "end_at").ok().flatten().map(|t| t.to_rfc3339()),
        "status": sqlx::Row::try_get::<String, _>(r, "status").unwrap_or_default(),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct AnnouncementCreateReq {
    pub title: String,
    pub content: String,
    pub scope: String,
    pub priority: Option<u8>,
    pub start_at: chrono::DateTime<chrono::Utc>,
    pub end_at: Option<chrono::DateTime<chrono::Utc>>,
}

pub async fn create(State(st): State<AppState>, c: AdminClaims, Json(req): Json<AnnouncementCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let id: u64 = sqlx::query_scalar(
        "INSERT INTO announcement (title, content, scope, priority, start_at, end_at, status, created_by)
         VALUES (?, ?, ?, COALESCE(?, 0), ?, ?, 'draft', ?)"
    )
    .bind(&req.title).bind(&req.content).bind(&req.scope).bind(req.priority)
    .bind(req.start_at).bind(req.end_at).bind(c.admin_user_id)
    .fetch_one(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": id}), common_error::current_request_id())))
}

pub async fn get(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r = sqlx::query("SELECT id, title, content, scope, priority, start_at, end_at, status FROM announcement WHERE id = ? AND deleted_at IS NULL")
        .bind(id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("announcement".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "id": sqlx::Row::try_get::<u64, _>(&r, "id")?,
        "title": sqlx::Row::try_get::<String, _>(&r, "title")?,
        "content": sqlx::Row::try_get::<String, _>(&r, "content")?,
        "scope": sqlx::Row::try_get::<String, _>(&r, "scope")?,
        "priority": sqlx::Row::try_get::<u8, _>(&r, "priority").unwrap_or(0),
        "status": sqlx::Row::try_get::<String, _>(&r, "status")?,
    }), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct AnnouncementUpdateReq {
    pub title: Option<String>,
    pub content: Option<String>,
    pub status: Option<String>,
    pub end_at: Option<chrono::DateTime<chrono::Utc>>,
}

pub async fn update(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>, Json(req): Json<AnnouncementUpdateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let n = sqlx::query(
        "UPDATE announcement SET title = COALESCE(?, title), content = COALESCE(?, content),
                                status = COALESCE(?, status), end_at = COALESCE(?, end_at)
         WHERE id = ? AND deleted_at IS NULL"
    )
    .bind(req.title.as_deref()).bind(req.content.as_deref()).bind(req.status.as_deref()).bind(req.end_at).bind(id)
    .execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("announcement".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"updated": true}), common_error::current_request_id())))
}

pub async fn delete(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let n = sqlx::query("UPDATE announcement SET deleted_at = NOW(3) WHERE id = ? AND deleted_at IS NULL")
        .bind(id).execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("announcement".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"deleted": true}), common_error::current_request_id())))
}