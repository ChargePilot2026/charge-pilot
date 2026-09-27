//! The gateway service publishes its own outbox; Redis failure never loses a receipt.
use crate::AppState;
use common_error::AppResult;
use common_redis::StreamEnvelope;
use sqlx::Row;
use std::time::Duration;

/// Persist gateway-owned events with the business write so a Redis outage cannot lose them.
pub async fn enqueue(
    tx: &mut sqlx::Transaction<'_, sqlx::MySql>,
    stream: &str,
    envelope: &StreamEnvelope,
) -> AppResult<()> {
    sqlx::query("INSERT INTO event_outbox (event_id, stream, envelope_json) VALUES (?, ?, ?)")
        .bind(&envelope.event_id)
        .bind(stream)
        .bind(serde_json::to_value(envelope)?)
        .execute(&mut **tx)
        .await?;
    Ok(())
}

pub fn spawn(st: AppState) {
    tokio::spawn(async move {
        let mut tick = tokio::time::interval(Duration::from_secs(2));
        loop {
            tick.tick().await;
            for _ in 0..50 {
                match publish_one(&st).await {
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

async fn publish_one(st: &AppState) -> AppResult<bool> {
    let mut tx = st.db.pool().begin().await?;
    let row=sqlx::query("SELECT id,stream,envelope_json FROM event_outbox WHERE status='pending' AND scheduled_at<=UTC_TIMESTAMP(3) ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED")
        .fetch_optional(&mut *tx).await?;
    let Some(row) = row else {
        tx.rollback().await?;
        return Ok(false);
    };
    let id: u64 = row.try_get("id")?;
    let stream: String = row.try_get("stream")?;
    let json: serde_json::Value = row.try_get("envelope_json")?;
    publish_locked(&mut tx, &st.redis_stream, id, &stream, json).await?;
    tx.commit().await?;
    Ok(true)
}

async fn publish_locked(
    tx: &mut sqlx::Transaction<'_, sqlx::MySql>,
    redis: &common_redis::RedisStream,
    id: u64,
    stream: &str,
    json: serde_json::Value,
) -> AppResult<()> {
    match serde_json::from_value::<StreamEnvelope>(json) {
        Ok(envelope) => {
            // A crash after XADD may replay this same event_id. Consumers must be idempotent.
            let sent = matches!(
                tokio::time::timeout(
                    Duration::from_secs(3),
                    redis.xadd_envelope(&stream, &envelope)
                )
                .await,
                Ok(Ok(_))
            );
            if sent {
                sqlx::query("UPDATE event_outbox SET status='published',published_at=UTC_TIMESTAMP(3),last_error=NULL WHERE id=?")
                    .bind(id).execute(&mut **tx).await?;
            } else {
                sqlx::query("UPDATE event_outbox SET retry_count=retry_count+1,scheduled_at=UTC_TIMESTAMP(3)+INTERVAL 30 SECOND,last_error='Redis publish failed or timed out' WHERE id=?")
                    .bind(id).execute(&mut **tx).await?;
            }
        }
        Err(_) => {
            sqlx::query("UPDATE event_outbox SET status='failed',last_error='Invalid event envelope' WHERE id=?")
                .bind(id).execute(&mut **tx).await?;
            tracing::error!(outbox_id = id, backtrace = %common_error::backtrace(), "invalid gateway outbox event requires repair");
        }
    }
    Ok(())
}
