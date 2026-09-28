//! cases 域 —— 客服工单(反馈 / 报修)与优惠券
//!
//! 票据本体归 user 服务,admin 只做**授权 + 审计 + 转发**;优惠券连业务数据
//! 都不在 admin 库,这里只落 `audit_log`。这是两个域放在一起的原因:
//! 它们共用同一套"case 权限"矩阵(feedback.* / fault.* / coupon.*)。

pub mod domain;
pub mod repository_sql;

use crate::AppState;
use crate::capability::identity::{require_permission, ActiveAdmin};
use axum::{extract::State, Json};
use common_error::AppResult;
use common_http::internal::ApiClient;
use repository_sql::{
    CouponCreateReq, CouponGrantReq, CouponStatsQuery, CouponUpdateReq, FaultDispatch,
    FaultDispatchBody, FaultResolve, FaultResolveBody, FeedbackReply, FeedbackReplyBody, QueueQuery,
};

fn client(st: &AppState) -> ApiClient {
    ApiClient::new(st.http.clone(), st.service_token.clone())
}

async fn require_case_permission(st: &AppState, c: &ActiveAdmin, code: &str) -> AppResult<()> {
    if !repository_sql::has_case_permission(st, c, code).await? {
        return Err(repository_sql::forbidden(code));
    }
    Ok(())
}

// ===== 反馈 =====

pub async fn feedback_list(
    State(st): State<AppState>, c: ActiveAdmin, axum::extract::Query(q): axum::extract::Query<QueueQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::PagedFeedback>>> {
    require_case_permission(&st, &c, "feedback.read").await?;
    let result: api_contracts::charge::PagedFeedback = client(&st)
        .get(st.cfg.service_urls.user.as_deref(), api_contracts::paths::USER_INTERNAL_FEEDBACK, &q).await?;
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn feedback_reply(
    State(st): State<AppState>, c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<String>, Json(req): Json<FeedbackReply>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ProcessedResponse>>> {
    require_case_permission(&st, &c, "feedback.reply").await?;
    domain::ensure_numeric_id(&id, "反馈编号无效")?;
    let result: api_contracts::common::ProcessedResponse = client(&st)
        .post(st.cfg.service_urls.user.as_deref(), &api_contracts::fill_path(api_contracts::paths::USER_INTERNAL_FEEDBACK_REPLY, "id", &id),
            &FeedbackReplyBody { actor_id: c.admin_user_id, action: &req.action, reply_content: &req.reply_content }).await?;
    // 幂等重放不重复写审计 —— 判定依据是上游显式的 already_processed 标记。
    if !result.already_processed {
        let payload = serde_json::to_value(repository_sql::AuditRequestResult { request: &req, result: &result })?;
        repository_sql::audit_feedback_reply(&st, &c, &id, if req.action == "reply" {"feedback.reply"} else {"feedback.close"}, &payload).await?;
    }
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

// ===== 报修 =====

pub async fn fault_list(
    State(st): State<AppState>, c: ActiveAdmin, axum::extract::Query(q): axum::extract::Query<QueueQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::PagedFault>>> {
    require_case_permission(&st, &c, "fault.read").await?;
    let result: api_contracts::charge::PagedFault = client(&st)
        .get(st.cfg.service_urls.user.as_deref(), api_contracts::paths::USER_INTERNAL_DEVICE_FAULT_REPORTS, &q).await?;
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn fault_history(
    State(st): State<AppState>, c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<String>, axum::extract::Query(q): axum::extract::Query<QueueQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::PagedResponse<api_contracts::charge::FaultHistoryEvent>>>> {
    require_case_permission(&st, &c, "fault.read").await?;
    domain::ensure_numeric_id(&id, "报修编号无效")?;
    let path = api_contracts::fill_path(api_contracts::paths::USER_INTERNAL_DEVICE_FAULT_HISTORY, "id", &id);
    let result: api_contracts::common::PagedResponse<api_contracts::charge::FaultHistoryEvent> = client(&st)
        .get(st.cfg.service_urls.user.as_deref(), &path, &q).await?;
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn fault_dispatch(
    State(st): State<AppState>, c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<String>, Json(req): Json<FaultDispatch>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::DispatchedResponse>>> {
    require_case_permission(&st, &c, "fault.dispatch").await?;
    domain::ensure_dispatch(&id, req.assigned_to)?;
    if !repository_sql::target_admin_active(&st, req.assigned_to).await? { return Err(common_error::AppError::BadRequest("指派的管理员账号无效或已停用".into())); }
    let result: api_contracts::common::DispatchedResponse = client(&st)
        .post(st.cfg.service_urls.user.as_deref(), &api_contracts::fill_path(api_contracts::paths::USER_INTERNAL_DEVICE_FAULT_DISPATCH, "id", &id),
            &FaultDispatchBody { actor_id: c.admin_user_id, assigned_to: req.assigned_to, note: &req.note }).await?;
    if !result.already_processed {
        let payload = serde_json::to_value(repository_sql::AuditAssigned { assigned_to: req.assigned_to, result: &result })?;
        repository_sql::audit_fault(&st, &c, &id, "fault.dispatch", &payload).await?;
    }
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn fault_resolve(
    State(st): State<AppState>, c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<String>, Json(req): Json<FaultResolve>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ProcessedResponse>>> {
    require_permission(&st,&c,"fault.resolve").await?;
    require_case_permission(&st, &c, "fault.dispatch").await?;
    domain::ensure_resolve(&id, &req.status)?;
    let result: api_contracts::common::ProcessedResponse = client(&st)
        .post(st.cfg.service_urls.user.as_deref(), &api_contracts::fill_path(api_contracts::paths::USER_INTERNAL_DEVICE_FAULT_RESOLVE, "id", &id),
            &FaultResolveBody { actor_id: c.admin_user_id, status: &req.status, note: &req.note }).await?;
    if !result.already_processed {
        let payload = serde_json::to_value(repository_sql::AuditResult { result: &result })?;
        repository_sql::audit_fault(&st, &c, &id, &format!("fault.{}", req.status), &payload).await?;
    }
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

// ===== 优惠券(数据归 user 服务) =====

async fn require_coupon_permission(st: &AppState, c: &ActiveAdmin, permission: &str) -> AppResult<()> {
    if !repository_sql::has_coupon_permission(st, c, permission).await? {
        return Err(repository_sql::forbidden(permission));
    }
    Ok(())
}

pub async fn coupon_list(State(st): State<AppState>, c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::charge::CouponTemplate>>>> {
    require_coupon_permission(&st, &c, "coupon.read").await?;
    let result: api_contracts::common::ListResponse<api_contracts::charge::CouponTemplate> = client(&st).get(st.cfg.service_urls.user.as_deref(), api_contracts::paths::USER_INTERNAL_COUPONS, &()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn coupon_create(State(st): State<AppState>, c: ActiveAdmin, Json(req): Json<CouponCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::CreatedResponse>>> {
    require_coupon_permission(&st, &c, "coupon.create").await?;
    let created: api_contracts::common::CreatedResponse = client(&st).post(st.cfg.service_urls.user.as_deref(), api_contracts::paths::USER_INTERNAL_COUPONS, &req).await?;
    let id = created.id;
    let payload = serde_json::to_value(repository_sql::AuditRequestResult { request: &req, result: &created })?;
    repository_sql::audit_coupon(&st, &c, "coupon.create", &id.to_string(), None, payload, None).await?;
    Ok(Json(common_error::ApiEnvelope::ok(created, common_error::current_request_id())))
}

pub async fn coupon_get(State(st): State<AppState>, c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::CouponTemplate>>> {
    require_coupon_permission(&st, &c, "coupon.read").await?;
    let result: api_contracts::charge::CouponTemplate = client(&st).get(st.cfg.service_urls.user.as_deref(), &api_contracts::fill_path(api_contracts::paths::USER_INTERNAL_COUPON_DETAIL, "id", &id.to_string()), &()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn coupon_update(State(st): State<AppState>, c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>, Json(req): Json<CouponUpdateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::UpdatedResponse>>> {
    require_coupon_permission(&st, &c, "coupon.update").await?;
    let path = api_contracts::fill_path(api_contracts::paths::USER_INTERNAL_COUPON_DETAIL, "id", &id.to_string());
    let before: api_contracts::charge::CouponTemplate = client(&st).get(st.cfg.service_urls.user.as_deref(), &path, &()).await?;
    let result: api_contracts::common::UpdatedResponse = client(&st).put(st.cfg.service_urls.user.as_deref(), &path, &req).await?;
    let payload = serde_json::to_value(repository_sql::AuditRequestResult { request: &req, result: &result })?;
    repository_sql::audit_coupon(&st, &c, "coupon.update", &id.to_string(), Some(serde_json::to_value(&before)?), payload, None).await?;
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn coupon_delete(State(st): State<AppState>, c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::DeletedResponse>>> {
    require_coupon_permission(&st, &c, "coupon.delete").await?;
    let path = api_contracts::fill_path(api_contracts::paths::USER_INTERNAL_COUPON_DETAIL, "id", &id.to_string());
    let before: api_contracts::charge::CouponTemplate = client(&st).get(st.cfg.service_urls.user.as_deref(), &path, &()).await?;
    let result: api_contracts::common::DeletedResponse = client(&st).delete(st.cfg.service_urls.user.as_deref(), &path, &()).await?;
    repository_sql::audit_coupon(&st, &c, "coupon.delete", &id.to_string(), Some(serde_json::to_value(&before)?), serde_json::to_value(&result)?, None).await?;
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn coupon_grant(State(st): State<AppState>, c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>, Json(req): Json<CouponGrantReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::CouponGrantResult>>> {
    require_coupon_permission(&st, &c, "coupon.grant").await?;
    if uuid::Uuid::parse_str(&req.request_id).is_err() || req.user_id == 0 {
        return Err(common_error::AppError::BadRequest("发券请求标识或用户编号无效".into()));
    }
    let path = api_contracts::fill_path(api_contracts::paths::USER_INTERNAL_COUPON_GRANTS, "id", &id.to_string());
    let result: api_contracts::charge::CouponGrantResult = client(&st).post(st.cfg.service_urls.user.as_deref(), &path, &req).await?;
    let payload = serde_json::to_value(repository_sql::AuditRequestResult { request: &req, result: &result })?;
    repository_sql::audit_coupon(&st, &c, "coupon.grant", &id.to_string(), None, payload, Some(&req.request_id)).await?;
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn coupon_stats(State(st): State<AppState>, c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::CouponStats>>> {
    require_coupon_permission(&st, &c, "coupon.read").await?;
    let query = CouponStatsQuery { coupon_id: id };
    let result: api_contracts::charge::CouponStats = client(&st).get(st.cfg.service_urls.user.as_deref(), api_contracts::paths::USER_INTERNAL_COUPON_STATS, &query).await?;
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}
