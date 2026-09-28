//! **order 域** — 充电订单、预付下单、设备启动/结束确认、实结计费、支付回调、订单事件。
//!
//! 分层约定(方案 §三):
//! - `domain.rs`   : 纯逻辑,零 I/O,可脱离环境单测
//! - 其余 `*.rs`   : 搬迁前的原子单元,**内容逐字保留**(SQL 文本、判定顺序、
//!                  错误文案、事务边界均未动),只是归档到了本目录下
//!
//! `sqlx::query*` 的豁免按文件给出并写明理由,不设 crate 级豁免。

pub mod api;
pub mod charge_end;
pub mod charge_fee;
pub mod charge_start;
pub mod checkout;
pub mod dashboard;
pub mod domain;
pub mod order_events;
pub mod orders;
pub mod outbox;
pub mod payment;
pub mod payment_receipt;
pub mod prepay;
pub mod quote_confirmation;
pub mod stream_consumer;
