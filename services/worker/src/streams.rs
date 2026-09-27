//! worker Stream 消费者

use crate::AppState;
use async_trait::async_trait;
use common_error::{AppError, AppResult};
use common_redis::StreamEntry;
use common_stream::{ConsumerGroup, StreamHandler};
use sha2::{Digest, Sha256};
use sqlx::Row;

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
pub struct CompTxHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for CompTxHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        if entry.envelope.event_type != "refund_completed" {
            return Err(AppError::BadRequest("unsupported comp_tx event type".into()));
        }
        let payload = &entry.envelope.payload;
        let refund_no = payload.get("refund_no").and_then(|v| v.as_str())
            .filter(|v| !v.is_empty() && v.len() <= 64 && !v.chars().any(char::is_control))
            .ok_or_else(|| AppError::BadRequest("refund_completed event has no valid refund_no".into()))?;
        let success = payload.get("success").and_then(|v| v.as_bool())
            .ok_or_else(|| AppError::BadRequest("refund_completed event has no success result".into()))?;
        let event_id = uuid::Uuid::parse_str(&entry.envelope.event_id)
            .map_err(|_| AppError::BadRequest("comp_tx event id is not a UUID".into()))?.to_string();
        let occurred_at = chrono::DateTime::parse_from_rfc3339(&entry.envelope.occurred_at)
            .map_err(|_| AppError::BadRequest("comp_tx event timestamp is invalid".into()))?.to_utc();
        let created_month = occurred_at.format("%Y-%m-01").to_string();
        let payload_hash = hex::encode(Sha256::digest(serde_json::to_vec(&entry.envelope)?));
        let status = if success { "committed" } else { "failed" };

        let mut tx = self.state.db.pool().begin().await?;
        let existing = sqlx::query("SELECT stream,payload_hash,status FROM comp_tx_log WHERE tx_id=? AND created_month=? FOR UPDATE")
            .bind(&event_id).bind(&created_month).fetch_optional(&mut *tx).await?;
        if let Some(row) = existing {
            let saved_stream: String = row.try_get("stream")?;
            let saved_hash: Option<String> = row.try_get("payload_hash")?;
            let saved_status: String = row.try_get("status")?;
            if saved_stream != entry.stream || saved_hash.as_deref() != Some(&payload_hash) || saved_status != status {
                return Err(AppError::Conflict("comp_tx event id replayed with different contents".into()));
            }
            tx.commit().await?;
            return Ok(());
        }
        sqlx::query("INSERT INTO comp_tx_log (tx_id,consumer_group,stream,payload_hash,status,created_month,committed_at) VALUES (?,'worker-cg',?,?,?,?,?)")
            .bind(&event_id).bind(&entry.stream).bind(payload_hash).bind(status).bind(&created_month)
            .bind(if success { Some(occurred_at.naive_utc()) } else { None::<chrono::NaiveDateTime> })
            .execute(&mut *tx).await?;
        tx.commit().await?;
        tracing::info!(event_id, refund_no, success, "recorded compensation outcome");
        Ok(())
    }
}
