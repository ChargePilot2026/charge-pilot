//! 钱包:余额查询 / 充值 / 流水 / 退款申请

use crate::AppState;
use axum::{extract::State, Json};
use common_db::IdGen;
use common_error::{AppError, AppResult};

use serde::Deserialize;
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
    common_wechat::validate_pay_config(wechat)?;
    let id_gen = IdGen::new("PAY");
    let pay_order_no = id_gen.next();
    let now_month = chrono::Utc::now().format("%Y-%m-01").to_string();

    let pay_order_id = sqlx::query(
        "INSERT INTO payment_order (order_no, biz_type, biz_id, user_id, pay_method, total_cents, status, created_month, expired_at, client_ip)
         VALUES (?, 'wallet_recharge', 0, ?, 'wechat', ?, 'initiated', ?, DATE_ADD(NOW(3), INTERVAL 5 MINUTE), ?)"
    )
    .bind(&pay_order_no)
    .bind(claims.user_id)
    .bind(req.amount_cents)
    .bind(&now_month)
    .bind(req.client_ip.as_deref())
    .execute(st.db.pool())
    .await?.last_insert_id();

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
    let pay_sign = common_wechat::sign_jsapi_pay(wechat, &resp.prepay_id)?;

    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "pay_order_id": pay_order_id,
        "pay_order_no": pay_order_no,
        "payment_params": pay_sign,
    }), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct WalletRefundReq {
    pub request_id:String,
    pub amount_cents: i64,
    pub reason: Option<String>,
}

pub async fn refund(
    State(st): State<AppState>,
    claims: common_auth::UserClaims,
    Json(req): Json<WalletRefundReq>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let mut tx=st.db.pool().begin().await?;
    let response=crate::wallet_refund::apply(&mut tx,claims.user_id,&req).await?;
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(response,common_error::current_request_id())))
}