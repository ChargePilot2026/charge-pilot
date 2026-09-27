//! worker 能力域服务对象(P3)
//!
//! `AppState` 上**不再有裸 `db` 字段** —— handler / 任务循环只能拿到本模块的
//! 服务对象,服务对象的 `base` 字段私有。SQL 一律落在本模块内。
//!
//! 能力域划分:
//! - [`event`]  事件消费域:`comp_tx_log` 补偿结果台账(**幂等重放判定**在此)。
//! - [`retry`]  重试域:DLQ 重放 + 后台任务注册表查询(export_run 探活)。
//!
//! 其余 worker 循环(announcement_expire / snapshot_warmer /
//! device_session_clean)只经内部 HTTP 调用其他服务,不碰本服务数据库,
//! 因此不需要服务对象 —— 它们保留读 `AppState` 的 `cfg` / `http` /
//! `service_token` / `redis_cache`。

pub mod event;
pub mod retry;

use std::sync::Arc;

use common_app::ServiceBase;
use common_config::AppConfig;
use common_db::Db;
use common_redis::RedisStream;

pub use event::EventService;
pub use retry::RetryService;

/// 一次性装配 worker 所需的全部能力域服务。
///
/// 每个服务对象各持一份 `ServiceBase`:`Db` 是 `Clone` 的连接池句柄,
/// 共用同一个池不会多开连接。
pub fn build(
    db: Db,
    http: reqwest::Client,
    service_token: Arc<String>,
    cfg: Arc<AppConfig>,
    redis_stream: RedisStream,
) -> WorkerServices {
    WorkerServices {
        event: EventService::new(ServiceBase::new(
            db.clone(),
            http.clone(),
            service_token.clone(),
            cfg.clone(),
        )),
        retry: RetryService::new(
            ServiceBase::new(db, http, service_token, cfg),
            redis_stream,
        ),
    }
}

/// worker 全部能力域服务的聚合,便于一次性装配。
pub struct WorkerServices {
    pub event: EventService,
    pub retry: RetryService,
}
