//! Stream 消费者抽象
//!
//! 提供 Consumer trait + ConsumerGroup runner,封装:
//!   - XGROUP CREATE (幂等)
//!   - XREADGROUP(并行多 consumer)
//!   - 指数退避(2s / 4s / 8s / 16s / 30s)
//!   - 首次处理 + 3 次重试失败 → 原子写完整 DLQ 并 ACK
//!   - 重启恢复(读 PEL)
//!
//! 各服务在异步启动流程中调用 `ConsumerGroup::register(...).await?`。

use async_trait::async_trait;
use common_error::{AppError, AppResult};
use common_redis::{RedisStream, StreamEntry, StreamEnvelope};
use std::time::Duration;
use tokio::task::JoinHandle;
use tracing::{error, info, warn};

/// Stream 消费者 trait
#[async_trait]
pub trait StreamHandler: Send + Sync + 'static {
    /// 业务处理函数;返回 Ok 触发 ACK,返回 Err 触发重试
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()>;
}

pub struct ConsumerGroup {
    stream: RedisStream,
}

impl ConsumerGroup {
    pub fn new(stream: RedisStream) -> Self {
        Self { stream }
    }

    /// 异步确保消费组存在后启动消费者;初始化失败时返回错误。
    pub async fn register(
        &self,
        stream_name: &str,
        group: &str,
        consumer: &str,
        handler: Box<dyn StreamHandler>,
        batch_size: usize,
        block_ms: usize,
    ) -> AppResult<JoinHandle<()>> {
        if batch_size == 0 || block_ms == 0 || group.is_empty() || consumer.is_empty() {
            return Err(AppError::Config(
                "stream consumer requires nonempty identity and bounded polling".into(),
            ));
        }
        let stream_clone = self.stream.clone();
        // 启动时确保 group 存在
        self.stream.ensure_group(stream_name, group).await?;
        let stream = stream_name.to_string();
        let group = group.to_string();
        let consumer = consumer.to_string();

        let h = tokio::spawn(async move {
            run_consumer(
                stream_clone,
                stream,
                group,
                consumer,
                batch_size,
                block_ms,
                handler,
            )
            .await;
        });
        Ok(h)
    }
}

async fn run_consumer(
    stream: RedisStream,
    stream_name: String,
    group: String,
    consumer: String,
    batch_size: usize,
    block_ms: usize,
    handler: Box<dyn StreamHandler>,
) {
    info!(stream=%stream_name, group=%group, consumer=%consumer, "stream consumer started");
    loop {
        // Recover this stable consumer identity's pending entries before reading new work.
        let entries = match next_batch(
            &stream,
            &stream_name,
            &group,
            &consumer,
            batch_size,
            block_ms,
        )
        .await
        {
            Ok(entries) => entries,
            Err(e) => {
                error!(error=%e,"stream read failed; retaining pending messages");
                tokio::time::sleep(Duration::from_secs(5)).await;
                continue;
            }
        };
        for entry in entries {
            if let Err(error) = process_entry(
                &stream,
                &group,
                &consumer,
                handler.as_ref(),
                &entry,
                &[
                    Duration::from_secs(2),
                    Duration::from_secs(4),
                    Duration::from_secs(8),
                ],
            )
            .await
            {
                // A failed ACK/DLQ write leaves the entry in PEL, recovered on the next pass.
                error!(entry_id=%entry.id,error=%error,"stream completion failed; keeping pending");
                tokio::time::sleep(Duration::from_secs(5)).await;
            }
        }
    }
}

async fn next_batch(
    stream: &RedisStream,
    name: &str,
    group: &str,
    consumer: &str,
    count: usize,
    block_ms: usize,
) -> AppResult<Vec<StreamEntry>> {
    let pending = stream
        .xreadgroup_pending(name, group, consumer, count)
        .await?;
    if !pending.is_empty() {
        return Ok(pending);
    }
    stream
        .xreadgroup(name, group, consumer, count, block_ms)
        .await
}

async fn process_entry(
    stream: &RedisStream,
    group: &str,
    consumer: &str,
    handler: &dyn StreamHandler,
    entry: &StreamEntry,
    delays: &[Duration],
) -> AppResult<()> {
    let mut attempt = 0;
    loop {
        // Preserve malformed/tombstoned entries for investigation instead of silently ACKing.
        let result = if entry.envelope.event_id.is_empty()
            || entry.envelope.event_type.is_empty()
            || entry.envelope.payload.is_null()
        {
            Err(AppError::BadRequest(
                "missing or malformed stream envelope".into(),
            ))
        } else {
            handler.handle(entry).await
        };
        match result {
            Ok(()) => {
                stream.xack(&entry.stream, group, &entry.id).await?;
                info!(entry_id=%entry.id,event_type=%entry.envelope.event_type,"acked");
                return Ok(());
            }
            Err(error) => {
                if attempt == delays.len() {
                    stream
                        .dead_letter(entry, group, consumer, &error.to_string())
                        .await?;
                    warn!(entry_id=%entry.id,attempts=attempt+1,"message retained in DLQ after retries");
                    return Ok(());
                }
                warn!(entry_id=%entry.id,attempt=attempt+1,error=%error,"handler failed; retrying");
                tokio::time::sleep(delays[attempt]).await;
                attempt += 1;
            }
        }
    }
}

/// 便捷:从 envelope 解析 payload 强类型
pub fn parse_payload<T: serde::de::DeserializeOwned>(env: &StreamEnvelope) -> AppResult<T> {
    serde_json::from_value(env.payload.clone())
        .map_err(|e| AppError::Internal(format!("parse payload: {e}")))
}

/// 工具:指数退避休眠
pub async fn backoff_sleep(attempt: u32) {
    let secs = match attempt {
        0 => 1,
        1 => 2,
        2 => 4,
        3 => 8,
        4 => 16,
        _ => 30,
    };
    tokio::time::sleep(Duration::from_secs(secs)).await;
}

#[cfg(test)]
mod tests;
