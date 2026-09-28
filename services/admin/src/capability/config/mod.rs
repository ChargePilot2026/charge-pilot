//! config 域 —— 定价 / 分账 / 白标 / 公告 / OTA 桩 / 客服坐席
//!
//! 白标的 `config_json` 是数据库 JSON 列,原先用 `json!` 拼。现在改为具名
//! 结构体 [`WhitelabelExtras`] 序列化 —— 键集合固定、形状已知,本可类型化。
//! 读回时仍按固定键 `pick`(行 → 公开视图),因为 `WhitelabelPublicConfig` 的
//! 那六个字段本身类型就是 `Option<Value>`(契约如此)。

pub mod domain;
pub mod repository_sql;

use crate::AppState;
use crate::capability::identity::{require_permission, ActiveAdmin};
use axum::{extract::State, Json};
use common_error::{AppError, AppResult};
use common_redis::StreamEnvelope;
use serde::{Deserialize, Serialize};

// ===== 计费规则 =====

pub async fn charge_rules(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::ChargeRule>>>> {
    let items = repository_sql::charge_rules(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

/// `pricing_rule_changed` 事件的载荷。原先是 `json!({"id":…, "key":…})`。
///
/// 供 [`repository_sql::insert_charge_rule_with_event`] 在拿到真实
/// `last_insert_id()` 后调用;`id` 绝不能是占位值,否则
/// `pricing_rule_changed_stream` 的消费者(billing / user 的缓存失效)
/// 会按错的规则 id 失效缓存。
#[derive(Serialize)]
struct PricingRuleChanged {
    id: u64,
    key: String,
}

/// 组装 `pricing_rule_changed` 事件。事件与业务写在同一事务里落 outbox。
fn pricing_rule_changed(id: u64, key: String) -> StreamEnvelope {
    StreamEnvelope::new(
        "pricing_rule_changed",
        "admin",
        // `PricingRuleChanged` 只有两个基本类型字段,序列化不会失败。
        serde_json::to_value(PricingRuleChanged { id, key }).expect("PricingRuleChanged 必可序列化"),
    )
}

pub async fn charge_rule_create(State(st): State<AppState>, _c: ActiveAdmin, Json(req): Json<repository_sql::ChargeRuleCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::CreatedResponse>>> {
    require_permission(&st,&_c,"pricing.rule.create").await?;
    // 载荷只依赖 `name`(已知的),`id` 由 repository 在事务内补齐。
    let payload = repository_sql::PricingRuleChangedForInsert { key: req.name.clone() };
    let id = repository_sql::insert_charge_rule_with_event(&st, &req, &payload).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::CreatedResponse { id }, common_error::current_request_id())))
}

// ===== 计费模板 =====

pub async fn pricing_templates(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::PricingTemplate>>>> {
    let items = repository_sql::pricing_templates(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

pub async fn pricing_template_create(State(st): State<AppState>, _c: ActiveAdmin, Json(req): Json<repository_sql::PricingTemplateCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::CreatedResponse>>> {
    require_permission(&st,&_c,"pricing.template.create").await?;
    let id = repository_sql::insert_pricing_template(&st,&req).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::CreatedResponse { id }, common_error::current_request_id())))
}

// ===== 分账模板 / 分账方 =====

pub async fn split_templates(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::SplitTemplate>>>> {
    let items = repository_sql::split_templates(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

pub async fn split_template_create(State(st): State<AppState>, _c: ActiveAdmin, Json(req): Json<repository_sql::SplitTemplateCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::CreatedResponse>>> {
    require_permission(&st,&_c,"finance.split_template.create").await?;
    let id = repository_sql::insert_split_template(&st,&req).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::CreatedResponse { id }, common_error::current_request_id())))
}

pub async fn split_parties(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::SplitParty>>>> {
    let items = repository_sql::split_parties(&st,id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

pub async fn split_party_create(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>, Json(req): Json<repository_sql::SplitPartyCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<crate::api_types::CreatedIdResponse>>> {
    require_permission(&st,&_c,"finance.split_party.create").await?;
    let new_id = repository_sql::insert_split_party(&st,id,&req).await?;
    Ok(Json(common_error::ApiEnvelope::ok(crate::api_types::CreatedIdResponse { id: new_id }, common_error::current_request_id())))
}

// ===== OTA 配置桩 =====

/// 未接入的桩:永远返回 `Err`,成功响应体不存在,故类型写 `()`。
pub async fn ota_get(State(_st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<()>>> {
    Err(AppError::ServiceUnavailable("OTA 配置存储尚未接入".into()))
}

/// 未接入的桩:OTA 配置的请求体形状**尚无 schema**(设备筛选 / 灰度策略未定),
/// 成功分支不存在、永远返回 `Err`,故此处的 `Value` 是显式占位 ——
/// 等契约定型后立即换成具名 DTO。
#[allow(clippy::disallowed_types)]
pub async fn ota_put(State(st): State<AppState>, c: ActiveAdmin, Json(_req): Json<serde_json::Value>) -> AppResult<Json<common_error::ApiEnvelope<crate::api_types::OkFlagResponse>>> {
    require_permission(&st,&c,"settings.ota.update").await?;
    Err(AppError::ServiceUnavailable("OTA 配置存储尚未接入，未保存设置".into()))
}

// ===== 公告 =====

pub async fn announcements(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::AnnouncementListItem>>>> {
    let items = repository_sql::announcements(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

pub async fn announcement_create(State(st): State<AppState>, c: ActiveAdmin, Json(req): Json<repository_sql::AnnouncementCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::CreatedResponse>>> {
    require_permission(&st,&c,"announcement.create").await?;
    let id = repository_sql::insert_announcement(&st,c.admin_user_id,&req).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::CreatedResponse { id }, common_error::current_request_id())))
}

pub async fn announcement_get(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::AnnouncementDetailV2>>> {
    let detail = repository_sql::announcement_detail(&st,id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(detail, common_error::current_request_id())))
}

pub async fn announcement_update(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>, Json(req): Json<repository_sql::AnnouncementUpdateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::UpdatedResponse>>> {
    require_permission(&st,&_c,"announcement.update").await?;
    if !repository_sql::update_announcement(&st,id,&req).await? { return Err(AppError::NotFound("announcement".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::UpdatedResponse::new(), common_error::current_request_id())))
}

pub async fn announcement_delete(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::DeletedResponse>>> {
    require_permission(&st,&_c,"announcement.delete").await?;
    if !repository_sql::delete_announcement(&st,id).await? { return Err(AppError::NotFound("announcement".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::DeletedResponse::new(), common_error::current_request_id())))
}

// ===== 白标 =====

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct WhitelabelUpdate {
    pub miniprogram_name: String,
    pub miniprogram_logo_url: Option<String>,
    pub admin_logo_url: Option<String>,
    pub theme_color: String,
    pub service_phone: Option<String>,
    pub service_wechat_id: Option<String>,
    pub icp_record_no: Option<String>,
    pub custom_domain: Option<String>,
    pub agreement_url: Option<String>,
    pub privacy_url: Option<String>,
    pub about_us: Option<String>,
}

/// `whitelabel_config.config_json` 的固定六键。原先用 `json!` 拼,
/// 键集合与取值类型都已知,故改为具名结构体。
#[derive(Debug, Serialize)]
struct WhitelabelExtras<'a> {
    admin_logo_url: &'a Option<String>,
    service_wechat_id: &'a Option<String>,
    icp_record_no: &'a Option<String>,
    custom_domain: &'a Option<String>,
    agreement_url: &'a Option<String>,
    privacy_url: &'a Option<String>,
}

pub async fn whitelabel_get(State(st): State<AppState>, c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::WhitelabelPublicConfig>>> {
    require_permission(&st, &c, "whitelabel.read").await?;
    let config = match repository_sql::whitelabel_id1(&st).await? {
        Some(row) => repository_sql::whitelabel_public(&row)?,
        None => {
            // 回退到最近一行;一行都没有时回默认视图(历史实现回空对象 `{}`)。
            match repository_sql::whitelabel_latest(&st).await? {
                Some(row) => repository_sql::whitelabel_public(&row)?,
                None => api_contracts::admin::WhitelabelPublicConfig::unset(),
            }
        }
    };
    Ok(Json(common_error::ApiEnvelope::ok(config, common_error::current_request_id())))
}

pub async fn whitelabel_put(
    State(st): State<AppState>, c: ActiveAdmin, Json(mut req): Json<WhitelabelUpdate>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::WhitelabelSaved>>> {
    require_permission(&st, &c, "whitelabel.update").await?;
    domain::clean_optional(&mut req.miniprogram_logo_url);
    domain::clean_optional(&mut req.admin_logo_url);
    domain::clean_optional(&mut req.service_phone);
    domain::clean_optional(&mut req.service_wechat_id);
    domain::clean_optional(&mut req.icp_record_no);
    domain::clean_optional(&mut req.custom_domain);
    domain::clean_optional(&mut req.agreement_url);
    domain::clean_optional(&mut req.privacy_url);
    require_permission(&st, &c, "whitelabel.update").await?;
    domain::validate_whitelabel(&req)?;
    let mut tx = st.config.begin().await?;
    let old = repository_sql::lock_whitelabel_id1(&mut tx).await?;
    let before = match old { Some(row) => serde_json::to_value(repository_sql::whitelabel_public(&row)?)?, None => serde_json::Value::Null };
    let config_json = serde_json::to_value(WhitelabelExtras {
        admin_logo_url: &req.admin_logo_url,
        service_wechat_id: &req.service_wechat_id,
        icp_record_no: &req.icp_record_no,
        custom_domain: &req.custom_domain,
        agreement_url: &req.agreement_url,
        privacy_url: &req.privacy_url,
    })?;
    repository_sql::upsert_whitelabel(&mut tx, &req, &config_json).await?;
    let after = api_contracts::admin::WhitelabelPublicConfig {
        id: 1,
        miniprogram_name: req.miniprogram_name.trim().to_string(),
        miniprogram_logo_url: req.miniprogram_logo_url.clone(),
        // config_json 里的六项经具名结构体组装,取值是 null 或字符串,
        // 故这里取出的 Value 原样塞进 Option<Value>,序列化仍是 null/字符串。
        admin_logo_url: config_json["admin_logo_url"].clone(),
        theme_color: Some(req.theme_color.clone()),
        service_phone: req.service_phone.clone(),
        service_wechat_id: config_json["service_wechat_id"].clone(),
        icp_record_no: config_json["icp_record_no"].clone(),
        custom_domain: config_json["custom_domain"].clone(),
        agreement_url: config_json["agreement_url"].clone(),
        privacy_url: config_json["privacy_url"].clone(),
        about_us: req.about_us.clone(),
    };
    repository_sql::audit_whitelabel(&mut tx, c.admin_user_id, &before, &serde_json::to_value(&after)?).await?;
    tx.commit().await?;
    st.redis_cache.del("whitelabel:config").await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::admin::WhitelabelSaved { id: 1, config: after }, common_error::current_request_id())))
}

// ===== 客服坐席 CRUD =====

pub async fn customer_service_list(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::CustomerServiceListItem>>>> {
    let items = repository_sql::customer_service_list(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

pub async fn customer_service_create(State(st): State<AppState>, _c: ActiveAdmin, Json(req): Json<repository_sql::CustomerServiceCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::CreatedResponse>>> {
    require_permission(&st,&_c,"customer_service.create").await?;
    domain::validate_customer_service(&req.agent_wechat, req.agent_name.as_deref(), req.path.as_deref())?;
    let id = repository_sql::insert_customer_service(&st,&req).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::CreatedResponse { id }, common_error::current_request_id())))
}

pub async fn customer_service_get(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::CustomerServiceDetail>>> {
    let detail = repository_sql::customer_service_detail(&st,id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(detail, common_error::current_request_id())))
}

pub async fn customer_service_update(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>, Json(req): Json<repository_sql::CustomerServiceCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::UpdatedResponse>>> {
    require_permission(&st,&_c,"customer_service.update").await?;
    domain::validate_customer_service(&req.agent_wechat, req.agent_name.as_deref(), req.path.as_deref())?;
    if !repository_sql::customer_service_exists(&st,id).await? { return Err(AppError::NotFound("cs".into())); }
    repository_sql::update_customer_service(&st,id,&req).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::UpdatedResponse::new(), common_error::current_request_id())))
}

pub async fn customer_service_delete(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::DeletedResponse>>> {
    require_permission(&st,&_c,"customer_service.delete").await?;
    if !repository_sql::customer_service_exists(&st,id).await? { return Err(AppError::NotFound("cs".into())); }
    repository_sql::disable_customer_service(&st,id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::DeletedResponse::new(), common_error::current_request_id())))
}

// ===== 内部读(供其它服务调用,无 JWT) =====

pub async fn device_pricing(State(st): State<AppState>, axum::extract::Path(id): axum::extract::Path<String>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::pricing::DevicePricing>>> {
    let result = repository_sql::device_pricing(&st,&id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(result,common_error::current_request_id())))
}

pub async fn active_announcements(State(st): State<AppState>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::ActiveAnnouncementsResponse>>> {
    let result = repository_sql::active_announcements(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn pricing_rule_get(State(st): State<AppState>, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::PricingRuleForBilling>>> {
    let result = repository_sql::pricing_rule(&st,id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn split_template_get(State(st): State<AppState>, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::SplitTemplateWithParties>>> {
    let result = repository_sql::split_template_with_parties(&st,id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

/// Worker calls this endpoint so only the admin service writes announcement state.
pub async fn expire_announcements(State(st): State<AppState>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::AnnouncementExpireResult>>> {
    let expired_count = repository_sql::expire_announcements(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::admin::AnnouncementExpireResult { expired_count },
        common_error::current_request_id(),
    )))
}

#[derive(Debug, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct CustomerServiceEntryQuery {
    pub scene: String,
}

pub async fn customer_service_entry(
    State(st): State<AppState>,
    axum::extract::Query(q): axum::extract::Query<CustomerServiceEntryQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::CustomerServiceEntry>>> {
    if !["general", "refund", "complaint"].contains(&q.scene.as_str()) {
        return Err(AppError::BadRequest("客服场景无效".into()));
    }
    let row = repository_sql::customer_service_entry(&st).await?;
    let entry_url: Option<String> = sqlx::Row::try_get(&row, "path")?;
    let entry_url = entry_url.filter(|url| url.starts_with("https://") && !url.chars().any(char::is_control));
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::charge::CustomerServiceEntry {
            agent_wechat: sqlx::Row::try_get::<String, _>(&row, "agent_wechat")?,
            agent_name: sqlx::Row::try_get::<Option<String>, _>(&row, "agent_name")?,
            entry_url,
            scene: q.scene,
        },
        common_error::current_request_id(),
    )))
}
