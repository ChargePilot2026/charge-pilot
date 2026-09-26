//! 发票申请

use crate::AppState;
use axum::{extract::{Query, State}, Json};
use common_db::IdGen;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use serde_json::{json, Value};

#[derive(Debug, Deserialize)]
pub struct InvoiceApplyReq {
    pub biz_type: String,
    pub biz_id: u64,
    pub total_cents: i64,
    pub invoice_type: Option<String>,
    pub title: String,
    pub tax_no: Option<String>,
    pub email: Option<String>,
}

pub async fn apply(
    State(st): State<AppState>,
    claims: common_auth::UserClaims,
    Json(req): Json<InvoiceApplyReq>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    if req.total_cents <= 0 {
        return Err(AppError::BadRequest("total_cents invalid".into()));
    }
    let invoice_no = IdGen::new("INV").next();
    sqlx::query(
        "INSERT INTO invoice_request
         (invoice_no, user_id, biz_type, biz_id, total_cents, invoice_type, title, tax_no, email, review_status)
         VALUES (?, ?, ?, ?, ?, COALESCE(?, 'normal'), ?, ?, ?, 'pending')"
    )
    .bind(&invoice_no)
    .bind(claims.user_id)
    .bind(&req.biz_type)
    .bind(req.biz_id)
    .bind(req.total_cents)
    .bind(req.invoice_type.as_deref())
    .bind(&req.title)
    .bind(req.tax_no.as_deref())
    .bind(req.email.as_deref())
    .execute(st.db.pool())
    .await?;

    Ok(Json(common_error::ApiEnvelope::ok(json!({"invoice_no": invoice_no}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct InvoiceQuery { pub page: Option<u32>, pub page_size: Option<u32> }

pub async fn my(
    State(st): State<AppState>,
    claims: common_auth::UserClaims,
    Query(q): Query<InvoiceQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let page = q.page.unwrap_or(1).max(1);
    let page_size = q.page_size.unwrap_or(20).min(100);
    let offset = (page - 1) * page_size;
    let rows = sqlx::query(
        "SELECT invoice_no, biz_type, total_cents, title, review_status, reject_reason, invoice_url, created_at
         FROM invoice_request WHERE user_id = ?
         ORDER BY id DESC LIMIT ? OFFSET ?"
    )
    .bind(claims.user_id)
    .bind(page_size as i64)
    .bind(offset as i64)
    .fetch_all(st.db.pool())
    .await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "invoice_no": sqlx::Row::try_get::<String, _>(r, "invoice_no").unwrap_or_default(),
        "biz_type": sqlx::Row::try_get::<String, _>(r, "biz_type").unwrap_or_default(),
        "total_cents": sqlx::Row::try_get::<i64, _>(r, "total_cents").unwrap_or(0),
        "title": sqlx::Row::try_get::<String, _>(r, "title").unwrap_or_default(),
        "review_status": sqlx::Row::try_get::<String, _>(r, "review_status").unwrap_or_default(),
        "reject_reason": sqlx::Row::try_get::<Option<String>, _>(r, "reject_reason").ok().flatten(),
        "invoice_url": sqlx::Row::try_get::<Option<String>, _>(r, "invoice_url").ok().flatten(),
        "created_at": sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "created_at").ok().map(|t| t.to_rfc3339()),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"page": page, "page_size": page_size, "items": items}), common_error::current_request_id())))
}

/// admin / billing 调: 获取发票详情
pub async fn internal_detail(
    State(st): State<AppState>,
    axum::extract::Path(invoice_id): axum::extract::Path<String>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r: Option<(u64, u64, String, i64, String, Option<String>)> = sqlx::query_as(
        "SELECT id, user_id, biz_type, total_cents, review_status, invoice_url
         FROM invoice_request WHERE invoice_no = ? LIMIT 1"
    )
    .bind(&invoice_id)
    .fetch_optional(st.db.pool())
    .await?;
    let r = r.ok_or_else(|| AppError::NotFound("invoice".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "id": r.0,
        "user_id": r.1,
        "biz_type": r.2,
        "total_cents": r.3,
        "review_status": r.4,
        "invoice_url": r.5,
    }), common_error::current_request_id())))
}
