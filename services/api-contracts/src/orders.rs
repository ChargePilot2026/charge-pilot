use serde::{Deserialize, Serialize};

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
}
