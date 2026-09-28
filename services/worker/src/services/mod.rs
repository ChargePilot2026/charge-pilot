//! worker 能力域服务对象(P3)
//!
//! `AppState` 上**不再有裸 `db` 字段** —— handler / 任务循环只能拿到本模块的
//! 服务对象,服务对象的 `base` 字段私有。SQL 一律落在本模块内。
//!
//! 能力域划分:
//! - [`event`]  事件消费域:`comp_tx_log` 补偿结果台账(**幂等重放判定**在此)。
//! - [`retry`]  重试域:DLQ 重放 + 后台任务注册表查询(export_run 探活)。
//! - [`webhook`] 投递域(D11):webhook 真实投递 + SSRF 校验 + HMAC 签名
//!   + 幂等判定。此前 webhook_retry 流的 handler 恒定返回
//!   `ServiceUnavailable`,这条链路从未真正投递过。
//!
//! 其余 worker 循环(announcement_expire / snapshot_warmer /
//! device_session_clean)只经内部 HTTP 调用其他服务,不碰本服务数据库,
//! 因此不需要服务对象 —— 它们保留读 `AppState` 的 `cfg` / `http` /
//! `service_token` / `redis_cache`。

pub mod event;
pub mod retry;
pub mod webhook;

use std::sync::Arc;

use common_app::ServiceBase;
use common_config::AppConfig;
use common_db::Db;
use common_redis::RedisStream;

pub use event::EventService;
pub use retry::RetryService;
pub use webhook::{ApiDeliveryReporter, DeliveryReporter, WebhookService};

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
    // D11:投递明细回写通道。`webhook_delivery_log` 在 admin_db,
    // worker 无权跨库访问,故经 admin 内部端点回写。
    let reporter = std::sync::Arc::new(ApiDeliveryReporter::new(
        http.clone(),
        service_token.clone(),
        &cfg,
    )) as std::sync::Arc<dyn DeliveryReporter>;
    WorkerServices {
        event: EventService::new(ServiceBase::new(
            db.clone(),
            http.clone(),
            service_token.clone(),
            cfg.clone(),
        )),
        retry: RetryService::new(
            ServiceBase::new(db.clone(), http.clone(), service_token.clone(), cfg.clone()),
            redis_stream,
        ),
        // D11:webhook 投递服务用自己的 HTTP 客户端(禁重定向 + https_only),
        // 内部调用能力不共享给外部订阅方。
        webhook: WebhookService::with_reporter(
            ServiceBase::new(db, http, service_token, cfg),
            Some(reporter),
        ),
    }
}

/// worker 全部能力域服务的聚合,便于一次性装配。
pub struct WorkerServices {
    pub event: EventService,
    pub retry: RetryService,
    pub webhook: WebhookService,
}
