//! 钱包:余额查询 / 充值 / 流水 / 退款申请

use crate::AppState;
use axum::{extract::State, Json};
use common_db::IdGen;
use common_error::{AppError, AppResult};
use common_redis::StreamEnvelope;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

#[derive(Debug, Deserialize)]
pub struct RechargeReq {
    pub amount_cents: i64,
    pub client_ip: Option<String>,
}

pub async fn recharge(
    State(st): State<AppState>,
    claims: common_auth::UserClaims,
    Json(req): Json<RechargeReq>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    if req.amount_cents <= 0 || req.amount_cents > 1_000_000_00 {
        return Err(AppError::BadRequest("amount out of range".into()));
    }
    let wechat = st.cfg.wechat.as_ref()
        .ok_or_else(|| AppError::Config("wechat missing".into()))?;
    let id_gen = IdGen::new("PAY");
    let pay_order_no = id_gen.next();
    let now_month = chrono::Utc::now().format("%Y-%m-01").to_string();

    let pay_order_id: u64 = sqlx::query_scalar(
        "INSERT INTO payment_order (order_no, biz_type, biz_id, user_id, pay_method, total_cents, status, created_month, expired_at, client_ip)
         VALUES (?, 'wallet_recharge', 0, ?, 'wechat', ?, 'initiated', ?, DATE_ADD(NOW(3), INTERVAL 5 MINUTE), ?)"
    )
    .bind(&pay_order_no)
    .bind(claims.user_id)
    .bind(req.amount_cents)
    .bind(&now_month)
    .bind(req.client_ip.as_deref())
    .fetch_one(st.db.pool())
    .await?;

    let jsapi_req = common_wechat::JsapiOrderReq {
        appid: wechat.appid.clone(),
        mchid: wechat.mch_id.clone(),
        description: format!("钱包充值 {pay_order_no}"),
        out_trade_no: pay_order_no.clone(),
        time_expire: (chrono::Utc::now() + chrono::Duration::minutes(5)).to_rfc3339(),
        attach: Some(serde_json::to_string(&json!({"pay_order_id": pay_order_id, "type": "wallet_recharge"})).unwrap_or_default()),
        notify_url: wechat.notify_url.clone(),
        amount: common_wechat::JsapiAmount { total: req.amount_cents as i32, currency: "CNY".into() },
        payer: common_wechat::JsapiPayer { openid: claims.sub.clone() },
    };
    let resp = common_wechat::jsapi_create_order(&st.http, wechat, &jsapi_req).await?;
    let pay_sign = common_wechat::sign_jsapi_pay(wechat, &resp.prepay_id);

    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "pay_order_id": pay_order_id,
        "pay_order_no": pay_order_no,
        "payment_params": pay_sign,
    }), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct WalletRefundReq {
    pub amount_cents: i64,
    pub reason: Option<String>,
}

pub async fn refund(
    State(st): State<AppState>,
    claims: common_auth::UserClaims,
    Json(req): Json<WalletRefundReq>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    if req.amount_cents <= 0 {
        return Err(AppError::BadRequest("amount must be positive".into()));
    }
    let mut tx = st.db.pool().begin().await?;
    let balance: i64 = sqlx::query_scalar(
        "SELECT balance_cents FROM wallet_account WHERE user_id = ? FOR UPDATE"
    )
    .bind(claims.user_id)
    .fetch_optional(&mut *tx)
    .await
    .ok()
    .flatten()
    .unwrap_or(0);
    if balance < req.amount_cents {
        return Err(AppError::InsufficientBalance);
    }
    sqlx::query(
        "UPDATE wallet_account SET balance_cents = balance_cents - ?, version = version + 1 WHERE user_id = ?"
    )
    .bind(req.amount_cents)
    .bind(claims.user_id)
    .execute(&mut *tx).await?;

    let now_month = chrono::Utc::now().format("%Y-%m-01").to_string();
    let txn_no = IdGen::new("WTX").next();
    sqlx::query(
        "INSERT INTO wallet_txn (txn_no, user_id, wallet_account_id, direction, amount_cents, balance_after_cents, biz_type, note, created_month)
         VALUES (?, ?, (SELECT id FROM wallet_account WHERE user_id=?), 'out', ?, ?, 'refund', ?, ?)"
    )
    .bind(&txn_no)
    .bind(claims.user_id)
    .bind(claims.user_id)
    .bind(req.amount_cents)
    .bind(balance - req.amount_cents)
    .bind(req.reason.as_deref())
    .bind(&now_month)
    .execute(&mut *tx).await?;

    let refund_no = crate::refund::create_refund_record(
        &st, 0, claims.user_id, req.amount_cents, "wallet_recharge", 0, req.reason.as_deref()
    ).await?;

    tx.commit().await?;

    let env = StreamEnvelope::new("refund_required", "user", json!({
        "refund_no": refund_no,
        "amount_cents": req.amount_cents,
        "user_id": claims.user_id,
    }));
    let _ = st.redis_stream.xadd_envelope(common_redis::streams::REFUND_REQUIRED, &env).await;

    Ok(Json(common_error::ApiEnvelope::ok(json!({"refund_no": refund_no, "txn_no": txn_no}), common_error::current_request_id())))
}
