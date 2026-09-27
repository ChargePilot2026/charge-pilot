//! Billing-owned order snapshot, read consistently without cross-schema queries.
//!
//! P3:快照查询与事务已下沉到 [`crate::services::SettlementService`],
//! 本文件只留 handler 编排。响应 DTO(`OrderBilling` / `OrderSettlement` /
//! `OrderSettlementParty`)与 `ApiEnvelope` 包装未变。
use crate::AppState;
use api_contracts::orders::OrderBilling;
use axum::{
    extract::{Path, State},
    Json,
};
use common_error::{ApiEnvelope, AppResult};

pub async fn summary(
    State(state): State<AppState>,
    Path(id): Path<u64>,
) -> AppResult<Json<ApiEnvelope<OrderBilling>>> {
    let result = state.settlement.order_summary(id).await?;
    Ok(Json(ApiEnvelope::ok(
        result,
        common_error::current_request_id(),
    )))
}
