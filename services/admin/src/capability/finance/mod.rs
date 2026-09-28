//! finance 域 —— 退款 / 发票 / 钱包风控 / 结算 / 对账
//!
//! 跨服务调用(user / billing)走类型化 `ApiClient`,生产代码零 `json!`:
//! 原来给 user 服务发的 `json!({"actor_id":…, "request": req})` 全部落成具名 DTO。

pub mod domain;
pub mod refund_task;
pub mod repository_sql;

use crate::AppState;
use crate::capability::identity::{require_permission, ActiveAdmin};
use api_contracts::paths as p;
use axum::{extract::State, Json};
use common_error::{AppError, AppResult};
use serde::{Deserialize, Serialize};

fn client(st: &AppState) -> common_http::internal::ApiClient {
    common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone())
}

// ===== 请求 DTO =====

#[derive(Deserialize)]
pub struct RefundRetryReq { pub reason: String }
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RefundApprovalReq { pub approve_comment:String }
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RefundRejectReq {pub reason:String}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct InvoiceApproveReq { pub invoice_url: String }

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct InvoiceRejectReq { pub reason: String }

#[derive(Deserialize,serde::Serialize)]
pub struct WalletRiskQuery {pub page:Option<u32>,pub page_size:Option<u32>,pub status:Option<String>}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct WalletRiskDecision {pub approved:bool,pub comment:String}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct WalletRiskRelease {pub comment:String}

// ===== 跨服务载荷(替代 json!) =====

/// 发起退款:user 服务只需要操作人与原始请求。
#[derive(Serialize)]
struct RefundCreateBody<'a, R> {
    actor_id: u64,
    request: &'a R,
}

/// 审核决策(同意 / 拒绝):user 侧据此二选一走 approve / reject 端点。
#[derive(Serialize)]
struct RefundDecisionBody<'a> {
    actor_id: u64,
    comment: &'a str,
}

/// 钱包风控审核 / 解冻。
#[derive(Serialize)]
struct WalletRiskBody<'a> {
    actor_id: u64,
    comment: &'a str,
}

/// 钱包风控审核的决策体(带 approved 布尔)。
#[derive(Serialize)]
struct WalletRiskReviewBody<'a> {
    actor_id: u64,
    approved: bool,
    comment: &'a str,
}

/// 发票开票 / 拒签。
#[derive(Serialize)]
struct InvoiceDecisionBody<'a> {
    decision: &'static str,
    actor_id: u64,
    invoice_url: Option<&'a str>,
    reason: Option<&'a str>,
}

/// 审计载荷:`{comment:…, result:…}`。
#[derive(Serialize)]
struct AuditRequestResult<'a, R, S> {
    request: &'a R,
    result: &'a S,
}

/// 审计载荷:`{"comment":…, "result":…}`。
#[derive(Serialize)]
struct AuditCommentResult<'a, S> {
    comment: &'a str,
    result: &'a S,
}

/// 审计载荷:`{"reason":…, "stage":…}`。
#[derive(Serialize)]
struct AuditRetry<'a> {
    reason: &'a str,
    stage: &'a str,
}

/// 审计载荷:`{"invoice_url":…}`。
#[derive(Serialize)]
struct AuditInvoiceUrl<'a> {
    invoice_url: &'a str,
}

/// 审计载荷:`{"invoice_url":…, "first_reviewer_id":…, "result":…}`。
#[derive(Serialize)]
struct AuditInvoiceSecond<'a, S> {
    invoice_url: &'a str,
    first_reviewer_id: Option<u64>,
    result: &'a S,
}

/// 审计载荷:`{"reason":…, "result":…}`。
#[derive(Serialize)]
struct AuditReasonResult<'a, S> {
    reason: &'a str,
    result: &'a S,
}

// ===== usecase =====

pub async fn settlements(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::Settlement>>>> {
    let items = repository_sql::settlements(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

pub async fn refunds(State(st): State<AppState>, c: ActiveAdmin, axum::extract::Query(q):axum::extract::Query<api_contracts::refunds::RefundQuery>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::PagedResponse<api_contracts::admin::AdminRefundRow>>>> {
    if !repository_sql::has_refund_read(&st, c.admin_user_id).await? {
        return Err(AppError::Forbidden("缺少 finance.refund.read 权限".into()));
    }
    if !q.valid(){return Err(AppError::BadRequest("退款筛选参数无效".into()));}
    let mut page:api_contracts::common::PagedResponse<api_contracts::charge::AdminRefund>=client(&st).get(st.cfg.service_urls.user.as_deref(),p::USER_INTERNAL_REFUND_LIST,&q).await?;
    let can_retry=repository_sql::has_refund_retry(&st,c.admin_user_id).await?;
    let can_review=repository_sql::has_refund_review(&st,c.admin_user_id).await?;
    let mut items=Vec::with_capacity(page.items.len());
    for item in std::mem::take(&mut page.items) {
        // 被拒且从未产生审核行时,review 是 null —— 原实现会在其上补出
        // {"status":"rejected"}(serde_json 对 Null 索引赋值会自动建对象),
        // 故这里显式构造,而不是让 Option 直接落 null。
        let me=c.admin_user_id.to_string();
        let review:Option<api_contracts::admin::AdminRefundReviewView>=match item.review.as_ref() {
            Some(r)=>Some(api_contracts::admin::AdminRefundReviewView{
                status:if item.status=="rejected"{"rejected".to_string()}else{r.status.clone()},
                first_signer:Some(r.first_signer.clone()),second_signer:r.second_signer.clone(),
                first_comment:Some(r.first_comment.clone()),second_comment:r.second_comment.clone(),
                approved_at:r.approved_at.clone(),
            }),
            None=>if item.status=="rejected"{Some(api_contracts::admin::AdminRefundReviewView{status:"rejected".into(),first_signer:None,second_signer:None,first_comment:None,second_comment:None,approved_at:None})}else{None},
        };
        let review_status=review.as_ref().map(|r|r.status.as_str());
        let first_signer=review.as_ref().and_then(|r|r.first_signer.as_deref());
        let can_approve=domain::can_approve(can_review,&item.status,&item.biz_type,review_status,first_signer,me.as_str());
        let can_reject=domain::can_reject(can_review,&item.status,&item.biz_type,review_status);
        let task_row=repository_sql::refund_task_by_no(&st,&item.refund_no).await?;
        use sqlx::Row;
        let (can_retry, task)=match task_row {
            Some(t)=>{
                let stage:String=t.try_get("stage")?;
                let error:Option<String>=t.try_get("last_error")?;
                (can_retry && error.is_some() && domain::retryable_stage(&stage),
                 Some(api_contracts::admin::AdminRefundTask{stage,attempts:t.try_get("attempts")?,last_error:error,scheduled_at:t.try_get::<chrono::NaiveDateTime,_>("scheduled_at")?.and_utc().to_rfc3339()}))
            }
            None=>(false,None),
        };
        items.push(api_contracts::admin::AdminRefundRow{
         id:item.id,refund_no:item.refund_no,user_id:item.user_id,payment_order_id:item.payment_order_id,
         biz_type:item.biz_type,refund_cents:item.refund_cents,status:item.status,reason:item.reason,
         failure_reason:item.failure_reason,created_at:item.created_at,completed_at:item.completed_at,
         review,can_approve,can_reject,can_retry,task,
        });
    }
    let response=api_contracts::common::PagedResponse{items,total:page.total,page:page.page,page_size:page.page_size,permissions:page.permissions};
    Ok(Json(common_error::ApiEnvelope::ok(response, common_error::current_request_id())))
}

pub async fn refund_approve(State(st):State<AppState>,c:ActiveAdmin,axum::extract::Path(no):axum::extract::Path<String>,Json(req):Json<RefundApprovalReq>)->AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::RefundReviewed>>>{
    require_permission(&st,&c,"order.refund.review").await?;
    review_decision::<api_contracts::charge::RefundReviewed>(st,c,no,req,false).await
}

pub async fn refund_create(State(st):State<AppState>,c:ActiveAdmin,axum::extract::Path(id):axum::extract::Path<u64>,Json(req):Json<api_contracts::refunds::ManualRefundRequest>)->AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::ManualRefundCreated>>>{
    require_permission(&st,&c,"order.refund.create").await?;
    let mut tx=st.finance.begin().await?;
    repository_sql::lock_refund_create_grants(&mut tx, c.admin_user_id).await?;
    let result:api_contracts::charge::ManualRefundCreated=client(&st).post(st.cfg.service_urls.user.as_deref(),&api_contracts::fill_path(p::USER_INTERNAL_ORDER_REFUND_CREATE,"order_id",&id.to_string()),&RefundCreateBody{actor_id:c.admin_user_id,request:&req}).await?;
    let audit = serde_json::to_value(AuditRequestResult { request: &req, result: &result })?;
    repository_sql::audit_refund_create(&mut tx, c.admin_user_id, id, &audit).await?;
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(result,common_error::current_request_id())))
}

pub async fn refund_reject(State(st):State<AppState>,c:ActiveAdmin,axum::extract::Path(no):axum::extract::Path<String>,Json(req):Json<RefundRejectReq>)->AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::RefundRejected>>>{
    require_permission(&st,&c,"order.refund.review").await?;
    review_decision::<api_contracts::charge::RefundRejected>(st,c,no,RefundApprovalReq{approve_comment:req.reason},true).await
}

/// 同意侧上游回 `RefundReviewed`(带签署人),拒绝侧回 `RefundRejected`(不带),
/// 键集合本就不同,故用泛型承载而不是强行合并成一个类型。
async fn review_decision<T:serde::de::DeserializeOwned+serde::Serialize>(st:AppState,c:ActiveAdmin,no:String,req:RefundApprovalReq,reject:bool)->AppResult<Json<common_error::ApiEnvelope<T>>>{
    domain::valid_refund_decision(&no, &req.approve_comment)?;
    let mut tx=st.finance.begin().await?;
    // Lock the actual account and grant rows until this approval has been sent.
    repository_sql::lock_refund_reviewer(&mut tx, c.admin_user_id).await?;
    let detail:api_contracts::charge::AdminRefundDetail=client(&st).get(st.cfg.service_urls.user.as_deref(),&api_contracts::fill_path(p::USER_INTERNAL_REFUND_DETAIL,"refund_id",&no),&()).await?;
    // 拒绝分支不看首签人;同意分支必须重新校验首签人账号仍有效。
    if let Some(first)=detail.first_signer.as_deref().filter(|_|!reject){
        let first=first.parse::<u64>().map_err(|_|AppError::ServiceUnavailable("审核身份响应无效".into()))?;
        repository_sql::lock_refund_reviewer(&mut tx, first).await?;
    }
    let path=if reject{p::USER_INTERNAL_REFUND_REJECT}else{p::USER_INTERNAL_REFUND_APPROVE};
    let result:T=client(&st).post(st.cfg.service_urls.user.as_deref(),&api_contracts::fill_path(path,"refund_id",&no),&RefundDecisionBody{actor_id:c.admin_user_id,comment:&req.approve_comment}).await?;
    if reject{repository_sql::mark_refund_manual_review(&mut tx, &no).await?;}
    let audit = serde_json::to_value(AuditCommentResult { comment: &req.approve_comment, result: &result })?;
    repository_sql::audit_refund_decision(&mut tx, c.admin_user_id, reject, &no, &audit).await?;
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(result,common_error::current_request_id())))
}

pub async fn refund_retry(State(st): State<AppState>, c: ActiveAdmin, axum::extract::Path(no): axum::extract::Path<String>, Json(req):Json<RefundRetryReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::RefundRetryQueued>>> {
    require_permission(&st,&c,"finance.refund.retry").await?;
    let reason=req.reason.trim();
    domain::valid_refund_retry(&no, reason)?;
    let mut tx=st.finance.begin().await?;
    let (stage, already_queued)=repository_sql::load_retry_context(&mut tx,&no,c.admin_user_id).await?;
    if !already_queued {
        // Only wake the existing task. Never reset provider request, result, stage or refund number.
        repository_sql::wake_refund_task(&mut tx,&no).await?;
        let audit = serde_json::to_value(AuditRetry { reason, stage: &stage })?;
        repository_sql::audit_refund_retry(&mut tx, c.admin_user_id, &no, &audit).await?;
    }
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::admin::RefundRetryQueued{queued:true,already_queued,refund_no:no,stage},common_error::current_request_id())))
}

pub async fn invoices(State(st): State<AppState>, c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::InvoiceReviewRow>>>> {
    repository_sql::authorize_invoice_review(&st, &c).await?;
    let rows = repository_sql::invoice_queue(&st).await?;
    let mut items = Vec::with_capacity(rows.len());
    for row in rows {
        let id: u64 = sqlx::Row::try_get(&row, "invoice_request_id")?;
        let path = api_contracts::fill_path(p::USER_INTERNAL_INVOICE_DETAIL, "invoice_id", &id.to_string());
        let detail: api_contracts::InvoiceDetailResponse = client(&st).get(st.cfg.service_urls.user.as_deref(), &path, &()).await?;
        items.push(api_contracts::admin::InvoiceReviewRow {
            invoice_request_id: detail.invoice_request_id,
            invoice_no: detail.invoice_no,
            user_id: detail.user_id,
            biz_type: detail.biz_type,
            biz_id: detail.biz_id,
            total_cents: detail.total_cents,
            invoice_type: detail.invoice_type,
            created_at: detail.created_at,
            // user 侧的审核态与 admin 队列态是两个库的列,前端要分开看
            review_status: detail.review_status,
            queue_review_status: sqlx::Row::try_get(&row, "review_status")?,
            queue_reviewed_by: sqlx::Row::try_get::<Option<u64>, _>(&row, "reviewed_by")?.map(|v| v.to_string()),
            queue_reject_reason: sqlx::Row::try_get::<Option<String>, _>(&row, "reject_reason")?,
            first_reviewer_id: sqlx::Row::try_get::<Option<u64>, _>(&row, "first_reviewer_id")?.map(|v| v.to_string()),
            first_reviewed_at: sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(&row, "first_reviewed_at")?.map(|v| v.to_rfc3339()),
            second_reviewer_id: sqlx::Row::try_get::<Option<u64>, _>(&row, "second_reviewer_id")?.map(|v| v.to_string()),
            second_reviewed_at: sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(&row, "second_reviewed_at")?.map(|v| v.to_rfc3339()),
            invoice_url: sqlx::Row::try_get::<Option<String>, _>(&row, "invoice_url")?,
        });
    }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::ListResponse::new(items), common_error::current_request_id())))
}

pub async fn invoice_approve(State(st): State<AppState>, c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>, Json(req): Json<InvoiceApproveReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::InvoiceReviewAck>>> {
    repository_sql::authorize_invoice_review(&st, &c).await?;
    let invoice_url = req.invoice_url.trim();
    domain::ensure_invoice_url(invoice_url)?;
    let mut tx = st.finance.begin().await?;
    repository_sql::ensure_invoice_row(&mut tx, id).await?;
    let local = repository_sql::lock_invoice_review(&mut tx, id).await?;
    let local_status: String = sqlx::Row::try_get(&local, "review_status")?;
    let first_reviewer: Option<u64> = sqlx::Row::try_get(&local, "first_reviewer_id")?;
    let second_reviewer: Option<u64> = sqlx::Row::try_get(&local, "second_reviewer_id")?;
    let saved_url: Option<String> = sqlx::Row::try_get(&local, "invoice_url")?;
    if local_status == "approved" {
        if second_reviewer == Some(c.admin_user_id) && saved_url.as_deref() == Some(invoice_url) {
            tx.commit().await?;
            return Ok(Json(common_error::ApiEnvelope::ok(api_contracts::admin::InvoiceReviewAck{reviewed:true,review_status:"issued".into(),first_reviewer_id:None,recovered:None,already_processed:Some(true)}, common_error::current_request_id())));
        }
        return Err(AppError::Conflict("发票申请已完成审核".into()));
    }
    if local_status == "rejected" { return Err(AppError::Conflict("已拒绝的发票申请不能开具".into())); }
    let path = api_contracts::fill_path(p::USER_INTERNAL_INVOICE_DETAIL, "invoice_id", &id.to_string());
    let detail: api_contracts::InvoiceDetailResponse = client(&st).get(st.cfg.service_urls.user.as_deref(), &path, &()).await?;
    let user_status = detail.review_status.as_str();
    if local_status == "pending" {
        if user_status != "pending" { return Err(AppError::Conflict("用户发票申请已处理，不能再次审核".into())); }
        repository_sql::mark_invoice_awaiting_second(&mut tx, id, c.admin_user_id, invoice_url).await?;
        let audit = serde_json::to_value(AuditInvoiceUrl { invoice_url })?;
        repository_sql::audit_invoice_first_approve(&mut tx, c.admin_user_id, id, &audit).await?;
        tx.commit().await?;
        return Ok(Json(common_error::ApiEnvelope::ok(api_contracts::admin::InvoiceReviewAck{reviewed:true,review_status:"awaiting_second".into(),first_reviewer_id:Some(c.admin_user_id.to_string()),recovered:None,already_processed:None}, common_error::current_request_id())));
    }
    if local_status != "awaiting_second" { return Err(AppError::Conflict("发票审核状态已变化".into())); }
    if first_reviewer == Some(c.admin_user_id) {
        if saved_url.as_deref() == Some(invoice_url) {
            tx.commit().await?;
            return Ok(Json(common_error::ApiEnvelope::ok(api_contracts::admin::InvoiceReviewAck{reviewed:true,review_status:"awaiting_second".into(),first_reviewer_id:None,recovered:None,already_processed:Some(true)}, common_error::current_request_id())));
        }
        return Err(AppError::Forbidden("首次审核人不能完成第二次复核".into()));
    }
    if saved_url.as_deref() != Some(invoice_url) { return Err(AppError::Conflict("第二次复核的发票链接必须与首次审核一致".into())); }
    if let Some(first_id) = first_reviewer {
        if !repository_sql::lock_first_invoice_reviewer(&mut tx, first_id).await? {
            return Err(AppError::Forbidden("首次审核人账号已停用或已撤销发票审核权限".into()));
        }
    }
    // 崩溃恢复:上游已由同一管理员签发过、只是 admin 库事务回滚,识别为幂等重放。
    let result: api_contracts::admin::InvoiceReviewAck = if user_status == "issued"
        && detail.reviewed_by == Some(c.admin_user_id)
        && detail.invoice_url.as_deref() == Some(invoice_url)
    {
        api_contracts::admin::InvoiceReviewAck{reviewed:true,review_status:"issued".into(),first_reviewer_id:None,recovered:Some(true),already_processed:None}
    } else {
        if user_status != "pending" { return Err(AppError::Conflict("用户发票申请已处理，不能再次审核".into())); }
        let upstream:api_contracts::charge::InvoiceReviewed=client(&st).post(st.cfg.service_urls.user.as_deref(), &path, &InvoiceDecisionBody{decision:"approve",actor_id:c.admin_user_id,invoice_url:Some(invoice_url),reason:None}).await?;
        api_contracts::admin::InvoiceReviewAck{reviewed:true,review_status:upstream.review_status,first_reviewer_id:None,recovered:None,already_processed:None}
    };
    repository_sql::mark_invoice_approved(&mut tx, id, c.admin_user_id).await?;
    let audit = serde_json::to_value(AuditInvoiceSecond { invoice_url, first_reviewer_id: first_reviewer, result: &result })?;
    repository_sql::audit_invoice_second_approve(&mut tx, c.admin_user_id, id, &audit).await?;
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn invoice_reject(State(st): State<AppState>, c: ActiveAdmin, axum::extract::Path(id): axum::extract::Path<u64>, Json(req): Json<InvoiceRejectReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::InvoiceRejectAck>>> {
    domain::ensure_invoice_reject_reason(&req.reason)?;
    repository_sql::authorize_invoice_review(&st, &c).await?;
    let mut tx = st.finance.begin().await?;
    repository_sql::ensure_invoice_row(&mut tx, id).await?;
    let local = repository_sql::lock_invoice_review_for_reject(&mut tx, id).await?;
    let local_status: String = sqlx::Row::try_get(&local, "review_status")?;
    if local_status == "rejected"
        && sqlx::Row::try_get::<Option<u64>, _>(&local, "reviewed_by")? == Some(c.admin_user_id)
        && sqlx::Row::try_get::<Option<String>, _>(&local, "reject_reason")?.as_deref() == Some(req.reason.trim())
    {
        tx.commit().await?;
        return Ok(Json(common_error::ApiEnvelope::ok(api_contracts::admin::InvoiceRejectAck{reviewed:true,review_status:"rejected".into(),rejected:None,already_processed:Some(true)}, common_error::current_request_id())));
    }
    if local_status == "approved" || local_status == "rejected" { return Err(AppError::Conflict("发票申请已完成审核".into())); }
    let path = api_contracts::fill_path(p::USER_INTERNAL_INVOICE_DETAIL, "invoice_id", &id.to_string());
    let detail: api_contracts::InvoiceDetailResponse = client(&st).get(st.cfg.service_urls.user.as_deref(), &path, &()).await?;
    let user_status = detail.review_status.as_str();
    // 崩溃恢复:上游已由同一管理员拒签过、只是 admin 库事务回滚,识别为幂等重放。
    let result: api_contracts::admin::InvoiceRejectAck = if user_status == "rejected"
        && detail.reviewed_by == Some(c.admin_user_id)
        && detail.reject_reason.as_deref() == Some(req.reason.trim())
    {
        api_contracts::admin::InvoiceRejectAck{reviewed:true,review_status:"rejected".into(),rejected:Some(true),already_processed:None}
    } else {
        if user_status != "pending" { return Err(AppError::Conflict("用户发票申请已处理，不能再次拒绝".into())); }
        let upstream:api_contracts::charge::InvoiceReviewed=client(&st).post(st.cfg.service_urls.user.as_deref(), &path, &InvoiceDecisionBody{decision:"reject",actor_id:c.admin_user_id,invoice_url:None,reason:Some(req.reason.trim())}).await?;
        api_contracts::admin::InvoiceRejectAck{reviewed:true,review_status:upstream.review_status,rejected:Some(true),already_processed:None}
    };
    repository_sql::mark_invoice_rejected(&mut tx, id, c.admin_user_id, req.reason.trim()).await?;
    let audit = serde_json::to_value(AuditReasonResult { reason: &req.reason, result: &result })?;
    repository_sql::audit_invoice_reject(&mut tx, c.admin_user_id, id, &audit).await?;
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn reconcile_logs(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::ReconcileLog>>>> {
    let items = repository_sql::reconcile_logs(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

pub async fn wallet_risks(State(st):State<AppState>,c:ActiveAdmin,axum::extract::Query(q):axum::extract::Query<WalletRiskQuery>)->AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::WalletRiskList>>>{
 let mut tx=st.finance.begin().await?;repository_sql::wallet_risk_authorize(&mut tx,c.admin_user_id,false).await?;
 let mut result:api_contracts::charge::WalletRiskList=client(&st).get(st.cfg.service_urls.user.as_deref(),p::USER_INTERNAL_WALLET_RISKS,&q).await?;
 let can_release=match repository_sql::wallet_risk_authorize(&mut tx,c.admin_user_id,true).await {Ok(())=>true,Err(AppError::Forbidden(_))=>false,Err(e)=>return Err(e)};
 // user 侧已按"已审核 + 冻结原因仍 frozen + 未解冻"算过一遍,
 // admin 再与本地 finance.wallet_risk.release 权限取**与**,不是覆盖。
 for item in &mut result.items{item.can_release=can_release && item.can_release;}
 tx.commit().await?;Ok(Json(common_error::ApiEnvelope::ok(result,common_error::current_request_id())))
}

pub async fn wallet_risk_review(State(st):State<AppState>,c:ActiveAdmin,axum::extract::Path(id):axum::extract::Path<String>,Json(req):Json<WalletRiskDecision>)->AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::WalletRefundApplied>>>{
 let id=domain::normalize_wallet_risk_id(&id)?;
 let mut tx=st.finance.begin().await?;repository_sql::wallet_risk_authorize(&mut tx,c.admin_user_id,false).await?;
 let result:api_contracts::charge::WalletRefundApplied=client(&st).post(st.cfg.service_urls.user.as_deref(),&api_contracts::fill_path(p::USER_INTERNAL_WALLET_RISK_REVIEW,"request_id",&id),&WalletRiskReviewBody{actor_id:c.admin_user_id,approved:req.approved,comment:&req.comment}).await?;
 let audit=serde_json::to_value(&result)?;
 repository_sql::audit_wallet_risk(&mut tx,c.admin_user_id,"wallet_risk.review",&id,&audit).await?;
 tx.commit().await?;Ok(Json(common_error::ApiEnvelope::ok(result,common_error::current_request_id())))
}

pub async fn wallet_risk_release(State(st):State<AppState>,c:ActiveAdmin,axum::extract::Path(id):axum::extract::Path<String>,Json(req):Json<WalletRiskRelease>)->AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::WalletRiskReleased>>>{
 let id=domain::normalize_wallet_risk_id(&id)?;
 let mut tx=st.finance.begin().await?;repository_sql::wallet_risk_authorize(&mut tx,c.admin_user_id,true).await?;
 let result:api_contracts::charge::WalletRiskReleased=client(&st).post(st.cfg.service_urls.user.as_deref(),&api_contracts::fill_path(p::USER_INTERNAL_WALLET_RISK_RELEASE,"request_id",&id),&WalletRiskBody{actor_id:c.admin_user_id,comment:&req.comment}).await?;
 let audit=serde_json::to_value(&result)?;
 repository_sql::audit_wallet_risk(&mut tx,c.admin_user_id,"wallet_risk.release",&id,&audit).await?;
 tx.commit().await?;Ok(Json(common_error::ApiEnvelope::ok(result,common_error::current_request_id())))
}

/// 订单详情页是否显示"发起退款"入口。放在 finance 域是因为权限口径与
/// `refund_create` 完全一致,两处必须同步改。
pub async fn refund_applicant_eligible(st: &AppState, admin_user_id: u64, status: &str, paid_cents: Option<i64>) -> AppResult<bool> {
    let grants = repository_sql::refund_create_grant_count(st, admin_user_id).await?;
    Ok(domain::can_start_refund(grants, status, paid_cents))
}

#[cfg(test)]
mod tests {
    use super::domain::*;
use common_error::AppError;

    #[test]
    fn refund_no_accepts_provider_safe_charset_only() {
        assert!(valid_refund_no("RFTEST_abc123"));
        assert!(valid_refund_no("a-b|c*d@e"));
        assert!(!valid_refund_no(""));
        assert!(!valid_refund_no("refund/1"));
        assert!(!valid_refund_no("退款单号"));
        assert!(!valid_refund_no(&"a".repeat(65)));
    }

    #[test]
    fn review_comment_must_be_present_bounded_and_printable() {
        assert!(valid_review_comment("同意"));
        assert!(!valid_review_comment("   "));
        assert!(!valid_review_comment("x\ny"));
        assert!(!valid_review_comment(&"字".repeat(256)));
    }

    #[test]
    fn retryable_stage_matches_the_durable_orchestration_phases() {
        for s in ["queued", "querying", "reporting"] {
            assert!(retryable_stage(s), "{s}");
        }
        for s in ["done", "manual_review"] {
            assert!(!retryable_stage(s), "{s}");
        }
    }

    #[test]
    fn completed_tasks_can_never_be_retried() {
        assert!(ensure_retryable("done", true).is_err());
        assert!(ensure_retryable("manual_review", true).is_err());
        // 阶段可重试但没有 last_error —— 说明不是异常中的任务
        assert!(ensure_retryable("queued", false).is_err());
        assert!(ensure_retryable("queued", true).is_ok());
    }

    #[test]
    fn provider_terminal_states_land_in_manual_review() {
        assert_eq!(stage_from_provider_status("SUCCESS"), "done");
        assert_eq!(stage_from_provider_status("PROCESSING"), "manual_review");
        assert_eq!(stage_from_provider_status("CLOSED"), "manual_review");
    }

    #[test]
    fn first_signer_cannot_approve_their_own_review() {
        let ok = can_approve(true, "pending", "charge", Some("awaiting_second"), Some("7"), "8");
        assert!(ok);
        // 首签人本人不得二次确认
        assert!(!can_approve(true, "pending", "charge", Some("awaiting_second"), Some("8"), "8"));
        // 已通过的不可再批
        assert!(!can_approve(true, "pending", "charge", Some("approved"), Some("7"), "8"));
        // 非充电单不走 admin 审核
        assert!(!can_approve(true, "pending", "withdraw", Some("awaiting_second"), Some("7"), "8"));
        assert!(!can_approve(false, "pending", "charge", Some("awaiting_second"), Some("7"), "8"));
    }

    #[test]
    fn reject_requires_a_pending_second_signature() {
        assert!(can_reject(true, "pending", "charge", Some("awaiting_second")));
        assert!(!can_reject(true, "pending", "charge", Some("approved")));
        assert!(!can_reject(false, "pending", "charge", Some("awaiting_second")));
    }

    #[test]
    fn refund_entry_needs_both_grants_and_a_paid_order() {
        assert!(can_start_refund(2, "completed", Some(100)));
        assert!(!can_start_refund(1, "completed", Some(100)));
        assert!(!can_start_refund(2, "completed", Some(0)));
        assert!(!can_start_refund(2, "completed", None));
        assert!(!can_start_refund(2, "charging", Some(100)));
    }

    #[test]
    fn invoice_url_must_be_https_with_a_host() {
        assert!(valid_invoice_url("https://cdn.example.com/i.pdf"));
        assert!(!valid_invoice_url("http://cdn.example.com/i.pdf"));
        assert!(!valid_invoice_url("javascript:alert(1)"));
        assert!(!valid_invoice_url("https://"));
    }

    #[test]
    fn wallet_risk_request_ids_must_be_uuids() {
        assert!(normalize_wallet_risk_id("11111111-1111-1111-1111-111111111111").is_ok());
        assert!(
            matches!(
                normalize_wallet_risk_id("nope").unwrap_err(),
                AppError::BadRequest(ref m) if m == "申请编号无效"
            )
        );
    }
}
