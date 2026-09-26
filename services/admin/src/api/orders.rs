//! 订单查询(只读)
//!
//! 跨服务调用走 [`crate::clients::ServiceClient`];路径取 `api_contracts::paths::*`。
//! 禁止在 handler 里拼 URL 或 `json!{}` 拼响应。

use crate::AppState;
use api_contracts::{paths as p, OrderListResponse};
use axum::{
    extract::{Path, State},
    Json,
};
use common_auth::AdminClaims;
use common_error::AppResult;
use serde_json::Value;

pub async fn list(
    State(st): State<AppState>,
    _c: AdminClaims,
) -> AppResult<Json<common_error::ApiEnvelope<OrderListResponse>>> {
    let cli = crate::clients::ServiceClient::new(st.http.clone(), st.service_token.clone());
    let resp: OrderListResponse = cli
        .get_typed(st.cfg.service_urls.user.as_deref(), p::USER_INTERNAL_ORDER_DETAIL)
        .await
        .unwrap_or_else(|_| OrderListResponse::empty());
    Ok(Json(common_error::ApiEnvelope::ok(resp, common_error::current_request_id())))
}

pub async fn get(
    State(st): State<AppState>,
    _c: AdminClaims,
    Path(_id): Path<String>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let cli = crate::clients::ServiceClient::new(st.http.clone(), st.service_token.clone());
    let v: Value = cli
        .get_typed(st.cfg.service_urls.user.as_deref(), p::USER_INTERNAL_ORDER_DETAIL)
        .await?;
    Ok(Json(common_error::ApiEnvelope::ok(v, common_error::current_request_id())))
}