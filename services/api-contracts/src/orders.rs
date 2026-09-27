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
    #[serde(default)]
    pub refund_applicant_id: Option<String>,
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

// ===== 设备维度订单列表(gateway 透传 / user 生产)=====

/// 设备维度订单摘要。
///
/// gateway 的 `GET /internal/devices/:id/orders` 直接透传本结构,
/// 因此它必须定义在契约层而不是任一服务内部。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DeviceOrderSummary {
    pub order_id: u64,
    pub order_no: String,
    pub user_id: u64,
    pub port_no: u8,
    pub status: String,
    pub started_at: Option<String>,
    pub ended_at: Option<String>,
    pub total_cents: Option<i64>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DeviceOrdersResponse {
    pub device_id: String,
    pub items: Vec<DeviceOrderSummary>,
}

#[cfg(test)]
mod device_order_tests {
    use super::*;

    #[test]
    fn device_orders_round_trip() {
        let r = DeviceOrdersResponse {
            device_id: "D1".into(),
            items: vec![DeviceOrderSummary {
                order_id: 1, order_no: "ORD-1".into(), user_id: 2, port_no: 3,
                status: "completed".into(),
                started_at: Some("2026-09-28T00:00:00Z".into()),
                ended_at: None,
                total_cents: Some(1200),
            }],
        };
        let v = serde_json::to_value(&r).unwrap();
        assert_eq!(v["device_id"], "D1");
        assert_eq!(v["items"][0]["order_no"], "ORD-1");
        assert!(v["items"][0]["ended_at"].is_null());
        let back: DeviceOrdersResponse = serde_json::from_value(v).unwrap();
        assert_eq!(back.items.len(), 1);
    }
}
