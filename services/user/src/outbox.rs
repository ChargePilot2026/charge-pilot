//! The user service publishes its own outbox; Redis failure never loses a receipt.
use crate::AppState;
use common_error::AppResult;
use common_redis::StreamEnvelope;
use sqlx::Row;
use std::time::Duration;

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
                        tracing::warn!("user outbox publish failed; retrying next tick");
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
            tracing::error!(outbox_id = id, "invalid user outbox event requires repair");
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[tokio::test]
    #[ignore = "requires development MySQL and stream Redis"]
    async fn failed_publish_stays_pending_then_retries_same_event() {
        let pool = sqlx::MySqlPool::connect(&std::env::var("DATABASE_URL").unwrap())
            .await
            .unwrap();
        let redis =
            common_redis::RedisStream::connect(&common_config::RedisConfig::stream().unwrap())
                .await
                .unwrap();
        let stream = format!("test_outbox_{}", uuid::Uuid::new_v4().simple());
        let envelope = StreamEnvelope::new("test", "user", serde_json::json!({"test":true}));
        let json = serde_json::to_value(&envelope).unwrap();
        let mut tx = pool.begin().await.unwrap();
        let id =
            sqlx::query("INSERT INTO event_outbox (event_id,stream,envelope_json) VALUES (?,?,?)")
                .bind(&envelope.event_id)
                .bind(&stream)
                .bind(&json)
                .execute(&mut *tx)
                .await
                .unwrap()
                .last_insert_id();
        let mut conn = redis.conn();
        // Wrong Redis key type deterministically fails XADD without disrupting other traffic.
        redis::cmd("SET")
            .arg(&stream)
            .arg("test")
            .arg("EX")
            .arg(60)
            .query_async::<()>(&mut conn)
            .await
            .unwrap();
        publish_locked(&mut tx, &redis, id, &stream, json.clone())
            .await
            .unwrap();
        let failed: (String, u32, Option<chrono::NaiveDateTime>) =
            sqlx::query_as("SELECT status,retry_count,published_at FROM event_outbox WHERE id=?")
                .bind(id)
                .fetch_one(&mut *tx)
                .await
                .unwrap();
        redis::cmd("DEL")
            .arg(&stream)
            .query_async::<i64>(&mut conn)
            .await
            .unwrap();
        publish_locked(&mut tx, &redis, id, &stream, json)
            .await
            .unwrap();
        let published: (String, Option<chrono::NaiveDateTime>) =
            sqlx::query_as("SELECT status,published_at FROM event_outbox WHERE id=?")
                .bind(id)
                .fetch_one(&mut *tx)
                .await
                .unwrap();
        let entries: redis::Value = redis::cmd("XRANGE")
            .arg(&stream)
            .arg("-")
            .arg("+")
            .query_async(&mut conn)
            .await
            .unwrap();
        redis::cmd("DEL")
            .arg(&stream)
            .query_async::<i64>(&mut conn)
            .await
            .unwrap();
        tx.rollback().await.unwrap();
        assert_eq!(failed, ("pending".into(), 1, None));
        assert_eq!(published.0, "published");
        assert!(published.1.is_some());
        let redis::Value::Array(entries) = entries else {
            panic!("expected stream entries")
        };
        assert_eq!(entries.len(), 1);
        assert!(format!("{:?}", entries[0]).contains(&envelope.event_id));
    }
}
