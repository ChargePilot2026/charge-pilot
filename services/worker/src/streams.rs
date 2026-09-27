//! worker Stream 消费者

use crate::AppState;
use async_trait::async_trait;
use common_error::{AppError, AppResult};
use common_redis::StreamEntry;
use common_stream::{ConsumerGroup, StreamHandler};

pub async fn spawn_all(state: AppState) -> AppResult<()> {
    let cg = ConsumerGroup::new(state.redis_stream.clone());
    cg.register(common_redis::streams::WEBHOOK_RETRY, "worker-cg", "worker-1",
        Box::new(WebhookRetryHandler), 16, 1000).await?;
    cg.register(common_redis::streams::OTA_SCHEDULE, "worker-cg", "worker-1",
        Box::new(OtaScheduleHandler), 8, 5000).await?;
    cg.register(common_redis::streams::COMP_TX, "worker-cg", "worker-1",
        Box::new(CompTxHandler { state: state.clone() }), 16, 2000).await?;
    Ok(())
}

pub struct WebhookRetryHandler;

#[async_trait]
impl StreamHandler for WebhookRetryHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let p = &entry.envelope.payload;
        let _url = p.get("url").and_then(|v| v.as_str())
            .filter(|url| !url.is_empty())
            .ok_or_else(|| AppError::BadRequest("webhook retry event has no target URL".into()))?;
        Err(AppError::ServiceUnavailable(
            "webhook delivery is not configured; retain this event in the worker DLQ".into(),
        ))
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
