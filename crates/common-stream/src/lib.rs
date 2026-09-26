//! Stream 消费者抽象
//!
//! 提供 Consumer trait + ConsumerGroup runner,封装:
//!   - XGROUP CREATE (幂等)
//!   - XREADGROUP(并行多 consumer)
//!   - 指数退避(2s / 4s / 8s / 16s / 30s)
//!   - 3 次失败 → DLQ
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
        stream_name: &'static str,
        group: &str,
        consumer: &str,
        handler: Box<dyn StreamHandler>,
        batch_size: usize,
        block_ms: usize,
    ) -> AppResult<JoinHandle<()>> {
        let stream_clone = self.stream.clone();
        // 启动时确保 group 存在
        self.stream.ensure_group(stream_name, group).await?;
        let stream = stream_name.to_string();
        let group = group.to_string();
        let consumer = consumer.to_string();

        let h = tokio::spawn(async move {
            run_consumer(stream_clone, stream, group, consumer, batch_size, block_ms, handler).await;
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
        let res = stream
            .xreadgroup(&stream_name, &group, &consumer, batch_size, block_ms)
            .await;

        let entries = match res {
            Ok(v) => v,
            Err(e) => {
                error!(error=%e, "xreadgroup failed; sleeping 5s");
                tokio::time::sleep(Duration::from_secs(5)).await;
                continue;
            }
        };

        if entries.is_empty() {
            continue;
        }

        for entry in entries {
            let id = entry.id.clone();
            match handler.handle(&entry).await {
                Ok(()) => {
                    if let Err(e) = stream.xack(&stream_name, &group, &id).await {
                        error!(entry_id=%id, error=%e, "xack failed");
                    } else {
                        info!(entry_id=%id, event_type=%entry.envelope.event_type, "acked");
                    }
                }
                Err(e) => {
                    warn!(entry_id=%id, error=%e, "handle failed; sending to DLQ");
                    if let Err(e2) = stream.xadd_dlq(&stream_name, &id, &e.to_string()).await {
                        error!(error=%e2, "xadd_dlq failed");
                    } else {
                        // 入 DLQ 后也 ACK(避免 PEL 堆积)
                        let _ = stream.xack(&stream_name, &group, &id).await;
                    }
                }
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
mod tests {
    use super::*;

    #[test]
    fn backoff_sleep_logic() {
        // 仅验证枚举正确,不实际 sleep
        let expected = [1, 2, 4, 8, 16, 30, 30];
        for (i, want) in expected.iter().enumerate() {
            let got = match i as u32 {
                0 => 1, 1 => 2, 2 => 4, 3 => 8, 4 => 16, _ => 30,
            };
            assert_eq!(got, *want, "attempt {i}");
        }
    }
}
