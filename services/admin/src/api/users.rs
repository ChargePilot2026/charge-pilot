//! 管理员账号 CRUD

use crate::AppState;
use axum::{
    extract::{Path, State},
    Json,
};
use common_auth::hash_password;
use crate::auth::ActiveAdmin;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use serde_json::{json, Value};

#[derive(Debug, Deserialize)]
pub struct UserCreateReq {
    pub username: String,
    pub display_name: Option<String>,
    pub password: String,
    pub phone: Option<String>,
    pub email: Option<String>,
    pub role_id: Option<u64>,
}

pub async fn list(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::AdminUserList>>> {
    let rows = sqlx::query(
        "SELECT id, username, display_name, role_id, status, last_login_at, created_at
         FROM admin_user_role WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 200"
    )
    .fetch_all(st.db.pool()).await?;
    let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::AdminUser> {
        Ok(api_contracts::admin::AdminUser {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            username: sqlx::Row::try_get::<String, _>(r, "username")?,
            display_name: sqlx::Row::try_get::<Option<String>, _>(r, "display_name")?,
            role_id: sqlx::Row::try_get::<Option<u64>, _>(r, "role_id")?,
            status: sqlx::Row::try_get::<String, _>(r, "status")?,
            last_login_at: sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(r, "last_login_at")?
                .map(|t| t.to_rfc3339()),
            created_at: sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "created_at")?
                .to_rfc3339(),
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::admin::AdminUserList::new(items),
        common_error::current_request_id(),
    )))
}

pub async fn create(State(st): State<AppState>, _c: ActiveAdmin, Json(req): Json<UserCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::CreatedFlag>>> {
    crate::auth::require_permission(&st,&_c,"admin_user.create").await?;
    let hash = hash_password(&req.password)?;
    sqlx::query(
        "INSERT INTO admin_user_role (username, display_name, password_hash, phone, email, role_id, status)
         VALUES (?, ?, ?, ?, ?, ?, 'active')"
    )
    .bind(&req.username).bind(req.display_name.as_deref()).bind(&hash)
    .bind(req.phone.as_deref()).bind(req.email.as_deref()).bind(req.role_id)
    .execute(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::CreatedFlag::new(), common_error::current_request_id())))
}

pub async fn get(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::AdminUserDetail>>> {
    let r = sqlx::query("SELECT id, username, display_name, role_id, status, phone, email, last_login_at, created_at FROM admin_user_role WHERE id = ? AND deleted_at IS NULL")
        .bind(id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("user".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::admin::AdminUserDetail {
            id: sqlx::Row::try_get::<u64, _>(&r, "id")?,
            username: sqlx::Row::try_get::<String, _>(&r, "username")?,
            display_name: sqlx::Row::try_get::<Option<String>, _>(&r, "display_name")?,
            role_id: sqlx::Row::try_get::<Option<u64>, _>(&r, "role_id")?,
            status: sqlx::Row::try_get::<String, _>(&r, "status")?,
            phone: sqlx::Row::try_get::<Option<String>, _>(&r, "phone")?,
            email: sqlx::Row::try_get::<Option<String>, _>(&r, "email")?,
            last_login_at: sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(&r, "last_login_at")?
                .map(|t| t.to_rfc3339()),
            created_at: sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(&r, "created_at")?
                .to_rfc3339(),
        },
        common_error::current_request_id(),
    )))
}

#[derive(Debug, Deserialize)]
pub struct UserUpdateReq {
    pub display_name: Option<String>,
    pub role_id: Option<u64>,
    pub status: Option<String>,
    pub phone: Option<String>,
    pub email: Option<String>,
}

pub async fn update(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>, Json(req): Json<UserUpdateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::UpdatedResponse>>> {
    crate::auth::require_permission(&st,&_c,"admin_user.update").await?;
    let n = sqlx::query(
        "UPDATE admin_user_role
         SET display_name = COALESCE(?, display_name),
             role_id = COALESCE(?, role_id),
             status = COALESCE(?, status),
             phone = COALESCE(?, phone),
             email = COALESCE(?, email)
         WHERE id = ? AND deleted_at IS NULL"
    )
    .bind(req.display_name.as_deref())
    .bind(req.role_id)
    .bind(req.status.as_deref())
    .bind(req.phone.as_deref())
    .bind(req.email.as_deref())
    .bind(id)
    .execute(st.db.pool()).await?;
    if n.rows_affected() == 0 {
        return Err(AppError::NotFound("user".into()));
    }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::UpdatedResponse::new(), common_error::current_request_id())))
}

pub async fn delete(State(st): State<AppState>, actor: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::DeletedResponse>>> {
    crate::auth::require_permission(&st,&actor,"admin_user.delete").await?;
    let n = sqlx::query("UPDATE admin_user_role SET deleted_at = NOW(3), deleted_by = ? WHERE id = ? AND deleted_at IS NULL")
        .bind(actor.admin_user_id).bind(id).execute(st.db.pool()).await?;
    if n.rows_affected() == 0 {
        return Err(AppError::NotFound("user".into()));
    }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::DeletedResponse::new(), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct ResetPasswordReq { pub new_password: String }

pub async fn reset_password(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>, Json(req): Json<ResetPasswordReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ResetFlag>>> {
    crate::auth::require_permission(&st,&_c,"admin_user.reset_password").await?;
    let hash = hash_password(&req.new_password)?;
    let n = sqlx::query("UPDATE admin_user_role SET password_hash = ?, failed_login_count = 0 WHERE id = ? AND deleted_at IS NULL")
        .bind(&hash).bind(id).execute(st.db.pool()).await?;
    if n.rows_affected() == 0 {
        return Err(AppError::NotFound("user".into()));
    }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::ResetFlag::new(), common_error::current_request_id())))
}
