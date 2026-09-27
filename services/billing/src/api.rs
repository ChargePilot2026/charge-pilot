//! billing 内部 API — 所有 handler 严格使用 [`crate::api_types`] 中定义的 DTO。
//!
//! 不允许:
//!   - 直接拼字符串路径(必须引用 `paths::*`)
//!   - 在 handler 内构造 `serde_json::json!{}` 响应(必须用 DTO + `Json<Envelope<T>>`)

use crate::{api_types as t, split};
use crate::AppState;
use axum::{
    extract::{Path, State},
    Json,
};
use common_db::IdGen;
use common_error::{AppError, AppResult, ApiEnvelope};
use serde::{de::DeserializeOwned, Deserialize, Serialize};
use serde_json::Value;

pub async fn health(State(st): State<AppState>) -> AppResult<&'static str> {
    st.db.ping().await?;
    st.redis_cache.ping().await?;
    st.redis_stream.ping().await?;
    Ok("ok")
}

// ===================== handlers =====================

pub async fn quote(
    State(st): State<AppState>,
    Json(req): Json<t::QuoteRequest>,
) -> AppResult<Json<ApiEnvelope<api_contracts::pricing::PriceQuote>>> {
    use chrono::Timelike;
    crate::quote_pricing::watt_hours(&req.estimated_kwh)?;
    if !(1..=1440).contains(&req.estimated_minutes) {return Err(AppError::BadRequest("预计时长必须为 1–1440 分钟".into()));}
    let client=common_http::internal::ApiClient::new(st.http.clone(),st.service_token.clone());
    let port:api_contracts::ScanPortDetail=client.post(st.cfg.service_urls.gateway.as_deref(),api_contracts::paths::GW_SCAN_PORT,&api_contracts::ScanPortRequest{port_id:req.port_id}).await?;
    if port.status!="idle" {return Err(AppError::PortOccupied);}
    let rule:api_contracts::pricing::DevicePricing=client.get(st.cfg.service_urls.admin.as_deref(),&api_contracts::paths::ADMIN_DEVICE_PRICING.replace(":id",&port.device_id),&()).await?;
    let local=chrono::Utc::now().with_timezone(&chrono::FixedOffset::east_opt(8*3600).expect("UTC+8"));
    let r=crate::quote_pricing::estimate(&rule,&req.estimated_kwh,req.estimated_minutes,(local.hour()*60+local.minute()) as usize)?;
    Ok(Json(ok_envelope(api_contracts::pricing::PriceQuote {
        amount:r,pricing:rule,estimated_kwh:req.estimated_kwh,estimated_minutes:req.estimated_minutes,
        quote_expires_at:(chrono::Utc::now()+chrono::Duration::minutes(5)).to_rfc3339(),
        estimation_basis:"按预计电量在充电时段内均匀分布估算，时区 Asia/Shanghai；实际费用按最终计量结算".into(),
    })))
}

pub async fn calculate(
    State(st): State<AppState>,
    Json(req): Json<t::CalculateRequest>,
) -> AppResult<Json<ApiEnvelope<t::CalculateResponse>>> {
    let result=crate::charge_fee::calculate(&st,req.charge_order_id,&req.order_no).await?;
    Ok(Json(ok_envelope(result)))
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
    let mut tx = st.db.pool().begin().await?;
    let calc: Option<(i64, i64, String)> = sqlx::query_as(
        "SELECT total_cents, service_cents, order_no FROM fee_calculation WHERE id = ? FOR UPDATE"
    ).bind(req.fee_calculation_id).fetch_optional(&mut *tx).await?;
    let (total_cents, service_cents, order_no) = calc.ok_or_else(|| AppError::NotFound("fee".into()))?;
    if total_cents < 0 || service_cents < 0 || service_cents > total_cents {
        return Err(AppError::ServiceUnavailable("计费金额无效，无法分账".into()));
    }
    let existing: Option<(u64, String)> = sqlx::query_as(
        "SELECT id, settlement_no FROM settlement WHERE fee_calculation_id = ? AND split_template_id = ? ORDER BY created_month, id LIMIT 1"
    ).bind(req.fee_calculation_id).bind(req.split_template_id).fetch_optional(&mut *tx).await?;
    if let Some((settlement_id, settlement_no)) = existing {
        let allocations = sqlx::query("SELECT party_id, party_code, amount_cents FROM settlement_party_amount WHERE settlement_id = ? ORDER BY id")
            .bind(settlement_id).fetch_all(&mut *tx).await?;
        let allocations: Vec<t::SplitAllocation> = allocations.iter().map(|row| -> AppResult<t::SplitAllocation> { Ok(t::SplitAllocation {
            party_id: sqlx::Row::try_get(row, "party_id")?,
            party_code: sqlx::Row::try_get(row, "party_code")?,
            amount_cents: sqlx::Row::try_get(row, "amount_cents")?,
        }) }).collect::<AppResult<Vec<_>>>()?;
        tx.commit().await?;
        return Ok(Json(ok_envelope(t::SplitResponse { settlement_id, settlement_no, allocations })));
    }
    let template_path = api_contracts::paths::ADMIN_INTERNAL_SPLIT_TEMPLATES_GET
        .replace(":id", &req.split_template_id.to_string());
    let template: SplitTemplateView = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone())
        .get(st.cfg.service_urls.admin.as_deref(), &template_path, &()).await?;
    if template.id != req.split_template_id || template.parties.is_empty()
        || template.parties.iter().map(|party| u64::from(party.ratio_bp)).sum::<u64>() != 10_000 {
        return Err(AppError::ServiceUnavailable("分账模板不存在或比例配置无效".into()));
    }
    let split_pool_cents = match template.mode.as_str() {
        "mode_a" => total_cents,
        "mode_b" => service_cents,
        _ => return Err(AppError::ServiceUnavailable("分账模板模式无效".into())),
    };
    let parties: Vec<split::Party> = template.parties.iter().map(|party| split::Party {
        id: party.id, party_code: party.party_code.clone(), ratio_bp: party.ratio_bp,
    }).collect();
    let mut allocations = split::split_pool(split_pool_cents, &parties);
    if template.mode == "mode_b" {
        let operator_share = total_cents - service_cents;
        let (_, amount) = allocations.iter_mut().find(|(party, _)| party.party_code == "operator")
            .ok_or_else(|| AppError::ServiceUnavailable("mode_b 模板必须包含 operator 参与方".into()))?;
        *amount = amount.checked_add(operator_share)
            .ok_or_else(|| AppError::ServiceUnavailable("运营商分账金额溢出".into()))?;
    }
    let settlement_no = IdGen::new("STL").next();
    let now_month = chrono::Utc::now().format("%Y-%m-01").to_string();
    let settlement_result = sqlx::query(
        "INSERT INTO settlement (settlement_no, split_template_id, mode, fee_calculation_id, order_no, total_cents, split_pool_cents, status, created_month)
         VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?)"
    )
    .bind(&settlement_no).bind(req.split_template_id).bind(&template.mode).bind(req.fee_calculation_id).bind(&order_no)
    .bind(total_cents).bind(split_pool_cents).bind(&now_month)
    .execute(&mut *tx).await?;
    let settlement_id = settlement_result.last_insert_id();
    for (p, amt) in &allocations {
        let party_name = template.parties.iter().find(|party| party.id == p.id)
            .map(|party| party.party_name.as_str()).ok_or_else(|| AppError::ServiceUnavailable("分账参与方不存在".into()))?;
        sqlx::query(
            "INSERT INTO settlement_party_amount (settlement_id, party_id, party_code, party_name, ratio_bp, amount_cents)
             VALUES (?, ?, ?, ?, ?, ?)"
        )
        .bind(settlement_id).bind(p.id).bind(&p.party_code).bind(party_name).bind(p.ratio_bp).bind(amt)
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
    let party_rows = sqlx::query("SELECT party_id, party_code, amount_cents, ratio_bp FROM settlement_party_amount WHERE settlement_id = ?")
        .bind(id).fetch_all(st.db.pool()).await
        ?;
    let parties: Vec<t::OrderSplitParty> = party_rows.iter().map(|row| -> AppResult<t::OrderSplitParty> { Ok(t::OrderSplitParty {
        party_id: sqlx::Row::try_get::<u64, _>(row, "party_id")?,
        party_code: sqlx::Row::try_get::<String, _>(row, "party_code")?,
        amount_cents: sqlx::Row::try_get::<i64, _>(row, "amount_cents")?,
    }) }).collect::<AppResult<Vec<_>>>()?;
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
    ).bind(invoice_id).fetch_optional(st.db.pool()).await?;
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
    let _ = (st, req);
    Err(AppError::ServiceUnavailable("提现功能尚未开放；银行收款账户与余额核对流程未接入".into()))
}

#[derive(Debug, Deserialize)]
struct SplitTemplateView {
    id: u64,
    mode: String,
    parties: Vec<SplitTemplateParty>,
}

#[derive(Debug, Deserialize)]
struct SplitTemplateParty {
    id: u64,
    party_code: String,
    party_name: String,
    ratio_bp: u32,
}

// ===================== helpers =====================

fn ok_envelope<T: Serialize>(data: T) -> ApiEnvelope<T> {
    ApiEnvelope::ok(data, common_error::current_request_id())
}

// 抑制未使用警告
#[allow(dead_code)]
fn _unused_type_anchor<T: DeserializeOwned>(_: T) -> Value { Value::Null }
