//! billing 服务所有 API 路径常量 + DTO 类型集中定义
//!
//! 设计原则(对齐 老杨师傅 修订):
//!   - 所有路径 / 方法 / 请求体 / 响应体**先在这里集中定义**,handler 只做参数提取 + 业务调用。
//!   - 禁止在 handler 里直接拼字符串路径或 `serde_json::json!{}` 宏构造响应。
//!   - 跨服务调用统一走 [`ServiceClientExt`],自动补充 service token + request id。

use serde::{Deserialize, Serialize};

// ===== 路径常量 =====

pub mod paths {
    // 内部 API(供其他服务调用)
    pub const QUOTE: &str = "/api/v1/internal/quote";
    pub const CALCULATE: &str = "/api/v1/internal/calculate";
    pub const FEE_BREAKDOWN: &str = "/api/v1/internal/orders/:order_id/fee-breakdown";
    pub const SPLIT: &str = "/api/v1/internal/split";
    pub const ORDER_SPLIT: &str = "/api/v1/internal/orders/:order_id/split";
    pub const SETTLEMENT_DETAIL: &str = "/api/v1/internal/settlements/:settlement_id";
    pub const INVOICE_SETTLE_DETAIL: &str = "/api/v1/internal/invoices/:invoice_id/settle-detail";
    pub const REFUND_CALC: &str = "/api/v1/internal/refunds/:refund_id/calc";
    pub const WITHDRAW_REQUESTS: &str = "/api/v1/internal/withdraw-requests";

    // 健康检查
    pub const HEALTH: &str = "/api/v1/health";
}

// ===== DTO:请求 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct QuoteRequest {
    pub port_id: String,
    pub user_id: u64,
    pub estimated_minutes: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CalculateRequest {
    pub order_no: String,
    pub charge_order_id: u64,
    pub pricing_rule_id: u64,
    pub charged_kwh: f64,
    pub charged_seconds: i64,
    pub peak_kwh: f64,
    pub off_kwh: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SplitRequest {
    pub fee_calculation_id: u64,
    pub split_template_id: u64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RefundCalcRequest {
    pub payment_order_id: u64,
    pub actual_paid_cents: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WithdrawCreateRequest {
    pub party_id: u64,
    pub amount_cents: i64,
}

// ===== DTO:响应 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct QuoteResponse {
    pub total_cents: i64,
    pub electric_cents: i64,
    pub service_cents: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CalculateResponse {
    pub calculation_id: u64,
    pub calculation_no: String,
    pub electric_cents: i64,
    pub service_cents: i64,
    pub total_cents: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FeeBreakdownResponse {
    pub calculation_no: String,
    pub electric_cents: i64,
    pub service_cents: i64,
    pub total_cents: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SplitAllocation {
    pub party_id: u64,
    pub party_code: String,
    pub amount_cents: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SplitResponse {
    pub settlement_id: u64,
    pub settlement_no: String,
    pub allocations: Vec<SplitAllocation>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OrderSplitParty {
    pub party_id: u64,
    pub party_code: String,
    pub amount_cents: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OrderSplitResponse {
    pub settlement_id: u64,
    pub settlement_no: String,
    pub mode: String,
    pub total_cents: i64,
    pub split_pool_cents: i64,
    pub parties: Vec<OrderSplitParty>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SettlementDetailResponse {
    pub id: u64,
    pub settlement_no: String,
    pub total_cents: i64,
    pub status: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct InvoiceSettleDetailResponse {
    pub invoice_id: u64,
    pub found: bool,
    pub total_cents: Option<i64>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RefundCalcResponse {
    pub refund_cents: i64,
    pub note: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WithdrawCreateResponse {
    pub withdraw_no: String,
}

// ===== DTO:其它(被本服务使用) =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct InternalSplitParty {
    pub id: u64,
    pub party_code: String,
    pub ratio_bp: u32,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn paths_match_legacy() {
        // 冻结路径常量,任何重命名都会触发 CI 失败
        assert_eq!(paths::QUOTE, "/api/v1/internal/quote");
        assert_eq!(paths::CALCULATE, "/api/v1/internal/calculate");
    }

    #[test]
    fn quote_request_roundtrip() {
        let r = QuoteRequest {
            port_id: "P1".into(),
            user_id: 42,
            estimated_minutes: 240,
        };
        let s = serde_json::to_string(&r).unwrap();
        let back: QuoteRequest = serde_json::from_str(&s).unwrap();
        assert_eq!(back.port_id, "P1");
        assert_eq!(back.user_id, 42);
    }

    #[test]
    fn quote_response_roundtrip() {
        let r = QuoteResponse { total_cents: 100, electric_cents: 64, service_cents: 30 };
        let s = serde_json::to_string(&r).unwrap();
        let back: QuoteResponse = serde_json::from_str(&s).unwrap();
        assert_eq!(back.total_cents, 100);
    }
}