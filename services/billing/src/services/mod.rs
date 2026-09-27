//! billing 能力域服务对象(P3)
//!
//! `AppState` 上**不再有裸 `db` 字段** —— handler 只能拿到本模块的服务对象,
//! 服务对象的 `base` 字段私有。SQL 一律落在本模块内,handler 只做编排。
//!
//! 能力域划分:
//! - [`fee`]         计费域:`fee_calculation` / `fee_receipt` 的一次性计价与
//!                   `fee_delivery` 的持久投递(可重放、幂等)。
//! - [`settlement`]  对账分账域:`settlement` / `settlement_party_amount` 的分账
//!                   落库与订单计费快照读取。
//! - [`invoice`]     开票域:发票归 user 服务所有,本域只经其内部端点取数。

pub mod fee;
pub mod invoice;
pub mod settlement;

use std::sync::Arc;

use common_app::ServiceBase;
use common_config::AppConfig;
use common_db::Db;

pub use fee::FeeService;
pub use invoice::InvoiceService;
pub use settlement::SettlementService;

/// 一次性装配 billing 所需的全部能力域服务。
///
/// 每个服务对象各持一份 `ServiceBase`:`Db` 是 `Clone` 的连接池句柄,
/// 共用同一个池不会多开连接。
pub fn build(
    db: Db,
    http: reqwest::Client,
    service_token: Arc<String>,
    cfg: Arc<AppConfig>,
) -> BillingServices {
    BillingServices {
        fee: FeeService::new(ServiceBase::new(
            db.clone(),
            http.clone(),
            service_token.clone(),
            cfg.clone(),
        )),
        settlement: SettlementService::new(ServiceBase::new(
            db.clone(),
            http.clone(),
            service_token.clone(),
            cfg.clone(),
        )),
        invoice: InvoiceService::new(ServiceBase::new(db, http, service_token, cfg)),
    }
}

/// billing 全部能力域服务的聚合,便于一次性装配。
pub struct BillingServices {
    pub fee: FeeService,
    pub settlement: SettlementService,
    pub invoice: InvoiceService,
}
