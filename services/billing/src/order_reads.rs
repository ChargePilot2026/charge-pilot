//! Billing-owned order snapshot, read consistently without cross-schema queries.
use crate::AppState;
use api_contracts::orders::{OrderBilling, OrderSettlement, OrderSettlementParty};
use axum::{
    extract::{Path, State},
    Json,
};
use common_error::{ApiEnvelope, AppResult};
use sqlx::Row;

pub async fn summary(
    State(state): State<AppState>,
    Path(id): Path<u64>,
) -> AppResult<Json<ApiEnvelope<OrderBilling>>> {
    let mut tx = state.db.pool().begin().await?;
    let fee = sqlx::query("SELECT id, calculation_no, electric_cents, service_cents, total_cents FROM fee_calculation WHERE charge_order_id = ? ORDER BY created_at DESC, id DESC LIMIT 1")
        .bind(id).fetch_optional(&mut *tx).await?;
    let mut result = OrderBilling {
        calculation_no: None,
        electric_cents: None,
        service_cents: None,
        total_cents: None,
        settlements: vec![],
    };
    if let Some(fee) = fee {
        result.calculation_no = Some(fee.try_get("calculation_no")?);
        result.electric_cents = Some(fee.try_get("electric_cents")?);
        result.service_cents = Some(fee.try_get("service_cents")?);
        result.total_cents = Some(fee.try_get("total_cents")?);
        let rows = sqlx::query("SELECT id, settlement_no, mode, status, total_cents, split_pool_cents FROM settlement WHERE fee_calculation_id = ? ORDER BY created_at, id")
            .bind(fee.try_get::<u64, _>("id")?).fetch_all(&mut *tx).await?;
        for row in rows {
            let settlement_id: u64 = row.try_get("id")?;
            let party_rows = sqlx::query("SELECT party_id, party_code, party_name, ratio_bp, amount_cents, status FROM settlement_party_amount WHERE settlement_id = ? ORDER BY id")
                .bind(settlement_id).fetch_all(&mut *tx).await?;
            let mut parties = Vec::with_capacity(party_rows.len());
            for party in party_rows {
                parties.push(OrderSettlementParty {
                    party_id: party.try_get("party_id")?,
                    party_code: party.try_get("party_code")?,
                    party_name: party.try_get("party_name")?,
                    ratio_bp: party.try_get("ratio_bp")?,
                    amount_cents: party.try_get("amount_cents")?,
                    status: party.try_get("status")?,
                });
            }
            result.settlements.push(OrderSettlement {
                settlement_id,
                settlement_no: row.try_get("settlement_no")?,
                mode: row.try_get("mode")?,
                status: row.try_get("status")?,
                total_cents: row.try_get("total_cents")?,
                split_pool_cents: row.try_get("split_pool_cents")?,
                parties,
            });
        }
    }
    tx.commit().await?;
    Ok(Json(ApiEnvelope::ok(
        result,
        common_error::current_request_id(),
    )))
}
