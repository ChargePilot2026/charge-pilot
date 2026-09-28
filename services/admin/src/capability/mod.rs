//! admin 能力域垂直切片(P5)
//!
//! 迁移前 admin 按"文件"组织(`billing.rs` / `api/stations.rs` / …),每个文件
//! 各自持有 SQL、`json!` 与 `serde_json::Value`。目录回答不了"这段代码属于哪个
//! 业务域",SQL 也散落在 handler 中间。
//!
//! 这里按 `services::AdminServices` 已有的域字段切分,每个域一个目录:
//!
//! ```text
//! capability/<域>/
//!     mod.rs            域的公开入口 —— usecase 方法 + 响应 DTO + 类型化载荷
//!     repository_sql.rs 本域全部 SQL(唯一允许 `sqlx::query*` 的地方)
//!     domain.rs         纯逻辑,零 I/O(能抽就抽)
//!     tests.rs          纯函数单测
//! ```
//!
//! 文件不必凑齐 —— 只在有实际内容时创建。宁可 3 个文件也不要 7 个空壳。
//!
//! **分层约束**由 `clippy.toml` 的 `disallowed-methods` / `disallowed-macros`
//! 保证:SQL 只能在 `repository_sql.rs`,生产代码不许 `json!`。
//! 豁免一律精确到文件(注明理由)或函数,不设 crate 级开关。

pub mod alert;
pub mod cases;
pub mod config;
pub mod device;
pub mod finance;
pub mod identity;
pub mod internal;
pub mod order;
pub mod webhook;
