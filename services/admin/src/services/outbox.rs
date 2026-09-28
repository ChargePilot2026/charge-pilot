//! admin 事件 outbox 发布器(D11)
//!
//! 事件与业务写**同事务**落 `event_outbox`(admin_db 0022),由本发布器投递到
//! Redis Stream:业务写已提交而 Redis 抖动时,事件不会随进程退出一起消失。
//!
//! 语义(改动前必读):
//! - `FOR UPDATE SKIP LOCKED` 取一批,多实例并发安全:两个 admin 实例不会抢到同一行。
//! - XADD 与状态回写在**同一事务**内。批次中途出错整批回滚,行留在 `pending`,
//!   下一轮重来;不会留下"状态已 published 但实际没投递"的空洞。
//! - XADD 成功后、commit 前崩溃仍可能重投同一 `event_id`,消费侧必须幂等
//!   (worker 侧以 `(subscription_id, event_id)` 为幂等键)。
//! - `retry_count` 超过 [`MAX_RETRY`] 判死:反复失败的行留在 `pending` 只会
//!   空耗发布器,而且没人会来看。

// 本文件是 admin 侧的 repository 层 —— SQL 与 `serde_json::Value` 的 JSON 列解码
// 都归属于此,不是 handler 顺手写的。
// 仓库根 clippy.toml 禁止 `sqlx::query*` 出现在服务对象之外;admin 当前还是
// `allow` 级别,S9/S10 转 `deny` 后本文件需要这两条例外才能独立成立。
#![allow(clippy::disallowed_methods, clippy::disallowed_types)]

use std::time::Duration;

use common_app::ServiceBase;
use common_error::AppResult;
use common_redis::{RedisStream, StreamEnvelope};
use sqlx::Row;

/// 单次 XADD 的超时上限。Redis 无响应时不能让发布器把整批事务吊住。
const PUBLISH_TIMEOUT_SECS: u64 = 3;
/// 重试次数上限,超过判死。
const MAX_RETRY: i64 = 10;
/// 指数退避上限(秒)。
const MAX_BACKOFF_SECS: u64 = 3600;

#[derive(Clone)]
pub struct OutboxService {
    base: ServiceBase,
    redis_stream: RedisStream,
}

impl OutboxService {
    pub fn new(base: ServiceBase, redis_stream: RedisStream) -> Self {
        Self { base, redis_stream }
    }

    /// 发布一批待发事件,返回本轮**成功投递**的条数(判死与推迟重试的不计入)。
    ///
    /// 整批共用一个事务:批次中途出错时所有行都留在 `pending`,由下一轮重投。
    pub async fn publish_pending(&self, batch: usize) -> AppResult<usize> {
        if batch == 0 {
            return Ok(0);
        }
        let mut tx = self.base.begin().await?;
        // LIMIT 的值来自调用方(后台循环写死 50),不是外部输入,拼进 SQL 是安全的;
        // MySQL 的 LIMIT 走占位符在不同客户端上行为不一致,这里选确定的那条路。
        let sql = format!(
            "SELECT id, stream, envelope_json, retry_count FROM event_outbox
             WHERE status='pending' AND scheduled_at <= UTC_TIMESTAMP(3)
             ORDER BY id LIMIT {batch} FOR UPDATE SKIP LOCKED"
        );
        let rows = match sqlx::query(&sql).fetch_all(tx.executor()).await {
            Ok(rows) => rows,
            Err(e) => {
                let _ = tx.rollback().await;
                return Err(e.into());
            }
        };
        if rows.is_empty() {
            tx.rollback().await?;
            return Ok(0);
        }

        let mut published = 0usize;
        for row in rows {
            let id: u64 = row.try_get("id")?;
            let stream: String = row.try_get("stream")?;
            let json: serde_json::Value = row.try_get("envelope_json")?;
            let retry_count: i64 = row.try_get("retry_count")?;
            match self.publish_locked(&mut tx, id, &stream, retry_count, json).await {
                Ok(true) => published += 1,
                Ok(false) => {}
                // 记账失败整批回滚:宁可下轮重投,也不能让状态与 Redis 实际不一致。
                Err(e) => {
                    let _ = tx.rollback().await;
                    return Err(e);
                }
            }
        }
        tx.commit().await?;
        Ok(published)
    }

    /// 健康检查。与其它能力域一致走同一条连接,Redis 不在这里探。
    pub async fn ping(&self) -> AppResult<()> {
        self.base.ping().await
    }

    /// 投递单行并回写状态。`Ok(true)` = 已投递。
    async fn publish_locked(
        &self,
        tx: &mut common_db::Tx<'_>,
        id: u64,
        stream: &str,
        retry_count: i64,
        json: serde_json::Value,
    ) -> AppResult<bool> {
        let envelope = match serde_json::from_value::<StreamEnvelope>(json) {
            Ok(env) => env,
            Err(e) => {
                // 结构错乱的行永远不会投递成功,直接判死,免得每轮都重试一遍。
                sqlx::query("UPDATE event_outbox SET status='failed', last_error=? WHERE id=?")
                    .bind(clip_error(&format!("envelope_json 不是合法的 StreamEnvelope: {e}")))
                    .bind(id)
                    .execute(tx.executor())
                    .await?;
                tracing::error!(outbox_id = id, error = %e, "outbox 载荷无法反序列化,已判死");
                return Ok(false);
            }
        };

        let message_id = match tokio::time::timeout(
            Duration::from_secs(PUBLISH_TIMEOUT_SECS),
            self.redis_stream.xadd_envelope(stream, &envelope),
        )
        .await
        {
            Ok(Ok(mid)) => Some(mid),
            Ok(Err(e)) => {
                tracing::warn!(outbox_id = id, error = %e, "outbox XADD 失败,推迟重试");
                None
            }
            Err(_elapsed) => {
                tracing::warn!(outbox_id = id, "outbox XADD 超时,推迟重试");
                None
            }
        };

        match message_id {
            Some(mid) => {
                sqlx::query(
                    "UPDATE event_outbox SET status='published', published_at=UTC_TIMESTAMP(3),
                     stream_message_id=?, last_error=NULL WHERE id=?",
                )
                .bind(&mid)
                .bind(id)
                .execute(tx.executor())
                .await?;
                Ok(true)
            }
            None => {
                let next = retry_count + 1;
                let reason = clip_error("Redis 发布失败或超时");
                if next > MAX_RETRY {
                    sqlx::query(
                        "UPDATE event_outbox SET status='failed', retry_count=?, last_error=? WHERE id=?",
                    )
                    .bind(next)
                    .bind(&reason)
                    .bind(id)
                    .execute(tx.executor())
                    .await?;
                    tracing::error!(
                        outbox_id = id, event_id = %envelope.event_id, stream, retry_count = next,
                        "outbox 重试次数超限,已判死,需人工介入"
                    );
                } else {
                    // MySQL 的 INTERVAL 语法不接受占位符;秒数由 backoff_secs 算出,
                    // 上界 3600,是内部常量不是外部输入,可以安全拼入。
                    let sec = backoff_secs(next);
                    let sql = format!(
                        "UPDATE event_outbox SET retry_count={next},
                         scheduled_at=UTC_TIMESTAMP(3)+INTERVAL {sec} SECOND, last_error=? WHERE id=?"
                    );
                    sqlx::query(&sql)
                        .bind(&reason)
                        .bind(id)
                        .execute(tx.executor())
                        .await?;
                }
                Ok(false)
            }
        }
    }
}

/// 指数退避:第 `retry_count` 次失败后推迟 `min(2^retry_count, 3600)` 秒。
///
/// `checked_shl` 而非 `<<`:`retry_count` 来自 DB,位移溢出是未定义行为。
pub fn backoff_secs(retry_count: i64) -> u64 {
    let n = retry_count.clamp(0, 63) as u32;
    1u64.checked_shl(n).unwrap_or(MAX_BACKOFF_SECS).min(MAX_BACKOFF_SECS)
}

/// `last_error` 是 VARCHAR(255),超长会在严格模式下直接把 UPDATE 打成失败,
/// 反而让整批回滚。截断要按字符而非字节,否则中文会切出半个字。
fn clip_error(msg: &str) -> String {
    msg.chars().take(255).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn backoff_grows_exponentially_then_is_capped() {
        assert_eq!(backoff_secs(0), 1);
        assert_eq!(backoff_secs(1), 2);
        assert_eq!(backoff_secs(2), 4);
        assert_eq!(backoff_secs(3), 8);
        assert_eq!(backoff_secs(10), 1024);
        // 2^12 已越过 3600 上限,此后恒定
        assert_eq!(backoff_secs(12), 3600);
        assert_eq!(backoff_secs(40), 3600);
    }

    #[test]
    fn backoff_never_panics_on_absurd_retry_count() {
        assert_eq!(backoff_secs(i64::MAX), 3600);
        assert_eq!(backoff_secs(-1), 1);
    }

    #[test]
    fn error_clipping_keeps_chinese_characters_intact() {
        let long = "错".repeat(400);
        let clipped = clip_error(&long);
        assert_eq!(clipped.chars().count(), 255);
        assert!(clipped.chars().all(|c| c == '错'));
    }
}
