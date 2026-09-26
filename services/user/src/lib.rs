//! user 服务 — Lib 入口
//!
//! 暴露 DTO + 类型化客户端 + AppState + 子模块,便于跨服务集成测试与单测。
//! 主入口在 `bin/user.rs`。

pub mod api;
pub mod checkout;
pub mod quote_confirmation;
pub mod login;
pub mod session;
pub mod profile;
pub mod api_envelope;
pub mod api_types;
pub mod clients;
pub mod payment;
pub mod refund;
pub mod wallet;
pub mod wallet_reads;
pub mod coupon;
pub mod invoice;
pub mod station;
pub mod stream_consumer;
pub mod wechat;
pub mod repo;
pub mod orders;
pub mod order_events;

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
