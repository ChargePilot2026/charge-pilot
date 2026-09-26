//! Admin reads order lifecycle data from its owning user service.
use crate::AppState;
use api_contracts::{
    orders::{OrderDetail, OrderPage, OrderQuery, OrderSummary},
    paths,
};
use axum::{
    extract::{Path, Query, State},
    Json,
};
use common_auth::AdminClaims;
use common_error::{ApiEnvelope, AppError, AppResult};
use common_http::internal::ApiClient;
use sqlx::{MySql, QueryBuilder};
use std::collections::HashMap;

async fn authorize(state: &AppState, claims: &AdminClaims) -> AppResult<()> {
    // Read current grants so revoked permissions do not remain valid until JWT expiry.
    let allowed: i64 = sqlx::query_scalar(
        "SELECT COUNT(*) FROM admin_user_role a JOIN role r ON r.id = a.role_id
         JOIN role_permission rp ON rp.role_id = r.id JOIN permission p ON p.id = rp.permission_id
         WHERE a.id = ? AND a.status = 'active' AND a.deleted_at IS NULL
         AND r.deleted_at IS NULL AND p.code = 'order.read'",
    )
    .bind(claims.admin_user_id)
    .fetch_one(state.db.pool())
    .await?;
    if allowed == 0 {
        return Err(AppError::Forbidden("缺少 order.read 权限".into()));
    }
    Ok(())
}

pub async fn timeline(
    State(state): State<AppState>,
    claims: AdminClaims,
    Path(id): Path<u64>,
) -> AppResult<Json<ApiEnvelope<api_contracts::orders::OrderTimeline>>> {
    authorize(&state, &claims).await?;
    let client = ApiClient::new(state.http.clone(), state.service_token.clone());
    let result = client
        .get(
            state.cfg.service_urls.user.as_deref(),
            &paths::USER_INTERNAL_ORDER_TIMELINE.replace(":order_id", &id.to_string()),
            &(),
        )
        .await?;
    Ok(Json(ApiEnvelope::ok(
        result,
        common_error::current_request_id(),
    )))
}

async fn stations(state: &AppState, items: &mut [OrderSummary]) -> AppResult<()> {
    if items.is_empty() {
        return Ok(());
    }
    let mut query = QueryBuilder::<MySql>::new(
        "SELECT d.device_id, s.id, s.name FROM device_meta d JOIN station s ON s.id = d.station_id
         WHERE d.deleted_at IS NULL AND s.deleted_at IS NULL AND d.device_id IN (",
    );
    let mut ids = query.separated(",");
    for item in items.iter() {
        ids.push_bind(&item.device_id);
    }
    ids.push_unseparated(")");
    let rows: Vec<(String, u64, String)> =
        query.build_query_as().fetch_all(state.db.pool()).await?;
    let names: HashMap<_, _> = rows
        .into_iter()
        .map(|(device, id, name)| (device, (id, name)))
        .collect();
    for item in items {
        if let Some((id, name)) = names.get(&item.device_id) {
            item.station_id = Some(*id);
            item.station_name = Some(name.clone());
        }
    }
    Ok(())
}

pub async fn list(
    State(state): State<AppState>,
    claims: AdminClaims,
    Query(mut query): Query<OrderQuery>,
) -> AppResult<Json<ApiEnvelope<OrderPage>>> {
    authorize(&state, &claims).await?;
    // Never accept the internal device scope directly from a browser.
    query.device_ids = None;
    if let Some(station_id) = query.station_id.take() {
        let devices: Vec<String> = sqlx::query_scalar(
            "SELECT d.device_id FROM device_meta d JOIN station s ON s.id = d.station_id
             WHERE d.station_id = ? AND d.deleted_at IS NULL AND s.deleted_at IS NULL",
        )
        .bind(station_id)
        .fetch_all(state.db.pool())
        .await?;
        query.device_ids = Some(devices.join(","));
    }
    let client = ApiClient::new(state.http.clone(), state.service_token.clone());
    let mut page: OrderPage = client
        .get(
            state.cfg.service_urls.user.as_deref(),
            paths::USER_INTERNAL_ORDERS,
            &query,
        )
        .await?;
    stations(&state, &mut page.items).await?;
    Ok(Json(ApiEnvelope::ok(
        page,
        common_error::current_request_id(),
    )))
}

pub async fn get(
    State(state): State<AppState>,
    claims: AdminClaims,
    Path(id): Path<u64>,
) -> AppResult<Json<ApiEnvelope<OrderDetail>>> {
    authorize(&state, &claims).await?;
    let path = paths::USER_INTERNAL_ORDER_DETAIL.replace(":order_id", &id.to_string());
    let client = ApiClient::new(state.http.clone(), state.service_token.clone());
    let mut detail: OrderDetail = client
        .get(state.cfg.service_urls.user.as_deref(), &path, &())
        .await?;
    stations(&state, std::slice::from_mut(&mut detail.order)).await?;
    let billing = client
        .get::<api_contracts::orders::OrderBilling, _>(
            state.cfg.service_urls.billing.as_deref(),
            &paths::BILLING_ORDER_SUMMARY.replace(":order_id", &id.to_string()),
            &(),
        )
        .await?;
    if billing.calculation_no.is_some() {
        detail.order.electric_fee_cents = billing.electric_cents;
        detail.order.service_fee_cents = billing.service_cents;
        detail.order.total_fee_cents = billing.total_cents;
    }
    detail.billing = Some(billing);
    Ok(Json(ApiEnvelope::ok(
        detail,
        common_error::current_request_id(),
    )))
}
