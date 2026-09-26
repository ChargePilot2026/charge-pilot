//! billing 内部 API — 所有 handler 严格使用 [`crate::api_types`] 中定义的 DTO。
//!
//! 不允许:
//!   - 直接拼字符串路径(必须引用 `paths::*`)
//!   - 在 handler 内构造 `serde_json::json!{}` 响应(必须用 DTO + `Json<Envelope<T>>`)

use crate::{api_types as t, engine, split};
use crate::AppState;
use axum::{
    extract::{Path, State},
    Json,
};
use common_db::IdGen;
use common_error::{AppError, AppResult, ApiEnvelope};
use serde::de::DeserializeOwned;
use serde::Serialize;
use serde_json::Value;

pub async fn health() -> &'static str { "ok" }

// ===================== handlers =====================

pub async fn quote(
    State(_st): State<AppState>,
    Json(req): Json<t::QuoteRequest>,
) -> AppResult<Json<ApiEnvelope<t::QuoteResponse>>> {
    let rule = engine::PricingRule::default_default();
    let charged_kwh = (req.estimated_minutes as f64 * 0.2).max(0.4);
    let peak_kwh = charged_kwh * 0.6;
    let off_kwh = charged_kwh - peak_kwh;
    let r = engine::calculate_fee_compat(&rule, charged_kwh, req.estimated_minutes, peak_kwh, off_kwh);
    Ok(Json(ok_envelope(t::QuoteResponse {
        total_cents: r.total_cents,
        electric_cents: r.electric_cents,
        service_cents: r.service_cents,
    })))
}

pub async fn calculate(
    State(st): State<AppState>,
    Json(req): Json<t::CalculateRequest>,
) -> AppResult<Json<ApiEnvelope<t::CalculateResponse>>> {
    let rule = engine::PricingRule::default_default();
    let minutes = req.charged_seconds / 60;
    let result = engine::calculate_fee_compat(&rule, req.charged_kwh, minutes, req.peak_kwh, req.off_kwh);
    let now_month = chrono::Utc::now().format("%Y-%m-01").to_string();
    let calc_no = IdGen::new("FEE").next();
    let calc_id: u64 = sqlx::query_scalar(
        "INSERT INTO fee_calculation (calculation_no, order_no, charge_order_id, user_id, pricing_rule_id, charged_kwh, charged_seconds, peak_kwh, off_kwh, electric_cents, service_cents, total_cents, created_month)
         VALUES (?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
    )
    .bind(&calc_no).bind(&req.order_no).bind(req.charge_order_id).bind(req.pricing_rule_id)
    .bind(req.charged_kwh).bind(req.charged_seconds).bind(req.peak_kwh).bind(req.off_kwh)
    .bind(result.electric_cents).bind(result.service_cents).bind(result.total_cents)
    .bind(&now_month).fetch_one(st.db.pool()).await?;
    Ok(Json(ok_envelope(t::CalculateResponse {
        calculation_id: calc_id,
        calculation_no: calc_no,
        electric_cents: result.electric_cents,
        service_cents: result.service_cents,
        total_cents: result.total_cents,
    })))
}

pub async fn fee_breakdown(
    State(st): State<AppState>,
    Path(order_id): Path<String>,
) -> AppResult<Json<ApiEnvelope<t::FeeBreakdownResponse>>> {
    let r: Option<(String, i64, i64, i64)> = sqlx::query_as(
        "SELECT calculation_no, electric_cents, service_cents, total_cents FROM fee_calculation WHERE order_no = ? LIMIT 1"
    ).bind(&order_id).fetch_optional(st.db.pool()).await?;
    let (no, electric, service, total) = r.ok_or_else(|| AppError::NotFound("fee".into()))?;
    Ok(Json(ok_envelope(t::FeeBreakdownResponse {
        calculation_no: no,
        electric_cents: electric,
        service_cents: service,
        total_cents: total,
    })))
}

pub async fn split(
    State(st): State<AppState>,
    Json(req): Json<t::SplitRequest>,
) -> AppResult<Json<ApiEnvelope<t::SplitResponse>>> {
    let calc: Option<(i64, i64, String)> = sqlx::query_as(
        "SELECT total_cents, electric_cents + service_cents, order_no FROM fee_calculation WHERE id = ?"
    ).bind(req.fee_calculation_id).fetch_optional(st.db.pool()).await?;
    let (total_cents, _splittable, order_no) = calc.ok_or_else(|| AppError::NotFound("fee".into()))?;
    let parties: Vec<split::Party> = sqlx::query_as::<_, (u64, String, u32)>(
        "SELECT id, party_code, ratio_bp FROM split_party WHERE split_template_id = ?"
    ).bind(req.split_template_id).fetch_all(st.db.pool()).await
        .ok()
        .map(|rows| rows.into_iter().map(|(id, code, ratio)| split::Party { id, party_code: code, ratio_bp: ratio }).collect())
        .unwrap_or_default();
    let allocations = split::split_pool(total_cents, &parties);
    let settlement_no = IdGen::new("STL").next();
    let now_month = chrono::Utc::now().format("%Y-%m-01").to_string();
    let mut tx = st.db.pool().begin().await?;
    let settlement_id: u64 = sqlx::query_scalar(
        "INSERT INTO settlement (settlement_no, split_template_id, mode, fee_calculation_id, order_no, total_cents, split_pool_cents, status, created_month)
         VALUES (?, ?, 'mode_a', ?, ?, ?, ?, 'pending', ?)"
    )
    .bind(&settlement_no).bind(req.split_template_id).bind(req.fee_calculation_id).bind(&order_no)
    .bind(total_cents).bind(total_cents).bind(&now_month)
    .fetch_one(&mut *tx).await?;
    for (p, amt) in &allocations {
        sqlx::query(
            "INSERT INTO settlement_party_amount (settlement_id, party_id, party_code, party_name, ratio_bp, amount_cents)
             VALUES (?, ?, ?, '', ?, ?)"
        )
        .bind(settlement_id).bind(p.id).bind(&p.party_code).bind(p.ratio_bp).bind(amt)
        .execute(&mut *tx).await?;
    }
    tx.commit().await?;
    Ok(Json(ok_envelope(t::SplitResponse {
        settlement_id,
        settlement_no,
        allocations: allocations.iter().map(|(p, a)| t::SplitAllocation {
            party_id: p.id,
            party_code: p.party_code.clone(),
            amount_cents: *a,
        }).collect(),
    })))
}

pub async fn order_split(
    State(st): State<AppState>,
    Path(order_id): Path<String>,
) -> AppResult<Json<ApiEnvelope<t::OrderSplitResponse>>> {
    let r: Option<(u64, String, String, i64, i64)> = sqlx::query_as(
        "SELECT id, settlement_no, mode, total_cents, split_pool_cents FROM settlement WHERE order_no = ? LIMIT 1"
    ).bind(&order_id).fetch_optional(st.db.pool()).await?;
    let (id, no, mode, total, pool) = r.ok_or_else(|| AppError::NotFound("settlement".into()))?;
    let parties: Vec<t::OrderSplitParty> = sqlx::query("SELECT party_id, party_code, amount_cents, ratio_bp FROM settlement_party_amount WHERE settlement_id = ?")
        .bind(id).fetch_all(st.db.pool()).await
        .ok()
        .map(|rows| rows.iter().map(|r| t::OrderSplitParty {
            party_id: sqlx::Row::try_get::<u64, _>(r, "party_id").unwrap_or(0),
            party_code: sqlx::Row::try_get::<String, _>(r, "party_code").unwrap_or_default(),
            amount_cents: sqlx::Row::try_get::<i64, _>(r, "amount_cents").unwrap_or(0),
        }).collect()).unwrap_or_default();
    Ok(Json(ok_envelope(t::OrderSplitResponse {
        settlement_id: id,
        settlement_no: no,
        mode,
        total_cents: total,
        split_pool_cents: pool,
        parties,
    })))
}

pub async fn settlement_detail(
    State(st): State<AppState>,
    Path(id): Path<u64>,
) -> AppResult<Json<ApiEnvelope<t::SettlementDetailResponse>>> {
    let r: Option<(u64, String, i64, String)> = sqlx::query_as(
        "SELECT id, settlement_no, total_cents, status FROM settlement WHERE id = ?"
    ).bind(id).fetch_optional(st.db.pool()).await?;
    let (id_, no, total, status) = r.ok_or_else(|| AppError::NotFound("settlement".into()))?;
    Ok(Json(ok_envelope(t::SettlementDetailResponse {
        id: id_, settlement_no: no, total_cents: total, status,
    })))
}

pub async fn invoice_settle_detail(
    State(st): State<AppState>,
    Path(invoice_id): Path<u64>,
) -> AppResult<Json<ApiEnvelope<t::InvoiceSettleDetailResponse>>> {
    let r: Option<(i64, String)> = sqlx::query_as(
        "SELECT total_cents, review_status FROM invoice_request WHERE id = ?"
    ).bind(invoice_id).fetch_optional(st.db.pool()).await.ok().flatten();
    Ok(Json(ok_envelope(t::InvoiceSettleDetailResponse {
        invoice_id,
        found: r.is_some(),
        total_cents: r.map(|x| x.0),
    })))
}

pub async fn refund_calc(
    State(_st): State<AppState>,
    Json(req): Json<t::RefundCalcRequest>,
) -> AppResult<Json<ApiEnvelope<t::RefundCalcResponse>>> {
    Ok(Json(ok_envelope(t::RefundCalcResponse {
        refund_cents: req.actual_paid_cents,
        note: "原路返回".into(),
    })))
}

pub async fn withdraw_create(
    State(st): State<AppState>,
    Json(req): Json<t::WithdrawCreateRequest>,
) -> AppResult<Json<ApiEnvelope<t::WithdrawCreateResponse>>> {
    let no = IdGen::new("WDR").next();
    sqlx::query(
        "INSERT INTO withdraw_request (withdraw_no, party_id, party_code, amount_cents, status)
         VALUES (?, ?, '', ?, 'pending')"
    )
    .bind(&no).bind(req.party_id).bind(req.amount_cents)
    .execute(st.db.pool()).await?;
    Ok(Json(ok_envelope(t::WithdrawCreateResponse { withdraw_no: no })))
}

// ===================== helpers =====================

fn ok_envelope<T: Serialize>(data: T) -> ApiEnvelope<T> {
    ApiEnvelope::ok(data, common_error::current_request_id())
}

// 抑制未使用警告
#[allow(dead_code)]
fn _unused_type_anchor<T: DeserializeOwned>(_: T) -> Value { Value::Null }