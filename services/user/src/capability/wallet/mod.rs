//! **wallet 域** — 钱包余额/流水/充值/退款申请/风控冻结与解冻。
//!
//! `mod_entry.rs` 是搬迁前的 `wallet.rs`(路由 handler)。
//! 其余 `*.rs` 为逐字保留的原子单元;SQL 豁免按文件给出并写明理由。

pub mod wallet;
pub mod wallet_reads;
pub mod wallet_recharge;
pub mod wallet_refund;
pub mod wallet_risk_release;
