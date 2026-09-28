//! billing 服务 — Lib 入口
//!
//! 作为 lib crate 暴露纯函数层 / DTO,便于跨服务集成测试与单测。
//! 主入口在 `bin/billing.rs`(见 [[bin]] 配置)。


// 分层与序列化约束(P1a 建立;P5 收口完成,转 deny)
// 说明:配置在仓库根 clippy.toml,级别在这里。repository 层与测试模块各自豁免。
#![deny(
    clippy::disallowed_macros,
    clippy::disallowed_types,
    clippy::disallowed_methods,
)]
pub mod api_types;
pub mod engine;
pub mod quote_pricing;
pub mod metered_pricing;
pub mod split;
