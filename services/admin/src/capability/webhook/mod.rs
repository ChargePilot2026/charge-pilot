//! webhook 域 —— 订阅 CRUD + 投递日志
//!
//! 本域没有可抽的纯逻辑层:校验只有"订阅/事件标识非空"一条,其余是
//! 行映射与跨服务回写。故不建 `domain.rs` —— 不制造空壳。

pub mod repository_sql;

use crate::AppState;
use crate::capability::identity::{require_permission, ActiveAdmin};
use axum::{extract::State, Json};
use common_error::{AppError, AppResult};

pub async fn list(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::WebhookSubscription>>>> {
    let items = repository_sql::subscriptions(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

pub async fn create(State(st): State<AppState>, _c: ActiveAdmin, Json(req): Json<repository_sql::WebhookCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::WebhookCreated>>> {
    require_permission(&st,&_c,"webhook.create").await?;
    let secret = format!("whsec_{}", uuid::Uuid::new_v4().simple());
    let id = repository_sql::insert_subscription(&st,&req,&secret).await?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::admin::WebhookCreated { id, secret },
        common_error::current_request_id(),
    )))
}

pub async fn get(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::WebhookSubscriptionDetail>>> {
    let detail = repository_sql::subscription_detail(&st,id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(detail, common_error::current_request_id())))
}

pub async fn update(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>, Json(req): Json<repository_sql::WebhookUpdateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::UpdatedResponse>>> {
    require_permission(&st,&_c,"webhook.update").await?;
    if !repository_sql::update_subscription(&st,id,&req).await? { return Err(AppError::NotFound("webhook".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::UpdatedResponse::new(), common_error::current_request_id())))
}

pub async fn delete(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::DeletedResponse>>> {
    require_permission(&st,&_c,"webhook.delete").await?;
    if !repository_sql::delete_subscription(&st,id).await? { return Err(AppError::NotFound("webhook".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::DeletedResponse::new(), common_error::current_request_id())))
}

pub async fn deliveries(State(st): State<AppState>, _c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::WebhookDelivery>>>> {
    let items = repository_sql::deliveries(&st,id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

/// **D11**:worker 投递 webhook 后的明细回写(内部服务间调用)。
///
/// `webhook_delivery_log` 在 admin_db,worker 无权跨库访问,故经本端点回写。
/// 鉴权由 `internal_token_mw` 中间件负责(`x-service-token`),与其它
/// `internal_routes` 一致 —— 本 handler **不取** `ActiveAdmin`。
pub async fn record_delivery(
    State(st): State<AppState>,
    Json(req): Json<repository_sql::DeliveryReportReq>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::AckFlag>>> {
    if req.subscription_id == 0 || req.event_id.is_empty() || req.event_id.len() > 64 {
        return Err(AppError::BadRequest("投递记录缺少订阅或事件标识".into()));
    }
    repository_sql::record_delivery(&st, &req).await?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::common::AckFlag::new(true),
        common_error::current_request_id(),
    )))
}
