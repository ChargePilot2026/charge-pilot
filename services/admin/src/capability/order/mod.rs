//! order 域 —— 充电订单读(数据归 user / billing,admin 不持有订单表)
//!
//! 本域没有可抽的纯逻辑层:筛选条件来自 `api_contracts::orders::OrderQuery`
//! (契约自带 `valid()`),其余全是跨服务调用与行列映射。故不建 `domain.rs`。

pub mod repository_sql;

use crate::AppState;
use crate::capability::identity::ActiveAdmin;
use api_contracts::{
    orders::{OrderDetail, OrderPage, OrderQuery},
    paths,
};
use axum::{
    extract::{Path, Query, State},
    Json,
};
use common_error::{ApiEnvelope, AppResult};
use common_http::internal::ApiClient;

/// 订单读权限:查当前授权,使被撤销的权限在 JWT 过期前即失效。
async fn authorize(state: &AppState, claims: &ActiveAdmin) -> AppResult<()> {
    crate::capability::identity::require_permission(state, claims, "order.read").await
}

pub async fn timeline(
    State(state): State<AppState>,
    claims: ActiveAdmin,
    Path(id): Path<u64>,
) -> AppResult<Json<ApiEnvelope<api_contracts::orders::OrderTimeline>>> {
    authorize(&state, &claims).await?;
    let result = ApiClient::new(state.http.clone(), state.service_token.clone())
        .get(
            state.cfg.service_urls.user.as_deref(),
            &api_contracts::fill_path(paths::USER_INTERNAL_ORDER_TIMELINE, "order_id", &id.to_string()),
            &(),
        )
        .await?;
    Ok(Json(ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn list(
    State(state): State<AppState>,
    claims: ActiveAdmin,
    Query(mut query): Query<OrderQuery>,
) -> AppResult<Json<ApiEnvelope<OrderPage>>> {
    authorize(&state, &claims).await?;
    // Never accept the internal device scope directly from a browser.
    query.device_ids = None;
    if let Some(station_id) = query.station_id.take() {
        let devices = repository_sql::device_ids_for_station(&state, station_id).await?;
        query.device_ids = Some(devices.join(","));
    }
    let client = ApiClient::new(state.http.clone(), state.service_token.clone());
    let mut page: OrderPage = client
        .get(state.cfg.service_urls.user.as_deref(), paths::USER_INTERNAL_ORDERS, &query)
        .await?;
    repository_sql::stations(&state, &mut page.items).await?;
    Ok(Json(ApiEnvelope::ok(page, common_error::current_request_id())))
}

pub async fn get(
    State(state): State<AppState>,
    claims: ActiveAdmin,
    Path(id): Path<u64>,
) -> AppResult<Json<ApiEnvelope<OrderDetail>>> {
    authorize(&state, &claims).await?;
    let path = api_contracts::fill_path(paths::USER_INTERNAL_ORDER_DETAIL, "order_id", &id.to_string());
    let client = ApiClient::new(state.http.clone(), state.service_token.clone());
    let mut detail: OrderDetail = client
        .get(state.cfg.service_urls.user.as_deref(), &path, &())
        .await?;
    repository_sql::stations(&state, std::slice::from_mut(&mut detail.order)).await?;
    let billing = client
        .get::<api_contracts::orders::OrderBilling, _>(
            state.cfg.service_urls.billing.as_deref(),
            &api_contracts::fill_path(paths::BILLING_ORDER_SUMMARY, "order_id", &id.to_string()),
            &(),
        )
        .await?;
    if billing.calculation_no.is_some() {
        detail.order.electric_fee_cents = billing.electric_cents;
        detail.order.service_fee_cents = billing.service_cents;
        detail.order.total_fee_cents = billing.total_cents;
    }
    detail.billing = Some(billing);
    detail.refund_applicant_id =
        if crate::capability::finance::refund_applicant_eligible(&state, claims.admin_user_id, &detail.order.status, detail.paid_cents).await? {
            Some(claims.admin_user_id.to_string())
        } else {
            None
        };
    Ok(Json(ApiEnvelope::ok(detail, common_error::current_request_id())))
}
