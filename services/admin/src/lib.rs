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
    pub db: Db,
    pub redis_cache: RedisCache,
    pub redis_stream: RedisStream,
    pub jwt: Arc<JwtCodec>,
    pub http: reqwest::Client,
    pub service_token: Arc<String>,
}
