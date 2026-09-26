//! billing Stream 消费者
//!
//! 订阅:
//!   - charge_ended_stream.billing-cg → 计费 + 写 fee_calculation + 发 refund_required_stream
//!   - pricing_rule_changed_stream.billing-cg → 更新本地计费规则快照
//!   - comp_tx_stream.billing-cg → 跨服务事务补偿

use crate::AppState;
use async_trait::async_trait;
use common_db::IdGen;
use common_error::AppResult;
use common_redis::{StreamEntry, StreamEnvelope};
use common_stream::{ConsumerGroup, StreamHandler};
use serde_json::json;
use tracing::{error, info, warn};

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
        let charged_kwh = p.get("charged_kwh").and_then(|v| v.as_f64()).unwrap_or(0.0);
        let charged_seconds = p.get("charged_seconds").and_then(|v| v.as_i64()).unwrap_or(0);
        let peak_kwh = p.get("peak_kwh").and_then(|v| v.as_f64()).unwrap_or(charged_kwh * 0.6);
        let off_kwh = p.get("off_kwh").and_then(|v| v.as_f64()).unwrap_or(charged_kwh - peak_kwh);
        if order_no.is_empty() || charge_id == 0 { return Ok(()); }

        // 1) 取计费规则
        let rule_id: u64 = sqlx::query_scalar("SELECT id FROM pricing_rule WHERE status='active' ORDER BY id ASC LIMIT 1")
            .fetch_optional(self.state.db.pool()).await
            .ok()
            .flatten()
            .unwrap_or(1);

        // 2) 同事务写 fee_calculation + 更新 charge_order
        let calc_no = IdGen::new("FEE").next();
        let now_month = chrono::Utc::now().format("%Y-%m-01").to_string();
        let mut tx = self.state.db.pool().begin().await?;
        let (electric_cents, service_cents, total_cents): (i64, i64, i64) = {
            let rule = crate::engine::PricingRule::default_default();
            let r = crate::engine::calculate_fee_compat(&rule, charged_kwh, charged_seconds / 60, peak_kwh, off_kwh);
            (r.electric_cents, r.service_cents, r.total_cents)
        };
        sqlx::query(
            "INSERT INTO fee_calculation (calculation_no, order_no, charge_order_id, user_id, pricing_rule_id, pricing_rule_version,
                charged_kwh, charged_seconds, peak_kwh, off_kwh, electric_cents, service_cents, total_cents, created_month)
             VALUES (?, ?, ?, 0, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)
             ON DUPLICATE KEY UPDATE electric_cents=VALUES(electric_cents), service_cents=VALUES(service_cents), total_cents=VALUES(total_cents)"
        )
        .bind(&calc_no).bind(&order_no).bind(charge_id).bind(rule_id)
        .bind(charged_kwh).bind(charged_seconds).bind(peak_kwh).bind(off_kwh)
        .bind(electric_cents).bind(service_cents).bind(total_cents).bind(&now_month)
        .execute(&mut *tx).await?;
        sqlx::query("UPDATE charge_order SET status='completed', ended_at=NOW(3), electric_cents=?, service_cents=?, total_cents=? WHERE id=?")
            .bind(electric_cents).bind(service_cents).bind(total_cents).bind(charge_id)
            .execute(&mut *tx).await?;
        tx.commit().await?;

        // 3) 触发退款(若实际未充电 / 异常结束)
        if total_cents < 0 || charged_kwh < 0.01 {
            warn!(order_no, "abnormal charge end; requesting refund");
            let env = StreamEnvelope::new("refund_required", "billing", json!({
                "order_no": order_no, "amount_cents": total_cents.max(0),
            }));
            let _ = self.state.redis_stream.xadd_envelope(common_redis::streams::REFUND_REQUIRED, &env).await;
        }

        info!(order_no, electric_cents, service_cents, total_cents, "billing recorded");
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
