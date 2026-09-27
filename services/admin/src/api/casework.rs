//! Customer service and inspection queues. User-owned reports remain in user_db.

use crate::AppState;
use axum::{extract::{Path, Query, State}, Json};
use crate::auth::ActiveAdmin;
use common_error::{AppError, AppResult};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

async fn require_permission(st: &AppState, c: &ActiveAdmin, code: &str) -> AppResult<()> {
    let allowed: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM admin_user_role a
         JOIN role r ON r.id=a.role_id AND r.deleted_at IS NULL
         JOIN role_permission rp ON rp.role_id=r.id
         JOIN permission p ON p.id=rp.permission_id
         WHERE a.id=? AND a.username=? AND a.status='active' AND a.deleted_at IS NULL AND p.code=?)",
    ).bind(c.admin_user_id).bind(&c.sub).bind(code).fetch_one(st.cases.pool()).await?;
    if !allowed { return Err(AppError::Forbidden(format!("缺少 {code} 权限"))); }
    Ok(())
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct QueueQuery {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub status: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub page: Option<u32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub page_size: Option<u32>,
}

pub async fn feedback_list(
    State(st): State<AppState>, c: ActiveAdmin, Query(q): Query<QueueQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::PagedFeedback>>> {
    require_permission(&st, &c, "feedback.read").await?;
    let result: api_contracts::charge::PagedFeedback = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone())
        .get(st.cfg.service_urls.user.as_deref(), api_contracts::paths::USER_INTERNAL_FEEDBACK, &q).await?;
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct FeedbackReply { pub action: String, pub reply_content: Option<String> }

pub async fn feedback_reply(
    State(st): State<AppState>, c: ActiveAdmin, Path(id): Path<String>, Json(req): Json<FeedbackReply>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ProcessedResponse>>> {
    require_permission(&st, &c, "feedback.reply").await?;
    if id.parse::<u64>().is_err() { return Err(AppError::BadRequest("反馈编号无效".into())); }
    let body = json!({"actor_id":c.admin_user_id,"action":req.action,"reply_content":req.reply_content});
    let result: api_contracts::common::ProcessedResponse = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone())
        .post(st.cfg.service_urls.user.as_deref(), &api_contracts::paths::USER_INTERNAL_FEEDBACK_REPLY.replace(":id", &id), &body).await?;
    // 幂等重放不重复写审计 —— 判定依据是上游显式的 already_processed 标记。
    if !result.already_processed {
        sqlx::query("INSERT INTO audit_log(actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'customer_service',?,'feedback',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
            .bind(c.admin_user_id).bind(if req.action == "reply" {"feedback.reply"} else {"feedback.close"})
            .bind(&id).bind(json!({"request":req,"result":result})).execute(st.cases.pool()).await?;
    }
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn fault_list(
    State(st): State<AppState>, c: ActiveAdmin, Query(q): Query<QueueQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::PagedFault>>> {
    require_permission(&st, &c, "fault.read").await?;
    let result: api_contracts::charge::PagedFault = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone())
        .get(st.cfg.service_urls.user.as_deref(), api_contracts::paths::USER_INTERNAL_DEVICE_FAULT_REPORTS, &q).await?;
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn fault_history(
    State(st): State<AppState>, c: ActiveAdmin, Path(id): Path<String>, Query(q): Query<QueueQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::PagedResponse<api_contracts::charge::FaultHistoryEvent>>>> {
    require_permission(&st, &c, "fault.read").await?;
    if id.parse::<u64>().is_err() { return Err(AppError::BadRequest("报修编号无效".into())); }
    let path = api_contracts::paths::USER_INTERNAL_DEVICE_FAULT_HISTORY.replace(":id", &id);
    let result: api_contracts::common::PagedResponse<api_contracts::charge::FaultHistoryEvent> = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone())
        .get(st.cfg.service_urls.user.as_deref(), &path, &q).await?;
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct FaultDispatch {
    pub assigned_to: u64,
    #[serde(default)]
    pub note: Option<String>,
}

pub async fn fault_dispatch(
    State(st): State<AppState>, c: ActiveAdmin, Path(id): Path<String>, Json(req): Json<FaultDispatch>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::DispatchedResponse>>> {
    require_permission(&st, &c, "fault.dispatch").await?;
    if id.parse::<u64>().is_err() || req.assigned_to == 0 { return Err(AppError::BadRequest("报修编号或指派账号无效".into())); }
    let target_active: bool = sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM admin_user_role WHERE id=? AND status='active' AND deleted_at IS NULL)")
        .bind(req.assigned_to).fetch_one(st.cases.pool()).await?;
    if !target_active { return Err(AppError::BadRequest("指派的管理员账号无效或已停用".into())); }
    let result: api_contracts::common::DispatchedResponse = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone())
        .post(st.cfg.service_urls.user.as_deref(), &api_contracts::paths::USER_INTERNAL_DEVICE_FAULT_DISPATCH.replace(":id", &id),
            &json!({"actor_id":c.admin_user_id,"assigned_to":req.assigned_to,"note":req.note})).await?;
    if !result.already_processed {
        sqlx::query("INSERT INTO audit_log(actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'inspection','fault.dispatch','device_fault_report',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
            .bind(c.admin_user_id).bind(&id).bind(json!({"assigned_to":req.assigned_to,"result":result})).execute(st.cases.pool()).await?;
    }
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct FaultResolve {
    pub status: String,
    #[serde(default)]
    pub note: Option<String>,
}

pub async fn fault_resolve(
    State(st): State<AppState>, c: ActiveAdmin, Path(id): Path<String>, Json(req): Json<FaultResolve>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ProcessedResponse>>> {
    crate::auth::require_permission(&st,&c,"fault.resolve").await?;
    require_permission(&st, &c, "fault.dispatch").await?;
    if id.parse::<u64>().is_err() || !["fixed", "closed"].contains(&req.status.as_str()) {
        return Err(AppError::BadRequest("报修编号或处理状态无效".into()));
    }
    let result: api_contracts::common::ProcessedResponse = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone())
        .post(st.cfg.service_urls.user.as_deref(), &api_contracts::paths::USER_INTERNAL_DEVICE_FAULT_RESOLVE.replace(":id", &id),
            &json!({"actor_id":c.admin_user_id,"status":req.status,"note":req.note})).await?;
    if !result.already_processed {
        sqlx::query("INSERT INTO audit_log(actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'inspection',?,'device_fault_report',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
            .bind(c.admin_user_id).bind(format!("fault.{}", req.status)).bind(&id)
            .bind(json!({"result":result})).execute(st.cases.pool()).await?;
    }
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}
