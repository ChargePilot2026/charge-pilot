//! admin 服务 — Lib 入口
//!
//! 暴露 DTO + 路径常量 + AppState,便于跨服务集成测试与单测。
//! 主入口在 `bin/admin.rs`。


// 分层与序列化约束(P1a 建立;随 P3 逐服务迁移完成转 deny)
// 说明:配置在仓库根 clippy.toml,级别在这里。测试模块豁免。
#![allow(
    clippy::disallowed_macros,
    clippy::disallowed_types,
    clippy::disallowed_methods,
)]
pub mod api;
pub mod api_types;
pub mod clients;
pub mod auth;
pub mod billing;
pub mod ota;
pub mod services;
pub mod password;
pub mod webhook;
pub mod alert;
pub mod stream_consumer;
mod refund_task;
pub mod static_serve;

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
