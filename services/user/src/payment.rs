//! 微信支付下单 + 回调处理 + 启动结果回写
//!
//! 流程(技术规格 § 2.1):
//!   - 微信支付回调 → 验签 → 幂等检查 → 事务写 payment_order.paid + charge_order.paid
//!     + 同事务写 event_outbox(charge_started_stream)→ 发布器 XADD
//!   - gateway 消费 charge_started_stream → 启动设备 → ACK → 回调本服务
//!     /internal/charge-orders/:order_id/start-result → 写 active_port_charge

use crate::AppState;
use axum::{extract::{Path, State}, Json};
use common_error::{AppError, AppResult};
use common_redis::PortLock;
use serde::Deserialize;
use serde_json::json;

/// Only acknowledge a verified payment after all database effects are durable.
pub async fn wechat_callback(
    State(st): State<AppState>,
    headers: axum::http::HeaderMap,
    body: axum::body::Bytes,
) -> AppResult<axum::http::StatusCode> {
    let cfg=st.cfg.wechat.as_ref().ok_or_else(||AppError::Config("wechat missing".into()))?;
    let receipt=common_wechat::decode_payment_notification(cfg,&headers,&body)?;
    crate::payment_receipt::process(st.db.pool(),&receipt).await?;
    Ok(axum::http::StatusCode::NO_CONTENT)
}

pub async fn start_result(
    State(st): State<AppState>,
    Path(order_id): Path<String>,
    Json(req): Json<api_contracts::StartResultRequest>,
) -> AppResult<Json<common_error::ApiEnvelope<serde_json::Value>>> {
    let (port_code,user_id)=crate::charge_start::process(st.db.pool(),&order_id,&req).await?;
    let lock=PortLock::new(st.redis_cache.clone());
    if lock.release_if_match(&port_code,&format!("{user_id}:{order_id}")).await.is_err() {
        tracing::warn!(order_no=%order_id,"start result committed; hold release will expire or retry");
    }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"ok":true}),common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct PaymentDetailResp {
    pub order_no: String,
    pub pay_method: String,
    pub total_cents: i64,
    pub paid_cents: i64,
    pub status: String,
}

pub async fn detail(
    State(st): State<AppState>,
    Path(payment_order_id): Path<String>,
) -> AppResult<Json<common_error::ApiEnvelope<serde_json::Value>>> {
    let r: Option<(u64, String, String, i64, i64, String)> = sqlx::query_as(
        "SELECT id, order_no, pay_method, total_cents, paid_cents, status FROM payment_order WHERE order_no = ? LIMIT 1"
    )
    .bind(&payment_order_id)
    .fetch_optional(st.db.pool())
    .await?;
    let r = r.ok_or_else(|| AppError::NotFound("payment".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "id": r.0,
        "order_no": r.1,
        "pay_method": r.2,
        "total_cents": r.3,
        "paid_cents": r.4,
        "status": r.5,
    }), common_error::current_request_id())))
}
