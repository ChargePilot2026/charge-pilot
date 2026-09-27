//! 发票申请

use crate::AppState;
use axum::{extract::{Query, State}, Json};
use common_db::IdGen;
use common_error::{AppError, AppResult};
use common_redis::{streams, StreamEnvelope};
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
    let title = req.title.trim();
    if req.biz_type != "charge" || req.biz_id == 0 || req.total_cents <= 0 {
        return Err(AppError::BadRequest("仅支持已完成的充电订单开票".into()));
    }
    if title.is_empty() || title.chars().count() > 255 || title.chars().any(char::is_control) {
        return Err(AppError::BadRequest("发票抬头无效".into()));
    }
    let invoice_type = req.invoice_type.as_deref().unwrap_or("normal");
    if !["normal", "vat_special"].contains(&invoice_type) {
        return Err(AppError::BadRequest("发票类型无效".into()));
    }
    let tax_no = req.tax_no.as_deref().map(str::trim).filter(|value| !value.is_empty());
    if invoice_type == "vat_special" && tax_no.is_none() {
        return Err(AppError::BadRequest("增值税专用发票必须填写税号".into()));
    }
    if tax_no.is_some_and(|value| value.len() > 64 || value.chars().any(char::is_control)) {
        return Err(AppError::BadRequest("税号格式无效".into()));
    }
    let email = req.email.as_deref().map(str::trim).filter(|value| !value.is_empty());
    if email.is_some_and(|value| value.len() > 128 || value.chars().any(char::is_control) || !value.contains('@')) {
        return Err(AppError::BadRequest("邮箱格式无效".into()));
    }

    let mut tx = st.db.pool().begin().await?;
    let payment = sqlx::query(
        "SELECT p.paid_cents, p.refunded_cents FROM charge_order c
         JOIN payment_order p ON p.id = c.payment_order_id AND p.user_id = c.user_id
         WHERE c.id = ? AND c.user_id = ? AND c.status = 'completed' AND c.deleted_at IS NULL
           AND p.biz_type = 'charge' AND p.status = 'paid' AND p.refunded_cents = 0
           AND p.deleted_at IS NULL FOR UPDATE",
    )
    .bind(req.biz_id)
    .bind(claims.user_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or_else(|| AppError::Conflict("订单不存在、未完成、未支付或已退款，暂不能开票".into()))?;
    let paid_cents: i64 = sqlx::Row::try_get(&payment, "paid_cents")?;
    if paid_cents != req.total_cents {
        return Err(AppError::BadRequest("开票金额与订单实付金额不一致".into()));
    }
    let duplicate: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM invoice_request WHERE user_id = ? AND biz_type = 'charge' AND biz_id = ? AND deleted_at IS NULL)",
    )
    .bind(claims.user_id)
    .bind(req.biz_id)
    .fetch_one(&mut *tx)
    .await?;
    if duplicate {
        return Err(AppError::Conflict("该订单已有发票申请".into()));
    }
    let invoice_no = IdGen::new("INV").next();
    let inserted = sqlx::query(
        "INSERT INTO invoice_request
         (invoice_no, user_id, biz_type, biz_id, total_cents, invoice_type, title, tax_no, email, review_status)
         VALUES (?, ?, ?, ?, ?, COALESCE(?, 'normal'), ?, ?, ?, 'pending')"
    )
    .bind(&invoice_no)
    .bind(claims.user_id)
    .bind(&req.biz_type)
    .bind(req.biz_id)
    .bind(req.total_cents)
    .bind(Some(invoice_type))
    .bind(title)
    .bind(tax_no)
    .bind(email)
    .execute(&mut *tx)
    .await?;
    let invoice_id = inserted.last_insert_id();
    let event = StreamEnvelope::new("invoice_required", "user", json!({"invoice_request_id": invoice_id}));
    let envelope = serde_json::to_value(&event)?;
    sqlx::query("INSERT INTO event_outbox (event_id, stream, envelope_json) VALUES (?, ?, ?)")
        .bind(&event.event_id)
        .bind(streams::INVOICE_REQUIRED)
        .bind(envelope)
        .execute(&mut *tx)
        .await?;
    tx.commit().await?;
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
    let page_size = q.page_size.unwrap_or(20);
    if page > 100_000 || !(1..=100).contains(&page_size) {
        return Err(AppError::BadRequest("分页参数无效".into()));
    }
    let offset = (page - 1) * page_size;
    let total: i64 = sqlx::query_scalar(
        "SELECT COUNT(*) FROM invoice_request WHERE user_id = ? AND deleted_at IS NULL",
    )
    .bind(claims.user_id)
    .fetch_one(st.db.pool())
    .await?;
    let rows = sqlx::query(
        "SELECT invoice_no, biz_type, total_cents, title, review_status, reject_reason, invoice_url, created_at
         FROM invoice_request WHERE user_id = ? AND deleted_at IS NULL
         ORDER BY id DESC LIMIT ? OFFSET ?"
    )
    .bind(claims.user_id)
    .bind(page_size as i64)
    .bind(offset as i64)
    .fetch_all(st.db.pool())
    .await?;
    let items: Vec<Value> = rows
        .iter()
        .map(|r| -> AppResult<Value> {
            Ok(json!({
                "invoice_no": sqlx::Row::try_get::<String, _>(r, "invoice_no")?,
                "biz_type": sqlx::Row::try_get::<String, _>(r, "biz_type")?,
                "total_cents": sqlx::Row::try_get::<i64, _>(r, "total_cents")?,
                "title": sqlx::Row::try_get::<String, _>(r, "title")?,
                "review_status": sqlx::Row::try_get::<String, _>(r, "review_status")?,
                "reject_reason": sqlx::Row::try_get::<Option<String>, _>(r, "reject_reason")?,
                "invoice_url": sqlx::Row::try_get::<Option<String>, _>(r, "invoice_url")?,
                "created_at": sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "created_at")?.to_rfc3339(),
            }))
        })
        .collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"page": page, "page_size": page_size, "total": total, "items": items}), common_error::current_request_id())))
}

/// admin / billing 调: 获取发票详情
pub async fn internal_detail(
    State(st): State<AppState>,
    axum::extract::Path(invoice_id): axum::extract::Path<String>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let row = if let Ok(id) = invoice_id.parse::<u64>() {
        sqlx::query("SELECT * FROM invoice_request WHERE id = ? AND deleted_at IS NULL")
            .bind(id).fetch_optional(st.db.pool()).await?
    } else {
        sqlx::query("SELECT * FROM invoice_request WHERE invoice_no = ? AND deleted_at IS NULL")
            .bind(&invoice_id).fetch_optional(st.db.pool()).await?
    }.ok_or_else(|| AppError::NotFound("invoice".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "invoice_request_id": sqlx::Row::try_get::<u64, _>(&row, "id")?,
        "invoice_no": sqlx::Row::try_get::<String, _>(&row, "invoice_no")?,
        "user_id": sqlx::Row::try_get::<u64, _>(&row, "user_id")?,
        "biz_type": sqlx::Row::try_get::<String, _>(&row, "biz_type")?,
        "biz_id": sqlx::Row::try_get::<u64, _>(&row, "biz_id")?,
        "total_cents": sqlx::Row::try_get::<i64, _>(&row, "total_cents")?,
        "invoice_type": sqlx::Row::try_get::<String, _>(&row, "invoice_type")?,
        "title": sqlx::Row::try_get::<String, _>(&row, "title")?,
        "tax_no": sqlx::Row::try_get::<Option<String>, _>(&row, "tax_no")?,
        "email": sqlx::Row::try_get::<Option<String>, _>(&row, "email")?,
        "review_status": sqlx::Row::try_get::<String, _>(&row, "review_status")?,
        "reviewed_by": sqlx::Row::try_get::<Option<u64>, _>(&row, "reviewed_by")?,
        "reviewed_at": sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(&row, "reviewed_at")?.map(|time| time.to_rfc3339()),
        "reject_reason": sqlx::Row::try_get::<Option<String>, _>(&row, "reject_reason")?,
        "invoice_url": sqlx::Row::try_get::<Option<String>, _>(&row, "invoice_url")?,
        "created_at": sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(&row, "created_at")?.to_rfc3339(),
    }), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct InvoiceReviewReq {
    pub decision: String,
    pub actor_id: u64,
    pub invoice_url: Option<String>,
    pub reason: Option<String>,
}

pub async fn internal_review(
    State(st): State<AppState>,
    axum::extract::Path(invoice_id): axum::extract::Path<String>,
    Json(req): Json<InvoiceReviewReq>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    if !["approve", "reject"].contains(&req.decision.as_str()) || req.actor_id == 0 {
        return Err(AppError::BadRequest("发票审核操作无效".into()));
    }
    if req.decision == "approve"
        && req.invoice_url.as_deref().is_none_or(|url| !url.starts_with("https://") || url.len() > 512 || url.chars().any(char::is_control))
    {
        return Err(AppError::BadRequest("开票后必须提供有效 HTTPS 发票链接".into()));
    }
    let reason = req.reason.as_deref().map(str::trim).filter(|reason| !reason.is_empty());
    if req.decision == "reject"
        && reason.is_none_or(|value| value.chars().count() > 255 || value.chars().any(char::is_control))
    {
        return Err(AppError::BadRequest("拒绝发票申请必须填写不超过 255 字的原因".into()));
    }

    let mut tx = st.db.pool().begin().await?;
    let row = if let Ok(id) = invoice_id.parse::<u64>() {
        sqlx::query("SELECT id, review_status, reviewed_by, reject_reason, invoice_url FROM invoice_request WHERE id = ? AND deleted_at IS NULL FOR UPDATE")
            .bind(id).fetch_optional(&mut *tx).await?
    } else {
        sqlx::query("SELECT id, review_status, reviewed_by, reject_reason, invoice_url FROM invoice_request WHERE invoice_no = ? AND deleted_at IS NULL FOR UPDATE")
            .bind(&invoice_id).fetch_optional(&mut *tx).await?
    }.ok_or_else(|| AppError::NotFound("invoice".into()))?;
    let current_status: String = sqlx::Row::try_get(&row, "review_status")?;
    let target_status = if req.decision == "reject" { "rejected" } else { "issued" };
    if current_status != "pending" {
        let same_decision = current_status == target_status
            && sqlx::Row::try_get::<Option<u64>, _>(&row, "reviewed_by")? == Some(req.actor_id)
            && sqlx::Row::try_get::<Option<String>, _>(&row, "reject_reason")?.as_deref() == reason
            && sqlx::Row::try_get::<Option<String>, _>(&row, "invoice_url")?.as_deref() == req.invoice_url.as_deref();
        if same_decision {
            tx.commit().await?;
            return Ok(Json(common_error::ApiEnvelope::ok(json!({"reviewed": true, "review_status": target_status}), common_error::current_request_id())));
        }
        return Err(AppError::Conflict("该发票申请已审核，不能更改审核结果".into()));
    }
    let id: u64 = sqlx::Row::try_get(&row, "id")?;
    sqlx::query("UPDATE invoice_request SET review_status = ?, reviewed_by = ?, reviewed_at = UTC_TIMESTAMP(3), reject_reason = ?, invoice_url = ? WHERE id = ? AND review_status = 'pending'")
        .bind(target_status).bind(req.actor_id).bind(reason).bind(req.invoice_url.as_deref()).bind(id)
        .execute(&mut *tx).await?;
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"reviewed": true, "review_status": target_status}), common_error::current_request_id())))
}
