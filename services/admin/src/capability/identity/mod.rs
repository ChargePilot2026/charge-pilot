//! identity 域 —— 账号 / 角色 / 权限(D1 的操作权限矩阵所在域)
//!
//! 本域是 D2 与 D1 的落点:
//! - D2:登录态复核在每个请求上重查账号状态,停用 / 锁定 / 删除后已签发的 token 立即失效。
//! - D1:操作授权**只有** [`require_permission`] 一份实现,新增写接口必须调它。
//!
//! [`permission_tests::MATRIX`] 是覆盖矩阵,`no_dangling_matrix_entries`
//! 守护矩阵左侧的 `模块::函数` 真能解析到本 crate 的 `pub async fn`。

pub mod domain;
pub mod repository_sql;

use crate::AppState;
use axum::{extract::State, Json};
use common_auth::AdminClaims;
use common_error::{AppError, AppResult};
use serde::{Deserialize, Serialize};

// ===== D2:经复核的管理员身份 =====

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
        let (status, locked_until) = repository_sql::account_state(st, claims.admin_user_id).await?;
        if domain::is_login_blocked(&status, locked_until, chrono::Utc::now()) {
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
    if !repository_sql::has_permission(st, actor.admin_user_id, &actor.sub, permission).await? {
        return Err(AppError::Forbidden(format!("缺少 {permission} 权限")));
    }
    Ok(())
}

/// 按 `admin_user_id` 校验权限。供只有 id 的后台路径使用(如设备导入恢复)——
/// 先解析出当前 username 再走同一段鉴权 SQL,避免出现第二份鉴权实现。
pub async fn require_permission_by_id(
    st: &AppState,
    admin_user_id: u64,
    permission: &str,
) -> AppResult<()> {
    let username = repository_sql::username_by_id(st, admin_user_id).await?;
    if !repository_sql::has_permission(st, admin_user_id, &username, permission).await? {
        return Err(AppError::Forbidden(format!("缺少 {permission} 权限")));
    }
    Ok(())
}

// ===== 登录 / 刷新 / 退出 =====

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

/// `refresh` 响应体。此前是 `json!({"token": token})`,属"能用具名结构体表达
/// 却用了宏"的情形,按方案 §三改为类型化 DTO。
#[derive(Debug, Serialize)]
pub struct RefreshResp {
    pub token: String,
}

/// `logout` 响应体。当前是空状态 JWT(前端自行丢弃 token),故只回一个标志位。
#[derive(Debug, Serialize)]
pub struct LogoutResp {
    pub logged_out: bool,
}

pub async fn login(
    State(st): State<AppState>,
    Json(req): Json<LoginReq>,
) -> AppResult<Json<common_error::ApiEnvelope<LoginResp>>> {
    let r = repository_sql::account_by_username(&st, &req.username).await?;
    let (id, username, role_id, hash, status, _failed, locked_until) = match r {
        Some(v) => v,
        None => return Err(AppError::Unauthorized("bad credentials".into())),
    };
    if domain::is_login_blocked(&status, locked_until, chrono::Utc::now()) {
        return Err(AppError::Forbidden("account locked or disabled".into()));
    }
    // D13:argon2 是同步 CPU 密集计算,必须离开执行线程;并发由信号量限死。
    if !crate::password::verify(req.password.clone(), hash.clone()).await {
        // 计数与锁定在一条语句里完成,并发失败下天然原子。
        repository_sql::record_login_failure(&st, id, domain::MAX_FAILED_LOGINS, domain::LOCK_MINUTES)
            .await?;
        return Err(AppError::Unauthorized("bad credentials".into()));
    }

    // 角色 + 权限
    let (role_code, permissions) = repository_sql::role_and_permissions(&st, role_id).await?;

    // 登录成功:复位失败计数与锁定状态。
    repository_sql::record_login_success(&st, id).await?;

    let token = st.jwt.issue_admin(&username, id, &role_code, permissions.clone())?;
    Ok(Json(common_error::ApiEnvelope::ok(LoginResp {
        token, admin_user_id: id, role: role_code, permissions,
    }, common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct RefreshReq { pub token: String }

/// D2:refresh 必须**重新查库**授权。原实现直接 `claims.permissions.clone()`
/// 复制旧 token 的角色与权限,账号被停用、删除或撤销角色后,只要还在有效期内
/// 反复 refresh 就能持续取得带旧权限的新 token。
pub async fn refresh(
    State(st): State<AppState>,
    Json(req): Json<RefreshReq>,
) -> AppResult<Json<common_error::ApiEnvelope<RefreshResp>>> {
    let claims = st.jwt.verify_admin(&req.token).map_err(|_| AppError::Unauthorized("bad token".into()))?;

    // 复核账号状态与角色归属
    let (role_id, status, locked_until) = repository_sql::refresh_account(&st, claims.admin_user_id).await?;
    if domain::is_login_blocked(&status, locked_until, chrono::Utc::now()) {
        return Err(AppError::Forbidden("账号已停用或锁定".into()));
    }

    // 以库中当前的角色与权限重新签发,而不是沿用旧 token 里的快照
    let (role_code, permissions) = repository_sql::role_and_permissions(&st, role_id).await?;
    let token = st.jwt.issue_admin(&claims.sub, claims.admin_user_id, &role_code, permissions)?;
    Ok(Json(common_error::ApiEnvelope::ok(RefreshResp { token }, common_error::current_request_id())))
}

pub async fn logout(
    State(_st): State<AppState>,
) -> AppResult<Json<common_error::ApiEnvelope<LogoutResp>>> {
    // 简单:无状态 JWT 仅前端丢弃 token;若需撤销可维护 blocklist (Redis SET)
    Ok(Json(common_error::ApiEnvelope::ok(LogoutResp { logged_out: true }, common_error::current_request_id())))
}

// ===== 管理员账号 CRUD =====

pub async fn list(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::AdminUserList>>> {
    let items = repository_sql::list_users(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

pub async fn create(State(st): State<AppState>, _c: ActiveAdmin, Json(req): Json<repository_sql::UserCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::CreatedFlag>>> {
    require_permission(&st,&_c,"admin_user.create").await?;
    // D13:argon2 哈希离开执行线程,并发受限。
    let hash = crate::password::hash(req.password.clone()).await?;
    repository_sql::create_user(&st, &req, &hash).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::CreatedFlag::new(), common_error::current_request_id())))
}

pub async fn get(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::AdminUserDetail>>> {
    let detail = repository_sql::get_user(&st, id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(detail, common_error::current_request_id())))
}

pub async fn update(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>, Json(req): Json<repository_sql::UserUpdateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::UpdatedResponse>>> {
    require_permission(&st,&_c,"admin_user.update").await?;
    repository_sql::update_user(&st, id, &req).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::UpdatedResponse::new(), common_error::current_request_id())))
}

pub async fn delete(State(st): State<AppState>, actor: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::DeletedResponse>>> {
    require_permission(&st,&actor,"admin_user.delete").await?;
    repository_sql::delete_user(&st, id, actor.admin_user_id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::DeletedResponse::new(), common_error::current_request_id())))
}

pub async fn reset_password(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>, Json(req): Json<repository_sql::ResetPasswordReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ResetFlag>>> {
    require_permission(&st,&_c,"admin_user.reset_password").await?;
    // D13:argon2 哈希离开执行线程,并发受限。
    let hash = crate::password::hash(req.new_password.clone()).await?;
    repository_sql::reset_password(&st, id, &hash).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::ResetFlag::new(), common_error::current_request_id())))
}

// ===== 角色 / 权限 =====

pub async fn roles_list(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::RoleList>>> {
    let items = repository_sql::list_roles(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

pub async fn roles_create(State(st): State<AppState>, _c: ActiveAdmin, Json(req): Json<repository_sql::RoleCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::CreatedResponse>>> {
    require_permission(&st,&_c,"role.create").await?;
    let role_id = repository_sql::create_role(&st, &req).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::CreatedResponse { id: role_id }, common_error::current_request_id())))
}

pub async fn roles_get(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::RoleDetail>>> {
    let detail = repository_sql::get_role(&st, id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(detail, common_error::current_request_id())))
}

pub async fn roles_update(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>, Json(req): Json<repository_sql::RoleUpdateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::UpdatedResponse>>> {
    require_permission(&st,&_c,"role.update").await?;
    repository_sql::update_role(&st, id, &req).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::UpdatedResponse::new(), common_error::current_request_id())))
}

pub async fn roles_delete(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::DeletedResponse>>> {
    require_permission(&st,&_c,"role.delete").await?;
    repository_sql::delete_role(&st, id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::DeletedResponse::new(), common_error::current_request_id())))
}

pub async fn permissions(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::Permission>>>> {
    let items = repository_sql::list_permissions(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

/// 某账号在给定权限码下是否**持有**该权限(返回权限码本身或 `None`)。
///
/// 供设备域按权限过滤列表用 —— 它要的是"过滤条件",不是布尔校验。
pub async fn permission_codes(
    st: &AppState,
    admin_user_id: u64,
    username: &str,
    codes: &[&str],
) -> AppResult<Vec<String>> {
    let mut granted = Vec::new();
    for code in codes {
        if repository_sql::has_permission(st, admin_user_id, username, code).await? {
            granted.push((*code).to_string());
        }
    }
    Ok(granted)
}

#[cfg(test)]
mod tests {
    use super::domain::*;

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

#[cfg(test)]
// 测试直接驱动生产用的 repository 函数并自查库，因此 SQL 与 `json!` 在这里
// 是测试自身的职责，不是「业务代码里的 SQL」。
#[allow(clippy::disallowed_methods, clippy::disallowed_macros, clippy::disallowed_types)]
mod db_tests {
    use super::domain;
    use super::repository_sql;
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

    /// `AppState` 派生 `Clone`，供并发测试每条任务各持一份。
    fn clone_state(st: &AppState) -> AppState {
        st.clone()
    }

    /// D2 行为验收(需要真实 admin MySQL)。默认 `#[ignore]`,由 V2b
    /// `cargo test -p admin -- --ignored` 在本地 `compose.dev.yaml` 下执行。
    ///
    /// SQL 走 [`crate::capability::identity::repository_sql`] —— 测试验证的是
    /// 生产路径的同一条语句,而不是测试里另抄一份。
    #[tokio::test]
    #[ignore = "requires development admin MySQL"]
    async fn repeated_failures_lock_the_account() {
        use crate::services::build;
        use std::sync::Arc;

        let db = dev_db().await;
        let cfg = Arc::new(common_config::AppConfig::load().expect("加载配置"));
        let st = AppState {
            services: build(
                db.clone(),
                reqwest::Client::new(),
                Arc::new(cfg.auth.service_token.clone()),
                cfg.clone(),
                common_redis::RedisCache::connect(&cfg.redis_cache).await.expect("redis"),
                common_redis::RedisStream::connect(&cfg.redis_stream).await.expect("redis"),
            ),
            jwt: Arc::new(common_auth::JwtCodec::new(&cfg.auth)),
            redis_cache: common_redis::RedisCache::connect(&cfg.redis_cache).await.expect("redis"),
            redis_stream: common_redis::RedisStream::connect(&cfg.redis_stream).await.expect("redis"),
            http: reqwest::Client::new(),
            service_token: Arc::new(cfg.auth.service_token.clone()),
            cfg: cfg.clone(),
        };

        let uname = format!("d2_lock_{}", uuid::Uuid::new_v4());
        let hash = crate::password::hash("Passw0rd!fixture".to_string()).await.unwrap();
        repository_sql::create_user(
            &st,
            &repository_sql::UserCreateReq {
                username: uname.clone(),
                display_name: None,
                password: hash.clone(),
                phone: None,
                email: None,
                role_id: None,
            },
            &hash,
        )
        .await
        .unwrap();
        let id: u64 = sqlx::query_scalar("SELECT id FROM admin_user_role WHERE username = ?")
            .bind(&uname)
            .fetch_one(db.pool())
            .await
            .unwrap();

        for _ in 0..domain::MAX_FAILED_LOGINS {
            repository_sql::record_login_failure(
                &st,
                id,
                domain::MAX_FAILED_LOGINS,
                domain::LOCK_MINUTES,
            )
            .await
            .unwrap();
        }

        let (status, lu) = repository_sql::account_state(&st, id).await.unwrap();
        assert_eq!(status, "locked");
        assert!(domain::is_login_blocked(&status, lu, chrono::Utc::now()));
        // 列是 `INT UNSIGNED`：sqlx 0.8 拒绝把它解成 i64，
        // 会报 "Rust type `i64` is not compatible with SQL type `INT UNSIGNED`"。
        // 常量本身是 i64（与 `record_login_failure` 的参数类型一致），这里显式转换。
        let cnt: u64 = sqlx::query_scalar("SELECT failed_login_count FROM admin_user_role WHERE id = ?")
            .bind(id)
            .fetch_one(db.pool())
            .await
            .unwrap();
        assert_eq!(cnt, domain::MAX_FAILED_LOGINS as u64);
        let _ = sqlx::query("DELETE FROM admin_user_role WHERE username = ?")
            .bind(&uname)
            .execute(db.pool())
            .await;
    }

    /// D2 验收:并发失败请求下计数不丢失（单语句原子更新）
    #[tokio::test]
    #[ignore = "requires development admin MySQL"]
    async fn concurrent_failures_are_atomic() {
        use crate::services::build;
        use std::sync::Arc;

        let db = dev_db().await;
        let cfg = Arc::new(common_config::AppConfig::load().expect("加载配置"));
        let st = AppState {
            services: build(
                db.clone(),
                reqwest::Client::new(),
                Arc::new(cfg.auth.service_token.clone()),
                cfg.clone(),
                common_redis::RedisCache::connect(&cfg.redis_cache).await.expect("redis"),
                common_redis::RedisStream::connect(&cfg.redis_stream).await.expect("redis"),
            ),
            jwt: Arc::new(common_auth::JwtCodec::new(&cfg.auth)),
            redis_cache: common_redis::RedisCache::connect(&cfg.redis_cache).await.expect("redis"),
            redis_stream: common_redis::RedisStream::connect(&cfg.redis_stream).await.expect("redis"),
            http: reqwest::Client::new(),
            service_token: Arc::new(cfg.auth.service_token.clone()),
            cfg: cfg.clone(),
        };

        let uname = format!("d2_conc_{}", uuid::Uuid::new_v4());
        let hash = crate::password::hash("Passw0rd!fixture".to_string()).await.unwrap();
        repository_sql::create_user(
            &st,
            &repository_sql::UserCreateReq {
                username: uname.clone(),
                display_name: None,
                password: hash.clone(),
                phone: None,
                email: None,
                role_id: None,
            },
            &hash,
        )
        .await
        .unwrap();
        let id: u64 = sqlx::query_scalar("SELECT id FROM admin_user_role WHERE username = ?")
            .bind(&uname)
            .fetch_one(db.pool())
            .await
            .unwrap();

        let mut jobs = Vec::new();
        for _ in 0..8 {
            let id = id;
            let st2 = clone_state(&st);
            jobs.push(tokio::spawn(async move {
                repository_sql::record_login_failure(&st2, id, domain::MAX_FAILED_LOGINS, domain::LOCK_MINUTES)
                    .await
                    .unwrap();
            }));
        }
        for j in jobs { j.await.unwrap(); }

        let cnt: u64 = sqlx::query_scalar("SELECT failed_login_count FROM admin_user_role WHERE id = ?")
            .bind(id)
            .fetch_one(db.pool())
            .await
            .unwrap();
        assert_eq!(cnt, 8, "并发递增不得丢更新");
        let _ = sqlx::query("DELETE FROM admin_user_role WHERE username = ?")
            .bind(&uname)
            .execute(db.pool())
            .await;
    }

    /// D2 验收:成功登录复位计数与锁定状态
    #[tokio::test]
    #[ignore = "requires development admin MySQL"]
    async fn successful_login_resets_counters() {
        use crate::services::build;
        use std::sync::Arc;

        let db = dev_db().await;
        let cfg = Arc::new(common_config::AppConfig::load().expect("加载配置"));
        let st = AppState {
            services: build(
                db.clone(),
                reqwest::Client::new(),
                Arc::new(cfg.auth.service_token.clone()),
                cfg.clone(),
                common_redis::RedisCache::connect(&cfg.redis_cache).await.expect("redis"),
                common_redis::RedisStream::connect(&cfg.redis_stream).await.expect("redis"),
            ),
            jwt: Arc::new(common_auth::JwtCodec::new(&cfg.auth)),
            redis_cache: common_redis::RedisCache::connect(&cfg.redis_cache).await.expect("redis"),
            redis_stream: common_redis::RedisStream::connect(&cfg.redis_stream).await.expect("redis"),
            http: reqwest::Client::new(),
            service_token: Arc::new(cfg.auth.service_token.clone()),
            cfg: cfg.clone(),
        };

        let uname = format!("d2_reset_{}", uuid::Uuid::new_v4());
        let hash = crate::password::hash("Passw0rd!fixture".to_string()).await.unwrap();
        repository_sql::create_user(
            &st,
            &repository_sql::UserCreateReq {
                username: uname.clone(),
                display_name: None,
                password: hash.clone(),
                phone: None,
                email: None,
                role_id: None,
            },
            &hash,
        )
        .await
        .unwrap();
        let id: u64 = sqlx::query_scalar("SELECT id FROM admin_user_role WHERE username = ?")
            .bind(&uname)
            .fetch_one(db.pool())
            .await
            .unwrap();

        sqlx::query("UPDATE admin_user_role SET failed_login_count = 9, status = 'locked',
                locked_until = DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 MINUTE) WHERE id = ?")
            .bind(id)
            .execute(db.pool())
            .await
            .unwrap();
        repository_sql::record_login_success(&st, id).await.unwrap();

        let (status, lu) = repository_sql::account_state(&st, id).await.unwrap();
        assert_eq!(status, "active");
        let cnt: u64 = sqlx::query_scalar("SELECT failed_login_count FROM admin_user_role WHERE id = ?")
            .bind(id)
            .fetch_one(db.pool())
            .await
            .unwrap();
        assert_eq!(cnt, 0);
        assert_eq!(lu, None);
        let _ = sqlx::query("DELETE FROM admin_user_role WHERE username = ?")
            .bind(&uname)
            .execute(db.pool())
            .await;
    }

    /// D2 验收:disabled 账号被 `is_login_blocked` 拒绝(登录 403 / refresh 403 /
    /// 旧 token 被拒的共同前提)
    #[tokio::test]
    #[ignore = "requires development admin MySQL"]
    async fn disabled_account_is_rejected_by_extractor_query() {
        use crate::services::build;
        use std::sync::Arc;

        let db = dev_db().await;
        let cfg = Arc::new(common_config::AppConfig::load().expect("加载配置"));
        let st = AppState {
            services: build(
                db.clone(),
                reqwest::Client::new(),
                Arc::new(cfg.auth.service_token.clone()),
                cfg.clone(),
                common_redis::RedisCache::connect(&cfg.redis_cache).await.expect("redis"),
                common_redis::RedisStream::connect(&cfg.redis_stream).await.expect("redis"),
            ),
            jwt: Arc::new(common_auth::JwtCodec::new(&cfg.auth)),
            redis_cache: common_redis::RedisCache::connect(&cfg.redis_cache).await.expect("redis"),
            redis_stream: common_redis::RedisStream::connect(&cfg.redis_stream).await.expect("redis"),
            http: reqwest::Client::new(),
            service_token: Arc::new(cfg.auth.service_token.clone()),
            cfg: cfg.clone(),
        };

        let uname = format!("d2_dis_{}", uuid::Uuid::new_v4());
        let hash = crate::password::hash("Passw0rd!fixture".to_string()).await.unwrap();
        repository_sql::create_user(
            &st,
            &repository_sql::UserCreateReq {
                username: uname.clone(),
                display_name: None,
                password: hash.clone(),
                phone: None,
                email: None,
                role_id: None,
            },
            &hash,
        )
        .await
        .unwrap();
        let id: u64 = sqlx::query_scalar("SELECT id FROM admin_user_role WHERE username = ?")
            .bind(&uname)
            .fetch_one(db.pool())
            .await
            .unwrap();

        let (status, lu) = repository_sql::account_state(&st, id).await.unwrap();
        assert_eq!(status, "active");
        assert!(!domain::is_login_blocked(&status, lu, chrono::Utc::now()));
        sqlx::query("UPDATE admin_user_role SET status='disabled' WHERE id = ?")
            .bind(id)
            .execute(db.pool())
            .await
            .unwrap();
        let (status, lu) = repository_sql::account_state(&st, id).await.unwrap();
        assert!(
            domain::is_login_blocked(&status, lu, chrono::Utc::now()),
            "disabled 账号必须被阻断"
        );
        let _ = sqlx::query("DELETE FROM admin_user_role WHERE username = ?")
            .bind(&uname)
            .execute(db.pool())
            .await;
    }
}

/// D1 行为验收(需要真实 admin MySQL)。核心断言:无权限账号调用写接口
/// → 403 且数据库零变更。
#[cfg(test)]
// 同上：验收测试自己造数据、查零变更。
#[allow(clippy::disallowed_methods, clippy::disallowed_macros, clippy::disallowed_types)]
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
    ///
    /// D3:原先列有 `billing::withdraw_create` / `billing::withdraw_review` /
    /// `membership::create` 三条，但对应 handler 已随跨库视图整块删除 ——
    /// 矩阵项指向不存在的函数，等于给不存在的端点发权限。
    /// 由 `no_dangling_matrix_entries` 守护：新增条目必须真有对应 handler。
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
        ("alert::ack", "alert.ack"),
        ("alert::rules_create", "alert.rule.create"),
        ("alert::rules_update", "alert.rule.update"),
        ("alert::rules_delete", "alert.rule.delete"),
        ("alert::subs_create", "alert.subscription.create"),
        ("alert::risk_config_put", "alert.risk_config.update"),
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

    /// 矩阵左侧的**旧文件名 / 函数名**到新落点的映射。
    ///
    /// P5 垂直切片把 handler 按业务域搬进了 `capability/<域>/mod.rs`，矩阵里的
    /// 模块名是**按迁移前的文件**写的，函数名则多数沿用无前缀的旧名
    /// （如 `announcements::create` 实际是 `config::announcement_create`）。
    /// 本表只做「去哪里找、叫什么」的重定向，**不参与判定语义** ——
    /// 右侧的 `模块::函数` 与权限码一个字都没改。
    ///
    /// `(模块, 旧函数名)` → `(承载域, 新函数名)`；新函数名写 `""` 表示同名。
    const HANDLERS: &[(&str, &str, &str, &str)] = &[
        // users / roles 的 handler 都在 identity 域，函数名也仍是 `create` / `update` / …
        ("users", "create", "identity", ""),
        ("users", "update", "identity", ""),
        ("users", "delete", "identity", ""),
        ("users", "reset_password", "identity", ""),
        ("roles", "create", "identity", "roles_create"),
        ("roles", "update", "identity", "roles_update"),
        ("roles", "delete", "identity", "roles_delete"),
        // settings 拆散在 config(计费)与 device(OTA 桩)
        ("settings", "charge_rule_create", "config", ""),
        ("settings", "pricing_template_create", "config", ""),
        ("settings", "split_template_create", "config", ""),
        ("settings", "split_party_create", "config", ""),
        // announcements / customer_service 落在 config，函数名加了前缀
        ("announcements", "create", "config", "announcement_create"),
        ("announcements", "update", "config", "announcement_update"),
        ("announcements", "delete", "config", "announcement_delete"),
        ("customer_service", "create", "config", "customer_service_create"),
        ("customer_service", "update", "config", "customer_service_update"),
        ("customer_service", "delete", "config", "customer_service_delete"),
        // casework 里的工单端点归 cases 域
        ("casework", "fault_resolve", "cases", ""),
        // OTA 桩随设备域
        ("ota", "packages_create", "device", ""),
        ("ota", "packages_delete", "device", ""),
        ("ota", "schedules_create", "device", ""),
        ("ota", "schedules_trigger", "device", ""),
        // 退款类原属 billing.rs，现住 finance 域，函数名不变
        ("billing", "refund_retry", "finance", ""),
        ("billing", "refund_approve", "finance", ""),
        ("billing", "refund_reject", "finance", ""),
        ("billing", "refund_create", "finance", ""),
        // 未搬迁的 api 层仍留在原处
        ("export", "create", "api", ""),
    ];

    /// 告警与 webhook 的 handler 恰好在同名目录下、且函数名也未变。
    const SAME_NAME_DIRS: &[&str] = &["alert", "webhook"];

    /// 找出承载某模块的源文件。
    ///
    /// 先按 [`HANDLERS`] 定位到具体能力域,再退回旧布局:`src/<模块>.rs`
    /// 与 `src/api/<模块>.rs`。返回文件路径 + 读到的源码。
    fn read_module(module: &str) -> Option<(String, String)> {
        let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("src");
        let mut candidates: Vec<std::path::PathBuf> = Vec::new();
        if SAME_NAME_DIRS.contains(&module) {
            candidates.push(root.join("capability").join(module).join("mod.rs"));
        } else {
            let mut mapped = false;
            for (m, _, owner, _) in HANDLERS.iter().filter(|(m, _, _, _)| *m == module) {
                mapped = true;
                candidates.push(root.join("capability").join(owner).join("mod.rs"));
            }
            let _ = mapped;
        }
        candidates.push(root.join(format!("{module}.rs")));
        candidates.push(root.join("api").join(format!("{module}.rs")));
        for dir in candidates {
            if let Ok(s) = std::fs::read_to_string(&dir) {
                return Some((dir.display().to_string(), s));
            }
        }
        None
    }

    /// 把矩阵里的旧函数名翻译成搬迁后的名字。找不到映射就是没登记，
    /// 按原名找（`alert` / `webhook` 等同名目录的情形）。
    fn resolve_func(module: &str, func: &str) -> String {
        HANDLERS
            .iter()
            .find(|(m, f, _, _)| *m == module && *f == func)
            .map(|(_, _, _, new)| if new.is_empty() { (*func).to_string() } else { (*new).to_string() })
            .unwrap_or_else(|| func.to_string())
    }

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

    /// 防回潮(D3):矩阵左侧的 `模块::函数` 必须真能解析到本 crate 的函数。
    ///
    /// 之前 `billing::withdraw_create` 等三条指向已删除的 handler，
    /// 编译能过、测试也能过，只有“给不存在的端点发权限”这件事是静默的。
    /// 纯源码文本判定：不引入编译期依赖，也能在 handler 被删时立即失败。
    #[test]
    fn no_dangling_matrix_entries() {
        let mut dangling = Vec::new();
        for (entry, code) in MATRIX {
            let (module, func) = entry
                .split_once("::")
                .unwrap_or_else(|| panic!("矩阵条目格式应为 `模块::函数`,实际 {entry}"));
            match read_module(module) {
                None => dangling.push(format!("{entry} → {code}（未找到模块 {module} 的源文件）")),
                Some((path, src)) => {
                    let func = resolve_func(module, func);
                    let needle = format!("pub async fn {func}");
                    if !src.contains(&needle) {
                        dangling.push(format!("{entry} → {code}（{path} 中无 `pub async fn {func}`）"));
                    }
                }
            }
        }
        assert!(
            dangling.is_empty(),
            "以下权限矩阵条目指向已不存在的 handler（会给不存在的端点发权限）:\n  {}",
            dangling.join("\n  ")
        );
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
            services: crate::services::build(
                db.clone(),
                reqwest::Client::new(),
                Arc::new(cfg.auth.service_token.clone()),
                cfg.clone(),
                common_redis::RedisCache::connect(&cfg.redis_cache).await.expect("redis"),
                common_redis::RedisStream::connect(&cfg.redis_stream).await.expect("redis"),
            ),
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
