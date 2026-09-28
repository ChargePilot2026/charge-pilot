//! alert 域 —— 告警事件 / 规则 / 订阅 / 风控配置
//!
//! `risk_config_put` 的请求体是**运营自由配置的键值对列表**,`value` 的形状
//! 由配置项自己决定(阈值是数字、开关是布尔、白名单是数组)。这不是"能类型化
//! 却没类型化",而是没有固定 schema —— 故这里是本域唯一保留 `Value` 的入口,
//! 落库即 `risk_config.value` 数据库 JSON 列(方案 §三例外清单第 2 条)。
//! 循环体逐项写入见 [`repository_sql::upsert_risk_config`]。

pub mod repository_sql;

use crate::AppState;
use crate::capability::identity::{require_permission, ActiveAdmin};
use axum::{extract::State, Json};
use common_error::{AppError, AppResult};

pub async fn list(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::AlertEvent>>>> {
    let items = repository_sql::alert_events(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

pub async fn ack(State(st): State<AppState>, c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::AckFlag>>> {
    require_permission(&st,&c,"alert.ack").await?;
    if !repository_sql::ack_alert(&st, id, c.admin_user_id).await? { return Err(AppError::Conflict("not active".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::AckFlag::new(true), common_error::current_request_id())))
}

pub async fn rules_list(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::AlertRule>>>> {
    let items = repository_sql::alert_rules(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

pub async fn rules_create(State(st): State<AppState>, _c: ActiveAdmin, Json(req): Json<repository_sql::AlertRuleCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::CreatedResponse>>> {
    require_permission(&st,&_c,"alert.rule.create").await?;
    let id = repository_sql::insert_alert_rule(&st,&req).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::CreatedResponse { id }, common_error::current_request_id())))
}

pub async fn rules_get(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::AlertRuleDetail>>> {
    let detail = repository_sql::alert_rule_detail(&st,id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(detail, common_error::current_request_id())))
}

pub async fn rules_update(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>, Json(req): Json<repository_sql::AlertRuleUpdateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::UpdatedResponse>>> {
    require_permission(&st,&_c,"alert.rule.update").await?;
    if !repository_sql::update_alert_rule(&st,id,&req).await? { return Err(AppError::NotFound("rule".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::UpdatedResponse::new(), common_error::current_request_id())))
}

pub async fn rules_delete(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::DeletedResponse>>> {
    require_permission(&st,&_c,"alert.rule.delete").await?;
    if !repository_sql::delete_alert_rule(&st,id).await? { return Err(AppError::NotFound("rule".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::DeletedResponse::new(), common_error::current_request_id())))
}

pub async fn subs_list(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::AlertSubscription>>>> {
    let items = repository_sql::alert_subscriptions(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

pub async fn subs_create(State(st): State<AppState>, _c: ActiveAdmin, Json(req): Json<repository_sql::SubCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::CreatedResponse>>> {
    require_permission(&st,&_c,"alert.subscription.create").await?;
    let id = repository_sql::insert_alert_subscription(&st,&req).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::CreatedResponse { id }, common_error::current_request_id())))
}

pub async fn risk_config_get(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::RiskConfigItem>>>> {
    let items = repository_sql::risk_config(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

// 请求体的每一项由运营自行定义,`value` 无固定 schema。
#[allow(clippy::disallowed_types)]
pub async fn risk_config_put(State(st): State<AppState>, c: ActiveAdmin, Json(req): Json<serde_json::Value>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::UpdatedResponse>>> {
    require_permission(&st,&c,"alert.risk_config.update").await?;
    let arr = req.as_array().cloned().unwrap_or_default();
    for item in arr {
        let key = item.get("key").and_then(|v| v.as_str()).unwrap_or("");
        let value = item.get("value").cloned().unwrap_or(serde_json::Value::Null);
        let desc = item.get("description").and_then(|v| v.as_str());
        repository_sql::upsert_risk_config(&st, key, &value, desc, c.admin_user_id).await?;
    }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::UpdatedResponse::new(), common_error::current_request_id())))
}

/// 内部读:活跃告警列表(供 charge / billing 等服务调用)。
pub async fn active(State(st): State<AppState>, axum::extract::Query(q): axum::extract::Query<repository_sql::AlertsQuery>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::ActiveAlert>>>> {
    let items = repository_sql::active_alerts(&st, q.device_id.as_deref()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}
