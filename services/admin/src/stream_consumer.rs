//! admin Stream 消费者
//!
//! 订阅:
//!   - alert_stream.admin-cg → 落库到 alert_event
//!   - refund_required_stream.admin-cg → claim + 调微信退款 + 写结果
//!   - invoice_required_stream.admin-cg → 写 invoice_review 待审核
//!   - webhook_retry_stream.admin-cg → 投递(本期由 worker 兜底,admin 只入队)
//!   - device_event_stream.admin-cg → 设备状态 UI 推送(本期仅记账)

use crate::AppState;
use async_trait::async_trait;
use common_error::{AppError, AppResult};
use common_redis::{StreamEntry, StreamEnvelope};
use common_stream::{ConsumerGroup, StreamHandler};
use serde_json::json;
use tracing::info;

pub async fn spawn_all(state: AppState) -> AppResult<()> {
    let cg = ConsumerGroup::new(state.redis_stream.clone());

    cg.register(common_redis::streams::ALERT, "admin-cg", "admin-1",
        Box::new(AlertHandler { state: state.clone() }), 32, 1000).await?;
    cg.register(common_redis::streams::REFUND_REQUIRED, "admin-cg", "admin-1",
        Box::new(RefundRequiredHandler { state: state.clone() }), 16, 2000).await?;
    cg.register(common_redis::streams::INVOICE_REQUIRED, "admin-cg", "admin-1",
        Box::new(InvoiceRequiredHandler { state: state.clone() }), 16, 2000).await?;
    cg.register(common_redis::streams::DEVICE_EVENT, "admin-cg", "admin-1",
        Box::new(DeviceEventHandler { state: state.clone() }), 32, 1000).await?;

    Ok(())
}

pub struct AlertHandler { pub state: AppState }

/// 告警事件展开成 webhook 投递事件时使用的事件类型。
/// 与 `webhook_subscription.event_types` 里登记的字符串对齐。
pub const ALERT_RECORDED_EVENT: &str = "alert_recorded";

/// 一个应当收到本条告警的 webhook 订阅(已按 `alert_subscription` 的启用/规则/等级过滤)。
#[derive(Debug, Clone)]
pub struct AlertSubscriptionTarget {
    pub id: u64,
    pub url: String,
    pub secret: String,
    pub headers: Option<serde_json::Value>,
    pub event_types: Vec<String>,
}

impl AlertSubscriptionTarget {
    /// 是否订阅了本条事件。
    ///
    /// 空数组按"订阅全部"处理:运营在 PC 后台建订阅时若不勾类型,
    /// 期待的是"都发",而不是静默地一条都收不到。
    pub fn wants(&self, event_type: &str) -> bool {
        self.event_types.is_empty() || self.event_types.iter().any(|t| t == event_type)
    }
}

#[async_trait]
impl StreamHandler for AlertHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let p = &entry.envelope.payload;
        let device_id = p.get("device_id").and_then(|v| v.as_str()).filter(|v| !v.is_empty())
            .ok_or_else(|| AppError::BadRequest("alert event is missing device_id".into()))?.to_string();
        let severity = p.get("severity").and_then(|v| v.as_str()).unwrap_or("warning").to_string();
        if !["warning", "critical", "fatal"].contains(&severity.as_str()) {
            return Err(AppError::BadRequest("alert event severity is invalid".into()));
        }
        let metric = p.get("metric").and_then(|v| v.as_str()).filter(|v| !v.is_empty())
            .ok_or_else(|| AppError::BadRequest("alert event is missing metric".into()))?.to_string();
        let event_id = entry.envelope.event_id.clone();
        // 告警帧可以带 rule_id(网关的设备告警帧目前不填,按无规则处理),
        // 有值时才能命中绑定了具体规则的订阅。
        let rule_id: Option<u64> = p.get("rule_id").and_then(|v| v.as_u64());

        // 告警落库与"要投递哪些 webhook"必须原子:只看事件有没有入库
        // 而不管投递入队,进程在两步之间退出会让订阅方永远收不到这条告警。
        let mut tx = self.state.alert.begin().await?;
        // `alert_event` 上只有非唯一索引 `idx_event(event_id, created_month)`,
        // INSERT IGNORE 并不能去重 —— 幂等仍靠 EXISTS 判定。
        let already_recorded: bool = sqlx::query_scalar(
            "SELECT EXISTS(SELECT 1 FROM alert_event WHERE event_id = ?)"
        ).bind(&event_id).fetch_one(tx.executor()).await?;
        if !already_recorded {
            let now_month = chrono::Utc::now().format("%Y-%m-01").to_string();
            sqlx::query(
                "INSERT INTO alert_event (device_id, severity, metric, status, event_id, created_at, created_month)
                 VALUES (?, ?, ?, 'active', ?, NOW(3), ?)"
            )
            .bind(&device_id).bind(&severity).bind(&metric).bind(&event_id).bind(&now_month)
            .execute(tx.executor()).await?;
        }

        let targets = if already_recorded {
            // 重复投递(consumer group 重放)不重复入队,否则订阅方会收到
            // 语义上的第二条告警。
            Vec::new()
        } else {
            load_alert_targets(&mut tx, rule_id, &severity).await?
        };
        let queued = targets.iter().filter(|t| t.wants(ALERT_RECORDED_EVENT)).count();
        for target in targets.iter().filter(|t| t.wants(ALERT_RECORDED_EVENT)) {
            let env = build_alert_delivery_envelope(target, &event_id, &device_id, &severity);
            // outbox 的 `event_id` 必须逐订阅唯一(`uk_event`):同一条告警要发给 N 个订阅,
            // 但 envelope 内的 event_id 保持原始告警 id,让 worker 侧的幂等键
            // `(subscription_id, event_id)` 仍能识别"同一告警的重复投递"。
            let outbox_event_id = format!("{event_id}:{}", target.id);
            // IGNORE 只用于吞掉并发重放撞唯一键的情况;上一轮的 EXISTS 判定已把
            // 正常重放挡掉,走到这里的重复必然是竞态,不值得为它让整条消息进 DLQ。
            sqlx::query(
                "INSERT IGNORE INTO event_outbox (event_id, stream, envelope_json) VALUES (?, ?, ?)"
            )
            .bind(&outbox_event_id)
            .bind(common_redis::streams::WEBHOOK_RETRY)
            .bind(serde_json::to_value(&env)?)
            .execute(tx.executor()).await?;
        }
        tx.commit().await?;
        info!(device_id, severity, targets = queued, "alert recorded");
        Ok(())
    }
}

/// 取出应当收到这条告警的 webhook 订阅。
///
/// 过滤分两层:SQL 负责启用/软删/规则/等级(可索引),`event_types` 的 JSON 数组
/// 匹配放在 Rust 侧 —— MySQL 里判 JSON 数组既难读又用不上索引。
async fn load_alert_targets(
    tx: &mut common_db::Tx<'_>,
    rule_id: Option<u64>,
    severity: &str,
) -> AppResult<Vec<AlertSubscriptionTarget>> {
    let rows = sqlx::query(
        "SELECT DISTINCT s.id, s.url, s.secret, s.headers_json, s.event_types
         FROM alert_subscription a
         JOIN webhook_subscription s ON s.id = a.webhook_subscription_id
         WHERE a.enabled = 1
           AND s.enabled = 1
           AND s.deleted_at IS NULL
           AND (a.rule_id IS NULL OR a.rule_id = ?)
           AND (a.severity IS NULL OR a.severity = ?)"
    )
    .bind(rule_id).bind(severity)
    .fetch_all(tx.executor()).await?;
    let mut out = Vec::with_capacity(rows.len());
    for r in rows.iter() {
        let event_types: serde_json::Value = sqlx::Row::try_get(r, "event_types")?;
        out.push(AlertSubscriptionTarget {
            id: sqlx::Row::try_get(r, "id")?,
            url: sqlx::Row::try_get(r, "url")?,
            secret: sqlx::Row::try_get(r, "secret")?,
            headers: sqlx::Row::try_get(r, "headers_json")?,
            // 列是 NOT NULL,但历史数据可能是 `null` 而非 `[]`;都按"订阅全部"处理。
            event_types: serde_json::from_value(event_types).unwrap_or_default(),
        });
    }
    Ok(out)
}

/// 组装投给 worker 的投递事件。
///
/// 载荷必须自带 url/secret:worker 拿到的只有这条消息,没有回查 admin 的通道。
/// envelope 内的 `event_id` 刻意用**原始告警 id**而非 outbox 行的 `event_id`
/// (`{告警id}:{订阅id}`)—— `webhook_delivery_log` 的幂等键是
/// `(subscription_id, event_id)`,换成前者会让同一告警的多次重试变成多条投递记录。
pub fn build_alert_delivery_envelope(
    target: &AlertSubscriptionTarget,
    alert_event_id: &str,
    device_id: &str,
    severity: &str,
) -> StreamEnvelope {
    let mut payload = json!({
        "subscription_id": target.id,
        "url": target.url,
        "secret": target.secret,
        "event_id": alert_event_id,
        "event_type": ALERT_RECORDED_EVENT,
        "alert_device_id": device_id,
        "severity": severity,
        "occurred_at": chrono::Utc::now().to_rfc3339(),
    });
    // headers 为空对象或非对象时省略该键,免得 worker 拿到 `{}` 还以为
    // 运营显式配了空 header。
    if let Some(obj @ serde_json::Value::Object(_)) = &target.headers {
        payload["headers"] = obj.clone();
    }
    StreamEnvelope::new(ALERT_RECORDED_EVENT, "admin", payload)
}

pub struct RefundRequiredHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for RefundRequiredHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        crate::refund_task::enqueue(&self.state,entry).await
    }
}

pub struct InvoiceRequiredHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for InvoiceRequiredHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let p = &entry.envelope.payload;
        let invoice_request_id = p.get("invoice_request_id").and_then(|v| v.as_u64())
            .filter(|id| *id > 0)
            .ok_or_else(|| AppError::BadRequest("invoice event is missing invoice_request_id".into()))?;
        sqlx::query("INSERT IGNORE INTO invoice_review (invoice_request_id, review_status) VALUES (?, 'pending')")
            .bind(invoice_request_id).execute(self.state.config.pool()).await?;
        info!(invoice_request_id, "invoice required recorded");
        Ok(())
    }
}

pub struct DeviceEventHandler { pub state: AppState }

#[async_trait]
impl StreamHandler for DeviceEventHandler {
    async fn handle(&self, entry: &StreamEntry) -> AppResult<()> {
        let p = &entry.envelope.payload;
        let device_id = p.get("device_id").and_then(|v| v.as_str()).filter(|v| !v.is_empty())
            .ok_or_else(|| AppError::BadRequest("device event is missing device_id".into()))?.to_string();
        // 更新设备最近一次遥测时间(可选:便于 PC 后台看板上显示)
        sqlx::query("UPDATE device_meta SET last_seen_at = NOW(3) WHERE device_id = ?")
            .bind(&device_id)
            .execute(self.state.config.pool())
            .await?;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn target(event_types: &[&str], headers: Option<serde_json::Value>) -> AlertSubscriptionTarget {
        AlertSubscriptionTarget {
            id: 7,
            url: "https://ops.example.com/hook".into(),
            secret: "whsec_test".into(),
            headers,
            event_types: event_types.iter().map(|s| s.to_string()).collect(),
        }
    }

    #[test]
    fn empty_event_types_means_subscribe_to_everything() {
        assert!(target(&[], None).wants(ALERT_RECORDED_EVENT));
    }

    #[test]
    fn subscription_matching_the_target_event_is_accepted() {
        assert!(target(&["alert_recorded"], None).wants(ALERT_RECORDED_EVENT));
        assert!(target(&["ota_scheduled", "alert_recorded"], None).wants(ALERT_RECORDED_EVENT));
    }

    #[test]
    fn subscription_without_the_target_event_is_skipped() {
        assert!(!target(&["ota_scheduled"], None).wants(ALERT_RECORDED_EVENT));
    }

    /// worker 侧第一件事就取 `url`,缺了就是 BadRequest —— 载荷字段是跨服务的硬契约。
    #[test]
    fn delivery_payload_carries_everything_the_worker_needs() {
        let env = build_alert_delivery_envelope(
            &target(&["alert_recorded"], None),
            "alert-1",
            "D1",
            "critical",
        );
        assert_eq!(env.event_type, ALERT_RECORDED_EVENT);
        assert_eq!(env.producer, "admin");
        // envelope 的 event_id 由 StreamEnvelope 自行生成,真正进 payload 的是告警 id
        assert_eq!(env.payload["subscription_id"], serde_json::json!(7));
        assert_eq!(env.payload["url"], serde_json::json!("https://ops.example.com/hook"));
        assert_eq!(env.payload["secret"], serde_json::json!("whsec_test"));
        assert_eq!(env.payload["event_id"], serde_json::json!("alert-1"));
        assert_eq!(env.payload["event_type"], serde_json::json!("alert_recorded"));
        assert_eq!(env.payload["alert_device_id"], serde_json::json!("D1"));
        assert_eq!(env.payload["severity"], serde_json::json!("critical"));
        assert!(env.payload["occurred_at"].as_str().is_some_and(|v| !v.is_empty()));
        assert!(env.payload.get("headers").is_none(), "未配 header 时不应出现该键");
    }

    #[test]
    fn configured_headers_are_forwarded_verbatim() {
        let headers = serde_json::json!({"X-Tenant": "acme"});
        let env = build_alert_delivery_envelope(
            &target(&["alert_recorded"], Some(headers.clone())),
            "alert-2",
            "D2",
            "warning",
        );
        assert_eq!(env.payload["headers"], headers);
    }
}
