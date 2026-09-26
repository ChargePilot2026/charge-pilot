//! billing 服务 — Lib 入口
//!
//! 作为 lib crate 暴露纯函数层 / DTO,便于跨服务集成测试与单测。
//! 主入口在 `bin/billing.rs`(见 [[bin]] 配置)。

pub mod api_types;
pub mod engine;
pub mod quote_pricing;
pub mod split;
