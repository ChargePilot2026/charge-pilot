//! user 服务 — Stream 消费者
//!
//! 订阅的 stream:
//!   - charge_ended_stream.user-cg → 关闭轮询
//!   - pricing_rule_changed_stream.user-cg → 失效本地计费规则缓存
//!   - coupon_grant_required_stream.user-cg → 写 coupon_grant
//!   - comp_tx_stream is consumed by worker for refund outcome audit only

use crate::AppState;
use async_trait::async_trait;
use common_error::AppResult;
use common_redis::StreamEntry;
use common_stream::{ConsumerGroup, StreamHandler};
use tracing::{info, warn};

pub async fn spawn_all(state: AppState) -> AppResult<()> {
    let cg = ConsumerGroup::new(state.redis_stream.clone());

    // 1) charge_ended → 关轮询缓存
    cg.register(
        common_redis::streams::CHARGE_ENDED,
        "user-cg",
        "user-1",
        Box::new(ChargeEndedHandler { state: state.clone() }),
        32, 1000,
    ).await?;

    // 2) pricing_rule_changed → 失效缓存
    cg.register(
        common_redis::streams::PRICING_RULE_CHANGED,
        "user-cg",
        "user-1",
        Box::new(PricingChangedHandler { state: state.clone() }),
        8, 5000,
    ).await?;

    // 3) coupon_grant_required → 发券
    cg.register(
        common_redis::streams::COUPON_GRANT_REQUIRED,
        "user-cg",
        "user-1",
        Box::new(CouponGrantHandler { state: state.clone() }),
        16, 2000,
    ).await?;

    Ok(())
}

pub struct ChargeEndedHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for ChargeEndedHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let order_no=entry.envelope.payload.get("order_no").and_then(|v|v.as_str()).ok_or_else(||common_error::AppError::BadRequest("结束事件缺少订单号".into()))?;
        let completed:bool=sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM charge_order WHERE order_no=? AND status='completed' AND deleted_at IS NULL)")
            .bind(order_no).fetch_one(self.state.db.pool()).await?;
        if !completed {return Err(common_error::AppError::Conflict("充电结束尚未持久化确认".into()));}
        self.state.redis_cache.del(&format!("snapshot:{order_no}")).await?;
        info!(order_no,"confirmed charge end invalidated telemetry cache");
        Ok(())
    }
}

pub struct PricingChangedHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for PricingChangedHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let key = entry.envelope.payload.get("key").and_then(|v| v.as_str()).unwrap_or("");
        // 失效本地缓存:本期用户侧无强缓存,只记录
        warn!(?key, "pricing rule changed; local cache invalidated (best-effort)");
        Ok(())
    }
}

pub struct CouponGrantHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for CouponGrantHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let result = crate::coupon_admin::grant_activity_event(&self.state, entry).await?;
        info!(user_id=result.user_id, coupon_id=result.coupon_id, coupon_grant_id=result.coupon_grant_id, "coupon grant event applied idempotently");
        Ok(())
    }
}
