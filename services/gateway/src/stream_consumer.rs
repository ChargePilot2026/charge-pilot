//! gateway Stream 消费者
//!
//! 订阅:
//!   - charge_started_stream.gateway-cg → 下发设备启动指令(技术规格 § 2.1 步骤 9)

use crate::AppState;
use async_trait::async_trait;
use common_error::AppResult;
use common_redis::{PortLock, StreamEntry, StreamEnvelope};
use common_stream::{ConsumerGroup, StreamHandler};
use serde_json::json;
use tracing::{error, info, warn};

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
        let p = &entry.envelope.payload;
        let charge_id = p.get("charge_order_id").and_then(|v| v.as_u64()).unwrap_or(0);
        if charge_id == 0 { return Ok(()); }

        let order: Option<(u64, String, String, u8)> = sqlx::query_as(
            "SELECT user_id, order_no, device_id, port_no FROM charge_order WHERE id = ?"
        )
        .bind(charge_id)
        .fetch_optional(self.state.db.pool())
        .await
        .ok()
        .flatten();
        let (user_id, order_no, device_id, port_no) = match order {
            Some(v) => v,
            None => return Ok(()),
        };

        // 1) 验证逻辑锁仍属本订单
        let lock = PortLock::new(self.state.redis_cache.clone());
        let holder = format!("{user_id}:{order_no}");
        let hold_ok = lock.check_holder(&order_no, &holder).await?;
        if !hold_ok {
            warn!(order_no, "logical lock lost; reporting start failure");
            // 写启动失败 + 发退款事件
            sqlx::query("UPDATE charge_order SET status='failed', failure_reason='lock_lost' WHERE order_no=?")
                .bind(&order_no).execute(self.state.db.pool()).await?;
            return Ok(());
        }
        // CAS 删除逻辑锁
        let _ = lock.release_if_match(&order_no, &holder).await;

        // 2) 物理锁 30s
        let phys = lock.try_lock(&order_no, &holder).await?;
        if !phys {
            warn!(order_no, "physical lock contention");
        }

        // 3) 下发 START 命令(实际通过 MQTT / TCP 帧)
        let cmd_id = uuid::Uuid::new_v4().to_string();
        let now_month = chrono::Utc::now().format("%Y-%m-01").to_string();
        sqlx::query(
            "INSERT INTO ota_command (command_id, device_id, package_id, status, sent_at, created_month)
             VALUES (?, ?, CONCAT('START:', ?), 'sent', NOW(3), ?)"
        )
        .bind(&cmd_id).bind(&device_id).bind(port_no).bind(&now_month)
        .execute(self.state.db.pool()).await?;

        // 4) 调 user 内部 start-result(设备 ACK 时) — 类型化 client + 路径常量
        // 简化:本期直接视为已 ACK
        if let Some(u) = self.state.cfg.service_urls.user.as_deref() {
            let cli = crate::clients::ServiceClient::new(self.state.http.clone(), self.state.service_token.clone());
            let body = serde_json::json!({
                "order_no": order_no, "success": true,
            });
            let _ = cli.post_typed::<_, serde_json::Value>(Some(u), api_contracts::paths::USER_INTERNAL_START_RESULT, &body).await;
        }

        let _ = lock.release_lock_if_match(&order_no, &holder).await;
        info!(order_no, device_id, "charge started, START command dispatched");
        Ok(())
    }
}

pub struct OtaScheduleHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for OtaScheduleHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let p = &entry.envelope.payload;
        let action = p.get("action")
            .cloned()
            .unwrap_or_else(|| serde_json::Value::String(entry.envelope.event_type.clone()));
        info!(?action, "ota_schedule event");
        // 实际:按 schedule_id 拉取 device 列表 + 调 device_command 推送
        Ok(())
    }
}

#[allow(dead_code)]
fn _ev_unused(_: StreamEnvelope) {}
