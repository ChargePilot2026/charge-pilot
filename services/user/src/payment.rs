//! 微信支付下单 + 回调处理 + 启动结果回写
//!
//! 流程(技术规格 § 2.1):
//!   - 微信支付回调 → 验签 → 幂等检查 → 事务写 payment_order.paid + charge_order.paid
//!     + 同事务写 event_outbox(charge_started_stream)→ 发布器 XADD
//!   - gateway 消费 charge_started_stream → 启动设备 → ACK → 回调本服务
//!     /internal/charge-orders/:order_id/start-result → 写 active_port_charge

use crate::AppState;
use axum::{extract::{Path, State}, Json};
use common_db::IdGen;
use common_error::{AppError, AppResult};
use common_redis::{PortLock, StreamEnvelope};
use serde::Deserialize;
use serde_json::{json, Value};

#[derive(Debug, Deserialize)]
pub struct WechatCallbackEnvelope {
    pub id: String,
    pub create_time: String,
    pub resource_type: String,
    pub event_type: String,
    pub summary: String,
    pub resource: WechatCallbackResource,
}

#[derive(Debug, Deserialize)]
pub struct WechatCallbackResource {
    pub original_type: String,
    pub algorithm: String,
    pub ciphertext: String,
    pub associated_data: Option<String>,
    pub nonce: String,
}

/// 微信支付回调(技术规格 § 5.4 幂等保证)
pub async fn wechat_callback(
    State(st): State<AppState>,
    headers: axum::http::HeaderMap,
    axum::extract::Json(body): axum::extract::Json<Value>,
) -> AppResult<Json<common_error::ApiEnvelope<serde_json::Value>>> {
    let timestamp = headers.get("wechatpay-timestamp").and_then(|v| v.to_str().ok()).unwrap_or("");
    let nonce = headers.get("wechatpay-nonce").and_then(|v| v.to_str().ok()).unwrap_or("");
    let signature = headers.get("wechatpay-signature").and_then(|v| v.to_str().ok()).unwrap_or("");
    let body_str = body.to_string();

    let wechat = st.cfg.wechat.as_ref()
        .ok_or_else(|| AppError::Config("wechat missing".into()))?;
    if !common_wechat::verify_callback_signature(wechat, &body_str, timestamp, nonce, signature) {
        return Err(AppError::Unauthorized("wechat signature invalid".into()));
    }

    // 简化:实际场景需 AES-256-GCM 解密 resource.ciphertext 拿 transaction_id
    // 本期把 transaction_id 暂用 attach 或 order_no 关联
    let callback: WechatCallbackEnvelope = serde_json::from_value(body.clone())?;
    let _ = callback; // 占位

    // 提取支付订单号(本系统约定: attach 含 order_id)
    let attach = body.get("attach").and_then(|v| v.as_str()).unwrap_or("{}");
    let attach_v: Value = serde_json::from_str(attach).unwrap_or(json!({}));
    let _charge_order_id = attach_v.get("order_id").and_then(|v| v.as_u64());

    // 取支付订单号:实际是 out_trade_no
    let pay_order_no = body.get("out_trade_no").and_then(|v| v.as_str()).unwrap_or("").to_string();
    let wechat_txn_id = body.get("transaction_id").and_then(|v| v.as_str()).unwrap_or("").to_string();

    // 1) 幂等检查
    let exists: Option<u64> = sqlx::query_scalar(
        "SELECT id FROM payment_callback_idempotent WHERE wechat_transaction_id = ?"
    )
    .bind(&wechat_txn_id)
    .fetch_optional(st.db.pool())
    .await?;
    if exists.is_some() {
        return Ok(Json(common_error::ApiEnvelope::ok(json!({"return_code": "SUCCESS"}), common_error::current_request_id())));
    }

    // 2) 事务更新
    let mut tx = st.db.pool().begin().await?;
    sqlx::query("INSERT IGNORE INTO payment_callback_idempotent (wechat_transaction_id) VALUES (?)")
        .bind(&wechat_txn_id)
        .execute(&mut *tx).await?;

    // 找 payment_order + charge_order
    let order: Option<(u64, u64, String)> = sqlx::query_as(
        "SELECT id, biz_id, status FROM payment_order WHERE order_no = ? LIMIT 1 FOR UPDATE"
    )
    .bind(&pay_order_no)
    .fetch_optional(&mut *tx)
    .await?;
    let (pay_id, charge_id, _status) = order.ok_or_else(|| AppError::NotFound("payment order".into()))?;

    sqlx::query(
        "UPDATE payment_order SET status='paid', wechat_transaction_id=?, paid_at=NOW(3)
         WHERE id=?"
    )
    .bind(&wechat_txn_id)
    .bind(pay_id)
    .execute(&mut *tx).await?;

    sqlx::query(
        "UPDATE charge_order SET status='paid' WHERE id=? AND status='pending_payment'"
    )
    .bind(charge_id)
    .execute(&mut *tx).await?;

    // 3) 写 event_outbox:charge_started_stream
    let event_id = IdGen::new("EVT").next();
    let envelope = StreamEnvelope::new("charge_started", "user", json!({
        "charge_order_id": charge_id,
        "payment_order_id": pay_id,
        "order_no": pay_order_no,
    }));
    sqlx::query(
        "INSERT INTO event_outbox (event_id, stream, envelope_json, status)
         VALUES (?, ?, ?, 'pending')"
    )
    .bind(&event_id)
    .bind(common_redis::streams::CHARGE_STARTED)
    .bind(serde_json::to_string(&envelope)?)
    .execute(&mut *tx).await?;

    tx.commit().await?;

    // 4) 立即发布 + outbox 兜底:实际靠后台 worker 周期重试未发布事件
    let _ = st.redis_stream.xadd_envelope(common_redis::streams::CHARGE_STARTED, &envelope).await;
    let _ = sqlx::query("UPDATE event_outbox SET status='published', published_at=NOW(3) WHERE event_id = ?")
        .bind(&event_id)
        .execute(st.db.pool())
        .await;

    Ok(Json(common_error::ApiEnvelope::ok(json!({"return_code": "SUCCESS"}), common_error::current_request_id())))
}

/// gateway 调: 启动结果回写(技术规格 § 2.1 步骤 9-10)
#[derive(Debug, Deserialize)]
pub struct StartResultReq {
    pub order_no: String,
    pub success: bool,
    pub error: Option<String>,
}

pub async fn start_result(
    State(st): State<AppState>,
    Path(order_id): Path<String>,
    Json(req): Json<StartResultReq>,
) -> AppResult<Json<common_error::ApiEnvelope<serde_json::Value>>> {
    let mut tx = st.db.pool().begin().await?;

    let order: Option<(u64, u64, String, String, u8)> = sqlx::query_as(
        "SELECT id, user_id, status, device_id, port_no FROM charge_order WHERE order_no = ? LIMIT 1 FOR UPDATE"
    )
    .bind(&order_id)
    .fetch_optional(&mut *tx)
    .await?;
    let (cid, user_id, _status, device_id, port_no) = order.ok_or_else(|| AppError::NotFound("order".into()))?;

    if req.success {
        sqlx::query(
            "UPDATE charge_order SET status='charging', started_at=NOW(3) WHERE id=?"
        )
        .bind(cid)
        .execute(&mut *tx).await?;
        // active_port_charge 跨月唯一性兜底
        let port_id: u64 = sqlx::query_scalar("SELECT id FROM device_port WHERE device_id = ? AND port_no = ? LIMIT 1")
            .bind(&device_id)
            .bind(port_no)
            .fetch_optional(&mut *tx)
            .await
            .ok()
            .flatten()
            .unwrap_or(0);
        sqlx::query(
            "INSERT INTO active_port_charge (port_id, device_id, port_no, charge_order_id, user_id, started_at)
             VALUES (?, ?, ?, ?, ?, NOW(3))
             ON DUPLICATE KEY UPDATE charge_order_id=VALUES(charge_order_id), user_id=VALUES(user_id), started_at=VALUES(started_at), ended_at=NULL"
        )
        .bind(port_id)
        .bind(&device_id)
        .bind(port_no)
        .bind(cid)
        .bind(user_id)
        .execute(&mut *tx).await?;
        // 释放逻辑锁
        let lock = PortLock::new(st.redis_cache.clone());
        let _ = lock.release_if_match(&order_id, &format!("{user_id}:{order_id}")).await;
    } else {
        sqlx::query(
            "UPDATE charge_order SET status='failed', failure_reason=?, ended_at=NOW(3) WHERE id=?"
        )
        .bind(req.error.as_deref())
        .bind(cid)
        .execute(&mut *tx).await?;
    }

    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"ok": true}), common_error::current_request_id())))
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
