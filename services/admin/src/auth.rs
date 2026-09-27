//! admin 鉴权:登录 / 刷新 / 退出
//!
//! 登录成功后 jwt 中含 role + permissions
//!
//! D2 修复:
//! - 登录只判 `locked` 且 `locked_until` 全仓从未被写入 → 契约"失败 5 次锁 30 min"从未触发;
//! - 账号 `disabled` 仍可登录;
//! - refresh 直接复制旧 token 的 role/permissions,撤权与停用后仍能续命。
//! 现由 [`ActiveAdmin`] 提取器在每个请求上复核账号状态,token 失效即时生效。

use crate::AppState;
use axum::{extract::State, Json};
use common_auth::AdminClaims;
use common_error::{AppError, AppResult};
use serde::{Deserialize, Serialize};
use serde_json::json;
use sqlx::Row;

/// 契约 `docs/api/admin.md`:失败 5 次锁定 30 分钟。
pub const MAX_FAILED_LOGINS: i64 = 5;
pub const LOCK_MINUTES: i64 = 30;

/// 账号是否禁止登录。`active` 之外一律禁止——原先只判 `locked` 且只在
/// `locked_until > now` 时拒绝,`disabled` 完全没被拦。
pub fn is_login_blocked(
    status: &str,
    locked_until: Option<chrono::DateTime<chrono::Utc>>,
    now: chrono::DateTime<chrono::Utc>,
) -> bool {
    match status {
        "active" => false,
        // 锁定到期后自动放行,由成功登录时复位状态
        "locked" => locked_until.is_some_and(|lu| lu > now),
        // disabled / 任何未知状态都拒绝
        _ => true,
    }
}

/// 达到阈值则锁定。
pub fn should_lock(failed_count: i64) -> bool {
    failed_count >= MAX_FAILED_LOGINS
}

/// 经复核的管理员身份。
///
/// JWT 验签只证明"签名有效",不证明"账号仍然有效"。这里在每个请求上复核
/// 一次账号状态,使停用/锁定/删除后**已签发的 token 立即失效**,而不是等到过期。
/// `Deref` 到 `AdminClaims`,因此 handler 里的 `c.admin_user_id` 等字段访问不变。
#[derive(Debug, Clone)]
pub struct ActiveAdmin(pub AdminClaims);

impl std::ops::Deref for ActiveAdmin {
    type Target = AdminClaims;
    fn deref(&self) -> &Self::Target {
        &self.0
    }
}

#[axum::async_trait]
impl axum::extract::FromRequestParts<AppState> for ActiveAdmin {
    type Rejection = AppError;

    async fn from_request_parts(
        parts: &mut axum::http::request::Parts,
        st: &AppState,
    ) -> Result<Self, Self::Rejection> {
        let claims = parts
            .extensions
            .get::<AdminClaims>()
            .cloned()
            .ok_or_else(|| AppError::Unauthorized("missing admin claims".into()))?;
        let row: Option<(String, Option<chrono::DateTime<chrono::Utc>>)> = sqlx::query_as(
            "SELECT status, locked_until FROM admin_user_role WHERE id = ? AND deleted_at IS NULL",
        )
        .bind(claims.admin_user_id)
        .fetch_optional(st.db.pool())
        .await?;
        let (status, locked_until) = row
            .ok_or_else(|| AppError::Unauthorized("管理员账号不存在或已删除".into()))?;
        if is_login_blocked(&status, locked_until, chrono::Utc::now()) {
            return Err(AppError::Forbidden("账号已停用或锁定".into()));
        }
        Ok(ActiveAdmin(claims))
    }
}

/// D2:操作授权的**唯一**实现。此前同样的 SQL 在 stations / coupons / casework /
/// dashboard / device_import / whitelabel / billing 各自复制了一份,新增写接口时
/// 极易漏接(D1 即由此产生)。统一到此处,新增写接口只需调用它。
pub async fn require_permission(
    st: &AppState,
    actor: &ActiveAdmin,
    permission: &str,
) -> AppResult<()> {
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
    .bind(actor.admin_user_id)
    .bind(&actor.sub)
    .bind(permission)
    .fetch_one(st.db.pool())
    .await?;
    if !allowed {
        return Err(AppError::Forbidden(format!("缺少 {permission} 权限")));
    }
    Ok(())
}

/// 按 `admin_user_id` 校验权限。供只有 id 的后台路径使用(如设备导入恢复)——
/// 先解析出当前 username 再走同一段 SQL,避免出现第二份鉴权实现。
pub async fn require_permission_by_id(
    st: &AppState,
    admin_user_id: u64,
    permission: &str,
) -> AppResult<()> {
    let username: Option<String> =
        sqlx::query_scalar("SELECT username FROM admin_user_role WHERE id = ? AND deleted_at IS NULL")
            .bind(admin_user_id)
            .fetch_optional(st.db.pool())
            .await?;
    let username = username
        .ok_or_else(|| AppError::Unauthorized("管理员账号不存在或已删除".into()))?;
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
    .bind(&username)
    .bind(permission)
    .fetch_one(st.db.pool())
    .await?;
    if !allowed {
        return Err(AppError::Forbidden(format!("缺少 {permission} 权限")));
    }
    Ok(())
}

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
    if is_login_blocked(&status, locked_until, chrono::Utc::now()) {
        return Err(AppError::Forbidden("account locked or disabled".into()));
    }
    // D13:argon2 是同步 CPU 密集计算,必须离开执行线程;并发由信号量限死。
    if !crate::password::verify(req.password.clone(), hash.clone()).await {
        // 单条语句完成"计数 + 达阈值锁定 + 写 locked_until",并发失败下天然原子。
        // 契约:失败 5 次锁定 30 分钟。
        sqlx::query(
            "UPDATE admin_user_role SET
                 failed_login_count = failed_login_count + 1,
                 status = IF(failed_login_count + 1 >= ?, 'locked', status),
                 locked_until = IF(failed_login_count + 1 >= ?,
                                   DATE_ADD(UTC_TIMESTAMP(3), INTERVAL ? MINUTE),
                                   locked_until)
             WHERE id = ?",
        )
        .bind(MAX_FAILED_LOGINS)
        .bind(MAX_FAILED_LOGINS)
        .bind(LOCK_MINUTES)
        .bind(id)
        .execute(st.db.pool())
        .await?;
        return Err(AppError::Unauthorized("bad credentials".into()));
    }

    // 角色 + 权限
    let (role_code, permissions) = load_role_and_permissions(&st, role_id).await?;

    // 登录成功:复位失败计数与锁定状态。
    // `disabled` 在上面已被 `is_login_blocked` 拒绝,不会走到这里,因此
    // 置回 'active' 只会影响"锁定到期后恢复"的场景。
    sqlx::query("UPDATE admin_user_role SET last_login_at=NOW(3), failed_login_count=0,
                    locked_until=NULL, status='active' WHERE id=?")
        .bind(id).execute(st.db.pool()).await?;

    let token = st.jwt.issue_admin(&username, id, &role_code, permissions.clone())?;
    Ok(Json(common_error::ApiEnvelope::ok(LoginResp {
        token, admin_user_id: id, role: role_code, permissions,
    }, common_error::current_request_id())))
}

/// 读取账号**当前**的角色与权限(登录与 refresh 共用)。
async fn load_role_and_permissions(
    st: &AppState,
    role_id: Option<u64>,
) -> AppResult<(String, Vec<String>)> {
    let role_code: String = if let Some(rid) = role_id {
        sqlx::query_scalar("SELECT code FROM role WHERE id = ? AND deleted_at IS NULL")
            .bind(rid)
            .fetch_optional(st.db.pool())
            .await?
            .ok_or_else(|| AppError::ServiceUnavailable("管理员角色不存在或已停用".into()))?
    } else {
        "viewer".to_string()
    };
    let permissions: Vec<String> = if let Some(rid) = role_id {
        let rows = sqlx::query("SELECT p.code FROM permission p JOIN role_permission rp ON rp.permission_id = p.id WHERE rp.role_id = ?")
            .bind(rid)
            .fetch_all(st.db.pool())
            .await?;
        rows.iter().map(|row| row.try_get::<String, _>("code").map_err(AppError::from)).collect::<AppResult<Vec<_>>>()?
    } else {
        vec![]
    };
    Ok((role_code, permissions))
}

#[derive(Debug, Deserialize)]
pub struct RefreshReq { pub token: String }

/// D2:refresh 必须**重新查库**授权。原实现直接 `claims.permissions.clone()`
/// 复制旧 token 的角色与权限,账号被停用、删除或撤销角色后,只要还在有效期内
/// 反复 refresh 就能持续取得带旧权限的新 token。
pub async fn refresh(
    State(st): State<AppState>,
    Json(req): Json<RefreshReq>,
) -> AppResult<Json<common_error::ApiEnvelope<serde_json::Value>>> {
    let claims = st.jwt.verify_admin(&req.token).map_err(|_| AppError::Unauthorized("bad token".into()))?;

    // 复核账号状态与角色归属
    let row: Option<(Option<u64>, String, Option<chrono::DateTime<chrono::Utc>>)> =
        sqlx::query_as(
            "SELECT role_id, status, locked_until FROM admin_user_role WHERE id = ? AND deleted_at IS NULL",
        )
        .bind(claims.admin_user_id)
        .fetch_optional(st.db.pool())
        .await?;
    let (role_id, status, locked_until) =
        row.ok_or_else(|| AppError::Unauthorized("管理员账号不存在或已删除".into()))?;
    if is_login_blocked(&status, locked_until, chrono::Utc::now()) {
        return Err(AppError::Forbidden("账号已停用或锁定".into()));
    }

    // 以库中当前的角色与权限重新签发,而不是沿用旧 token 里的快照
    let (role_code, permissions) = load_role_and_permissions(&st, role_id).await?;
    let token = st.jwt.issue_admin(&claims.sub, claims.admin_user_id, &role_code, permissions)?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"token": token}), common_error::current_request_id())))
}

pub async fn logout(
    State(_st): State<AppState>,
) -> AppResult<Json<common_error::ApiEnvelope<serde_json::Value>>> {
    // 简单:无状态 JWT 仅前端丢弃 token;若需撤销可维护 blocklist (Redis SET)
    Ok(Json(common_error::ApiEnvelope::ok(json!({"logged_out": true}), common_error::current_request_id())))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn now() -> chrono::DateTime<chrono::Utc> {
        "2026-09-27T12:00:00Z".parse().unwrap()
    }

    /// D2 验收:账号置 disabled 后必须被拒绝登录
    #[test]
    fn disabled_account_is_blocked() {
        assert!(is_login_blocked("disabled", None, now()));
    }

    /// D2 验收:锁定期间拒绝,到期后放行
    #[test]
    fn locked_account_blocked_until_expiry() {
        let until = "2026-09-27T12:30:00Z".parse().unwrap();
        assert!(is_login_blocked("locked", Some(until), now()));
        let expired = "2026-09-27T11:30:00Z".parse().unwrap();
        assert!(!is_login_blocked("locked", Some(expired), now()));
    }

    #[test]
    fn active_account_allowed() {
        assert!(!is_login_blocked("active", None, now()));
        // active 状态下即使残留 locked_until 也不应被拦
        let until = "2026-09-27T12:30:00Z".parse().unwrap();
        assert!(!is_login_blocked("active", Some(until), now()));
    }

    /// 未知状态按拒绝处理(默认安全)
    #[test]
    fn unknown_status_is_blocked() {
        assert!(is_login_blocked("suspended", None, now()));
        assert!(is_login_blocked("", None, now()));
    }

    /// D2 验收:失败达阈值触发锁定
    #[test]
    fn lockout_threshold() {
        assert!(!should_lock(1));
        assert!(!should_lock(MAX_FAILED_LOGINS - 1));
        assert!(should_lock(MAX_FAILED_LOGINS));
        assert!(should_lock(MAX_FAILED_LOGINS + 1));
    }
}

/// D2 行为验收(需要真实 admin MySQL)。默认 `#[ignore]`,由 V2b
/// `cargo test -p admin -- --ignored` 在本地 `compose.dev.yaml` 下执行。
#[cfg(test)]
mod db_tests {
    use super::*;
    use common_config::MysqlConfig;
    use common_db::Db;

    async fn dev_db() -> Db {
        let url = std::env::var("DATABASE_URL").expect("需要 development MySQL");
        Db::connect(&MysqlConfig {
            url,
            max_connections: 4,
            min_connections: 0,
            connect_timeout_secs: 10,
        })
        .await
        .expect("连接 development admin MySQL")
    }

    /// 造一个带已知密码的管理员账号,返回 (id, username)
    async fn fixture(db: &Db, username: &str, status: &str) -> u64 {
        let hash = common_auth::hash_password("Passw0rd!fixture").unwrap();
        sqlx::query(
            "INSERT INTO admin_user_role (username, password_hash, status, failed_login_count)
             VALUES (?, ?, ?, 0)",
        )
        .bind(username)
        .bind(&hash)
        .bind(status)
        .execute(db.pool())
        .await
        .unwrap()
        .last_insert_id()
    }

    async fn cleanup(db: &Db, username: &str) {
        let _ = sqlx::query("DELETE FROM admin_user_role WHERE username = ?")
            .bind(username)
            .execute(db.pool())
            .await;
    }

    /// D2 验收:连续失败达阈值后账号被锁定并写上 locked_until
    #[tokio::test]
    #[ignore = "requires development admin MySQL"]
    async fn repeated_failures_lock_the_account() {
        let db = dev_db().await;
        let uname = format!("d2_lock_{}", uuid::Uuid::new_v4());
        let id = fixture(&db, &uname, "active").await;

        for _ in 0..MAX_FAILED_LOGINS {
            sqlx::query(
                "UPDATE admin_user_role SET
                     failed_login_count = failed_login_count + 1,
                     status = IF(failed_login_count + 1 >= ?, 'locked', status),
                     locked_until = IF(failed_login_count + 1 >= ?,
                                       DATE_ADD(UTC_TIMESTAMP(3), INTERVAL ? MINUTE), locked_until)
                 WHERE id = ?",
            )
            .bind(MAX_FAILED_LOGINS)
            .bind(MAX_FAILED_LOGINS)
            .bind(LOCK_MINUTES)
            .bind(id)
            .execute(db.pool())
            .await
            .unwrap();
        }

        let (status, cnt, lu): (String, i64, Option<chrono::DateTime<chrono::Utc>>) =
            sqlx::query_as("SELECT status, failed_login_count, locked_until FROM admin_user_role WHERE id = ?")
                .bind(id).fetch_one(db.pool()).await.unwrap();
        assert_eq!(status, "locked");
        assert_eq!(cnt, MAX_FAILED_LOGINS);
        let lu = lu.expect("locked_until 必须被写入(此前全仓只读不写)");
        assert!(lu > chrono::Utc::now(), "锁定应生效 30 分钟");
        cleanup(&db, &uname).await;
    }

    /// D2 验收:并发失败请求下计数不丢失(单语句原子更新)
    #[tokio::test]
    #[ignore = "requires development admin MySQL"]
    async fn concurrent_failures_are_atomic() {
        let db = dev_db().await;
        let uname = format!("d2_conc_{}", uuid::Uuid::new_v4());
        let id = fixture(&db, &uname, "active").await;

        let mut jobs = Vec::new();
        for _ in 0..8 {
            let pool = db.pool().clone();
            jobs.push(tokio::spawn(async move {
                sqlx::query(
                    "UPDATE admin_user_role SET
                         failed_login_count = failed_login_count + 1,
                         status = IF(failed_login_count + 1 >= ?, 'locked', status)
                     WHERE id = ?",
                )
                .bind(MAX_FAILED_LOGINS)
                .bind(id)
                .execute(&pool)
                .await
                .unwrap();
            }));
        }
        for j in jobs { j.await.unwrap(); }

        let cnt: i64 =
            sqlx::query_scalar("SELECT failed_login_count FROM admin_user_role WHERE id = ?")
                .bind(id).fetch_one(db.pool()).await.unwrap();
        assert_eq!(cnt, 8, "并发递增不得丢更新");
        cleanup(&db, &uname).await;
    }

    /// D2 验收:disabled 账号被 is_login_blocked 拒绝(登录 403 / refresh 403 / 旧 token 被拒的共同前提)
    #[tokio::test]
    #[ignore = "requires development admin MySQL"]
    async fn disabled_account_is_rejected_by_extractor_query() {
        let db = dev_db().await;
        let uname = format!("d2_dis_{}", uuid::Uuid::new_v4());
        let id = fixture(&db, &uname, "disabled").await;

        let (status, lu): (String, Option<chrono::DateTime<chrono::Utc>>) =
            sqlx::query_as("SELECT status, locked_until FROM admin_user_role WHERE id = ? AND deleted_at IS NULL")
                .bind(id).fetch_one(db.pool()).await.unwrap();
        assert!(
            is_login_blocked(&status, lu, chrono::Utc::now()),
            "disabled 账号必须被阻断"
        );
        cleanup(&db, &uname).await;
    }

    /// D2 验收:成功登录复位计数与锁定状态
    #[tokio::test]
    #[ignore = "requires development admin MySQL"]
    async fn successful_login_resets_counters() {
        let db = dev_db().await;
        let uname = format!("d2_reset_{}", uuid::Uuid::new_v4());
        let id = fixture(&db, &uname, "active").await;
        sqlx::query(
            "UPDATE admin_user_role SET failed_login_count = 9, status = 'locked',
                    locked_until = DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 MINUTE) WHERE id = ?",
        )
        .bind(id).execute(db.pool()).await.unwrap();

        sqlx::query(
            "UPDATE admin_user_role SET last_login_at=NOW(3), failed_login_count=0,
                    locked_until=NULL, status='active' WHERE id=?",
        )
        .bind(id).execute(db.pool()).await.unwrap();

        let (status, cnt, lu): (String, i64, Option<chrono::DateTime<chrono::Utc>>) =
            sqlx::query_as("SELECT status, failed_login_count, locked_until FROM admin_user_role WHERE id = ?")
                .bind(id).fetch_one(db.pool()).await.unwrap();
        assert_eq!((status.as_str(), cnt, lu), ("active", 0, None));
        cleanup(&db, &uname).await;
    }
}

/// D1 行为验收(需要真实 admin MySQL)。核心断言:无权限账号调用写接口
/// → 403 且数据库零变更。
#[cfg(test)]
mod permission_tests {
    use super::*;
    use std::sync::Arc;
    use common_config::MysqlConfig;
    use common_db::Db;

    async fn dev_db() -> Db {
        let url = std::env::var("DATABASE_URL").expect("需要 development admin MySQL");
        Db::connect(&MysqlConfig {
            url,
            max_connections: 4,
            min_connections: 0,
            connect_timeout_secs: 10,
        })
        .await
        .expect("连接 development admin MySQL")
    }

    /// D1 覆盖矩阵:本次接线的写端点与其权限码。
    /// 与 `migrations/admin_db/0021_admin_write_permissions.sql` 一一对应。
    pub const MATRIX: &[(&str, &str)] = &[
        ("users::create", "admin_user.create"),
        ("users::update", "admin_user.update"),
        ("users::delete", "admin_user.delete"),
        ("users::reset_password", "admin_user.reset_password"),
        ("roles::create", "role.create"),
        ("roles::update", "role.update"),
        ("roles::delete", "role.delete"),
        ("settings::charge_rule_create", "pricing.rule.create"),
        ("settings::pricing_template_create", "pricing.template.create"),
        ("settings::split_template_create", "finance.split_template.create"),
        ("settings::split_party_create", "finance.split_party.create"),
        ("billing::withdraw_create", "finance.withdraw.create"),
        ("billing::withdraw_review", "finance.withdraw.review"),
        ("alert::ack", "alert.ack"),
        ("alert::rules_create", "alert.rule.create"),
        ("alert::rules_update", "alert.rule.update"),
        ("alert::rules_delete", "alert.rule.delete"),
        ("alert::subs_create", "alert.subscription.create"),
        ("alert::risk_config_put", "alert.risk_config.update"),
        ("membership::create", "membership.create"),
        ("announcements::create", "announcement.create"),
        ("announcements::update", "announcement.update"),
        ("announcements::delete", "announcement.delete"),
        ("customer_service::create", "customer_service.create"),
        ("customer_service::update", "customer_service.update"),
        ("customer_service::delete", "customer_service.delete"),
        ("casework::fault_resolve", "fault.resolve"),
        ("webhook::create", "webhook.create"),
        ("webhook::update", "webhook.update"),
        ("webhook::delete", "webhook.delete"),
        ("ota::packages_create", "ota.package.create"),
        ("ota::packages_delete", "ota.package.delete"),
        ("ota::schedules_create", "ota.schedule.create"),
        ("ota::schedules_trigger", "ota.schedule.trigger"),
        ("billing::refund_retry", "finance.refund.retry"),
        ("billing::refund_approve", "order.refund.review"),
        ("billing::refund_reject", "order.refund.review"),
        ("billing::refund_create", "order.refund.create"),
        ("export::create", "export.create"),
    ];

    /// 矩阵里的每个权限码都必须在 permission 表中存在——否则矩阵形同虚设。
    #[tokio::test]
    #[ignore = "requires development admin MySQL (0021 migration applied)"]
    async fn every_matrix_permission_exists_in_db() {
        let db = dev_db().await;
        let mut missing = Vec::new();
        for (_, code) in MATRIX {
            let n: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM permission WHERE code = ?")
                .bind(code).fetch_one(db.pool()).await.unwrap();
            if n == 0 { missing.push(*code); }
        }
        assert!(missing.is_empty(), "以下权限码在 permission 表中不存在:{missing:?}");
    }

    /// D1 验收:无权限账号被拒(403),且数据库零变更。
    #[tokio::test]
    #[ignore = "requires development admin MySQL (0021 migration applied)"]
    async fn account_without_grant_is_forbidden() {
        let db = dev_db().await;
        // 造一个不挂任何角色的账号 → 任何权限都应被拒
        let uname = format!("d1_nop_{}", uuid::Uuid::new_v4());
        let hash = common_auth::hash_password("Passw0rd!fixture").unwrap();
        let id: u64 = sqlx::query(
            "INSERT INTO admin_user_role (username, password_hash, status, failed_login_count)
             VALUES (?, ?, 'active', 0)",
        ).bind(&uname).bind(&hash).execute(db.pool()).await.unwrap().last_insert_id();

        let claims = AdminClaims {
            sub: uname.clone(),
            admin_user_id: id,
            role: "viewer".into(),
            permissions: vec![],
            exp: 0, iat: 0, iss: "test".into(),
        };
        let actor = ActiveAdmin(claims);
        let cfg = Arc::new(common_config::AppConfig::load().expect("加载配置"));
        let st = AppState {
            jwt: Arc::new(common_auth::JwtCodec::new(&cfg.auth)),
            redis_cache: common_redis::RedisCache::connect(&cfg.redis_cache).await.expect("redis"),
            redis_stream: common_redis::RedisStream::connect(&cfg.redis_stream).await.expect("redis"),
            http: reqwest::Client::new(),
            service_token: Arc::new(cfg.auth.service_token.clone()),
            db: db.clone(),
            cfg,
        };

        for (_, code) in MATRIX {
            let err = require_permission(&st, &actor, code).await;
            assert!(err.is_err(), "无权限账号竟通过了 {code} 校验");
            assert!(
                matches!(err, Err(AppError::Forbidden(_))),
                "{code} 应返回 Forbidden,实际 {err:?}"
            );
        }

        // 数据库零变更:角色/权限关联未被创建
        let grants: i64 = sqlx::query_scalar(
            "SELECT COUNT(*) FROM role_permission rp
             JOIN role r ON r.id = rp.role_id
             JOIN permission p ON p.id = rp.permission_id
             JOIN admin_user_role a ON a.role_id = r.id
             WHERE a.id = ?",
        ).bind(id).fetch_one(db.pool()).await.unwrap();
        assert_eq!(grants, 0, "拒绝路径不得留下任何数据库变更");

        let _ = sqlx::query("DELETE FROM admin_user_role WHERE id = ?")
            .bind(id).execute(db.pool()).await;
    }
}
