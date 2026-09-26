//! 退款编排(技术规格 § 5.4 + § 7.6 退款 SOP)
//!
//! - claim: admin 消费 refund_required_stream → 幂等领取
//! - result: admin 调微信退款 → 写结果回 user
//! - detail: 退款详情查询

use crate::AppState;
use axum::{extract::{Path, State}, Json};
use common_db::IdGen;
use common_error::{AppError, AppResult};
use common_redis::StreamEnvelope;
use serde::Deserialize;
use serde_json::{json, Value};

#[derive(Debug, Deserialize)]
pub struct ClaimReq {
    pub event_id: String,
    pub refund_no: String,
    pub admin_user_id: u64,
}

pub async fn claim(
    State(st): State<AppState>,
    Json(req): Json<ClaimReq>,
) -> AppResult<Json<common_error::ApiEnvelope<serde_json::Value>>> {
    let mut tx = st.db.pool().begin().await?;
    let affected = sqlx::query(
        "UPDATE refund_record SET claimed_by=?, claimed_at=NOW(3), status='processing'
         WHERE refund_no=? AND status='pending' AND claimed_by IS NULL"
    )
    .bind(req.admin_user_id)
    .bind(&req.refund_no)
    .execute(&mut *tx)
    .await?
    .rows_affected();
    if affected == 0 {
        return Err(AppError::Conflict("refund already claimed or not pending".into()));
    }
    tx.commit().await?;

    // 拉详情
    let r: Option<(u64, i64, String, String)> = sqlx::query_as(
        "SELECT user_id, refund_cents, status, payment_order_id FROM refund_record WHERE refund_no=?"
    )
    .bind(&req.refund_no)
    .fetch_optional(st.db.pool())
    .await?;
    let (user_id, cents, status, pay_id) = r.ok_or_else(|| AppError::NotFound("refund".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "refund_no": req.refund_no,
        "user_id": user_id,
        "payment_order_id": pay_id,
        "refund_cents": cents,
        "status": status,
    }), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct ResultReq {
    pub refund_no: String,
    pub success: bool,
    pub wechat_refund_id: Option<String>,
    pub failure_reason: Option<String>,
}

pub async fn result(
    State(st): State<AppState>,
    Path(refund_id): Path<String>,
    Json(req): Json<ResultReq>,
) -> AppResult<Json<common_error::ApiEnvelope<serde_json::Value>>> {
    let mut tx = st.db.pool().begin().await?;
    if req.success {
        sqlx::query(
            "UPDATE refund_record SET status='success', wechat_refund_id=?, completed_at=NOW(3)
             WHERE refund_no=?"
        )
        .bind(req.wechat_refund_id.as_deref())
        .bind(&refund_id)
        .execute(&mut *tx).await?;
        // 同步 payment_order
        sqlx::query(
            "UPDATE payment_order po
             JOIN refund_record rr ON rr.payment_order_id = po.id
             SET po.refunded_cents = po.refunded_cents + rr.refund_cents,
                 po.status = CASE WHEN po.refunded_cents + rr.refund_cents >= po.total_cents
                                  THEN 'refunded' ELSE 'partial_refunded' END
             WHERE rr.refund_no=?"
        )
        .bind(&refund_id)
        .execute(&mut *tx).await?;
    } else {
        sqlx::query(
            "UPDATE refund_record SET status='failed', failure_reason=?
             WHERE refund_no=? AND retry_count < 3"
        )
        .bind(req.failure_reason.as_deref())
        .bind(&refund_id)
        .execute(&mut *tx).await?;
    }
    tx.commit().await?;

    // 发 comp_tx_stream 通知 billing 标记订单最终状态
    let env = StreamEnvelope::new("refund_completed", "user", json!({
        "refund_no": refund_id,
        "success": req.success,
    }));
    let _ = st.redis_stream.xadd_envelope(common_redis::streams::COMP_TX, &env).await;

    Ok(Json(common_error::ApiEnvelope::ok(json!({"ok": true}), common_error::current_request_id())))
}

pub async fn detail(
    State(st): State<AppState>,
    Path(refund_id): Path<String>,
) -> AppResult<Json<common_error::ApiEnvelope<serde_json::Value>>> {
    let r: Option<(u64, u64, i64, String, String, Option<String>)> = sqlx::query_as(
        "SELECT id, user_id, refund_cents, status, biz_type, wechat_refund_id
         FROM refund_record WHERE refund_no=? LIMIT 1"
    )
    .bind(&refund_id)
    .fetch_optional(st.db.pool())
    .await?;
    let r = r.ok_or_else(|| AppError::NotFound("refund".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "id": r.0,
        "user_id": r.1,
        "refund_cents": r.2,
        "status": r.3,
        "biz_type": r.4,
        "wechat_refund_id": r.5,
    }), common_error::current_request_id())))
}

/// 给 billing 用的辅助:写一条 pending refund_record
pub async fn create_refund_record(
    st: &AppState,
    pay_order_id: u64,
    user_id: u64,
    refund_cents: i64,
    biz_type: &str,
    biz_id: u64,
    reason: Option<&str>,
) -> AppResult<String> {
    let now_month = chrono::Utc::now().format("%Y-%m-01").to_string();
    let refund_no = IdGen::new("RFD").next();
    sqlx::query(
        "INSERT INTO refund_record (refund_no, payment_order_id, user_id, biz_type, biz_id, refund_cents, reason, status, created_month)
         VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?)"
    )
    .bind(&refund_no)
    .bind(pay_order_id)
    .bind(user_id)
    .bind(biz_type)
    .bind(biz_id)
    .bind(refund_cents)
    .bind(reason)
    .bind(&now_month)
    .execute(st.db.pool())
    .await?;
    Ok(refund_no)
}
