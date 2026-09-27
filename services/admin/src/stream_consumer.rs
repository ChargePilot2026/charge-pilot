//! admin Stream 消费者
//!
//! 订阅:
//!   - alert_stream.admin-cg → 落库到 alert_event
//!   - refund_required_stream.admin-cg → claim + 调微信退款 + 写结果
//!   - invoice_required_stream.admin-cg → 写 invoice_review 待审核
//!   - webhook_retry_stream.admin-cg → 投递(本期由 worker 兜底,admin 只入队)
//!   - device_event_stream.admin-cg → 设备状态 UI 推送(本期仅记账)

use crate::AppState;
use async_trait::async_trait;
use common_error::{AppError, AppResult};
use common_redis::{StreamEntry, StreamEnvelope};
use common_stream::{ConsumerGroup, StreamHandler};
use serde_json::json;
use tracing::info;

pub async fn spawn_all(state: AppState) -> AppResult<()> {
    let cg = ConsumerGroup::new(state.redis_stream.clone());

    cg.register(common_redis::streams::ALERT, "admin-cg", "admin-1",
        Box::new(AlertHandler { state: state.clone() }), 32, 1000).await?;
    cg.register(common_redis::streams::REFUND_REQUIRED, "admin-cg", "admin-1",
        Box::new(RefundRequiredHandler { state: state.clone() }), 16, 2000).await?;
    cg.register(common_redis::streams::INVOICE_REQUIRED, "admin-cg", "admin-1",
        Box::new(InvoiceRequiredHandler { state: state.clone() }), 16, 2000).await?;
    cg.register(common_redis::streams::DEVICE_EVENT, "admin-cg", "admin-1",
        Box::new(DeviceEventHandler { state: state.clone() }), 32, 1000).await?;

    Ok(())
}

pub struct AlertHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for AlertHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let p = &entry.envelope.payload;
        let device_id = p.get("device_id").and_then(|v| v.as_str()).filter(|v| !v.is_empty())
            .ok_or_else(|| AppError::BadRequest("alert event is missing device_id".into()))?.to_string();
        let severity = p.get("severity").and_then(|v| v.as_str()).unwrap_or("warning").to_string();
        if !["warning", "critical", "fatal"].contains(&severity.as_str()) {
            return Err(AppError::BadRequest("alert event severity is invalid".into()));
        }
        let metric = p.get("metric").and_then(|v| v.as_str()).filter(|v| !v.is_empty())
            .ok_or_else(|| AppError::BadRequest("alert event is missing metric".into()))?.to_string();
        let event_id = entry.envelope.event_id.clone();
        let already_recorded: bool = sqlx::query_scalar(
            "SELECT EXISTS(SELECT 1 FROM alert_event WHERE event_id = ?)"
        ).bind(&event_id).fetch_one(self.state.config.pool()).await?;
        if !already_recorded {
            let now_month = chrono::Utc::now().format("%Y-%m-01").to_string();
            sqlx::query(
                "INSERT INTO alert_event (device_id, severity, metric, status, event_id, created_at, created_month)
                 VALUES (?, ?, ?, 'active', ?, NOW(3), ?)"
            )
            .bind(&device_id).bind(&severity).bind(&metric).bind(&event_id).bind(&now_month)
            .execute(self.state.config.pool()).await?;
        }
        info!(device_id, severity, "alert recorded");

        // 推送 webhook(由 worker 处理 webhook_retry_stream)
        let env = StreamEnvelope::new("alert_recorded", "admin", json!({
            "alert_device_id": device_id,
            "severity": severity,
            "event_id": event_id,
        }));
        self.state.redis_stream.xadd_envelope(common_redis::streams::WEBHOOK_RETRY, &env).await?;
        Ok(())
    }
}

pub struct RefundRequiredHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for RefundRequiredHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        crate::refund_task::enqueue(&self.state,entry).await
    }
}

pub struct InvoiceRequiredHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for InvoiceRequiredHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let p = &entry.envelope.payload;
        let invoice_request_id = p.get("invoice_request_id").and_then(|v| v.as_u64())
            .filter(|id| *id > 0)
            .ok_or_else(|| AppError::BadRequest("invoice event is missing invoice_request_id".into()))?;
        sqlx::query("INSERT IGNORE INTO invoice_review (invoice_request_id, review_status) VALUES (?, 'pending')")
            .bind(invoice_request_id).execute(self.state.config.pool()).await?;
        info!(invoice_request_id, "invoice required recorded");
        Ok(())
    }
}

pub struct DeviceEventHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for DeviceEventHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let p = &entry.envelope.payload;
        let device_id = p.get("device_id").and_then(|v| v.as_str()).filter(|v| !v.is_empty())
            .ok_or_else(|| AppError::BadRequest("device event is missing device_id".into()))?.to_string();
        // 更新设备最近一次遥测时间(可选:便于 PC 后台看板上显示)
        sqlx::query("UPDATE device_meta SET last_seen_at = NOW(3) WHERE device_id = ?")
            .bind(&device_id)
            .execute(self.state.config.pool())
            .await?;
        Ok(())
    }
}
