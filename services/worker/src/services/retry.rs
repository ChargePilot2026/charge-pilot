//! 重试能力域(P3)
//!
//! `db` 私有,任务循环拿不到裸 pool。承载两块与「重试/补偿」相关的状态:
//!
//! 1. **DLQ 重放**(`replay_once`)。`common_stream` 的失败处理在重试耗尽后写
//!    DLQ 并 `XACK` 原消息 —— 消息已离开 PEL,正常消费者永不再见。因此本域
//!    每日把 `.dlq` 中的原始 entry 重新 XADD 回原 stream。
//!    口径(勿动):
//!    - 扫描的 6 条业务流是**硬编码白名单**,不在表内的新流不会被重放。
//!    - 每条流 `XRANGE - + COUNT batch_limit` 取最早的一批
//!      (调用方传入的批大小,默认 200,避免一次性把 DLQ 全量灌回)。
//!    - 缺 `orig_stream` 或 `envelope_json` 的 entry **跳过不投**,宁可留滞。
//!    - **先 XADD 成功再 XDEL**,保证"确认投回后才删除",崩溃不丢消息。
//!    - 单条 XADD 失败只记 error 保留待下轮,不影响同批其他 entry。
//! 2. **export_run 探活**(`export_run_configured`):`scheduled_task` 里是否
//!    注册了该任务。查询失败与"未注册"在原实现里**同样表现为 `has=false`**
//!    (只打 info,不打 error),此口径保留。

use common_app::ServiceBase;
use common_error::{AppError, AppResult};
use common_redis::RedisStream;
use tracing::{error, info, warn};

#[derive(Clone)]
pub struct RetryService {
    base: ServiceBase,
    redis_stream: RedisStream,
}

impl RetryService {
    pub fn new(base: ServiceBase, redis_stream: RedisStream) -> Self {
        Self { base, redis_stream }
    }

    /// 扫描各业务流的 `.dlq`,把其中的原始消息投回原 stream。
    ///
    /// DLQ entry 由 `dead_letter` 写入,字段为
    /// `orig_stream` / `orig_id` / `orig_group` / `reason` / `ts` / `envelope_json` / `raw_json`。
    pub async fn replay_once(&self, batch_limit: usize) -> AppResult<()> {
        let mut c = self.redis_stream.conn();
        let streams: Vec<String> = vec![
            common_redis::streams::CHARGE_ENDED.into(),
            common_redis::streams::REFUND_REQUIRED.into(),
            common_redis::streams::INVOICE_REQUIRED.into(),
            common_redis::streams::COMP_TX.into(),
            common_redis::streams::COUPON_GRANT_REQUIRED.into(),
            common_redis::streams::PRICING_RULE_CHANGED.into(),
        ];

        let mut total = 0usize;
        for stream in streams {
            let dlq = format!("{stream}.dlq");
            // XRANGE 取最早的若干条待重放
            let raw: Vec<(String, Vec<String>)> = redis::cmd("XRANGE")
                .arg(&dlq)
                .arg("-")
                .arg("+")
                .arg("COUNT")
                .arg(batch_limit)
                .query_async(&mut c)
                .await
                .map_err(|e| AppError::Internal(format!("XRANGE {dlq}: {e}")))?;

            if raw.is_empty() { continue; }

            let mut replayed = 0usize;
            for (id, flat) in raw {
                let entry = parse_dlq_entry(id.clone(), flat);
                let orig_stream = entry.field("orig_stream").to_string();
                let orig_id = entry.field("orig_id").to_string();
                let envelope_json = entry.field("envelope_json").to_string();
                if !entry.replayable() {
                    warn!(dlq_entry = %id, "DLQ entry 缺少 orig_stream/envelope_json,跳过");
                    continue;
                }

                // 重新投回原 stream
                let result: Result<String, _> = redis::cmd("XADD")
                    .arg(&orig_stream)
                    .arg("*")
                    .arg("event_id")
                    .arg(entry.field("event_id"))
                    .arg("event_type")
                    .arg(entry.field("event_type"))
                    .arg("occurred_at")
                    .arg(entry.field("occurred_at"))
                    .arg("payload")
                    .arg(&envelope_json)
                    .query_async(&mut c)
                    .await;

                match result {
                    Ok(new_id) => {
                        // 确认投回后再从 DLQ 删除,保证不丢
                        let _: i64 = redis::cmd("XDEL")
                            .arg(&dlq)
                            .arg(&id)
                            .query_async(&mut c)
                            .await
                            .map_err(|e| AppError::Internal(format!("XDEL {dlq}: {e}")))?;
                        replayed += 1;
                        warn!(
                            orig_stream = %orig_stream,
                            orig_id = %orig_id,
                            new_id = %new_id,
                            "DLQ 已重放"
                        );
                    }
                    Err(e) => {
                        error!(dlq_entry = %id, error = %e, "DLQ 重放失败,保留待下轮");
                    }
                }
            }
            if replayed > 0 {
                info!(stream = %stream, replayed, "DLQ 重放完成");
            }
            total += replayed;
        }
        info!(total, "dlq_replay 一轮结束");
        Ok(())
    }

    /// `scheduled_task` 中是否注册了 `export_run`。
    ///
    /// 查询错误**不外泄** —— 与原实现一致,调用方只据此打一行 `info`。
    pub async fn export_run_configured(&self) -> bool {
        let r = sqlx::query("SELECT config_json FROM scheduled_task WHERE task_code='export_run' LIMIT 1")
            .fetch_optional(self.base.pool()).await;
        matches!(&r, Ok(Some(_)))
    }
}

/// DLQ entry 结构:由 `dead_letter` 写入,字段为
/// `orig_stream` / `orig_id` / `orig_group` / `reason` / `ts` / `envelope_json` / `raw_json`。
/// 用 `from_redis_value` 让 redis crate 负责形态解析,避免手写变体匹配。
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct DlqEntry {
    pub id: String,
    pub fields: Vec<(String, String)>,
}

impl DlqEntry {
    pub fn field(&self, name: &str) -> &str {
        self.fields
            .iter()
            .find(|(k, _)| k == name)
            .map(|(_, v)| v.as_str())
            .unwrap_or_default()
    }

    /// 能否安全重放:必须同时有原 stream 与载荷
    pub fn replayable(&self) -> bool {
        !self.field("orig_stream").is_empty() && !self.field("envelope_json").is_empty()
    }
}

/// 从 redis 返回值解析出 DLQ entry
pub fn parse_dlq_entry(id: String, flat: Vec<String>) -> DlqEntry {
    let mut fields = Vec::new();
    let mut i = 0;
    while i + 1 < flat.len() {
        fields.push((flat[i].clone(), flat[i + 1].clone()));
        i += 2;
    }
    DlqEntry { id, fields }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sample() -> DlqEntry {
        parse_dlq_entry(
            "1-0".into(),
            vec![
                "orig_stream".into(), "charge_ended_stream".into(),
                "orig_id".into(), "1-0".into(),
                "event_id".into(), "EV1".into(),
                "envelope_json".into(), "{\"a\":1}".into(),
            ],
        )
    }

    #[test]
    fn parses_fields_pairwise() {
        let e = sample();
        assert_eq!(e.id, "1-0");
        assert_eq!(e.field("orig_stream"), "charge_ended_stream");
        assert_eq!(e.field("event_id"), "EV1");
        assert!(e.replayable());
    }

    /// 回归护栏:缺原 stream 或载荷时不得盲投
    #[test]
    fn unreplayable_when_key_fields_missing() {
        let e = parse_dlq_entry("2-0".into(), vec!["reason".into(), "boom".into()]);
        assert!(!e.replayable());
        assert_eq!(e.field("orig_stream"), "");
    }

    /// 奇数个字段(截断)不应 panic
    #[test]
    fn odd_field_count_is_tolerated() {
        let e = parse_dlq_entry("3-0".into(), vec!["a".into(), "1".into(), "b".into()]);
        assert_eq!(e.fields.len(), 1);
    }
}
