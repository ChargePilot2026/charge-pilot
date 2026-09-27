//! dlq_replay:DLQ 重放(每天 04:00)
//! 频率: 1 day
//!
//! **D4 ③b 修复**:此前本任务只 `info!("dlq_replay tick")` 就结束,且**未注册**。
//! 而 `common_stream` 的失败处理在重试耗尽后写 DLQ 并 `XACK` 原消息
//! (`common-redis/src/lib.rs:dead_letter` 的 Lua)——消息已离开 PEL,正常消费者
//! 永不再见。结果是**依赖短暂故障超过重试周期后,计费/退款事件永久停在 DLQ**,
//! 且"ACK 水位"不能代表"业务完成水位"。
//!
//! 现在本任务做真实重放:把 DLQ 中的原始 entry 重新 XADD 回原 stream。
//! 消费者的 receipt 去重保证重复投递安全(逐消费者幂等是 D4 ④ 的前置条件)。

use crate::AppState;
use std::time::Duration;
use tracing::{error, info, warn};

/// 单批重放上限,避免一次性把 DLQ 全量灌回
const BATCH_LIMIT: usize = 200;

pub async fn run(state: AppState) {
    let mut iv = tokio::time::interval(Duration::from_secs(86_400));
    loop {
        iv.tick().await;
        if let Err(e) = replay_once(&state).await {
            error!(error = %e, "dlq replay failed");
        }
    }
}

/// 重放一轮:扫描各业务流的 `.dlq`,把其中的原始消息投回原 stream。
///
/// DLQ entry 由 `dead_letter` 写入,字段为
/// `orig_stream` / `orig_id` / `orig_group` / `reason` / `ts` / `envelope_json` / `raw_json`。
async fn replay_once(state: &AppState) -> common_error::AppResult<()> {
    let mut c = state.redis_stream.conn();
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
            .arg(BATCH_LIMIT)
            .query_async(&mut c)
            .await
            .map_err(|e| common_error::AppError::Internal(format!("XRANGE {dlq}: {e}")))?;

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
                        .map_err(|e| common_error::AppError::Internal(format!("XDEL {dlq}: {e}")))?;
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
