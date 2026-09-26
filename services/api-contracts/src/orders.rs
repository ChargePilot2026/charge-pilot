use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct OrderTimeline {
    pub order_id: u64,
    pub timeline: Vec<OrderEvent>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct OrderEvent {
    pub event_id: String,
    pub at: String,
    pub event: String,
    pub actor: String,
    pub detail: String,
}

fn first_page() -> u32 {
    1
}
fn page_size() -> u32 {
    20
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct OrderQuery {
    #[serde(default = "first_page")]
    pub page: u32,
    #[serde(default = "page_size")]
    pub page_size: u32,
    pub station_id: Option<u64>,
    pub status: Option<String>,
    pub started_from: Option<String>,
    pub started_to: Option<String>,
    pub order_no: Option<String>,
    pub device_id: Option<String>,
    /// Internal filter resolved from admin-owned station metadata.
    pub device_ids: Option<String>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct OrderSummary {
    pub order_id: u64,
    pub order_no: String,
    pub user_id: u64,
    pub device_id: String,
    pub port_no: u8,
    pub station_id: Option<u64>,
    pub station_name: Option<String>,
    pub status: String,
    pub created_at: String,
    pub started_at: Option<String>,
    pub ended_at: Option<String>,
    pub duration_seconds: Option<u32>,
    pub meter_kwh: Option<String>,
    pub electric_fee_cents: Option<i64>,
    pub service_fee_cents: Option<i64>,
    pub total_fee_cents: Option<i64>,
    pub refund_status: String,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct OrderPage {
    pub items: Vec<OrderSummary>,
    pub total: u64,
    pub page: u32,
    pub page_size: u32,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct OrderDetail {
    #[serde(flatten)]
    pub order: OrderSummary,
    pub payment_order_id: Option<u64>,
    pub payment_order_no: Option<String>,
    pub payment_status: Option<String>,
    pub paid_cents: Option<i64>,
    pub refunded_cents: Option<i64>,
    pub failure_reason: Option<String>,
    #[serde(default)]
    pub billing: Option<OrderBilling>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct OrderBilling {
    pub calculation_no: Option<String>,
    pub electric_cents: Option<i64>,
    pub service_cents: Option<i64>,
    pub total_cents: Option<i64>,
    pub settlements: Vec<OrderSettlement>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct OrderSettlement {
    pub settlement_id: u64,
    pub settlement_no: String,
    pub mode: String,
    pub status: String,
    pub total_cents: i64,
    pub split_pool_cents: i64,
    pub parties: Vec<OrderSettlementParty>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct OrderSettlementParty {
    pub party_id: u64,
    pub party_code: String,
    pub party_name: String,
    pub ratio_bp: u32,
    pub amount_cents: i64,
    pub status: String,
}
