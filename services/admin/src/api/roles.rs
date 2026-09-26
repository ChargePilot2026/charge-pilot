//! 角色 + 权限

use crate::AppState;
use axum::{extract::{Path, State}, Json};
use common_auth::AdminClaims;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use serde_json::{json, Value};

pub async fn list(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query("SELECT id, code, name, description, is_builtin, created_at FROM role WHERE deleted_at IS NULL ORDER BY id ASC")
        .fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "code": sqlx::Row::try_get::<String, _>(r, "code").unwrap_or_default(),
        "name": sqlx::Row::try_get::<String, _>(r, "name").unwrap_or_default(),
        "description": sqlx::Row::try_get::<Option<String>, _>(r, "description").ok().flatten(),
        "is_builtin": sqlx::Row::try_get::<i8, _>(r, "is_builtin").unwrap_or(0) != 0,
        "created_at": sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "created_at").ok().map(|t| t.to_rfc3339()),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct RoleCreateReq {
    pub code: String,
    pub name: String,
    pub description: Option<String>,
    pub permission_ids: Option<Vec<u64>>,
}

pub async fn create(State(st): State<AppState>, _c: AdminClaims, Json(req): Json<RoleCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let mut tx = st.db.pool().begin().await?;
    let role_id: u64 = sqlx::query_scalar("INSERT INTO role (code, name, description) VALUES (?, ?, ?)")
        .bind(&req.code).bind(&req.name).bind(req.description.as_deref())
        .fetch_one(&mut *tx).await?;
    if let Some(pids) = req.permission_ids {
        for pid in pids {
            sqlx::query("INSERT IGNORE INTO role_permission (role_id, permission_id) VALUES (?, ?)")
                .bind(role_id).bind(pid).execute(&mut *tx).await?;
        }
    }
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": role_id}), common_error::current_request_id())))
}

pub async fn get(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r: Option<(u64, String, String, Option<String>)> = sqlx::query_as("SELECT id, code, name, description FROM role WHERE id = ? AND deleted_at IS NULL")
        .bind(id).fetch_optional(st.db.pool()).await?;
    let (id, code, name, desc) = r.ok_or_else(|| AppError::NotFound("role".into()))?;
    let perms: Vec<u64> = sqlx::query_scalar("SELECT permission_id FROM role_permission WHERE role_id = ?")
        .bind(id).fetch_all(st.db.pool()).await.unwrap_or_default();
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "id": id, "code": code, "name": name, "description": desc, "permission_ids": perms,
    }), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct RoleUpdateReq {
    pub name: Option<String>,
    pub description: Option<String>,
    pub permission_ids: Option<Vec<u64>>,
}

pub async fn update(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>, Json(req): Json<RoleUpdateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let mut tx = st.db.pool().begin().await?;
    let n = sqlx::query("UPDATE role SET name = COALESCE(?, name), description = COALESCE(?, description) WHERE id = ? AND deleted_at IS NULL")
        .bind(req.name.as_deref()).bind(req.description.as_deref()).bind(id)
        .execute(&mut *tx).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("role".into())); }
    if let Some(pids) = req.permission_ids {
        sqlx::query("DELETE FROM role_permission WHERE role_id = ?").bind(id).execute(&mut *tx).await?;
        for pid in pids {
            sqlx::query("INSERT IGNORE INTO role_permission (role_id, permission_id) VALUES (?, ?)")
                .bind(id).bind(pid).execute(&mut *tx).await?;
        }
    }
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"updated": true}), common_error::current_request_id())))
}

pub async fn delete(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let n = sqlx::query("UPDATE role SET deleted_at = NOW(3) WHERE id = ? AND is_builtin = 0 AND deleted_at IS NULL")
        .bind(id).execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::Conflict("role is builtin or not found".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"deleted": true}), common_error::current_request_id())))
}

pub async fn permissions(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query("SELECT id, code, name, module, description FROM permission ORDER BY module, id")
        .fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "code": sqlx::Row::try_get::<String, _>(r, "code").unwrap_or_default(),
        "name": sqlx::Row::try_get::<String, _>(r, "name").unwrap_or_default(),
        "module": sqlx::Row::try_get::<String, _>(r, "module").unwrap_or_default(),
        "description": sqlx::Row::try_get::<Option<String>, _>(r, "description").ok().flatten(),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}