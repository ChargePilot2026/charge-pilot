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

pub async fn refunds(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    // 调 user 服务内部接口拿退款列表
    let user_url = st.cfg.service_urls.user.as_deref()
        .ok_or_else(|| AppError::Internal("USER_INTERNAL_URL missing".into()))?;
    let v: Value = common_http::ServiceClient::new(st.service_token.as_str())
        .get_json(user_url, "/api/v1/internal/refund-claims?limit=200").await
        .unwrap_or_else(|_| json!({"items": []}));
    Ok(Json(common_error::ApiEnvelope::ok(v, common_error::current_request_id())))
}

pub async fn refund_retry(State(st): State<AppState>, _c: AdminClaims, Path(_id): Path<String>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    // 重新入队退款(写 retry_queue)
    sqlx::query(
        "INSERT INTO retry_queue (queue_name, payload_json, run_at, max_attempts)
         VALUES ('pay_retry', JSON_OBJECT('action', 'refund_retry', 'refund_id', ?), NOW(3), 5)"
    ).bind(&_id).execute(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"queued": true}), common_error::current_request_id())))
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
