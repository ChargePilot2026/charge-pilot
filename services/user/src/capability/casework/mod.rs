//! **casework 域** — 客服工单(反馈)与设备巡检(报修)队列。
//!
//! `casework.rs` 为逐字保留的原子单元;含动态 WHERE 拼装(`QueryBuilder`),
//! 属方案 §三 的正用途第 3 类,文件级豁免已写明理由。

pub mod casework;
