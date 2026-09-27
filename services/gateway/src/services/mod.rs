//! gateway 能力域服务对象(P3)
//!
//! `AppState` 上**不再有 `pub db`** —— handler 只能拿到本模块的服务对象,
//! 服务对象的 `db` 字段私有。SQL 一律落在本模块内,handler 只做编排。

pub mod command;
pub mod device;
pub mod outbox;
pub mod stop;

use std::sync::Arc;

use common_app::ServiceBase;
use common_config::AppConfig;
use common_db::Db;
use common_redis::{RedisCache, RedisStream};

pub use command::ChargeCommandService;
pub use device::DeviceService;
pub use outbox::OutboxService;
pub use stop::ChargeStopService;

/// 一次性装配 gateway 所需的全部能力域服务。
///
/// 每个服务对象各持一份 `ServiceBase`:`Db` 是 `Clone` 的连接池句柄,
/// 共用同一个池不会多开连接。
pub fn build(
    db: Db,
    http: reqwest::Client,
    service_token: Arc<String>,
    cfg: Arc<AppConfig>,
    redis_cache: RedisCache,
    redis_stream: RedisStream,
) -> GatewayServices {
    GatewayServices {
        device: DeviceService::new(ServiceBase::new(
            db.clone(),
            http.clone(),
            service_token.clone(),
            cfg.clone(),
        )),
        command: ChargeCommandService::new(
            ServiceBase::new(db.clone(), http.clone(), service_token.clone(), cfg.clone()),
            redis_cache,
        ),
        stop: ChargeStopService::new(ServiceBase::new(
            db.clone(),
            http.clone(),
            service_token.clone(),
            cfg.clone(),
        )),
        outbox: OutboxService::new(
            ServiceBase::new(db, http, service_token, cfg),
            redis_stream,
        ),
    }
}

/// gateway 全部能力域服务的聚合,便于一次性装配。
pub struct GatewayServices {
    pub device: DeviceService,
    pub command: ChargeCommandService,
    pub stop: ChargeStopService,
    pub outbox: OutboxService,
}
