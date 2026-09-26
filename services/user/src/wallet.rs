//! 钱包:余额查询 / 充值 / 流水 / 退款申请

use crate::AppState;
use axum::{extract::State, Json};

use common_error::AppResult;

use serde::Deserialize;
use serde_json::Value;

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RechargeReq {
    pub request_id: String,
    pub amount_cents: i64,
    pub client_ip: Option<String>,
}

pub async fn recharge(
    State(st): State<AppState>,
    claims: common_auth::UserClaims,
    Json(req): Json<RechargeReq>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let response=crate::wallet_recharge::prepare(&st,claims.user_id,&claims.sub,&req).await?;
    Ok(Json(common_error::ApiEnvelope::ok(response,common_error::current_request_id())))
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
