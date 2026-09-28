//! config 域的 repository 层 —— SQL 只允许出现在这里
//!
//! (方案 §三:handler / usecase / domain 层禁 SQL,由 clippy disallowed-methods 保证)
//! 覆盖 `pricing_rule` / `pricing_template` / `split_template` / `split_party` /
//! `announcement` / `whitelabel_config` / `customer_service_config` /
//! `event_outbox` / `audit_log`。

#![allow(clippy::disallowed_methods, clippy::disallowed_types)]

// `disallowed_types` 只服务数据库 JSON 列的原样透出(方案 §三例外清单第 2 条):
// `pricing_rule.time_of_use_json`、`customer_service_config.working_hours_json`、
// `whitelabel_config.config_json`、`announcement` 的 `config_json`、`audit_log`
// 的 `before_json` / `after_json` —— 都是**原样写入 / 原样读出**的 JSON 列。

use crate::AppState;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use sqlx::Row;

// ===== 计费规则 =====

pub async fn charge_rules(st: &AppState) -> AppResult<api_contracts::common::ListResponse<api_contracts::admin::ChargeRule>> {
    let rows = sqlx::query("SELECT id, name, mode, service_fee_cents_per_kwh, service_fee_cents_per_min, min_charge_cents, version, status FROM pricing_rule WHERE deleted_at IS NULL")
        .fetch_all(st.config.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::ChargeRule> {
        Ok(api_contracts::admin::ChargeRule {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            name: sqlx::Row::try_get::<String, _>(r, "name")?,
            mode: sqlx::Row::try_get::<String, _>(r, "mode")?,
            service_fee_cents_per_kwh: sqlx::Row::try_get::<i64, _>(r, "service_fee_cents_per_kwh")?,
            service_fee_cents_per_min: sqlx::Row::try_get::<i64, _>(r, "service_fee_cents_per_min")?,
            min_charge_cents: sqlx::Row::try_get::<i64, _>(r, "min_charge_cents")?,
            version: sqlx::Row::try_get::<u32, _>(r, "version")?,
            status: sqlx::Row::try_get::<String, _>(r, "status")?,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::common::ListResponse::new(items))
}

#[derive(Debug, Deserialize)]
pub struct ChargeRuleCreateReq {
    pub name: String,
    pub station_id: Option<u64>,
    pub mode: String,
    pub time_of_use_json: Option<serde_json::Value>,
    pub service_fee_cents_per_kwh: i64,
    pub service_fee_cents_per_min: i64,
    pub min_charge_cents: i64,
}

/// 送进 [`insert_charge_rule_with_event`] 的载荷(尚无 `id`)。
///
/// `id` 要在 INSERT 之后才拿得到,而事件与业务写必须**同事务**落 outbox
/// (D5),所以这里先只带 `key`,真正的组装在 repository 内按真实 id 重做。
#[derive(Debug, Clone, serde::Serialize)]
pub struct PricingRuleChangedForInsert {
    pub key: String,
}

/// 插入计费规则**并把 `pricing_rule_changed` 事件同事务落 outbox**。
///
/// **D5 修复**:原先用 `let _ =` 吞掉发布失败 —— DB 已提交而事件永久丢失。
/// 现在事件与业务写**同事务**落 `event_outbox`(admin_db 见 0022 迁移),
/// 由发布器负责投递;投递失败也只是 outbox 状态变化,不会丢事件。
///
/// 载荷的 `id` 只有 INSERT 之后才拿得到(旧实现是在 handler 里
/// `json!({"id": id, "key": req.name})` 现拼),因此这里接收**不含 id 的
/// 载荷** [`PricingRuleChangedForInsert`],拿到 `last_insert_id()` 后再按真实
/// id 组装事件 —— 事件与业务写仍在同一事务内(方案 §三要求)。
pub async fn insert_charge_rule_with_event(
    st: &AppState,
    req: &ChargeRuleCreateReq,
    payload: &PricingRuleChangedForInsert,
) -> AppResult<u64> {
    let mut tx = st.config.begin().await?;
    let result = sqlx::query(
        "INSERT INTO pricing_rule (name, station_id, mode, time_of_use_json, service_fee_cents_per_kwh, service_fee_cents_per_min, min_charge_cents)
         VALUES (?, ?, ?, ?, ?, ?, ?)"
    )
    .bind(&req.name).bind(req.station_id).bind(&req.mode).bind(&req.time_of_use_json)
    .bind(req.service_fee_cents_per_kwh).bind(req.service_fee_cents_per_min).bind(req.min_charge_cents)
    .execute(tx.executor()).await?;
    let id = result.last_insert_id();
    let env = crate::capability::config::pricing_rule_changed(id, payload.key.clone());
    sqlx::query("INSERT INTO event_outbox (event_id, stream, envelope_json) VALUES (?, ?, ?)")
        .bind(&env.event_id)
        .bind(common_redis::streams::PRICING_RULE_CHANGED)
        .bind(serde_json::to_value(&env)?)
        .execute(tx.executor())
        .await?;
    tx.commit().await?;
    Ok(id)
}

// ===== 计费模板 =====

pub async fn pricing_templates(st: &AppState) -> AppResult<api_contracts::common::ListResponse<api_contracts::admin::PricingTemplate>> {
    let rows = sqlx::query("SELECT id, code, name, default_pricing_rule_id FROM pricing_template WHERE deleted_at IS NULL")
        .fetch_all(st.config.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::PricingTemplate> {
        Ok(api_contracts::admin::PricingTemplate {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            code: sqlx::Row::try_get::<String, _>(r, "code")?,
            name: sqlx::Row::try_get::<String, _>(r, "name")?,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::common::ListResponse::new(items))
}

#[derive(Debug, Deserialize)]
pub struct PricingTemplateCreateReq {
    pub code: String,
    pub name: String,
    pub default_pricing_rule_id: Option<u64>,
}

pub async fn insert_pricing_template(st: &AppState, req: &PricingTemplateCreateReq) -> AppResult<u64> {
    let result = sqlx::query("INSERT INTO pricing_template (code, name, default_pricing_rule_id) VALUES (?, ?, ?)")
        .bind(&req.code).bind(&req.name).bind(req.default_pricing_rule_id)
        .execute(st.config.pool()).await?;
    Ok(result.last_insert_id())
}

// ===== 分账模板 / 分账方 =====

pub async fn split_templates(st: &AppState) -> AppResult<api_contracts::common::ListResponse<api_contracts::admin::SplitTemplate>> {
    let rows = sqlx::query("SELECT id, code, name, mode, status FROM split_template WHERE deleted_at IS NULL")
        .fetch_all(st.config.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::SplitTemplate> {
        Ok(api_contracts::admin::SplitTemplate {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            code: sqlx::Row::try_get::<String, _>(r, "code")?,
            name: sqlx::Row::try_get::<String, _>(r, "name")?,
            mode: sqlx::Row::try_get::<String, _>(r, "mode")?,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::common::ListResponse::new(items))
}

#[derive(Debug, Deserialize)]
pub struct SplitTemplateCreateReq {
    pub code: String,
    pub name: String,
    pub mode: String,
}

pub async fn insert_split_template(st: &AppState, req: &SplitTemplateCreateReq) -> AppResult<u64> {
    let result = sqlx::query("INSERT INTO split_template (code, name, mode) VALUES (?, ?, ?)")
        .bind(&req.code).bind(&req.name).bind(&req.mode)
        .execute(st.config.pool()).await?;
    Ok(result.last_insert_id())
}

pub async fn split_parties(st: &AppState, id: u64) -> AppResult<api_contracts::common::ListResponse<api_contracts::admin::SplitParty>> {
    let rows = sqlx::query("SELECT id, party_code, party_name, ratio_bp FROM split_party WHERE split_template_id = ?")
        .bind(id).fetch_all(st.config.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::SplitParty> {
        Ok(api_contracts::admin::SplitParty {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            party_code: sqlx::Row::try_get::<String, _>(r, "party_code")?,
            party_name: sqlx::Row::try_get::<String, _>(r, "party_name")?,
            ratio_bp: sqlx::Row::try_get::<u32, _>(r, "ratio_bp")?,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::common::ListResponse::new(items))
}

#[derive(Debug, Deserialize)]
pub struct SplitPartyCreateReq {
    pub party_code: String,
    pub party_name: String,
    pub ratio_bp: u32,
    pub bank_account: Option<String>,
    pub bank_name: Option<String>,
}

pub async fn insert_split_party(st: &AppState, id: u64, req: &SplitPartyCreateReq) -> AppResult<u64> {
    let result = sqlx::query(
        "INSERT INTO split_party (split_template_id, party_code, party_name, ratio_bp, bank_account, bank_name) VALUES (?, ?, ?, ?, ?, ?)"
    )
    .bind(id).bind(&req.party_code).bind(&req.party_name).bind(req.ratio_bp)
    .bind(req.bank_account.as_deref()).bind(req.bank_name.as_deref())
    .execute(st.config.pool()).await?;
    Ok(result.last_insert_id())
}

// ===== 公告 =====

pub async fn announcements(st: &AppState) -> AppResult<api_contracts::common::ListResponse<api_contracts::admin::AnnouncementListItem>> {
    let rows = sqlx::query(
        "SELECT id, title, content, scope, priority, start_at, end_at, status, created_at
         FROM announcement WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.config.pool()).await?;
    let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::AnnouncementListItem> {
        Ok(api_contracts::admin::AnnouncementListItem {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            title: sqlx::Row::try_get::<String, _>(r, "title")?,
            content: sqlx::Row::try_get::<String, _>(r, "content")?,
            scope: sqlx::Row::try_get::<String, _>(r, "scope")?,
            priority: sqlx::Row::try_get::<u8, _>(r, "priority")?,
            start_at: sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "start_at")?.to_rfc3339(),
            end_at: sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(r, "end_at")?
                .map(|t| t.to_rfc3339()),
            status: sqlx::Row::try_get::<String, _>(r, "status")?,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::common::ListResponse::new(items))
}

#[derive(Debug, Deserialize)]
pub struct AnnouncementCreateReq {
    pub title: String,
    pub content: String,
    pub scope: String,
    pub priority: Option<u8>,
    pub start_at: chrono::DateTime<chrono::Utc>,
    pub end_at: Option<chrono::DateTime<chrono::Utc>>,
}

pub async fn insert_announcement(st: &AppState, c_admin_id: u64, req: &AnnouncementCreateReq) -> AppResult<u64> {
    let result = sqlx::query(
        "INSERT INTO announcement (title, content, scope, priority, start_at, end_at, status, created_by)
         VALUES (?, ?, ?, COALESCE(?, 0), ?, ?, 'draft', ?)"
    )
    .bind(&req.title).bind(&req.content).bind(&req.scope).bind(req.priority)
    .bind(req.start_at).bind(req.end_at).bind(c_admin_id)
    .execute(st.config.pool()).await?;
    Ok(result.last_insert_id())
}

pub async fn announcement_detail(st: &AppState, id: u64) -> AppResult<api_contracts::admin::AnnouncementDetailV2> {
    let r = sqlx::query("SELECT id, title, content, scope, priority, start_at, end_at, status FROM announcement WHERE id = ? AND deleted_at IS NULL")
        .bind(id).fetch_optional(st.config.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("announcement".into()))?;
    Ok(api_contracts::admin::AnnouncementDetailV2 {
        id: sqlx::Row::try_get::<u64, _>(&r, "id")?,
        title: sqlx::Row::try_get::<String, _>(&r, "title")?,
        content: sqlx::Row::try_get::<String, _>(&r, "content")?,
        scope: sqlx::Row::try_get::<String, _>(&r, "scope")?,
        priority: sqlx::Row::try_get::<u8, _>(&r, "priority")?,
        status: sqlx::Row::try_get::<String, _>(&r, "status")?,
    })
}

#[derive(Debug, Deserialize)]
pub struct AnnouncementUpdateReq {
    pub title: Option<String>,
    pub content: Option<String>,
    pub status: Option<String>,
    pub end_at: Option<chrono::DateTime<chrono::Utc>>,
}

pub async fn update_announcement(st: &AppState, id: u64, req: &AnnouncementUpdateReq) -> AppResult<bool> {
    let n = sqlx::query(
        "UPDATE announcement SET title = COALESCE(?, title), content = COALESCE(?, content),
                                status = COALESCE(?, status), end_at = COALESCE(?, end_at)
         WHERE id = ? AND deleted_at IS NULL"
    )
    .bind(req.title.as_deref()).bind(req.content.as_deref()).bind(req.status.as_deref()).bind(req.end_at).bind(id)
    .execute(st.config.pool()).await?;
    Ok(n.rows_affected() > 0)
}

pub async fn delete_announcement(st: &AppState, id: u64) -> AppResult<bool> {
    let n = sqlx::query("UPDATE announcement SET deleted_at = NOW(3) WHERE id = ? AND deleted_at IS NULL")
        .bind(id).execute(st.config.pool()).await?;
    Ok(n.rows_affected() > 0)
}

// ===== 白标 =====

/// 行 → 公开视图。`config_json` 里的六项是固定键,逐个 `pick` 出来。
///
/// `disallowed_macros` 的窄豁免：回退值就是一个空 JSON 对象
/// (`whitelabel_config` 一行都没有时的历史行为)，类型化反而把「无配置」
/// 这个**状态**写成了另一种形状。
#[allow(clippy::disallowed_macros)]
pub fn whitelabel_public(row: &sqlx::mysql::MySqlRow) -> AppResult<api_contracts::admin::WhitelabelPublicConfig> {
    let extra: Option<serde_json::Value> = row.try_get("config_json")?;
    // 回退:一行都没有时回默认视图(历史实现回空对象 `{}`)。
    let extra = extra.unwrap_or_else(|| serde_json::json!({}));
    let pick = |key: &str| extra.get(key).cloned().unwrap_or(serde_json::Value::Null);
    Ok(api_contracts::admin::WhitelabelPublicConfig {
        id: row.try_get::<u64, _>("id")?,
        miniprogram_name: row.try_get::<String, _>("mini_program_name")?,
        miniprogram_logo_url: row.try_get::<Option<String>, _>("logo_url")?,
        admin_logo_url: pick("admin_logo_url"),
        theme_color: row.try_get::<Option<String>, _>("theme_color")?,
        service_phone: row.try_get::<Option<String>, _>("contact_phone")?,
        service_wechat_id: pick("service_wechat_id"),
        icp_record_no: pick("icp_record_no"),
        custom_domain: pick("custom_domain"),
        agreement_url: pick("agreement_url"),
        privacy_url: pick("privacy_url"),
        about_us: row.try_get::<Option<String>, _>("about_text")?,
    })
}

const WHITELABEL_COLUMNS: &str =
    "SELECT id, mini_program_name, logo_url, theme_color, contact_phone, about_text, config_json FROM whitelabel_config";

pub async fn whitelabel_id1(st: &AppState) -> AppResult<Option<sqlx::mysql::MySqlRow>> {
    Ok(sqlx::query(&format!("{WHITELABEL_COLUMNS} WHERE id=1"))
        .fetch_optional(st.config.pool()).await?)
}

pub async fn whitelabel_latest(st: &AppState) -> AppResult<Option<sqlx::mysql::MySqlRow>> {
    Ok(sqlx::query(&format!("{WHITELABEL_COLUMNS} ORDER BY id DESC LIMIT 1"))
        .fetch_optional(st.config.pool()).await?)
}

pub async fn lock_whitelabel_id1(tx: &mut common_db::Tx<'_>) -> AppResult<Option<sqlx::mysql::MySqlRow>> {
    Ok(sqlx::query(&format!("{WHITELABEL_COLUMNS} WHERE id=1 FOR UPDATE"))
        .fetch_optional(tx.executor()).await?)
}

pub async fn upsert_whitelabel(
    tx: &mut common_db::Tx<'_>,
    req: &crate::capability::config::WhitelabelUpdate,
    config_json: &serde_json::Value,
) -> AppResult<()> {
    sqlx::query(
        "INSERT INTO whitelabel_config (id,name,logo_url,mini_program_name,theme_color,contact_phone,about_text,config_json)
         VALUES (1,'Default',?,?,?,?,?,?)
         ON DUPLICATE KEY UPDATE id=LAST_INSERT_ID(id), logo_url=VALUES(logo_url),
         mini_program_name=VALUES(mini_program_name), theme_color=VALUES(theme_color),
         contact_phone=VALUES(contact_phone), about_text=VALUES(about_text), config_json=VALUES(config_json)",
    )
    .bind(req.miniprogram_logo_url.as_deref())
    .bind(req.miniprogram_name.trim())
    .bind(&req.theme_color)
    .bind(req.service_phone.as_deref())
    .bind(req.about_us.as_deref())
    .bind(config_json)
    .execute(tx.executor()).await?;
    Ok(())
}

pub async fn audit_whitelabel(
    tx: &mut common_db::Tx<'_>,
    admin_user_id: u64,
    before: &serde_json::Value,
    after: &serde_json::Value,
) -> AppResult<()> {
    sqlx::query("INSERT INTO audit_log(actor_id,module,action,target_type,target_id,before_json,after_json,created_month) VALUES (?,'settings','whitelabel.update','whitelabel_config','1',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
        .bind(admin_user_id).bind(before).bind(after).execute(tx.executor()).await?;
    Ok(())
}

// ===== 客服坐席 =====

pub async fn customer_service_list(st: &AppState) -> AppResult<api_contracts::common::ListResponse<api_contracts::admin::CustomerServiceListItem>> {
    let rows = sqlx::query("SELECT id, agent_wechat, agent_name, path, priority, enabled FROM customer_service_config ORDER BY priority DESC, id ASC")
        .fetch_all(st.cases.pool()).await?;
    let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::CustomerServiceListItem> {
        Ok(api_contracts::admin::CustomerServiceListItem {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            agent_wechat: sqlx::Row::try_get::<String, _>(r, "agent_wechat")?,
            agent_name: sqlx::Row::try_get::<Option<String>, _>(r, "agent_name")?,
            path: sqlx::Row::try_get::<Option<String>, _>(r, "path")?,
            priority: sqlx::Row::try_get::<u32, _>(r, "priority")?,
            enabled: sqlx::Row::try_get::<i8, _>(r, "enabled")? != 0,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::common::ListResponse::new(items))
}

#[derive(Debug, Deserialize)]
pub struct CustomerServiceCreateReq {
    pub agent_wechat: String,
    pub agent_name: Option<String>,
    pub path: Option<String>,
    pub priority: Option<u32>,
    pub enabled: Option<bool>,
    pub working_hours_json: Option<serde_json::Value>,
}

pub async fn insert_customer_service(st: &AppState, req: &CustomerServiceCreateReq) -> AppResult<u64> {
    let result = sqlx::query(
        "INSERT INTO customer_service_config (agent_wechat, agent_name, path, priority, enabled, working_hours_json)
         VALUES (?, ?, ?, COALESCE(?, 0), COALESCE(?, 1), ?)"
    )
    .bind(&req.agent_wechat).bind(req.agent_name.as_deref()).bind(req.path.as_deref())
    .bind(req.priority).bind(req.enabled).bind(&req.working_hours_json)
    .execute(st.cases.pool()).await?;
    Ok(result.last_insert_id())
}

pub async fn customer_service_detail(st: &AppState, id: u64) -> AppResult<api_contracts::admin::CustomerServiceDetail> {
    let r = sqlx::query("SELECT id, agent_wechat, agent_name, path, priority, enabled, working_hours_json FROM customer_service_config WHERE id = ?")
        .bind(id).fetch_optional(st.cases.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("cs".into()))?;
    Ok(api_contracts::admin::CustomerServiceDetail {
        id: sqlx::Row::try_get::<u64, _>(&r, "id")?,
        agent_wechat: sqlx::Row::try_get::<String, _>(&r, "agent_wechat")?,
        agent_name: sqlx::Row::try_get::<Option<String>, _>(&r, "agent_name")?,
        path: sqlx::Row::try_get::<Option<String>, _>(&r, "path")?,
        priority: sqlx::Row::try_get::<u32, _>(&r, "priority")?,
        enabled: sqlx::Row::try_get::<i8, _>(&r, "enabled")? != 0,
        working_hours_json: sqlx::Row::try_get::<Option<serde_json::Value>, _>(&r, "working_hours_json")?,
    })
}

pub async fn customer_service_exists(st: &AppState, id: u64) -> AppResult<bool> {
    let exists: bool = sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM customer_service_config WHERE id = ?)")
        .bind(id).fetch_one(st.cases.pool()).await?;
    Ok(exists)
}

pub async fn update_customer_service(st: &AppState, id: u64, req: &CustomerServiceCreateReq) -> AppResult<()> {
    sqlx::query(
        "UPDATE customer_service_config
         SET agent_wechat = ?, agent_name = ?, path = ?, priority = ?, enabled = ?, working_hours_json = ?
         WHERE id = ?"
    )
    .bind(&req.agent_wechat).bind(req.agent_name.as_deref()).bind(req.path.as_deref())
    .bind(req.priority.unwrap_or_default()).bind(req.enabled.unwrap_or(true)).bind(&req.working_hours_json).bind(id)
    .execute(st.cases.pool()).await?;
    Ok(())
}

pub async fn disable_customer_service(st: &AppState, id: u64) -> AppResult<()> {
    sqlx::query("UPDATE customer_service_config SET enabled = 0 WHERE id = ?")
        .bind(id).execute(st.cases.pool()).await?;
    Ok(())
}

/// 客服入口(供 charge 服务读)。取启用中优先级最高的一条。
pub async fn customer_service_entry(st: &AppState) -> AppResult<sqlx::mysql::MySqlRow> {
    sqlx::query(
        "SELECT agent_wechat, agent_name, path FROM customer_service_config
         WHERE enabled = 1 ORDER BY priority DESC, id ASC LIMIT 1",
    )
    .fetch_optional(st.device.pool())
    .await?
    .ok_or_else(|| AppError::NotFound("当前暂无可用客服".into()))
}

// ===== 内部读:公告 =====

pub async fn active_announcements(st: &AppState) -> AppResult<api_contracts::charge::ActiveAnnouncementsResponse> {
    let rows = sqlx::query(
        "SELECT id, title, content, priority, start_at, end_at
         FROM announcement
         WHERE status = 'published' AND deleted_at IS NULL AND start_at <= NOW() AND (end_at IS NULL OR end_at >= NOW())
         ORDER BY priority DESC, id DESC LIMIT 50"
    ).fetch_all(st.device.pool()).await?;
    let items = rows.iter().map(|r| -> AppResult<api_contracts::charge::Announcement> {
        Ok(api_contracts::charge::Announcement {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            title: sqlx::Row::try_get::<String, _>(r, "title")?,
            content: sqlx::Row::try_get::<String, _>(r, "content")?,
            priority: sqlx::Row::try_get::<u8, _>(r, "priority")?,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::charge::ActiveAnnouncementsResponse { items })
}

/// Worker 调本端点,使公告状态只由 admin 写。
pub async fn expire_announcements(st: &AppState) -> AppResult<u64> {
    let result = sqlx::query(
        "UPDATE announcement SET status = 'expired'
         WHERE status = 'published' AND end_at IS NOT NULL AND end_at < UTC_TIMESTAMP(3) AND deleted_at IS NULL"
    ).execute(st.device.pool()).await?;
    Ok(result.rows_affected())
}

// ===== 内部读:计费规则 =====

pub async fn pricing_rule(st: &AppState, id: u64) -> AppResult<api_contracts::admin::PricingRuleForBilling> {
    let r: Option<(u64, String, String, i64, i64, i64, u32)> = sqlx::query_as(
        "SELECT id, name, mode, service_fee_cents_per_kwh, service_fee_cents_per_min, min_charge_cents, version
         FROM pricing_rule WHERE id = ? AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.device.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("pricing_rule".into()))?;
    Ok(api_contracts::admin::PricingRuleForBilling {
        id: r.0, name: r.1, mode: r.2,
        service_fee_cents_per_kwh: r.3,
        service_fee_cents_per_min: r.4,
        min_charge_cents: r.5,
        version: r.6,
    })
}

// ===== 内部读:分账模板 =====

pub async fn split_template_with_parties(
    st: &AppState,
    id: u64,
) -> AppResult<api_contracts::admin::SplitTemplateWithParties> {
    let r: Option<(u64, String, String, String)> = sqlx::query_as(
        "SELECT id, code, name, mode FROM split_template WHERE id = ? AND status = 'active' AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.device.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("split_template".into()))?;
    let party_rows = sqlx::query("SELECT id, party_code, party_name, ratio_bp FROM split_party WHERE split_template_id = ? ORDER BY id")
        .bind(id).fetch_all(st.device.pool()).await?;
    let parties = party_rows.iter().map(|p| -> AppResult<api_contracts::admin::SplitParty> { Ok(api_contracts::admin::SplitParty {
        id: sqlx::Row::try_get::<u64, _>(p, "id")?,
        party_code: sqlx::Row::try_get::<String, _>(p, "party_code")?,
        party_name: sqlx::Row::try_get::<String, _>(p, "party_name")?,
        ratio_bp: sqlx::Row::try_get::<u32, _>(p, "ratio_bp")?,
    }) }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::admin::SplitTemplateWithParties {
        id: r.0, code: r.1, name: r.2, mode: r.3, parties,
    })
}

/// Stream 消费侧:登记一条待审核的发票申请(幂等)。
pub async fn ensure_invoice_review(st: &AppState, invoice_request_id: u64) -> AppResult<()> {
    sqlx::query("INSERT IGNORE INTO invoice_review (invoice_request_id, review_status) VALUES (?, 'pending')")
        .bind(invoice_request_id).execute(st.config.pool()).await?;
    Ok(())
}

// ===== 设备生效计费规则(供 billing / gateway 读) =====

pub async fn device_pricing(
    st: &AppState,
    device_id: &str,
) -> AppResult<api_contracts::pricing::DevicePricing> {
    let mut tx=st.config.begin().await?;
    let stations:Vec<(u64,String,Option<u64>)>=sqlx::query_as(
        "SELECT s.id,s.name,s.pricing_template_id FROM device_meta d JOIN station s ON s.id=d.station_id
         WHERE d.device_id=? AND d.deleted_at IS NULL AND d.status='enabled' AND s.deleted_at IS NULL AND s.status='active'"
    ).bind(device_id).fetch_all(tx.executor()).await?;
    if stations.len()>1 {return Err(AppError::Conflict("设备站点配置重复".into()));}
    let (station_id,station_name,template_id)=stations.into_iter().next().ok_or_else(||AppError::NotFound("设备未绑定可运营站点".into()))?;
    let columns="SELECT id,name,version,mode,time_of_use_json,service_fee_cents_per_kwh,service_fee_cents_per_min,min_charge_cents FROM pricing_rule";
    let active="deleted_at IS NULL AND status='active' AND (effective_from IS NULL OR effective_from<=UTC_TIMESTAMP(3)) AND (effective_to IS NULL OR effective_to>UTC_TIMESTAMP(3))";
    let mut rows=sqlx::query(&format!("{columns} WHERE station_id=? AND {active}"))
        .bind(station_id).fetch_all(tx.executor()).await?;
    if rows.len()>1 {return Err(AppError::Conflict("站点同时存在多个生效计费规则".into()));}
    if rows.is_empty() {
        if let Some(template_id)=template_id {
            rows=sqlx::query(&format!("{columns} WHERE id=(SELECT default_pricing_rule_id FROM pricing_template WHERE id=? AND deleted_at IS NULL) AND (station_id IS NULL OR station_id=?) AND {active}"))
                .bind(template_id).bind(station_id).fetch_all(tx.executor()).await?;
        }
    }
    let row=rows.pop().ok_or_else(||AppError::NotFound("站点未配置生效计费规则".into()))?;
    let result=api_contracts::pricing::DevicePricing {station_id,station_name,rule_id:row.try_get("id")?,name:row.try_get("name")?,version:row.try_get("version")?,mode:row.try_get("mode")?,
        time_of_use:row.try_get::<Option<serde_json::Value>,_>("time_of_use_json")?.unwrap_or(serde_json::Value::Null),
        service_fee_cents_per_kwh:row.try_get("service_fee_cents_per_kwh")?,service_fee_cents_per_min:row.try_get("service_fee_cents_per_min")?,min_charge_cents:row.try_get("min_charge_cents")?};
    tx.commit().await?;
    Ok(result)
}
