//! user 服务 — Stream 消费者
//!
//! 订阅的 stream:
//!   - charge_ended_stream.user-cg → 关闭轮询
//!   - pricing_rule_changed_stream.user-cg → 失效本地计费规则缓存
//!   - coupon_grant_required_stream.user-cg → 写 coupon_grant
//!   - comp_tx_stream.user-cg → 用户侧业务补偿(本期仅记账)

use crate::AppState;
use async_trait::async_trait;
use common_error::AppResult;
use common_redis::{StreamEntry, StreamEnvelope};
use common_stream::{ConsumerGroup, StreamHandler};
use serde_json::json;
use tracing::{error, info, warn};

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
        let payload = &entry.envelope.payload;
        let user_id = payload.get("user_id").and_then(|v| v.as_u64()).unwrap_or(0);
        let coupon_id = payload.get("coupon_id").and_then(|v| v.as_u64()).unwrap_or(0);
        let source = payload.get("source").and_then(|v| v.as_str()).unwrap_or("activity");
        if user_id == 0 || coupon_id == 0 {
            error!(?payload, "invalid coupon_grant payload");
            return Ok(());
        }
        let now = chrono::Utc::now();
        let valid_hours: i64 = sqlx::query_scalar("SELECT valid_hours FROM coupon WHERE id = ?")
            .bind(coupon_id)
            .fetch_optional(self.state.db.pool())
            .await
            .ok()
            .flatten()
            .unwrap_or(720) as i64;
        sqlx::query(
            "INSERT INTO coupon_grant (coupon_id, user_id, grant_source, expired_at)
             VALUES (?, ?, ?, DATE_ADD(NOW(3), INTERVAL ? HOUR))"
        )
        .bind(coupon_id)
        .bind(user_id)
        .bind(source)
        .bind(valid_hours)
        .execute(self.state.db.pool())
        .await?;
        info!(user_id, coupon_id, source, "coupon granted");
        Ok(())
    }
}
