//! 事务 outbox 发布器(P3)
//!
//! 事件与业务写同事务落 `event_outbox`,由本发布器投递到 Redis Stream。
//! 语义(改动前必读):
//! - 投递成功才置 `published`;失败/超时只累加重试次数并推迟 30 秒。
//! - XADD 之后进程崩溃会导致**同一 event_id 重复投递**,消费者必须幂等。

use common_app::ServiceBase;
use common_error::AppResult;
use common_redis::{RedisStream, StreamEnvelope};
use sqlx::Row;
use std::time::Duration;

#[derive(Clone)]
pub struct OutboxService {
    base: ServiceBase,
    redis_stream: RedisStream,
}

impl OutboxService {
    pub fn new(base: ServiceBase, redis_stream: RedisStream) -> Self {
        Self { base, redis_stream }
    }

    /// 取一条待发事件并投递。返回 `false` 表示当前没有可投递的事件。
    pub async fn publish_one(&self) -> AppResult<bool> {
        let mut tx = self.base.begin().await?;
        let row = sqlx::query(
            "SELECT id,stream,envelope_json FROM event_outbox WHERE status='pending' AND scheduled_at<=UTC_TIMESTAMP(3) ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED",
        )
        .fetch_optional(tx.executor())
        .await?;
        let Some(row) = row else {
            tx.rollback().await?;
            return Ok(false);
        };
        let id: u64 = row.try_get("id")?;
        let stream: String = row.try_get("stream")?;
        let json: serde_json::Value = row.try_get("envelope_json")?;
        self.publish_locked(&mut tx, id, &stream, json).await?;
        tx.commit().await?;
        Ok(true)
    }

    async fn publish_locked(
        &self,
        tx: &mut common_db::Tx<'_>,
        id: u64,
        stream: &str,
        json: serde_json::Value,
    ) -> AppResult<()> {
        match serde_json::from_value::<StreamEnvelope>(json) {
            Ok(envelope) => {
                // XADD 之后崩溃可能重放同一 event_id,消费者必须幂等。
                let sent = matches!(
                    tokio::time::timeout(
                        Duration::from_secs(3),
                        self.redis_stream.xadd_envelope(&stream, &envelope)
                    )
                    .await,
                    Ok(Ok(_))
                );
                if sent {
                    sqlx::query("UPDATE event_outbox SET status='published',published_at=UTC_TIMESTAMP(3),last_error=NULL WHERE id=?")
                        .bind(id).execute(tx.executor()).await?;
                } else {
                    sqlx::query("UPDATE event_outbox SET retry_count=retry_count+1,scheduled_at=UTC_TIMESTAMP(3)+INTERVAL 30 SECOND,last_error='Redis publish failed or timed out' WHERE id=?")
                        .bind(id).execute(tx.executor()).await?;
                }
            }
            Err(_) => {
                // 无法反序列化的载荷永远不会成功,直接判死,不再无限重试。
                sqlx::query("UPDATE event_outbox SET status='failed',last_error='envelope_json is not a valid StreamEnvelope' WHERE id=?")
                    .bind(id).execute(tx.executor()).await?;
            }
        }
        Ok(())
    }
}
