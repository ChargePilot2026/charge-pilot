//! usecase 服务对象基座(P1a 新增)
//!
//! **P1a 只新增、不动既有调用方。** 各服务 `AppState.db` 现有 298 处引用,
//! 提前删除必然编译失败——删 `AppState.db` 属 P3 该服务调用方全部迁完之后的事。
//!
//! 本阶段的作用:把"服务对象持有 `Db`、依赖私有、handler 取不到裸 pool"这条
//! 边界**先建出来**,让 P3 迁移时只需改调用方指向,不必重新设计归属。
//!
//! 依赖倒置的最终形态(Rust 跨 crate 无法按"层"授权,只能靠不暴露):
//! ```ignore
//! pub struct AppState {
//!     pub charge: ChargeService,   // 内部 db 字段私有
//!     pub wallet: WalletService,
//! }
//! // 不再有 `pub db: Db`
//! ```

#![allow(
    clippy::disallowed_macros,
    clippy::disallowed_types,
    clippy::disallowed_methods,
)]

pub mod service;

pub use service::{build_service_base, ServiceBase, ServiceDeps};
