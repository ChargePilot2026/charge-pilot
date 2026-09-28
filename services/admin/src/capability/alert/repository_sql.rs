//! alert 域的 repository 层 —— SQL 只允许出现在这里
//!
//! (方案 §三:handler / usecase / domain 层禁 SQL,由 clippy disallowed-methods 保证)
//! 覆盖 `alert_event` / `alert_rule` / `alert_subscription` / `risk_config`。

#![allow(clippy::disallowed_methods, clippy::disallowed_types)]

// `disallowed_types` 只服务两处数据库 JSON 列:`alert_rule.threshold` 与
// `risk_config.value`。两者都是**原样透出**的 JSON 列(方案 §三例外清单
// 第 2 条):阈值可以是任意 JSON 结构(数字 / 对象 / 数组,取决于规则类型),
// 风控值同理 —— 类型化会迫使调用方为每种规则形态各写一个 DTO。

use crate::AppState;
use common_error::{AppError, AppResult};
use serde::Deserialize;

pub async fn alert_events(st: &AppState) -> AppResult<api_contracts::common::ListResponse<api_contracts::admin::AlertEvent>> {
    let rows = sqlx::query(
        "SELECT id, device_id, rule_id, severity, metric, value, threshold, status, acked_by, acked_at, created_at
         FROM alert_event ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.alert.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::AlertEvent> {
        Ok(api_contracts::admin::AlertEvent {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            device_id: sqlx::Row::try_get::<String, _>(r, "device_id")?,
            rule_id: sqlx::Row::try_get::<Option<u64>, _>(r, "rule_id")?,
            severity: sqlx::Row::try_get::<String, _>(r, "severity")?,
            metric: sqlx::Row::try_get::<String, _>(r, "metric")?,
            value: sqlx::Row::try_get::<Option<f64>, _>(r, "value")?,
            status: sqlx::Row::try_get::<String, _>(r, "status")?,
            acked_by: sqlx::Row::try_get::<Option<u64>, _>(r, "acked_by")?,
            created_at: sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "created_at")?.to_rfc3339(),
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::common::ListResponse::new(items))
}

/// ACK 只对 `active` 事件生效;返回 false 表示已被别人处理过。
pub async fn ack_alert(st: &AppState, id: u64, admin_user_id: u64) -> AppResult<bool> {
    let n = sqlx::query("UPDATE alert_event SET status = 'acknowledged', acked_by = ?, acked_at = NOW(3) WHERE id = ? AND status = 'active'")
        .bind(admin_user_id).bind(id).execute(st.alert.pool()).await?;
    Ok(n.rows_affected() > 0)
}

pub async fn alert_rules(st: &AppState) -> AppResult<api_contracts::common::ListResponse<api_contracts::admin::AlertRule>> {
    let rows = sqlx::query("SELECT id, name, device_id_pattern, metric, op, threshold, window_seconds, severity, enabled FROM alert_rule WHERE deleted_at IS NULL")
        .fetch_all(st.alert.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::AlertRule> {
        Ok(api_contracts::admin::AlertRule {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            name: sqlx::Row::try_get::<String, _>(r, "name")?,
            metric: sqlx::Row::try_get::<String, _>(r, "metric")?,
            op: sqlx::Row::try_get::<String, _>(r, "op")?,
            severity: sqlx::Row::try_get::<String, _>(r, "severity")?,
            enabled: sqlx::Row::try_get::<i8, _>(r, "enabled")? != 0,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::common::ListResponse::new(items))
}

#[derive(Debug, Deserialize)]
pub struct AlertRuleCreateReq {
    pub name: String,
    pub device_id_pattern: Option<String>,
    pub metric: String,
    pub op: String,
    pub threshold: serde_json::Value,
    pub window_seconds: Option<u32>,
    pub severity: String,
}

pub async fn insert_alert_rule(st: &AppState, req: &AlertRuleCreateReq) -> AppResult<u64> {
    let result = sqlx::query(
        "INSERT INTO alert_rule (name, device_id_pattern, metric, op, threshold, window_seconds, severity, enabled)
         VALUES (?, COALESCE(?, '*'), ?, ?, ?, COALESCE(?, 60), ?, 1)"
    )
    .bind(&req.name).bind(req.device_id_pattern.as_deref()).bind(&req.metric).bind(&req.op)
    .bind(&req.threshold).bind(req.window_seconds).bind(&req.severity)
    .execute(st.alert.pool()).await?;
    Ok(result.last_insert_id())
}

pub async fn alert_rule_detail(st: &AppState, id: u64) -> AppResult<api_contracts::admin::AlertRuleDetail> {
    let r: Option<(u64, String, String, String, String, serde_json::Value, u32, String, i8)> = sqlx::query_as(
        "SELECT id, name, device_id_pattern, metric, op, threshold, window_seconds, severity, enabled FROM alert_rule WHERE id = ? AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.alert.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("rule".into()))?;
    Ok(api_contracts::admin::AlertRuleDetail {
        id: r.0, name: r.1, device_id_pattern: r.2, metric: r.3, op: r.4,
        threshold: r.5, window_seconds: r.6, severity: r.7, enabled: r.8 != 0,
    })
}

#[derive(Debug, Deserialize)]
pub struct AlertRuleUpdateReq {
    pub name: Option<String>,
    pub severity: Option<String>,
    pub enabled: Option<bool>,
}

pub async fn update_alert_rule(st: &AppState, id: u64, req: &AlertRuleUpdateReq) -> AppResult<bool> {
    let n = sqlx::query(
        "UPDATE alert_rule SET name = COALESCE(?, name), severity = COALESCE(?, severity),
                                enabled = COALESCE(?, enabled)
         WHERE id = ? AND deleted_at IS NULL"
    )
    .bind(req.name.as_deref()).bind(req.severity.as_deref()).bind(req.enabled).bind(id)
    .execute(st.alert.pool()).await?;
    Ok(n.rows_affected() > 0)
}

pub async fn delete_alert_rule(st: &AppState, id: u64) -> AppResult<bool> {
    let n = sqlx::query("UPDATE alert_rule SET deleted_at = NOW(3) WHERE id = ? AND deleted_at IS NULL")
        .bind(id).execute(st.alert.pool()).await?;
    Ok(n.rows_affected() > 0)
}

pub async fn alert_subscriptions(st: &AppState) -> AppResult<api_contracts::common::ListResponse<api_contracts::admin::AlertSubscription>> {
    let rows = sqlx::query("SELECT id, rule_id, severity, webhook_subscription_id, admin_user_id, enabled FROM alert_subscription")
        .fetch_all(st.alert.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::AlertSubscription> {
        Ok(api_contracts::admin::AlertSubscription {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            rule_id: sqlx::Row::try_get::<Option<u64>, _>(r, "rule_id")?,
            severity: sqlx::Row::try_get::<Option<String>, _>(r, "severity")?,
            webhook_subscription_id: sqlx::Row::try_get::<Option<u64>, _>(r, "webhook_subscription_id")?,
            admin_user_id: sqlx::Row::try_get::<Option<u64>, _>(r, "admin_user_id")?,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::common::ListResponse::new(items))
}

#[derive(Debug, Deserialize)]
pub struct SubCreateReq {
    pub rule_id: Option<u64>,
    pub severity: Option<String>,
    pub webhook_subscription_id: Option<u64>,
    pub admin_user_id: Option<u64>,
}

pub async fn insert_alert_subscription(st: &AppState, req: &SubCreateReq) -> AppResult<u64> {
    let result = sqlx::query(
        "INSERT INTO alert_subscription (rule_id, severity, webhook_subscription_id, admin_user_id, enabled) VALUES (?, ?, ?, ?, 1)"
    )
    .bind(req.rule_id).bind(req.severity.as_deref()).bind(req.webhook_subscription_id).bind(req.admin_user_id)
    .execute(st.alert.pool()).await?;
    Ok(result.last_insert_id())
}

pub async fn risk_config(st: &AppState) -> AppResult<api_contracts::common::ListResponse<api_contracts::admin::RiskConfigItem>> {
    let rows = sqlx::query("SELECT `key`, value, description FROM risk_config").fetch_all(st.alert.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::RiskConfigItem> {
        Ok(api_contracts::admin::RiskConfigItem {
            key: sqlx::Row::try_get::<String, _>(r, "key")?,
            value: sqlx::Row::try_get::<serde_json::Value, _>(r, "value")?,
            description: sqlx::Row::try_get::<Option<String>, _>(r, "description")?,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::common::ListResponse::new(items))
}

pub async fn upsert_risk_config(
    st: &AppState,
    key: &str,
    value: &serde_json::Value,
    description: Option<&str>,
    admin_user_id: u64,
) -> AppResult<()> {
    sqlx::query(
        "INSERT INTO risk_config (`key`, value, description, updated_by) VALUES (?, ?, ?, ?)
         ON DUPLICATE KEY UPDATE value = VALUES(value), description = VALUES(description), updated_by = VALUES(updated_by)"
    )
    .bind(key).bind(value).bind(description).bind(admin_user_id)
    .execute(st.alert.pool()).await?;
    Ok(())
}

// ===== 活跃告警(供内部服务读) =====

#[derive(Debug, serde::Deserialize)]
pub struct AlertsQuery { pub device_id: Option<String>, pub status: Option<String> }

pub async fn active_alerts(
    st: &AppState,
    device_id: Option<&str>,
) -> AppResult<api_contracts::common::ListResponse<api_contracts::admin::ActiveAlert>> {
    let mut sql = String::from("SELECT id, device_id, severity, metric, status, created_at FROM alert_event WHERE status = 'active'");
    if device_id.is_some() { sql.push_str(" AND device_id = ?"); }
    sql.push_str(" ORDER BY id DESC LIMIT 100");
    let mut query = sqlx::query(&sql);
    if let Some(d) = device_id { query = query.bind(d); }
    let rows = query.fetch_all(st.alert.pool()).await?;
    let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::ActiveAlert> { Ok(api_contracts::admin::ActiveAlert {
        id: sqlx::Row::try_get::<u64, _>(r, "id")?,
        device_id: sqlx::Row::try_get::<String, _>(r, "device_id")?,
        severity: sqlx::Row::try_get::<String, _>(r, "severity")?,
        metric: sqlx::Row::try_get::<String, _>(r, "metric")?,
    }) }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::common::ListResponse::new(items))
}

/// 仪表盘用的活跃告警计数。
pub async fn active_alert_count(st: &AppState) -> AppResult<i64> {
    Ok(sqlx::query_scalar("SELECT COUNT(*) FROM alert_event WHERE status='active'")
        .fetch_one(st.order.pool()).await?)
}

/// 仪表盘读的权限复核。迁移前这段 SQL 在 `api/dashboard.rs` 里,
/// 现与仪表盘读用到的其它 SQL 一并归到本域 repository。
pub async fn has_dashboard_permission(
    st: &AppState,
    c: &crate::capability::identity::ActiveAdmin,
) -> AppResult<bool> {
    let allowed: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM admin_user_role a JOIN role r ON r.id=a.role_id AND r.deleted_at IS NULL
         JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id
         WHERE a.id=? AND a.username=? AND a.status='active' AND a.deleted_at IS NULL AND p.code='dashboard.read')",
    ).bind(c.admin_user_id).bind(&c.sub).fetch_one(st.order.pool()).await?;
    Ok(allowed)
}

// ===== Stream 消费侧(alert 域)

// `alert_event` 上只有非唯一索引 `idx_event(event_id, created_month)`,
// INSERT IGNORE 并不能去重 —— 幂等仍靠 EXISTS 判定。
pub async fn alert_already_recorded(
    tx: &mut common_db::Tx<'_>,
    event_id: &str,
) -> AppResult<bool> {
    let already_recorded: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM alert_event WHERE event_id = ?)"
    ).bind(event_id).fetch_one(tx.executor()).await?;
    Ok(already_recorded)
}

pub async fn insert_alert_event(
    tx: &mut common_db::Tx<'_>,
    device_id: &str,
    severity: &str,
    metric: &str,
    event_id: &str,
    created_month: &str,
) -> AppResult<()> {
    sqlx::query(
        "INSERT INTO alert_event (device_id, severity, metric, status, event_id, created_at, created_month)
         VALUES (?, ?, ?, 'active', ?, NOW(3), ?)"
    )
    .bind(device_id).bind(severity).bind(metric).bind(event_id).bind(created_month)
    .execute(tx.executor()).await?;
    Ok(())
}

/// outbox 的 `event_id` 必须逐订阅唯一(`uk_event`)。
/// IGNORE 只用于吞掉并发重放撞唯一键的情况。
pub async fn enqueue_webhook_delivery(
    tx: &mut common_db::Tx<'_>,
    outbox_event_id: &str,
    envelope: &common_redis::StreamEnvelope,
) -> AppResult<()> {
    sqlx::query(
        "INSERT IGNORE INTO event_outbox (event_id, stream, envelope_json) VALUES (?, ?, ?)"
    )
    .bind(outbox_event_id)
    .bind(common_redis::streams::WEBHOOK_RETRY)
    .bind(serde_json::to_value(envelope)?)
    .execute(tx.executor()).await?;
    Ok(())
}

/// 取出应当收到这条告警的 webhook 订阅。
///
/// 过滤分两层:SQL 负责启用/软删/规则/等级(可索引),`event_types` 的 JSON 数组
/// 匹配放在 Rust 侧 —— MySQL 里判 JSON 数组既难读又用不上索引。
pub async fn load_alert_targets(
    tx: &mut common_db::Tx<'_>,
    rule_id: Option<u64>,
    severity: &str,
) -> AppResult<Vec<crate::stream_consumer::AlertSubscriptionTarget>> {
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
        out.push(crate::stream_consumer::AlertSubscriptionTarget {
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
