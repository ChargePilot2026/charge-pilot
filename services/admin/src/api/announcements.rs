//! 公告 CRUD

use crate::AppState;
use axum::{extract::{Path, State}, Json};
use crate::auth::ActiveAdmin;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use serde_json::{json, Value};

pub async fn list(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::AnnouncementListItem>>>> {
    let rows = sqlx::query(
        "SELECT id, title, content, scope, priority, start_at, end_at, status, created_at
         FROM announcement WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.db.pool()).await?;
    let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::AnnouncementListItem> {
        Ok(api_contracts::admin::AnnouncementListItem {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            title: sqlx::Row::try_get::<String, _>(r, "title")?,
            content: sqlx::Row::try_get::<String, _>(r, "content")?,
            scope: sqlx::Row::try_get::<String, _>(r, "scope")?,
            priority: sqlx::Row::try_get::<u8, _>(r, "priority")?,
            start_at: sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "start_at")?.to_rfc3339(),
            end_at: sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(r, "end_at")?
                .map(|t| t.to_rfc3339()),
            status: sqlx::Row::try_get::<String, _>(r, "status")?,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::common::ListResponse::new(items),
        common_error::current_request_id(),
    )))
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

pub async fn create(State(st): State<AppState>, c: ActiveAdmin, Json(req): Json<AnnouncementCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::CreatedResponse>>> {
    crate::auth::require_permission(&st,&c,"announcement.create").await?;
    let result = sqlx::query(
        "INSERT INTO announcement (title, content, scope, priority, start_at, end_at, status, created_by)
         VALUES (?, ?, ?, COALESCE(?, 0), ?, ?, 'draft', ?)"
    )
    .bind(&req.title).bind(&req.content).bind(&req.scope).bind(req.priority)
    .bind(req.start_at).bind(req.end_at).bind(c.admin_user_id)
    .execute(st.db.pool()).await?;
    let id = result.last_insert_id();
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::CreatedResponse { id: id }, common_error::current_request_id())))
}

pub async fn get(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::AnnouncementDetailV2>>> {
    let r = sqlx::query("SELECT id, title, content, scope, priority, start_at, end_at, status FROM announcement WHERE id = ? AND deleted_at IS NULL")
        .bind(id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("announcement".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::admin::AnnouncementDetailV2 {
            id: sqlx::Row::try_get::<u64, _>(&r, "id")?,
            title: sqlx::Row::try_get::<String, _>(&r, "title")?,
            content: sqlx::Row::try_get::<String, _>(&r, "content")?,
            scope: sqlx::Row::try_get::<String, _>(&r, "scope")?,
            priority: sqlx::Row::try_get::<u8, _>(&r, "priority")?,
            status: sqlx::Row::try_get::<String, _>(&r, "status")?,
        },
        common_error::current_request_id(),
    )))
}

#[derive(Debug, Deserialize)]
pub struct AnnouncementUpdateReq {
    pub title: Option<String>,
    pub content: Option<String>,
    pub status: Option<String>,
    pub end_at: Option<chrono::DateTime<chrono::Utc>>,
}

pub async fn update(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>, Json(req): Json<AnnouncementUpdateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::UpdatedResponse>>> {
    crate::auth::require_permission(&st,&_c,"announcement.update").await?;
    let n = sqlx::query(
        "UPDATE announcement SET title = COALESCE(?, title), content = COALESCE(?, content),
                                status = COALESCE(?, status), end_at = COALESCE(?, end_at)
         WHERE id = ? AND deleted_at IS NULL"
    )
    .bind(req.title.as_deref()).bind(req.content.as_deref()).bind(req.status.as_deref()).bind(req.end_at).bind(id)
    .execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("announcement".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::UpdatedResponse::new(), common_error::current_request_id())))
}

pub async fn delete(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::DeletedResponse>>> {
    crate::auth::require_permission(&st,&_c,"announcement.delete").await?;
    let n = sqlx::query("UPDATE announcement SET deleted_at = NOW(3) WHERE id = ? AND deleted_at IS NULL")
        .bind(id).execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("announcement".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::DeletedResponse::new(), common_error::current_request_id())))
}
