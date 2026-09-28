//! admin 服务 — Lib 入口
//!
//! 暴露 DTO + 路径常量 + AppState,便于跨服务集成测试与单测。
//! 主入口在 `bin/admin.rs`。


// 分层与序列化约束(P1a 建立;P5 admin 已完成迁移)
// 说明:配置在仓库根 clippy.toml,级别在这里。
// 生产代码禁 SQL / `json!`;豁免只在 `repository_sql.rs` 与少数具名函数上。
#![deny(
    clippy::disallowed_macros,
    clippy::disallowed_types,
    clippy::disallowed_methods,
)]
pub mod capability;
pub mod api;
pub mod api_types;
pub mod clients;
pub mod services;
pub mod password;
pub mod stream_consumer;
pub mod static_serve;

/// 鉴权与账号:HTTP handler 入口在 `capability::identity`,
/// 这里只是**历史路径**的兼容再导出,新代码请直接用
/// `crate::capability::identity::{ActiveAdmin, require_permission, …}`。
pub mod auth {
    pub use crate::capability::identity::{
        require_permission, require_permission_by_id, ActiveAdmin, LoginReq, LoginResp,
    };
}

use common_auth::JwtCodec;
use common_config::AppConfig;
use common_db::Db;
use common_redis::{RedisCache, RedisStream};
use std::sync::Arc;

#[derive(Clone)]
pub struct AppState {
    pub cfg: Arc<AppConfig>,
    /// P3:裸 `Db` 已从 AppState 移除,见 `services.rs`。
    pub services: services::AdminServices,
    pub redis_cache: RedisCache,
    pub redis_stream: RedisStream,
    pub jwt: Arc<JwtCodec>,
    pub http: reqwest::Client,
    pub service_token: Arc<String>,
}

/// `AppState` 直接 `Deref` 到能力域集合,于是 `st.identity` / `st.finance` 这类
/// 字段访问成立。`cfg` / `redis_*` / `jwt` / `http` 等自身字段优先级更高,
/// 两者不冲突。
impl std::ops::Deref for AppState {
    type Target = services::AdminServices;
    fn deref(&self) -> &Self::Target {
        &self.services
    }
}
