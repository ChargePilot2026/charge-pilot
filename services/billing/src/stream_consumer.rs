//! billing Stream 消费者
//!
//! 订阅:
//!   - charge_ended_stream.billing-cg → 计费 + 写 fee_calculation + 投递 fee_delivery 给 user
//!   - pricing_rule_changed_stream.billing-cg → 更新本地计费规则快照

use crate::AppState;
use async_trait::async_trait;

use common_error::AppResult;
use common_redis::StreamEntry;
use common_stream::{ConsumerGroup, StreamHandler};

use tracing::info;

pub async fn spawn_all(state: AppState) -> AppResult<()> {
    let cg = ConsumerGroup::new(state.redis_stream.clone());
    cg.register(common_redis::streams::CHARGE_ENDED, "billing-cg", "billing-1",
        Box::new(ChargeEndedHandler { state: state.clone() }), 32, 1000).await?;
    cg.register(common_redis::streams::PRICING_RULE_CHANGED, "billing-cg", "billing-1",
        Box::new(PricingChangedHandler { state: state.clone() }), 8, 5000).await?;
    Ok(())
}

pub struct ChargeEndedHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for ChargeEndedHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let p = &entry.envelope.payload;
        let order_no = p.get("order_no").and_then(|v| v.as_str())
            .filter(|v| !v.is_empty() && v.len() <= 64)
            .ok_or_else(|| common_error::AppError::BadRequest("结束事件缺少有效订单号".into()))?;
        let charge_id = p.get("charge_order_id").and_then(|v| v.as_u64())
            .filter(|id| *id > 0)
            .ok_or_else(|| common_error::AppError::BadRequest("结束事件缺少有效充电订单 ID".into()))?;
        self.state.fee.calculate(charge_id,&order_no).await?;
        Ok(())
    }
}

pub struct PricingChangedHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for PricingChangedHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let p = &entry.envelope.payload;
        let rule_id = p.get("id").and_then(|v| v.as_u64()).filter(|id| *id > 0)
            .ok_or_else(|| common_error::AppError::BadRequest("计费规则变更事件缺少有效规则 ID".into()))?;
        // Price rules are fetched fresh for each quote; record the valid event for visibility.
        info!(rule_id, "pricing rule changed; future quotes will fetch the new rule");
        Ok(())
    }
}
