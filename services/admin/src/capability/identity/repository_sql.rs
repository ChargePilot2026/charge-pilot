//! identity 域的 repository 层 —— SQL 只允许出现在这里
//!
//! (方案 §三:handler / usecase / domain 层禁 SQL,由 clippy disallowed-methods 保证)
//! 覆盖 `admin_user_role` / `role` / `role_permission` / `permission` 四张表,
//! 即 admin 侧"账号 + 角色 + 权限"的全部读写。

#![allow(clippy::disallowed_methods)]

use crate::AppState;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use sqlx::Row;

/// `ActiveAdmin` 提取器在每个请求上复核的账号状态。
pub type AccountState = (String, Option<chrono::DateTime<chrono::Utc>>);

/// 登录所需的账号快照。
#[allow(clippy::type_complexity)]
pub type LoginAccount = (
    u64,
    String,
    Option<u64>,
    String,
    String,
    u32,
    Option<chrono::DateTime<chrono::Utc>>,
);

/// refresh 复核的账号状态(角色 + 状态 + 锁定时间)。
pub type RefreshAccount = (
    Option<u64>,
    String,
    Option<chrono::DateTime<chrono::Utc>>,
);

/// 按 id 取账号状态,供 `ActiveAdmin` 提取器复核。
pub async fn account_state(st: &AppState, admin_user_id: u64) -> AppResult<AccountState> {
    let row: Option<AccountState> = sqlx::query_as(
        "SELECT status, locked_until FROM admin_user_role WHERE id = ? AND deleted_at IS NULL",
    )
    .bind(admin_user_id)
    .fetch_optional(st.identity.pool())
    .await?;
    row.ok_or_else(|| AppError::Unauthorized("管理员账号不存在或已删除".into()))
}

/// D2:操作授权的**唯一**实现。此前同样的 SQL 在 stations / coupons / casework /
/// dashboard / device_import / whitelabel / billing 各自复制了一份,新增写接口时
/// 极易漏接(D1 即由此产生)。统一到此处,新增写接口只需调用它。
pub async fn has_permission(
    st: &AppState,
    admin_user_id: u64,
    username: &str,
    permission: &str,
) -> AppResult<bool> {
    let allowed: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM admin_user_role a
                        JOIN role r ON r.id = a.role_id
                        JOIN role_permission rp ON rp.role_id = r.id
                        JOIN permission p ON p.id = rp.permission_id
                        WHERE a.id = ? AND a.username = ?
                          AND a.status = 'active' AND a.deleted_at IS NULL
                          AND r.deleted_at IS NULL
                          AND p.code = ?)",
    )
    .bind(admin_user_id)
    .bind(username)
    .bind(permission)
    .fetch_one(st.identity.pool())
    .await?;
    Ok(allowed)
}

/// 按 `admin_user_id` 取当前 username。供只有 id 的后台路径解析身份,
/// 避免出现第二份鉴权实现。
pub async fn username_by_id(st: &AppState, admin_user_id: u64) -> AppResult<String> {
    let username: Option<String> =
        sqlx::query_scalar("SELECT username FROM admin_user_role WHERE id = ? AND deleted_at IS NULL")
            .bind(admin_user_id)
            .fetch_optional(st.identity.pool())
            .await?;
    username.ok_or_else(|| AppError::Unauthorized("管理员账号不存在或已删除".into()))
}

/// 按 username 取账号(登录)。
#[allow(clippy::type_complexity)]
pub async fn account_by_username(st: &AppState, username: &str) -> AppResult<Option<LoginAccount>> {
    let row: Option<LoginAccount> = sqlx::query_as(
        "SELECT id, username, role_id, password_hash, status, failed_login_count, locked_until
         FROM admin_user_role WHERE username = ? AND deleted_at IS NULL LIMIT 1",
    )
    .bind(username)
    .fetch_optional(st.identity.pool())
    .await?;
    Ok(row)
}

/// refresh 复核用:取账号的角色与状态。
pub async fn refresh_account(st: &AppState, admin_user_id: u64) -> AppResult<RefreshAccount> {
    let row: Option<RefreshAccount> =
        sqlx::query_as(
            "SELECT role_id, status, locked_until FROM admin_user_role WHERE id = ? AND deleted_at IS NULL",
        )
        .bind(admin_user_id)
        .fetch_optional(st.identity.pool())
        .await?;
    row.ok_or_else(|| AppError::Unauthorized("管理员账号不存在或已删除".into()))
}

/// 登录失败:单条语句完成"计数 + 达阈值锁定 + 写 locked_until",并发失败下天然原子。
/// 契约:失败 5 次锁定 30 分钟。
pub async fn record_login_failure(
    st: &AppState,
    id: u64,
    max_failed: i64,
    lock_minutes: i64,
) -> AppResult<()> {
    sqlx::query(
        "UPDATE admin_user_role SET
             failed_login_count = failed_login_count + 1,
             status = IF(failed_login_count + 1 >= ?, 'locked', status),
             locked_until = IF(failed_login_count + 1 >= ?,
                               DATE_ADD(UTC_TIMESTAMP(3), INTERVAL ? MINUTE),
                               locked_until)
         WHERE id = ?",
    )
    .bind(max_failed)
    .bind(max_failed)
    .bind(lock_minutes)
    .bind(id)
    .execute(st.identity.pool())
    .await?;
    Ok(())
}

/// 登录成功:复位失败计数与锁定状态。
///
/// `disabled` 在 usecase 层已被 `is_login_blocked` 拒绝,不会走到这里,
/// 因此置回 'active' 只会影响"锁定到期后恢复"的场景。
pub async fn record_login_success(st: &AppState, id: u64) -> AppResult<()> {
    sqlx::query("UPDATE admin_user_role SET last_login_at=NOW(3), failed_login_count=0,
                    locked_until=NULL, status='active' WHERE id=?")
        .bind(id)
        .execute(st.identity.pool())
        .await?;
    Ok(())
}

/// 读取账号**当前**的角色与权限(登录与 refresh 共用)。
///
/// `role_id` 为空 = 账号未挂角色,回落为只读 `viewer` 且无任何权限码。
pub async fn role_and_permissions(
    st: &AppState,
    role_id: Option<u64>,
) -> AppResult<(String, Vec<String>)> {
    let role_code: String = if let Some(rid) = role_id {
        sqlx::query_scalar("SELECT code FROM role WHERE id = ? AND deleted_at IS NULL")
            .bind(rid)
            .fetch_optional(st.identity.pool())
            .await?
            .ok_or_else(|| AppError::ServiceUnavailable("管理员角色不存在或已停用".into()))?
    } else {
        "viewer".to_string()
    };
    let permissions: Vec<String> = if let Some(rid) = role_id {
        let rows = sqlx::query("SELECT p.code FROM permission p JOIN role_permission rp ON rp.permission_id = p.id WHERE rp.role_id = ?")
            .bind(rid)
            .fetch_all(st.identity.pool())
            .await?;
        rows.iter()
            .map(|row| row.try_get::<String, _>("code").map_err(AppError::from))
            .collect::<AppResult<Vec<_>>>()?
    } else {
        vec![]
    };
    Ok((role_code, permissions))
}

// ===== 角色 / 权限维护 =====

#[derive(Debug, Deserialize)]
pub struct RoleCreateReq {
    pub code: String,
    pub name: String,
    pub description: Option<String>,
    pub permission_ids: Option<Vec<u64>>,
}

#[derive(Debug, Deserialize)]
pub struct RoleUpdateReq {
    pub name: Option<String>,
    pub description: Option<String>,
    pub permission_ids: Option<Vec<u64>>,
}

pub async fn list_roles(st: &AppState) -> AppResult<api_contracts::admin::RoleList> {
    let rows = sqlx::query("SELECT id, code, name, description, is_builtin, created_at FROM role WHERE deleted_at IS NULL ORDER BY id ASC")
        .fetch_all(st.identity.pool()).await?;
    let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::Role> {
        Ok(api_contracts::admin::Role {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            code: sqlx::Row::try_get::<String, _>(r, "code")?,
            name: sqlx::Row::try_get::<String, _>(r, "name")?,
            description: sqlx::Row::try_get::<Option<String>, _>(r, "description")?,
            is_builtin: sqlx::Row::try_get::<i8, _>(r, "is_builtin")? != 0,
            created_at: sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "created_at")?
                .to_rfc3339(),
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::admin::RoleList::new(items))
}

pub async fn create_role(st: &AppState, req: &RoleCreateReq) -> AppResult<u64> {
    let mut tx = st.identity.begin().await?;
    let result = sqlx::query("INSERT INTO role (code, name, description) VALUES (?, ?, ?)")
        .bind(&req.code).bind(&req.name).bind(req.description.as_deref())
        .execute(tx.executor()).await?;
    let role_id = result.last_insert_id();
    if let Some(pids) = req.permission_ids.as_ref() {
        for pid in pids {
            sqlx::query("INSERT IGNORE INTO role_permission (role_id, permission_id) VALUES (?, ?)")
                .bind(role_id).bind(pid).execute(tx.executor()).await?;
        }
    }
    tx.commit().await?;
    Ok(role_id)
}

pub async fn get_role(st: &AppState, id: u64) -> AppResult<api_contracts::admin::RoleDetail> {
    let r: Option<(u64, String, String, Option<String>)> = sqlx::query_as("SELECT id, code, name, description FROM role WHERE id = ? AND deleted_at IS NULL")
        .bind(id).fetch_optional(st.identity.pool()).await?;
    let (id, code, name, desc) = r.ok_or_else(|| AppError::NotFound("role".into()))?;
    let perms: Vec<u64> = sqlx::query_scalar("SELECT permission_id FROM role_permission WHERE role_id = ?")
        .bind(id).fetch_all(st.identity.pool()).await?;
    Ok(api_contracts::admin::RoleDetail { id, code, name, description: desc, permission_ids: perms })
}

pub async fn update_role(st: &AppState, id: u64, req: &RoleUpdateReq) -> AppResult<()> {
    let mut tx = st.identity.begin().await?;
    let n = sqlx::query("UPDATE role SET name = COALESCE(?, name), description = COALESCE(?, description) WHERE id = ? AND deleted_at IS NULL")
        .bind(req.name.as_deref()).bind(req.description.as_deref()).bind(id)
        .execute(tx.executor()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("role".into())); }
    if let Some(pids) = req.permission_ids.as_ref() {
        sqlx::query("DELETE FROM role_permission WHERE role_id = ?").bind(id).execute(tx.executor()).await?;
        for pid in pids {
            sqlx::query("INSERT IGNORE INTO role_permission (role_id, permission_id) VALUES (?, ?)")
                .bind(id).bind(pid).execute(tx.executor()).await?;
        }
    }
    tx.commit().await?;
    Ok(())
}

pub async fn delete_role(st: &AppState, id: u64) -> AppResult<()> {
    let n = sqlx::query("UPDATE role SET deleted_at = NOW(3) WHERE id = ? AND is_builtin = 0 AND deleted_at IS NULL")
        .bind(id).execute(st.identity.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::Conflict("role is builtin or not found".into())); }
    Ok(())
}

pub async fn list_permissions(st: &AppState) -> AppResult<api_contracts::common::ListResponse<api_contracts::admin::Permission>> {
    let rows = sqlx::query("SELECT id, code, name, module, description FROM permission ORDER BY module, id")
        .fetch_all(st.identity.pool()).await?;
    let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::Permission> {
        Ok(api_contracts::admin::Permission {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            code: sqlx::Row::try_get::<String, _>(r, "code")?,
            name: sqlx::Row::try_get::<String, _>(r, "name")?,
            module: sqlx::Row::try_get::<String, _>(r, "module")?,
            description: sqlx::Row::try_get::<Option<String>, _>(r, "description")?,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::common::ListResponse::new(items))
}

// ===== 管理员账号维护 =====

#[derive(Debug, Deserialize)]
pub struct UserCreateReq {
    pub username: String,
    pub display_name: Option<String>,
    pub password: String,
    pub phone: Option<String>,
    pub email: Option<String>,
    pub role_id: Option<u64>,
}

#[derive(Debug, Deserialize)]
pub struct UserUpdateReq {
    pub display_name: Option<String>,
    pub role_id: Option<u64>,
    pub status: Option<String>,
    pub phone: Option<String>,
    pub email: Option<String>,
}

#[derive(Debug, Deserialize)]
pub struct ResetPasswordReq {
    pub new_password: String,
}

pub async fn list_users(st: &AppState) -> AppResult<api_contracts::admin::AdminUserList> {
    let rows = sqlx::query(
        "SELECT id, username, display_name, role_id, status, last_login_at, created_at
         FROM admin_user_role WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 200",
    )
    .fetch_all(st.identity.pool()).await?;
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
    Ok(api_contracts::admin::AdminUserList::new(items))
}

pub async fn create_user(st: &AppState, req: &UserCreateReq, hash: &str) -> AppResult<()> {
    sqlx::query(
        "INSERT INTO admin_user_role (username, display_name, password_hash, phone, email, role_id, status)
         VALUES (?, ?, ?, ?, ?, ?, 'active')",
    )
    .bind(&req.username).bind(req.display_name.as_deref()).bind(hash)
    .bind(req.phone.as_deref()).bind(req.email.as_deref()).bind(req.role_id)
    .execute(st.identity.pool()).await?;
    Ok(())
}

pub async fn get_user(st: &AppState, id: u64) -> AppResult<api_contracts::admin::AdminUserDetail> {
    let r = sqlx::query("SELECT id, username, display_name, role_id, status, phone, email, last_login_at, created_at FROM admin_user_role WHERE id = ? AND deleted_at IS NULL")
        .bind(id).fetch_optional(st.identity.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("user".into()))?;
    Ok(api_contracts::admin::AdminUserDetail {
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
    })
}

pub async fn update_user(st: &AppState, id: u64, req: &UserUpdateReq) -> AppResult<()> {
    let n = sqlx::query(
        "UPDATE admin_user_role
         SET display_name = COALESCE(?, display_name),
             role_id = COALESCE(?, role_id),
             status = COALESCE(?, status),
             phone = COALESCE(?, phone),
             email = COALESCE(?, email)
         WHERE id = ? AND deleted_at IS NULL",
    )
    .bind(req.display_name.as_deref())
    .bind(req.role_id)
    .bind(req.status.as_deref())
    .bind(req.phone.as_deref())
    .bind(req.email.as_deref())
    .bind(id)
    .execute(st.identity.pool()).await?;
    if n.rows_affected() == 0 {
        return Err(AppError::NotFound("user".into()));
    }
    Ok(())
}

pub async fn delete_user(st: &AppState, id: u64, actor_id: u64) -> AppResult<()> {
    let n = sqlx::query("UPDATE admin_user_role SET deleted_at = NOW(3), deleted_by = ? WHERE id = ? AND deleted_at IS NULL")
        .bind(actor_id).bind(id).execute(st.identity.pool()).await?;
    if n.rows_affected() == 0 {
        return Err(AppError::NotFound("user".into()));
    }
    Ok(())
}

pub async fn reset_password(st: &AppState, id: u64, hash: &str) -> AppResult<()> {
    let n = sqlx::query("UPDATE admin_user_role SET password_hash = ?, failed_login_count = 0 WHERE id = ? AND deleted_at IS NULL")
        .bind(hash).bind(id).execute(st.identity.pool()).await?;
    if n.rows_affected() == 0 {
        return Err(AppError::NotFound("user".into()));
    }
    Ok(())
}
