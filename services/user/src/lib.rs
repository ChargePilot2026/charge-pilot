//! user 服务 — Lib 入口
//!
//! 暴露 DTO + 类型化客户端 + AppState + 子模块,便于跨服务集成测试与单测。
//! 主入口在 `bin/user.rs`。


// 分层与序列化约束(P1a 建立;随 P3 逐服务迁移完成转 deny)
// 说明:配置在仓库根 clippy.toml,级别在这里。测试模块豁免。
#![allow(
    clippy::disallowed_macros,
    clippy::disallowed_types,
    clippy::disallowed_methods,
)]
pub mod api;
pub mod checkout;
mod charge_start;
mod charge_end;
mod charge_fee;
mod refund_result;
mod refund_execution;
mod refund_review;
mod manual_refund;
mod prepay;
mod wallet_refund;
pub mod quote_confirmation;
pub mod login;
pub mod session;
pub mod profile;
pub mod api_envelope;
pub mod api_types;
pub mod clients;
pub mod payment;
mod payment_receipt;
mod outbox;
pub mod refund;
pub mod wallet;
pub mod wallet_reads;
pub mod coupon;
pub mod coupon_admin;
pub mod invoice;
pub mod station;
pub mod stream_consumer;
pub mod orders;
pub mod order_events;
pub mod casework;
pub mod dashboard;

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

mod wallet_recharge;

mod wallet_risk_release;
