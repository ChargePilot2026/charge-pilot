//! gateway Stream 消费者
//!
//! 订阅:
//!   - charge_started_stream.gateway-cg → 下发设备启动指令(技术规格 § 2.1 步骤 9)

use crate::AppState;
use async_trait::async_trait;
use common_error::{AppError, AppResult};
use common_redis::StreamEntry;
use common_stream::{ConsumerGroup, StreamHandler};

pub async fn spawn_all(state: AppState) -> AppResult<()> {
    let cg = ConsumerGroup::new(state.redis_stream.clone());
    cg.register(common_redis::streams::CHARGE_STARTED, "gateway-cg", "gateway-1",
        Box::new(ChargeStartedHandler { state: state.clone() }), 32, 1000).await?;
    cg.register(common_redis::streams::OTA_SCHEDULE, "gateway-cg", "gateway-1",
        Box::new(OtaScheduleHandler { state: state.clone() }), 8, 5000).await?;
    Ok(())
}

pub struct ChargeStartedHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for ChargeStartedHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        crate::charge_command::handle(&self.state,entry).await
    }
}

pub struct OtaScheduleHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for OtaScheduleHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let _ = entry;
        Err(AppError::ServiceUnavailable(
            "OTA 设备选择、固件传输与 ACK 尚未接入；保留事件供重试和 DLQ 处置".into(),
        ))
    }
}
