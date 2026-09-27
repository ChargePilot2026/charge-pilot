//! The gateway service publishes its own outbox; Redis failure never loses a receipt.
//!
//! P3:发布逻辑已下沉到 `OutboxService`,本文件只留入队辅助与后台任务编排。
use crate::AppState;
use common_error::AppResult;
use common_redis::StreamEnvelope;
use std::time::Duration;

/// Persist gateway-owned events with the business write so a Redis outage cannot lose them.
pub async fn enqueue(
    tx: &mut common_db::Tx<'_>,
    stream: &str,
    envelope: &StreamEnvelope,
) -> AppResult<()> {
    sqlx::query("INSERT INTO event_outbox (event_id, stream, envelope_json) VALUES (?, ?, ?)")
        .bind(&envelope.event_id)
        .bind(stream)
        .bind(serde_json::to_value(envelope)?)
        .execute(tx.executor())
        .await?;
    Ok(())
}

pub fn spawn(st: AppState) {
    tokio::spawn(async move {
        let mut tick = tokio::time::interval(Duration::from_secs(2));
        loop {
            tick.tick().await;
            for _ in 0..50 {
                match st.outbox.publish_one().await {
                    Ok(true) => {}
                    Ok(false) => break,
                    Err(_) => {
                        tracing::warn!("gateway outbox publish failed; retrying next tick");
                        break;
                    }
                }
            }
        }
    });
}
