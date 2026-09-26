//! admin 视角 — 财务(分账 / 提现 / 退款审核 / 发票审核 / 对账日志)

use crate::AppState;
use crate::api_types;
use api_contracts::paths as p;
use axum::{extract::{Path, State}, Json};
use common_auth::AdminClaims;
use common_db::IdGen;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use serde_json::{json, Value};

pub async fn settlements(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query(
        "SELECT id, settlement_no, split_template_id, period_start, period_end, total_cents, status, created_at
         FROM settled_record ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "settlement_no": sqlx::Row::try_get::<String, _>(r, "settlement_no").unwrap_or_default(),
        "split_template_id": sqlx::Row::try_get::<u64, _>(r, "split_template_id").unwrap_or(0),
        "period_start": sqlx::Row::try_get::<chrono::NaiveDate, _>(r, "period_start").ok().map(|d| d.to_string()),
        "period_end": sqlx::Row::try_get::<chrono::NaiveDate, _>(r, "period_end").ok().map(|d| d.to_string()),
        "total_cents": sqlx::Row::try_get::<i64, _>(r, "total_cents").unwrap_or(0),
        "status": sqlx::Row::try_get::<String, _>(r, "status").unwrap_or_default(),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

pub async fn withdraw_list(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query("SELECT id, withdraw_no, party_id, party_code, amount_cents, status, created_at FROM withdraw_request ORDER BY id DESC LIMIT 200")
        .fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "withdraw_no": sqlx::Row::try_get::<String, _>(r, "withdraw_no").unwrap_or_default(),
        "party_id": sqlx::Row::try_get::<u64, _>(r, "party_id").unwrap_or(0),
        "amount_cents": sqlx::Row::try_get::<i64, _>(r, "amount_cents").unwrap_or(0),
        "status": sqlx::Row::try_get::<String, _>(r, "status").unwrap_or_default(),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct WithdrawCreateReq {
    pub party_id: u64,
    pub amount_cents: i64,
}

pub async fn withdraw_create(State(st): State<AppState>, _c: AdminClaims, Json(req): Json<WithdrawCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let no = IdGen::new("WDR").next();
    sqlx::query(
        "INSERT INTO withdraw_request (withdraw_no, party_id, party_code, amount_cents, status)
         VALUES (?, ?, '', ?, 'pending')"
    )
    .bind(&no).bind(req.party_id).bind(req.amount_cents)
    .execute(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"withdraw_no": no}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct WithdrawReviewReq {
    pub approved: bool,
    pub note: Option<String>,
}

pub async fn withdraw_review(State(st): State<AppState>, c: AdminClaims, Path(id): Path<u64>, Json(req): Json<WithdrawReviewReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let status = if req.approved { "approved" } else { "rejected" };
    let n = sqlx::query("UPDATE withdraw_request SET status = ?, reviewed_by = ?, reviewed_at = NOW(3), note = ? WHERE id = ? AND status = 'pending'")
        .bind(status).bind(c.admin_user_id).bind(req.note.as_deref()).bind(id)
        .execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::Conflict("not pending".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"reviewed": true}), common_error::current_request_id())))
}

pub async fn refunds(State(st): State<AppState>, c: AdminClaims, axum::extract::Query(q):axum::extract::Query<api_contracts::refunds::RefundQuery>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let allowed:i64=sqlx::query_scalar("SELECT COUNT(*) FROM admin_user_role a JOIN role r ON r.id=a.role_id JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id WHERE a.id=? AND a.status='active' AND a.deleted_at IS NULL AND r.deleted_at IS NULL AND p.code='finance.refund.read'").bind(c.admin_user_id).fetch_one(st.db.pool()).await?;
    if allowed==0{return Err(AppError::Forbidden("缺少 finance.refund.read 权限".into()));}
    if !q.valid(){return Err(AppError::BadRequest("退款筛选参数无效".into()));}
    let mut v:Value=common_http::internal::ApiClient::new(st.http.clone(),st.service_token.clone()).get(st.cfg.service_urls.user.as_deref(),p::USER_INTERNAL_REFUND_LIST,&q).await?;
    use sqlx::Row;
    let can_retry:i64=sqlx::query_scalar("SELECT COUNT(*) FROM admin_user_role a JOIN role_permission rp ON rp.role_id=a.role_id JOIN permission p ON p.id=rp.permission_id WHERE a.id=? AND p.code='finance.refund.retry'").bind(c.admin_user_id).fetch_one(st.db.pool()).await?;
    let items=v.get_mut("items").and_then(Value::as_array_mut).ok_or_else(||AppError::ServiceUnavailable("退款列表响应异常".into()))?;
    let can_review:i64=sqlx::query_scalar("SELECT COUNT(*) FROM admin_user_role a JOIN role r ON r.id=a.role_id JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id WHERE a.id=? AND a.status='active' AND a.deleted_at IS NULL AND r.deleted_at IS NULL AND r.code='customer_finance' AND p.code='order.refund.review'").bind(c.admin_user_id).fetch_one(st.db.pool()).await?;
    for item in items {
        item["can_approve"]=json!(can_review>0 && item["status"]=="pending" && item["biz_type"]=="charge" && item["review"]["status"]!="approved" && item["review"]["first_signer"]!=c.admin_user_id.to_string());
        item["can_reject"]=json!(can_review>0 && item["status"]=="pending" && item["biz_type"]=="charge" && item["review"]["status"]=="awaiting_second");
        if item["status"]=="rejected"{item["review"]["status"]=json!("rejected");}
        let no=item.get("refund_no").and_then(Value::as_str).ok_or_else(||AppError::ServiceUnavailable("退款列表缺少单号".into()))?;
        let task=sqlx::query("SELECT stage,attempts,last_error,scheduled_at FROM refund_task WHERE refund_no=?").bind(no).fetch_optional(st.db.pool()).await?;
        item["can_retry"]=json!(false);
        item["task"]=Value::Null;
        if let Some(task)=task {
            let stage:String=task.try_get("stage")?;
            let error:Option<String>=task.try_get("last_error")?;
            item["can_retry"]=json!(can_retry>0 && error.is_some() && ["queued","querying","reporting"].contains(&stage.as_str()));
            item["task"]=json!({"stage":stage,"attempts":task.try_get::<u32,_>("attempts")?,"last_error":error,"scheduled_at":task.try_get::<chrono::NaiveDateTime,_>("scheduled_at")?.and_utc().to_rfc3339()});
        }
    }
    Ok(Json(common_error::ApiEnvelope::ok(v, common_error::current_request_id())))
}

#[derive(Deserialize)]
pub struct RefundRetryReq { pub reason: String }
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RefundApprovalReq { pub approve_comment:String }
pub async fn refund_approve(State(st):State<AppState>,c:AdminClaims,Path(no):Path<String>,Json(req):Json<RefundApprovalReq>)->AppResult<Json<common_error::ApiEnvelope<Value>>>{
    review_decision(st,c,no,req,false).await
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RefundRejectReq {pub reason:String}
pub async fn refund_create(State(st):State<AppState>,c:AdminClaims,Path(id):Path<u64>,Json(req):Json<api_contracts::refunds::ManualRefundRequest>)->AppResult<Json<common_error::ApiEnvelope<Value>>>{
    let mut tx=st.db.pool().begin().await?;
    let grants:Vec<String>=sqlx::query_scalar("SELECT p.code FROM admin_user_role a JOIN role r ON r.id=a.role_id JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id WHERE a.id=? AND a.status='active' AND a.deleted_at IS NULL AND r.deleted_at IS NULL AND r.code='customer_finance' AND p.code IN ('order.read','order.refund.create','order.refund.review') FOR SHARE").bind(c.admin_user_id).fetch_all(&mut *tx).await?;
    if grants.len()!=3{return Err(AppError::Forbidden("发起退款需客户财务角色及订单查看、退款申请、退款审核权限".into()));}
    let result:Value=common_http::internal::ApiClient::new(st.http.clone(),st.service_token.clone()).post(st.cfg.service_urls.user.as_deref(),&p::USER_INTERNAL_ORDER_REFUND_CREATE.replace(":order_id",&id.to_string()),&json!({"actor_id":c.admin_user_id,"request":req})).await?;
    sqlx::query("INSERT INTO audit_log(actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'finance','refund.create','charge_order',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))").bind(c.admin_user_id).bind(id.to_string()).bind(json!({"request":req,"result":result})).execute(&mut *tx).await?;
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(result,common_error::current_request_id())))
}
pub async fn refund_reject(State(st):State<AppState>,c:AdminClaims,Path(no):Path<String>,Json(req):Json<RefundRejectReq>)->AppResult<Json<common_error::ApiEnvelope<Value>>>{
    review_decision(st,c,no,RefundApprovalReq{approve_comment:req.reason},true).await
}
async fn review_decision(st:AppState,c:AdminClaims,no:String,req:RefundApprovalReq,reject:bool)->AppResult<Json<common_error::ApiEnvelope<Value>>>{
    if no.is_empty() || no.len()>64 || !no.bytes().all(|b|b.is_ascii_alphanumeric() || b"_-|*@".contains(&b)) || req.approve_comment.trim().is_empty() || req.approve_comment.chars().count()>255 || req.approve_comment.chars().any(char::is_control){return Err(AppError::BadRequest("退款单号或审核意见无效".into()));}
    let mut tx=st.db.pool().begin().await?;
    // Lock the actual account and grant rows until this approval has been sent.
    async fn authorize(tx:&mut sqlx::Transaction<'_,sqlx::MySql>,id:u64)->AppResult<()> {
        let rows:Vec<u64>=sqlx::query_scalar("SELECT a.id FROM admin_user_role a JOIN role r ON r.id=a.role_id JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id WHERE a.id=? AND a.status='active' AND a.deleted_at IS NULL AND r.deleted_at IS NULL AND r.code='customer_finance' AND p.code='order.refund.review' FOR SHARE").bind(id).fetch_all(&mut **tx).await?;
        if rows.is_empty(){return Err(AppError::Forbidden("审核人必须是具备 order.refund.review 权限的有效客户财务账号".into()));}Ok(())
    }
    authorize(&mut tx,c.admin_user_id).await?;
    let client=common_http::internal::ApiClient::new(st.http.clone(),st.service_token.clone());
    let detail:Value=client.get(st.cfg.service_urls.user.as_deref(),&p::USER_INTERNAL_REFUND_DETAIL.replace(":refund_id",&no),&()).await?;
    if let Some(first)=detail.get("first_signer").and_then(Value::as_str).filter(|_|!reject){
        let first=first.parse::<u64>().map_err(|_|AppError::ServiceUnavailable("审核身份响应无效".into()))?;
        authorize(&mut tx,first).await?;
    }
    let path=if reject{p::USER_INTERNAL_REFUND_REJECT}else{p::USER_INTERNAL_REFUND_APPROVE};
    let result:Value=client.post(st.cfg.service_urls.user.as_deref(),&path.replace(":refund_id",&no),&json!({"actor_id":c.admin_user_id,"comment":req.approve_comment})).await?;
    if reject{sqlx::query("UPDATE refund_task SET stage='manual_review',last_error='退款审核已拒绝' WHERE refund_no=? AND stage IN ('queued','querying','reporting')").bind(&no).execute(&mut *tx).await?;}
    sqlx::query("INSERT INTO audit_log(actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'finance',?,'refund_record',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))").bind(c.admin_user_id).bind(if reject{"refund.reject"}else{"refund.approve"}).bind(&no).bind(json!({"comment":req.approve_comment,"result":result})).execute(&mut *tx).await?;
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(result,common_error::current_request_id())))
}
pub async fn refund_retry(State(st): State<AppState>, c: AdminClaims, Path(no): Path<String>, Json(req):Json<RefundRetryReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    use sqlx::Row;
    let reason=req.reason.trim();
    if reason.is_empty() || reason.chars().count()>255 || reason.chars().any(char::is_control) || no.is_empty() || no.len()>64 || !no.bytes().all(|b|b.is_ascii_alphanumeric() || b"_-|*@".contains(&b)) {
        return Err(AppError::BadRequest("退款单号或重试原因无效".into()));
    }
    let mut tx=st.db.pool().begin().await?;
    let allowed:i64=sqlx::query_scalar("SELECT COUNT(*) FROM admin_user_role a JOIN role r ON r.id=a.role_id JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id WHERE a.id=? AND a.status='active' AND a.deleted_at IS NULL AND r.deleted_at IS NULL AND p.code='finance.refund.retry'").bind(c.admin_user_id).fetch_one(&mut *tx).await?;
    if allowed==0{return Err(AppError::Forbidden("缺少 finance.refund.retry 权限".into()));}
    let row=sqlx::query("SELECT stage,last_error,scheduled_at<=UTC_TIMESTAMP(3) AS due FROM refund_task WHERE refund_no=? FOR UPDATE").bind(&no).fetch_optional(&mut *tx).await?.ok_or_else(||AppError::NotFound("退款任务不存在".into()))?;
    let stage:String=row.try_get("stage")?;
    if !["queued","querying","reporting"].contains(&stage.as_str()) || row.try_get::<Option<String>,_>("last_error")?.is_none(){
        return Err(AppError::Conflict("仅异常中的自动退款任务可重试；已完成或需人工审核的退款不能直接重启".into()));
    }
    let already_queued=row.try_get::<i32,_>("due")?!=0;
    if !already_queued {
        // Only wake the existing task. Never reset provider request, result, stage or refund number.
        sqlx::query("UPDATE refund_task SET scheduled_at=UTC_TIMESTAMP(3) WHERE refund_no=?").bind(&no).execute(&mut *tx).await?;
        sqlx::query("INSERT INTO audit_log (actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'finance','refund.retry','refund_task',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
            .bind(c.admin_user_id).bind(&no).bind(json!({"reason":reason,"stage":stage})).execute(&mut *tx).await?;
    }
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"queued":true,"already_queued":already_queued,"refund_no":no,"stage":stage}),common_error::current_request_id())))
}

pub async fn invoices(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query(
        "SELECT invoice_request_id, review_status, reviewed_by, reviewed_at, reject_reason
         FROM invoice_review ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "invoice_request_id": sqlx::Row::try_get::<u64, _>(r, "invoice_request_id").unwrap_or(0),
        "review_status": sqlx::Row::try_get::<String, _>(r, "review_status").unwrap_or_default(),
        "reject_reason": sqlx::Row::try_get::<Option<String>, _>(r, "reject_reason").ok().flatten(),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct InvoiceApproveReq { pub invoice_url: Option<String> }

pub async fn invoice_approve(State(st): State<AppState>, c: AdminClaims, Path(id): Path<u64>, Json(req): Json<InvoiceApproveReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let mut tx = st.db.pool().begin().await?;
    sqlx::query(
        "INSERT INTO invoice_review (invoice_request_id, review_status, reviewed_by, reviewed_at)
         VALUES (?, 'approved', ?, NOW(3))
         ON DUPLICATE KEY UPDATE review_status='approved', reviewed_by=VALUES(reviewed_by), reviewed_at=VALUES(reviewed_at)"
    )
    .bind(id).bind(c.admin_user_id).execute(&mut *tx).await?;
    // 调 user 内部接口写回 invoice_request — 类型化 client + 路径常量
    if let Some(u) = st.cfg.service_urls.user.as_deref() {
        let body = json!({"invoice_url": req.invoice_url});
        let cli = crate::clients::ServiceClient::new(st.http.clone(), st.service_token.clone());
        let _ = cli.post_typed::<_, serde_json::Value>(Some(u), p::USER_INTERNAL_INVOICE_DETAIL, &body).await;
    }
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"approved": true}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct InvoiceRejectReq { pub reason: String }

pub async fn invoice_reject(State(st): State<AppState>, c: AdminClaims, Path(id): Path<u64>, Json(req): Json<InvoiceRejectReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let mut tx = st.db.pool().begin().await?;
    sqlx::query(
        "INSERT INTO invoice_review (invoice_request_id, review_status, reviewed_by, reviewed_at, reject_reason)
         VALUES (?, 'rejected', ?, NOW(3), ?)
         ON DUPLICATE KEY UPDATE review_status='rejected', reviewed_by=VALUES(reviewed_by), reviewed_at=VALUES(reviewed_at), reject_reason=VALUES(reject_reason)"
    )
    .bind(id).bind(c.admin_user_id).bind(&req.reason).execute(&mut *tx).await?;
    let user_url = st.cfg.service_urls.user.as_deref();
    if let Some(u) = user_url {
        let body = json!({"reject": true, "reason": req.reason});
        let cli = crate::clients::ServiceClient::new(st.http.clone(), st.service_token.clone());
        let _ = cli.post_typed::<_, serde_json::Value>(Some(u), p::USER_INTERNAL_INVOICE_DETAIL, &body).await;
    }
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"rejected": true}), common_error::current_request_id())))
}

pub async fn reconcile_logs(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query(
        "SELECT id, reconcile_type, reconcile_date, internal_count, wechat_count, diff_count,
                internal_cents, wechat_cents, diff_cents, resolved, created_at
         FROM finance_reconcile_log ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "reconcile_type": sqlx::Row::try_get::<String, _>(r, "reconcile_type").unwrap_or_default(),
        "reconcile_date": sqlx::Row::try_get::<chrono::NaiveDate, _>(r, "reconcile_date").ok().map(|d| d.to_string()),
        "internal_count": sqlx::Row::try_get::<u32, _>(r, "internal_count").unwrap_or(0),
        "wechat_count": sqlx::Row::try_get::<u32, _>(r, "wechat_count").unwrap_or(0),
        "diff_count": sqlx::Row::try_get::<i32, _>(r, "diff_count").unwrap_or(0),
        "resolved": sqlx::Row::try_get::<i8, _>(r, "resolved").unwrap_or(0) != 0,
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Deserialize,serde::Serialize)]
pub struct WalletRiskQuery {pub page:Option<u32>,pub page_size:Option<u32>,pub status:Option<String>}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct WalletRiskDecision {pub approved:bool,pub comment:String}
async fn wallet_risk_authorize(tx:&mut sqlx::Transaction<'_,sqlx::MySql>,actor:u64,release:bool)->AppResult<()> {
 let permission=if release{"finance.wallet_risk.release"}else{"finance.wallet_risk.review"};
 let rows:Vec<u64>=sqlx::query_scalar("SELECT a.id FROM admin_user_role a JOIN role r ON r.id=a.role_id JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id WHERE a.id=? AND a.status='active' AND a.deleted_at IS NULL AND r.deleted_at IS NULL AND r.code IN ('customer_finance','customer_cs') AND p.code=? AND (?=FALSE OR r.code='customer_finance') FOR SHARE").bind(actor).bind(permission).bind(release).fetch_all(&mut **tx).await?;
 if rows.is_empty(){return Err(AppError::Forbidden(format!("角色或权限不足，需要 {permission}")));}Ok(())
}pub async fn wallet_risks(State(st):State<AppState>,c:AdminClaims,axum::extract::Query(q):axum::extract::Query<WalletRiskQuery>)->AppResult<Json<common_error::ApiEnvelope<Value>>>{
 let mut tx=st.db.pool().begin().await?;wallet_risk_authorize(&mut tx,c.admin_user_id,false).await?;
 let mut result:Value=common_http::internal::ApiClient::new(st.http.clone(),st.service_token.clone()).get(st.cfg.service_urls.user.as_deref(),p::USER_INTERNAL_WALLET_RISKS,&q).await?;
 let can_release=match wallet_risk_authorize(&mut tx,c.admin_user_id,true).await {Ok(())=>true,Err(AppError::Forbidden(_))=>false,Err(e)=>return Err(e)};
 if let Some(items)=result["items"].as_array_mut(){for item in items{item["can_release"]=json!(can_release && item["can_release"]==true);}}
 tx.commit().await?;Ok(Json(common_error::ApiEnvelope::ok(result,common_error::current_request_id())))
}
pub async fn wallet_risk_review(State(st):State<AppState>,c:AdminClaims,Path(id):Path<String>,Json(req):Json<WalletRiskDecision>)->AppResult<Json<common_error::ApiEnvelope<Value>>>{
 let id=uuid::Uuid::parse_str(&id).map_err(|_|AppError::BadRequest("申请编号无效".into()))?.to_string();
 let mut tx=st.db.pool().begin().await?;wallet_risk_authorize(&mut tx,c.admin_user_id,false).await?;
 let result:Value=common_http::internal::ApiClient::new(st.http.clone(),st.service_token.clone()).post(st.cfg.service_urls.user.as_deref(),&p::USER_INTERNAL_WALLET_RISK_REVIEW.replace(":request_id",&id),&json!({"actor_id":c.admin_user_id,"approved":req.approved,"comment":req.comment})).await?;
 sqlx::query("INSERT INTO audit_log(actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'finance','wallet_risk.review','wallet_refund_request',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))").bind(c.admin_user_id).bind(&id).bind(&result).execute(&mut *tx).await?;
 tx.commit().await?;Ok(Json(common_error::ApiEnvelope::ok(result,common_error::current_request_id())))
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct WalletRiskRelease {pub comment:String}
pub async fn wallet_risk_release(State(st):State<AppState>,c:AdminClaims,Path(id):Path<String>,Json(req):Json<WalletRiskRelease>)->AppResult<Json<common_error::ApiEnvelope<Value>>>{
 let id=uuid::Uuid::parse_str(&id).map_err(|_|AppError::BadRequest("申请编号无效".into()))?.to_string();
 let mut tx=st.db.pool().begin().await?;wallet_risk_authorize(&mut tx,c.admin_user_id,true).await?;
 let result:Value=common_http::internal::ApiClient::new(st.http.clone(),st.service_token.clone()).post(st.cfg.service_urls.user.as_deref(),&p::USER_INTERNAL_WALLET_RISK_RELEASE.replace(":request_id",&id),&json!({"actor_id":c.admin_user_id,"comment":req.comment})).await?;
 sqlx::query("INSERT INTO audit_log(actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'finance','wallet_risk.release','wallet_refund_request',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))").bind(c.admin_user_id).bind(&id).bind(&result).execute(&mut *tx).await?;
 tx.commit().await?;Ok(Json(common_error::ApiEnvelope::ok(result,common_error::current_request_id())))
}
