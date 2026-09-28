//! worker Stream 消费者

use crate::services::webhook::WebhookDelivery;
use crate::AppState;
use async_trait::async_trait;
use common_error::{AppError, AppResult};
use common_redis::StreamEntry;
use common_stream::{ConsumerGroup, StreamHandler};

pub async fn spawn_all(state: AppState) -> AppResult<()> {
    let cg = ConsumerGroup::new(state.redis_stream.clone());
    cg.register(common_redis::streams::WEBHOOK_RETRY, "worker-cg", "worker-1",
        Box::new(WebhookRetryHandler { state: state.clone() }), 16, 1000).await?;
    cg.register(common_redis::streams::OTA_SCHEDULE, "worker-cg", "worker-1",
        Box::new(OtaScheduleHandler), 8, 5000).await?;
    cg.register(common_redis::streams::COMP_TX, "worker-cg", "worker-1",
        Box::new(CompTxHandler { state: state.clone() }), 16, 2000).await?;
    Ok(())
}

/// D11:webhook 真实投递。
///
/// 原实现对任何事件都恒定返回 `ServiceUnavailable("webhook delivery is not
/// configured")` —— 失败重试耗尽后全部进 DLQ,订阅方**从未收到过任何一次
/// 通知**。现在交给 [`WebhookService::deliver`]。
///
/// 三类结果的语义差异(由 `deliver` 内部决定,此处只做编排):
/// - `Delivered` / `Permanent` → `Ok(())`,交 consumer ACK
/// - `Transient`(408/429/5xx)、网络错误、超时 → `Err`,交 `common_stream`
///   退避重试 `[2s, 4s, 8s]`,3 次仍失败则进 DLQ
/// - 载荷缺 `url` → 记 error 后 `Ok(())`,**不重试**
pub struct WebhookRetryHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for WebhookRetryHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let delivery = WebhookDelivery::from_payload(
            &entry.envelope.event_id,
            &entry.envelope.event_type,
            &entry.envelope.occurred_at,
            &entry.envelope.payload,
        );
        // 缺 url 的根因在 **admin 侧没展开订阅**(事件发布时未带上 url),
        // 不是临时故障。重试多少次都是同一个结果,只会把一条坏消息在
        // DLQ 里反复打转并盖掉真正的失败原因。记 error 后直接 ACK。
        if delivery.url.trim().is_empty() {
            tracing::error!(
                event_id = %entry.envelope.event_id,
                event_type = %entry.envelope.event_type,
                subscription_id = delivery.subscription_id,
                "webhook 事件缺少目标 URL,无法投递;不重试(admin 侧发布时未展开订阅)"
            );
            return Ok(());
        }
        self.state.webhook.deliver(&delivery).await?;
        Ok(())
    }
}

pub struct OtaScheduleHandler;

#[async_trait]
impl StreamHandler for OtaScheduleHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        if entry.envelope.payload.get("schedule_id").and_then(|v| v.as_u64()).is_none() {
            return Err(AppError::BadRequest("OTA event is missing schedule_id".into()));
        }
        Err(AppError::ServiceUnavailable(
            "OTA dispatch is not configured; retain this event in the worker DLQ".into(),
        ))
    }
}

/// Persist confirmed cross-service outcomes in worker_db. This is an audit ledger;
/// it does not claim to execute a refund or compensation action itself.
///
/// P3:事件校验与台账落库已下沉到 [`crate::services::EventService`],
/// 本文件只留 Stream 消费编排。
pub struct CompTxHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for CompTxHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        self.state.event.record_comp_tx(entry).await
    }
}
