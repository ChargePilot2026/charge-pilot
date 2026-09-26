//! worker Stream 消费者

use crate::AppState;
use async_trait::async_trait;
use common_error::AppResult;
use common_redis::{StreamEntry, StreamEnvelope};
use common_stream::{ConsumerGroup, StreamHandler};
use tracing::info;

pub async fn spawn_all(state: AppState) -> AppResult<()> {
    let cg = ConsumerGroup::new(state.redis_stream.clone());
    cg.register(common_redis::streams::WEBHOOK_RETRY, "worker-cg", "worker-1",
        Box::new(WebhookRetryHandler { state: state.clone() }), 16, 1000).await?;
    cg.register(common_redis::streams::OTA_SCHEDULE, "worker-cg", "worker-1",
        Box::new(OtaScheduleHandler { state: state.clone() }), 8, 5000).await?;
    cg.register(common_redis::streams::DEVICE_EVENT, "worker-cg", "worker-1",
        Box::new(DeviceEventHandler { state: state.clone() }), 32, 1000).await?;
    cg.register(common_redis::streams::ALERT, "worker-cg", "worker-1",
        Box::new(AlertHandler { state: state.clone() }), 16, 2000).await?;
    Ok(())
}

pub struct WebhookRetryHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for WebhookRetryHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let p = &entry.envelope.payload;
        let url = p.get("url").and_then(|v| v.as_str()).unwrap_or_default().to_string();
        if url.is_empty() { return Ok(()); }
        info!(%url, "webhook_retry handler");
        Ok(())
    }
}

pub struct OtaScheduleHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for OtaScheduleHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        info!(event_type = %entry.envelope.event_type, "ota_schedule worker");
        Ok(())
    }
}

pub struct DeviceEventHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for DeviceEventHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        info!(event_type = %entry.envelope.event_type, "device_event worker: snapshot warming");
        // 实际:由 snapshot_warmer 任务填充 Redis
        Ok(())
    }
}

pub struct AlertHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for AlertHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        info!(event_type = %entry.envelope.event_type, "alert worker: periodic recheck");
        Ok(())
    }
}

#[allow(dead_code)]
fn _e(_: StreamEnvelope) {}
