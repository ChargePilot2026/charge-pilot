//! user 服务 — Lib 入口
//!
//! 暴露 DTO + 类型化客户端 + AppState + 子模块,便于跨服务集成测试与单测。
//! 主入口在 `bin/user.rs`。

// 分层与序列化约束(P1a 建立;P5 user 侧已转 deny)
// 说明:配置在仓库根 clippy.toml,级别在这里。
//   - 生产代码零 `json!`(`disallowed_macros` deny)
//   - `serde_json::Value` 仅限方案 §三 的三类正用途(Stream 载荷 / DB JSON 列
//     原样透出 / 动态 WHERE 拼装),各自文件级豁免已写明理由
//   - `sqlx::query*` 只出现在本域 repository 文件,豁免精确到文件
#![deny(
    clippy::disallowed_macros,
    clippy::disallowed_types,
    clippy::disallowed_methods,
)]
pub mod api_envelope;
pub mod api_types;
pub mod capability;
pub mod clients;
mod services;

use common_auth::JwtCodec;
use common_config::AppConfig;
use common_db::Db;
use common_redis::{RedisCache, RedisStream};
use std::sync::Arc;

// ===================== 按域转出(保持搬迁前的 crate 短名) =====================
//
// handler / usecase 的实现全部落在 `capability/<domain>/` 下;这里只做再导出,
// 让路由表、测试与跨模块调用不必因为目录搬迁而大面积改路径。
// 路由注册与 handler 函数名**一字未改**。

// order
pub use capability::order::{
    api, charge_end, charge_fee, charge_start, checkout, dashboard, order_events, orders, outbox,
    payment, payment_receipt, prepay, quote_confirmation, stream_consumer,
};
// wallet
pub use capability::wallet::wallet as wallet;
pub use capability::wallet::{
    wallet_reads, wallet_recharge, wallet_refund, wallet_risk_release,
};
// coupon / invoice / casework / station / identity
pub use capability::casework::casework;
pub use capability::coupon::{coupon, coupon_admin};
pub use capability::identity::{login, profile, session};
pub use capability::invoice::invoice;
pub use capability::refund::{
    manual_refund, refund, refund_execution, refund_result, refund_review,
};
pub use capability::station::station;

#[derive(Clone)]
pub struct AppState {
    pub cfg: Arc<AppConfig>,
    /// P3:裸 `Db` 已从 AppState 移除,见 `services.rs`。
    pub services: services::UserServices,
    pub redis_cache: RedisCache,
    pub redis_stream: RedisStream,
    pub jwt: Arc<JwtCodec>,
    pub http: reqwest::Client,
    pub service_token: Arc<String>,
}

impl std::ops::Deref for AppState {
    type Target = services::UserServices;
    fn deref(&self) -> &Self::Target {
        &self.services
    }
}
