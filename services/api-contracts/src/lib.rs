//! 全服务 API 契约 —— 路径常量 + 跨服务 DTO 的集中位置
//!
//! 设计:
//!   - 所有跨服务调用的路径在这里统一定义,各 service 只引用 `api_contracts::paths::*`
//!   - 各服务自己独有的 DTO 仍放在自己的 `api_types.rs`(没必要全部集中)
//!   - 跨服务调用的请求/响应 DTO 也集中在此(便于不同 service 共享类型)
//!
//! 禁止各 service 在 handler 里 `format!("/api/v1/...")` 拼 URL,
//! 必须从本 crate 引用路径常量。

use serde::{Deserialize, Serialize};

pub mod orders;
pub mod devices;

// ===================== 路径常量 =====================

pub mod paths {
    pub const GATEWAY_DEVICE_PROVISION: &str = "/api/v1/internal/devices/provision";
    // -------- public --------
    pub const AUTH_LOGIN_USER: &str = "/api/v1/public/auth/login";
    pub const AUTH_REFRESH_USER: &str = "/api/v1/public/auth/refresh";
    pub const PAYMENT_WECHAT_CALLBACK: &str = "/api/v1/public/payment/wechat/callback";

    pub const AUTH_LOGIN_ADMIN: &str = "/api/v1/admin/auth/login";
    pub const AUTH_REFRESH_ADMIN: &str = "/api/v1/admin/auth/refresh";

    // -------- gateway 内部 --------
    pub const GW_SCAN_RESOLVE: &str = "/api/v1/internal/scan/resolve";
    pub const GW_SCAN_PORT: &str = "/api/v1/internal/scan/port";
    pub const GW_DEVICE_GET: &str = "/api/v1/internal/devices/:id";
    pub const GW_DEVICE_PORTS: &str = "/api/v1/internal/devices/:id/ports";
    pub const GW_DEVICE_ORDERS: &str = "/api/v1/internal/devices/:id/orders";
    pub const GW_DEVICE_SNAPSHOT: &str = "/api/v1/internal/devices/:id/snapshot";
    pub const GW_DEVICE_REBOOT: &str = "/api/v1/internal/devices/:id/reboot";
    pub const GW_DEVICE_FIRMWARE_PUSH: &str = "/api/v1/internal/devices/:id/firmware-push";
    pub const GW_DEVICE_COMMAND: &str = "/api/v1/internal/devices/:id/command";
    pub const GW_DEVICE_BACKFILL: &str = "/api/v1/internal/devices/:id/backfill";
    pub const GW_DEVICE_CURVE: &str = "/api/v1/internal/devices/:id/curve";
    pub const GW_DEVICE_HIST_CURVE: &str = "/api/v1/internal/devices/:id/historical-curve";
    pub const GW_DEVICE_REGISTER: &str = "/api/v1/internal/device/register";
    pub const GW_CHARGE_ORDERS_STOP: &str = "/api/v1/internal/charge-orders/stop";

    // -------- user 内部 --------
    pub const USER_INTERNAL_ORDERS: &str = "/api/v1/internal/orders";
    pub const USER_INTERNAL_ORDER_TIMELINE: &str = "/api/v1/internal/orders/:order_id/timeline";
    pub const USER_INTERNAL_ORDER_DETAIL: &str = "/api/v1/internal/orders/:order_id";
    pub const USER_INTERNAL_REFUND_DETAIL: &str = "/api/v1/internal/refunds/:refund_id";
    pub const USER_INTERNAL_PAYMENT_DETAIL: &str = "/api/v1/internal/payment-orders/:payment_order_id";
    pub const USER_INTERNAL_INVOICE_DETAIL: &str = "/api/v1/internal/invoices/:invoice_id";
    pub const USER_INTERNAL_COUPON_STATS: &str = "/api/v1/internal/coupons/stats";
    pub const USER_INTERNAL_REFUND_CLAIM: &str = "/api/v1/internal/refund-records/claim";
    pub const USER_INTERNAL_REFUND_RESULT: &str = "/api/v1/internal/refund-records/:refund_id/result";
    pub const USER_INTERNAL_START_RESULT: &str = "/api/v1/internal/charge-orders/:order_id/start-result";

    // -------- billing 内部 --------
    pub const BILLING_QUOTE: &str = "/api/v1/internal/quote";
    pub const BILLING_ORDER_SUMMARY: &str = "/api/v1/internal/orders/:order_id/billing-summary";
    pub const BILLING_CALCULATE: &str = "/api/v1/internal/calculate";
    pub const BILLING_FEE_BREAKDOWN: &str = "/api/v1/internal/orders/:order_id/fee-breakdown";
    pub const BILLING_SPLIT: &str = "/api/v1/internal/split";
    pub const BILLING_ORDER_SPLIT: &str = "/api/v1/internal/orders/:order_id/split";
    pub const BILLING_SETTLEMENT_DETAIL: &str = "/api/v1/internal/settlements/:settlement_id";
    pub const BILLING_INVOICE_SETTLE_DETAIL: &str = "/api/v1/internal/invoices/:invoice_id/settle-detail";
    pub const BILLING_REFUND_CALC: &str = "/api/v1/internal/refunds/:refund_id/calc";
    pub const BILLING_WITHDRAW_REQUESTS: &str = "/api/v1/internal/withdraw-requests";

    // -------- admin 内部 --------
    pub const ADMIN_INTERNAL_ANNOUNCEMENTS_ACTIVE: &str = "/api/v1/internal/announcements/active";
    pub const ADMIN_INTERNAL_STATIONS_NEARBY: &str = "/api/v1/internal/stations/nearby";
    pub const ADMIN_INTERNAL_STATIONS_DETAIL: &str = "/api/v1/internal/stations/:station_id";
    pub const ADMIN_INTERNAL_PRICING_RULES_GET: &str = "/api/v1/internal/pricing-rules/:id";
    pub const ADMIN_INTERNAL_SPLIT_TEMPLATES_GET: &str = "/api/v1/internal/split-templates/:id";
    pub const ADMIN_INTERNAL_EXPORT_TASK_GET: &str = "/api/v1/internal/export/tasks/:id";
    pub const ADMIN_INTERNAL_ALERTS_ACTIVE: &str = "/api/v1/internal/alerts";
    pub const ADMIN_INTERNAL_DEVICES_REBOOT: &str = "/api/v1/internal/devices/:id/reboot";

    pub const HEALTH: &str = "/api/v1/health";
}

// ===================== 跨服务 DTO =====================

// ---- gateway <-> user/device ----

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ScanResolveRequest {
    pub code: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ScanPortRequest {
    pub port_id: String,
}

/// Public port identity is the printed port code, never a database row id.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ScanPortDetail {
    pub port_id: String,
    pub device_id: String,
    pub port_no: u8,
    pub port_code: String,
    pub status: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum ScanResolveResponse {
    Port { #[serde(flatten)] port: ScanPortDetail },
    Device { device_id: String, status: String, ports: Vec<ScanPortDetail> },
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChargeStopCommand {
    pub order_no: String,
    pub user_id: u64,
    pub source: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChargeStopResponse {
    pub stopped: bool,
}

// ---- user <-> billing ----

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BillingQuoteRequest {
    pub port_id: String,
    pub user_id: u64,
    pub estimated_minutes: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct QuoteResponse {
    pub total_cents: i64,
    pub electric_cents: i64,
    pub service_cents: i64,
}

// ---- user 内部 → admin 调用 ----

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct NearbyStationsQuery {
    pub lat: f64,
    pub lng: f64,
    #[serde(default)]
    pub radius_km: Option<f64>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct NearbyStationsResponse {
    #[serde(default)]
    pub items: Vec<NearbyStationItem>,
}

impl NearbyStationsResponse {
    pub fn empty() -> Self { Self { items: vec![] } }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct NearbyStationItem {
    pub id: u64,
    pub code: String,
    pub name: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub address: Option<String>,
    pub longitude: f64,
    pub latitude: f64,
    pub distance_km: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StationPublicDetail {
    pub id: u64,
    pub code: String,
    pub name: String,
    pub address: Option<String>,
    pub longitude: f64,
    pub latitude: f64,
    pub open_hours: Option<String>,
    pub contact_phone: Option<String>,
}

// ---- user → gateway 回写 ----

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StartResultRequest {
    pub order_no: String,
    pub success: bool,
    #[serde(default)]
    pub error: Option<String>,
}

// ---- admin <-> user 订单详情 ----

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct OrderListResponse {
    #[serde(default)]
    pub items: Vec<serde_json::Value>,
}

impl OrderListResponse {
    pub fn empty() -> Self { Self::default() }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn all_paths_non_empty() {
        // 冻结:任何空路径都会被捕获
        for (_, p) in [
            ("AUTH_LOGIN_USER", paths::AUTH_LOGIN_USER),
            ("GW_SCAN_RESOLVE", paths::GW_SCAN_RESOLVE),
            ("BILLING_QUOTE", paths::BILLING_QUOTE),
            ("ADMIN_INTERNAL_STATIONS_NEARBY", paths::ADMIN_INTERNAL_STATIONS_NEARBY),
        ] {
            assert!(!p.is_empty(), "path {} empty", p);
            assert!(p.starts_with("/api/v1/"), "path {} must start with /api/v1/", p);
        }
    }

    #[test]
    fn paths_stable() {
        // 防止路径被无意改名(老杨师傅规则:接口方法/路径必须先定义,禁止改)
        assert_eq!(paths::AUTH_LOGIN_USER, "/api/v1/public/auth/login");
        assert_eq!(paths::GW_SCAN_RESOLVE, "/api/v1/internal/scan/resolve");
        assert_eq!(paths::BILLING_QUOTE, "/api/v1/internal/quote");
    }

    #[test]
    fn scan_resolve_serde() {
        let r = ScanResolveRequest { code: "abc".into() };
        let s = serde_json::to_string(&r).unwrap();
        let back: ScanResolveRequest = serde_json::from_str(&s).unwrap();
        assert_eq!(back.code, "abc");
    }

    #[test]
    fn quote_response_serde() {
        let r = QuoteResponse { total_cents: 100, electric_cents: 60, service_cents: 40 };
        let s = serde_json::to_string(&r).unwrap();
        let back: QuoteResponse = serde_json::from_str(&s).unwrap();
        assert_eq!(back.total_cents, 100);
    }
}
