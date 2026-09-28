//! billing 内部 API — 所有 handler 严格使用 [`crate::api_types`] 中定义的 DTO。
//!
//! 不允许:
//!   - 直接拼字符串路径(必须引用 `paths::*`)
//!   - 在 handler 内构造 `serde_json::json!{}` 响应(必须用 DTO + `Json<Envelope<T>>`)
//!
//! P3:所有 SQL 与事务已下沉到 [`crate::services`]。handler 手上只有能力域
//! 服务对象,拿不到裸连接池,因此本文件不再出现 `sqlx::`。`health` 的连通性
//! 探测走 `st.fee.ping()`。
//!
//! D3:提现端点(恒 503 的预留桩)随跨库视图整块删除。

use crate::{api_types as t, AppState};
use axum::{
    extract::{Path, State},
    Json,
};
use common_error::{AppError, AppResult, ApiEnvelope};
use serde::Serialize;

pub async fn health(State(st): State<AppState>) -> AppResult<&'static str> {
    st.fee.ping().await?;
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
    let rule:api_contracts::pricing::DevicePricing=client.get(st.cfg.service_urls.admin.as_deref(),&api_contracts::fill_path(api_contracts::paths::ADMIN_DEVICE_PRICING,"id",&port.device_id),&()).await?;
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
    let result=st.fee.calculate(req.charge_order_id,&req.order_no).await?;
    Ok(Json(ok_envelope(result)))
}

pub async fn fee_breakdown(
    State(st): State<AppState>,
    Path(order_id): Path<String>,
) -> AppResult<Json<ApiEnvelope<t::FeeBreakdownResponse>>> {
    let result=st.fee.fee_breakdown(&order_id).await?;
    Ok(Json(ok_envelope(result)))
}

pub async fn split(
    State(st): State<AppState>,
    Json(req): Json<t::SplitRequest>,
) -> AppResult<Json<ApiEnvelope<t::SplitResponse>>> {
    let result=st.settlement.split(req).await?;
    Ok(Json(ok_envelope(result)))
}

pub async fn order_split(
    State(st): State<AppState>,
    Path(order_id): Path<String>,
) -> AppResult<Json<ApiEnvelope<t::OrderSplitResponse>>> {
    let result=st.settlement.order_split(&order_id).await?;
    Ok(Json(ok_envelope(result)))
}

pub async fn settlement_detail(
    State(st): State<AppState>,
    Path(id): Path<u64>,
) -> AppResult<Json<ApiEnvelope<t::SettlementDetailResponse>>> {
    let result=st.settlement.settlement_detail(id).await?;
    Ok(Json(ok_envelope(result)))
}

/// **D6 修复**:原先直接查 `invoice_request` —— 该表只在 `user_db` 建,本服务连接
/// 指向 `billing_db`,且 billing_db 内无对应视图,该端点运行时必然报
/// `Table 'billing_db.invoice_request' doesn't exist`。
///
/// `invoice_request` 归 user 服务所有,改经其内部端点获取。
pub async fn invoice_settle_detail(
    State(st): State<AppState>,
    Path(invoice_id): Path<u64>,
) -> AppResult<Json<ApiEnvelope<t::InvoiceSettleDetailResponse>>> {
    let result=st.invoice.settle_detail(invoice_id).await?;
    Ok(Json(ok_envelope(result)))
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

// ===================== helpers =====================

fn ok_envelope<T: Serialize>(data: T) -> ApiEnvelope<T> {
    ApiEnvelope::ok(data, common_error::current_request_id())
}
