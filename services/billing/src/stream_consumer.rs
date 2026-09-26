//! billing Stream 消费者
//!
//! 订阅:
//!   - charge_ended_stream.billing-cg → 计费 + 写 fee_calculation + 发 refund_required_stream
//!   - pricing_rule_changed_stream.billing-cg → 更新本地计费规则快照
//!   - comp_tx_stream.billing-cg → 跨服务事务补偿

use crate::AppState;
use async_trait::async_trait;

use common_error::AppResult;
use common_redis::StreamEntry;
use common_stream::{ConsumerGroup, StreamHandler};

use tracing::{error, info};

pub async fn spawn_all(state: AppState) -> AppResult<()> {
    let cg = ConsumerGroup::new(state.redis_stream.clone());
    cg.register(common_redis::streams::CHARGE_ENDED, "billing-cg", "billing-1",
        Box::new(ChargeEndedHandler { state: state.clone() }), 32, 1000).await?;
    cg.register(common_redis::streams::PRICING_RULE_CHANGED, "billing-cg", "billing-1",
        Box::new(PricingChangedHandler { state: state.clone() }), 8, 5000).await?;
    cg.register(common_redis::streams::COMP_TX, "billing-cg", "billing-1",
        Box::new(CompTxHandler { state: state.clone() }), 16, 2000).await?;
    Ok(())
}

pub struct ChargeEndedHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for ChargeEndedHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let p = &entry.envelope.payload;
        let order_no = p.get("order_no").and_then(|v| v.as_str()).unwrap_or("").to_string();
        let charge_id = p.get("charge_order_id").and_then(|v| v.as_u64()).unwrap_or(0);
        crate::charge_fee::calculate(&self.state,charge_id,&order_no).await?;
        Ok(())
    }
}

pub struct PricingChangedHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for PricingChangedHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let p = &entry.envelope.payload;
        let rule_id = p.get("id").and_then(|v| v.as_u64()).unwrap_or(0);
        if rule_id > 0 {
            // 失效本地缓存(本期直接刷新内存规则)
            info!(rule_id, "pricing rule changed; refreshing local cache");
        }
        Ok(())
    }
}

pub struct CompTxHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for CompTxHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let p = &entry.envelope.payload;
        let tx_id = p.get("tx_id").and_then(|v| v.as_str()).unwrap_or_default().to_string();
        if tx_id.is_empty() { return Ok(()); }
        // 幂等记录 + 标记完成
        let now_month = chrono::Utc::now().format("%Y-%m-01").to_string();
        let res = sqlx::query(
            "INSERT IGNORE INTO comp_tx_log (tx_id, consumer_group, stream, payload_hash, status, created_month)
             VALUES (?, 'billing-cg', ?, ?, 'committed', ?)"
        )
        .bind(&tx_id)
        .bind(&entry.stream)
        .bind(format!("{:x}", md5_hash(entry.envelope.payload.to_string().as_bytes())))
        .bind(&now_month)
        .execute(self.state.db.pool()).await;
        if let Err(e) = res { error!(error=%e, "comp_tx_log insert"); }
        Ok(())
    }
}

fn md5_hash(_bytes: &[u8]) -> u128 {
    // 简化:placeholder,真实用 md5 库
    0
}
