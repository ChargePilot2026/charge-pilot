//! admin 鉴权:登录 / 刷新 / 退出
//!
//! 登录成功后 jwt 中含 role + permissions

use crate::AppState;
use axum::{extract::State, Json};
use common_auth::{hash_password, verify_password};
use common_db::IdGen;
use common_error::{AppError, AppResult};
use serde::{Deserialize, Serialize};
use serde_json::json;
use sqlx::Row;

#[derive(Debug, Deserialize)]
pub struct LoginReq {
    pub username: String,
    pub password: String,
}

#[derive(Debug, Serialize)]
pub struct LoginResp {
    pub token: String,
    pub admin_user_id: u64,
    pub role: String,
    pub permissions: Vec<String>,
}

pub async fn login(
    State(st): State<AppState>,
    Json(req): Json<LoginReq>,
) -> AppResult<Json<common_error::ApiEnvelope<LoginResp>>> {
    let r: Option<(u64, String, Option<u64>, String, String, u32, Option<chrono::DateTime<chrono::Utc>>)> = sqlx::query_as(
        "SELECT id, username, role_id, password_hash, status, failed_login_count, locked_until
         FROM admin_user_role WHERE username = ? AND deleted_at IS NULL LIMIT 1"
    )
    .bind(&req.username)
    .fetch_optional(st.db.pool())
    .await?;
    let (id, username, role_id, hash, status, _failed, locked_until) = match r {
        Some(v) => v,
        None => return Err(AppError::Unauthorized("bad credentials".into())),
    };
    if status == "locked" {
        if let Some(lu) = locked_until {
            if lu > chrono::Utc::now() {
                return Err(AppError::Forbidden("account locked".into()));
            }
        }
    }
    if !verify_password(&req.password, &hash) {
        sqlx::query("UPDATE admin_user_role SET failed_login_count = failed_login_count + 1 WHERE id = ?")
            .bind(id).execute(st.db.pool()).await?;
        return Err(AppError::Unauthorized("bad credentials".into()));
    }

    // 角色 + 权限
    let role_code: String = if let Some(rid) = role_id {
        sqlx::query_scalar("SELECT code FROM role WHERE id = ? AND deleted_at IS NULL")
            .bind(rid)
            .fetch_optional(st.db.pool())
            .await
            .ok()
            .flatten()
            .unwrap_or_else(|| "viewer".to_string())
    } else {
        "viewer".to_string()
    };
    let permissions: Vec<String> = if let Some(rid) = role_id {
        sqlx::query("SELECT p.code FROM permission p JOIN role_permission rp ON rp.permission_id = p.id WHERE rp.role_id = ?")
            .bind(rid)
            .fetch_all(st.db.pool())
            .await
            .ok()
            .map(|rows| rows.iter().filter_map(|r| r.try_get::<String, _>("code").ok()).collect())
            .unwrap_or_default()
    } else {
        vec![]
    };

    sqlx::query("UPDATE admin_user_role SET last_login_at=NOW(3), failed_login_count=0 WHERE id=?")
        .bind(id).execute(st.db.pool()).await?;

    let token = st.jwt.issue_admin(&username, id, &role_code, permissions.clone())?;
    Ok(Json(common_error::ApiEnvelope::ok(LoginResp {
        token, admin_user_id: id, role: role_code, permissions,
    }, common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct RefreshReq { pub token: String }

pub async fn refresh(
    State(st): State<AppState>,
    Json(req): Json<RefreshReq>,
) -> AppResult<Json<common_error::ApiEnvelope<serde_json::Value>>> {
    let claims = st.jwt.verify_admin(&req.token).map_err(|_| AppError::Unauthorized("bad token".into()))?;
    let token = st.jwt.issue_admin(&claims.sub, claims.admin_user_id, &claims.role, claims.permissions.clone())?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"token": token}), common_error::current_request_id())))
}

pub async fn logout(
    State(_st): State<AppState>,
) -> AppResult<Json<common_error::ApiEnvelope<serde_json::Value>>> {
    // 简单:无状态 JWT 仅前端丢弃 token;若需撤销可维护 blocklist (Redis SET)
    Ok(Json(common_error::ApiEnvelope::ok(json!({"logged_out": true}), common_error::current_request_id())))
}

#[allow(dead_code)]
fn _id_unused() { let _ = IdGen::new("U"); }
