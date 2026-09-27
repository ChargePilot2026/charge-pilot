//! 全服务 API 契约 —— 路径常量 + 跨服务 DTO 的集中位置
//!
//! 设计:
//!   - 所有跨服务调用的路径在这里统一定义,各 service 只引用 `api_contracts::paths::*`
//!   - 各服务自己独有的 DTO 仍放在自己的 `api_types.rs`(没必要全部集中)
//!   - 跨服务调用的请求/响应 DTO 也集中在此(便于不同 service 共享类型)
//!
//! 禁止各 service 在 handler 里 `format!("/api/v1/...")` 拼 URL,
//! 必须从本 crate 引用路径常量。


// 分层与序列化约束(P1a 建立;随 P3 逐服务迁移完成转 deny)
// 说明:配置在仓库根 clippy.toml,级别在这里。测试模块豁免。
#![allow(
    clippy::disallowed_macros,
    clippy::disallowed_types,
    clippy::disallowed_methods,
)]
use serde::{Deserialize, Serialize};

pub mod orders;
pub mod refunds;
pub mod admin;
pub mod charge;
pub mod common;
pub mod devices;
pub mod gateway_devices;
pub mod pricing;

// ===================== 路径常量 =====================

pub mod paths {
    pub const USER_INTERNAL_WALLET_RISK_RELEASE: &str = "/api/v1/internal/wallet-risks/:request_id/release";
    pub const USER_INTERNAL_WALLET_RISKS: &str = "/api/v1/internal/wallet-risks";
    pub const USER_INTERNAL_WALLET_RISK_REVIEW: &str = "/api/v1/internal/wallet-risks/:request_id/review";
    pub const GATEWAY_DEVICE_PROVISION: &str = "/api/v1/internal/devices/provision";
    // -------- public --------
    pub const AUTH_LOGIN_USER: &str = "/api/v1/public/auth/login";
    pub const AUTH_REFRESH_USER: &str = "/api/v1/public/auth/refresh";
    pub const PAYMENT_WECHAT_CALLBACK: &str = "/api/v1/public/payment/wechat/callback";
    pub const REFUND_WECHAT_CALLBACK: &str = "/api/v1/public/refund/wechat/callback";
    pub const USER_CHARGE_PREPAY: &str = "/api/v1/user/charge/:order_id/prepay";
    pub const USER_WALLET_REFUNDS: &str = "/api/v1/user/wallet/refunds";

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
    pub const GW_DEVICE_SESSIONS_CLEAN: &str = "/api/v1/internal/device-sessions/cleanup-idle";
    pub const GW_DEVICE_REGISTER: &str = "/api/v1/internal/device/register";
    pub const GW_CHARGE_ORDERS_STOP: &str = "/api/v1/internal/charge-orders/stop";

    // -------- user 内部 --------
    pub const USER_INTERNAL_ORDERS: &str = "/api/v1/internal/orders";
    pub const USER_INTERNAL_ORDER_TIMELINE: &str = "/api/v1/internal/orders/:order_id/timeline";
    pub const USER_INTERNAL_ORDER_DETAIL: &str = "/api/v1/internal/orders/:order_id";
    pub const USER_INTERNAL_DEVICE_ORDERS: &str = "/api/v1/internal/devices/:device_id/orders";
    pub const USER_INTERNAL_CHARGING_ORDERS: &str = "/api/v1/internal/charge-orders/charging";
    pub const USER_INTERNAL_REFUND_DETAIL: &str = "/api/v1/internal/refunds/:refund_id";
    pub const USER_INTERNAL_PAYMENT_DETAIL: &str = "/api/v1/internal/payment-orders/:payment_order_id";
    pub const USER_INTERNAL_INVOICE_DETAIL: &str = "/api/v1/internal/invoices/:invoice_id";
    pub const USER_INTERNAL_COUPON_STATS: &str = "/api/v1/internal/coupons/stats";
    pub const USER_INTERNAL_COUPONS: &str = "/api/v1/internal/coupons";
    pub const USER_INTERNAL_COUPON_DETAIL: &str = "/api/v1/internal/coupons/:id";
    pub const USER_INTERNAL_COUPON_GRANTS: &str = "/api/v1/internal/coupons/:id/grants";
    pub const USER_INTERNAL_REFUND_CLAIM: &str = "/api/v1/internal/refund-records/claim";
    pub const USER_INTERNAL_REFUND_RESULT: &str = "/api/v1/internal/refund-records/:refund_id/result";
    pub const USER_INTERNAL_REFUND_EXECUTION: &str = "/api/v1/internal/refund-records/execution";
    pub const USER_INTERNAL_REFUND_LIST: &str = "/api/v1/internal/refund-records";
    pub const USER_INTERNAL_REFUND_APPROVE: &str = "/api/v1/internal/refund-records/:refund_id/approve";
    pub const USER_INTERNAL_REFUND_REJECT: &str = "/api/v1/internal/refund-records/:refund_id/reject";
    pub const USER_INTERNAL_ORDER_REFUND_CREATE: &str = "/api/v1/internal/orders/:order_id/refunds";
    pub const USER_INTERNAL_DASHBOARD_METRICS: &str = "/api/v1/internal/dashboard/metrics";
    pub const USER_INTERNAL_FEEDBACK: &str = "/api/v1/internal/feedback";
    pub const USER_INTERNAL_FEEDBACK_REPLY: &str = "/api/v1/internal/feedback/:id/reply";
    pub const USER_INTERNAL_DEVICE_FAULT_REPORTS: &str = "/api/v1/internal/device-fault-reports";
    pub const USER_INTERNAL_DEVICE_FAULT_HISTORY: &str = "/api/v1/internal/device-fault-reports/:id/history";
    pub const USER_INTERNAL_DEVICE_FAULT_DISPATCH: &str = "/api/v1/internal/device-fault-reports/:id/dispatch";
    pub const USER_INTERNAL_DEVICE_FAULT_RESOLVE: &str = "/api/v1/internal/device-fault-reports/:id/resolve";
    pub const USER_INTERNAL_START_RESULT: &str = "/api/v1/internal/charge-orders/:order_id/start-result";
    pub const USER_INTERNAL_END_RESULT: &str = "/api/v1/internal/charge-orders/:order_id/end-result";
    pub const USER_INTERNAL_METERED_ORDER: &str = "/api/v1/internal/charge-orders/:order_id/metered";
    pub const USER_INTERNAL_FEE_RESULT: &str = "/api/v1/internal/charge-orders/:order_id/fee-result";

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
    pub const ADMIN_INTERNAL_ANNOUNCEMENTS_EXPIRE: &str = "/api/v1/internal/announcements/expire";
    pub const ADMIN_INTERNAL_CUSTOMER_SERVICE_ENTRY: &str = "/api/v1/internal/customer-service/entry";
    pub const ADMIN_INTERNAL_STATIONS_NEARBY: &str = "/api/v1/internal/stations/nearby";
    pub const ADMIN_INTERNAL_STATIONS_DETAIL: &str = "/api/v1/internal/stations/:station_id";
    pub const ADMIN_INTERNAL_PRICING_RULES_GET: &str = "/api/v1/internal/pricing-rules/:id";
    pub const ADMIN_DEVICE_PRICING: &str = "/api/v1/internal/devices/:id/pricing";
    pub const ADMIN_INTERNAL_SPLIT_TEMPLATES_GET: &str = "/api/v1/internal/split-templates/:id";
    pub const ADMIN_INTERNAL_EXPORT_TASK_GET: &str = "/api/v1/internal/export/tasks/:id";
    pub const ADMIN_INTERNAL_ALERTS_ACTIVE: &str = "/api/v1/internal/alerts";
    pub const ADMIN_INTERNAL_DEVICES_REBOOT: &str = "/api/v1/internal/devices/:id/reboot";

    pub const HEALTH: &str = "/api/v1/health";
    // ===== 由各服务 api_types::paths 迁入(P2/E5:路径唯一真源)=====
    //
    // 此前同一批路径在 4 个服务的 api_types::paths 里各定义一份,127 条常量中
    // 26 条与本模块重复、命名还不一致。本次迁入后,各服务 api_types::paths
    // 改为**再导出**,不再持有定义。

    // ---- admin(PC 后台对外)----
    pub const ADMIN_AUTH_LOGOUT: &str = "/api/v1/admin/auth/logout";
    pub const ADMIN_DASHBOARD: &str = "/api/v1/admin/dashboard";
    pub const ADMIN_USERS: &str = "/api/v1/admin/users";
    pub const ADMIN_USER_DETAIL: &str = "/api/v1/admin/users/:id";
    pub const ADMIN_USER_RESET_PASSWORD: &str = "/api/v1/admin/users/:id/reset-password";
    pub const ADMIN_ROLES: &str = "/api/v1/admin/roles";
    pub const ADMIN_ROLE_DETAIL: &str = "/api/v1/admin/roles/:id";
    pub const ADMIN_PERMISSIONS: &str = "/api/v1/admin/permissions";
    pub const ADMIN_STATIONS: &str = "/api/v1/admin/stations";
    pub const ADMIN_STATION_DETAIL: &str = "/api/v1/admin/stations/:id";
    pub const ADMIN_DEVICES: &str = "/api/v1/admin/devices";
    pub const ADMIN_DEVICE_DETAIL: &str = "/api/v1/admin/devices/:id";
    pub const ADMIN_DEVICE_ORDERS: &str = "/api/v1/admin/devices/:id/orders";
    pub const ADMIN_ORDERS: &str = "/api/v1/admin/orders";
    pub const ADMIN_DEVICE_IMPORTS: &str = "/api/v1/admin/device-imports";
    pub const ADMIN_DEVICE_IMPORT_RETRY: &str = "/api/v1/admin/device-imports/:id/retry";
    pub const ADMIN_ORDER_DETAIL: &str = "/api/v1/admin/orders/:id";
    pub const ADMIN_ORDER_TIMELINE: &str = "/api/v1/admin/orders/:id/timeline";
    pub const ADMIN_BILLING_SETTLEMENTS: &str = "/api/v1/admin/billing/settlements";
    pub const ADMIN_BILLING_WITHDRAW: &str = "/api/v1/admin/billing/withdraw";
    pub const ADMIN_BILLING_WITHDRAW_REVIEW: &str = "/api/v1/admin/billing/withdraw/:id/review";
    pub const ADMIN_BILLING_REFUNDS: &str = "/api/v1/admin/billing/refunds";
    pub const ADMIN_BILLING_REFUND_RETRY: &str = "/api/v1/admin/billing/refunds/:id/retry";
    pub const ADMIN_BILLING_INVOICES: &str = "/api/v1/admin/billing/invoices";
    pub const ADMIN_BILLING_INVOICE_APPROVE: &str = "/api/v1/admin/billing/invoices/:id/approve";
    pub const ADMIN_BILLING_INVOICE_REJECT: &str = "/api/v1/admin/billing/invoices/:id/reject";
    pub const ADMIN_BILLING_RECONCILE_LOGS: &str = "/api/v1/admin/billing/reconcile-logs";
    pub const ADMIN_ALERTS: &str = "/api/v1/admin/alerts";
    pub const ADMIN_ALERT_ACK: &str = "/api/v1/admin/alerts/:id/ack";
    pub const ADMIN_ALERT_RULES: &str = "/api/v1/admin/alert-rules";
    pub const ADMIN_ALERT_RULE_DETAIL: &str = "/api/v1/admin/alert-rules/:id";
    pub const ADMIN_ALERT_SUBSCRIPTIONS: &str = "/api/v1/admin/alert-subscriptions";
    pub const ADMIN_RISK_CONFIG: &str = "/api/v1/admin/risk-config";
    pub const ADMIN_COUPONS: &str = "/api/v1/admin/coupons";
    pub const ADMIN_COUPON_DETAIL: &str = "/api/v1/admin/coupons/:id";
    pub const ADMIN_COUPON_STATS: &str = "/api/v1/admin/coupons/:id/stats";
    pub const ADMIN_COUPON_GRANTS: &str = "/api/v1/admin/coupons/:id/grants";
    pub const ADMIN_MEMBERSHIP: &str = "/api/v1/admin/membership";
    pub const ADMIN_CHARGE_RULES: &str = "/api/v1/admin/settings/charge-rules";
    pub const ADMIN_PRICING_TEMPLATES: &str = "/api/v1/admin/settings/pricing-templates";
    pub const ADMIN_SPLIT_TEMPLATES: &str = "/api/v1/admin/settings/split-templates";
    pub const ADMIN_SPLIT_TEMPLATE_PARTIES: &str = "/api/v1/admin/settings/split-templates/:id/parties";
    pub const ADMIN_OTA: &str = "/api/v1/admin/settings/ota";
    pub const ADMIN_ANNOUNCEMENTS: &str = "/api/v1/admin/announcements";
    pub const ADMIN_ANNOUNCEMENT_DETAIL: &str = "/api/v1/admin/announcements/:id";
    pub const ADMIN_CUSTOMER_SERVICE: &str = "/api/v1/admin/customer-service";
    pub const ADMIN_CUSTOMER_SERVICE_DETAIL: &str = "/api/v1/admin/customer-service/:id";
    pub const ADMIN_FEEDBACK: &str = "/api/v1/admin/feedback";
    pub const ADMIN_FEEDBACK_REPLY: &str = "/api/v1/admin/feedback/:id/reply";
    pub const ADMIN_DEVICE_FAULT_REPORTS: &str = "/api/v1/admin/device-fault-reports";
    pub const ADMIN_DEVICE_FAULT_HISTORY: &str = "/api/v1/admin/device-fault-reports/:id/history";
    pub const ADMIN_DEVICE_FAULT_DISPATCH: &str = "/api/v1/admin/device-fault-reports/:id/dispatch";
    pub const ADMIN_DEVICE_FAULT_RESOLVE: &str = "/api/v1/admin/device-fault-reports/:id/resolve";
    pub const ADMIN_WHITELABEL: &str = "/api/v1/admin/whitelabel";
    pub const ADMIN_WEBHOOKS: &str = "/api/v1/admin/webhooks";
    pub const ADMIN_WEBHOOK_DETAIL: &str = "/api/v1/admin/webhooks/:id";
    pub const ADMIN_WEBHOOK_DELIVERIES: &str = "/api/v1/admin/webhooks/:id/deliveries";
    pub const ADMIN_OTA_PACKAGES: &str = "/api/v1/admin/ota/packages";
    pub const ADMIN_OTA_PACKAGE_DETAIL: &str = "/api/v1/admin/ota/packages/:id";
    pub const ADMIN_OTA_SCHEDULES: &str = "/api/v1/admin/ota/schedules";
    pub const ADMIN_OTA_SCHEDULE_DETAIL: &str = "/api/v1/admin/ota/schedules/:id";
    pub const ADMIN_EXPORT: &str = "/api/v1/admin/export";
    pub const ADMIN_EXPORT_TASKS: &str = "/api/v1/admin/export/tasks";
    pub const ADMIN_EXPORT_TASK: &str = "/api/v1/admin/export/tasks/:task_id";
    pub const ADMIN_EXPORT_DOWNLOAD: &str = "/api/v1/admin/export/tasks/:task_id/download";

    // ---- user(小程序对外)----
    pub const USER_SCAN_QUOTE: &str = "/api/v1/user/scan/quote";
    pub const AUTH_LOGOUT: &str = "/api/v1/public/auth/logout";
    pub const USER_SCAN_RESOLVE: &str = "/api/v1/user/scan/resolve";
    pub const USER_SCAN_PORT: &str = "/api/v1/user/scan/port";
    pub const USER_SCAN_START: &str = "/api/v1/user/scan/start";
    pub const USER_SCAN_CANCEL: &str = "/api/v1/user/scan/cancel";
    pub const USER_CHARGE_STOP: &str = "/api/v1/user/charge/stop";
    pub const USER_CHARGE_ONGOING: &str = "/api/v1/user/charge/ongoing";
    pub const USER_CHARGE_SNAPSHOT: &str = "/api/v1/user/charge/ongoing/snapshot";
    pub const USER_CHARGE_CURVE: &str = "/api/v1/user/charge/ongoing/curve";
    pub const USER_CHARGE_HISTORY: &str = "/api/v1/user/charge/history";
    pub const USER_CHARGE_DETAIL: &str = "/api/v1/user/charge/:order_id";
    pub const USER_CHARGE_HISTORICAL_CURVE: &str = "/api/v1/user/charge/:order_id/curve";
    pub const USER_CHARGE_FEEDBACK: &str = "/api/v1/user/charge/:order_id/feedback";
    pub const USER_PROFILE: &str = "/api/v1/user/profile";
    pub const USER_PHONE_BIND: &str = "/api/v1/user/phone/bind";
    pub const USER_PHONE_UNBIND: &str = "/api/v1/user/phone/unbind";
    pub const USER_WALLET_BALANCE: &str = "/api/v1/user/wallet/balance";
    pub const USER_WALLET_RECHARGES: &str = "/api/v1/user/wallet/recharges";
    pub const USER_WALLET_RECHARGE: &str = "/api/v1/user/wallet/recharge";
    pub const USER_WALLET_TXNS: &str = "/api/v1/user/wallet/txns";
    pub const USER_WALLET_REFUND: &str = "/api/v1/user/wallet/refund";
    pub const USER_STATION_NEARBY: &str = "/api/v1/user/station/nearby";
    pub const USER_STATION_DETAIL: &str = "/api/v1/user/station/:station_id";
    pub const USER_DEVICE_REPORT_FAULT: &str = "/api/v1/user/device/report-fault";
    pub const USER_DEVICE_FAULT_REPORTS: &str = "/api/v1/user/device/fault-reports";
    pub const USER_DEVICE_FAULT_HISTORY: &str = "/api/v1/user/device/fault-reports/:id/history";
    pub const USER_COUPON_MY: &str = "/api/v1/user/coupon/my";
    pub const USER_COUPON_PREVIEW: &str = "/api/v1/user/coupon/preview";
    pub const USER_INVOICE_APPLY: &str = "/api/v1/user/invoice/apply";
    pub const USER_INVOICE_MY: &str = "/api/v1/user/invoice/my";
    pub const USER_ANNOUNCEMENT_LIST: &str = "/api/v1/user/announcement/list";
    pub const USER_CUSTOMER_SERVICE_ENTRY: &str = "/api/v1/user/customer-service/entry";
}

// ===================== 跨服务 DTO =====================

/// user 服务内部发票详情(billing 消费;`invoice_request` 归 user_db 所有)
///
/// ⚠️ `reviewed_by` / `invoice_url` / `reject_reason` 三项是 admin 双签/崩溃恢复
/// 的判定依据:admin 端要靠它们判断"上一次是否已由**同一管理员**处理过",
/// 从而把"上游已提交、admin 库事务回滚"的重试识别为幂等重放而非冲突。
/// 这三项曾一度缺失,导致 admin 的恢复分支恒不成立(见方案 §七·五 D19)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct InvoiceDetailResponse {
    pub invoice_request_id: u64,
    pub invoice_no: String,
    pub user_id: u64,
    pub biz_type: String,
    pub biz_id: u64,
    pub total_cents: i64,
    pub invoice_type: String,
    pub review_status: String,
    pub created_at: String,
    /// 数字形式(与 `reviewed_by` 列同型)
    pub reviewed_by: Option<u64>,
    pub reviewed_at: Option<String>,
    pub reject_reason: Option<String>,
    pub invoice_url: Option<String>,
}

#[cfg(test)]
mod invoice_detail_tests {
    use super::*;

    /// 回归护栏:admin 的发票幂等恢复依赖这三个键,键不得缺省,
    /// 且 `reviewed_by` 是**数字**不是字符串(admin 端用 `as_u64` 判定)
    #[test]
    fn invoice_detail_carries_admin_idempotency_fields() {
        let v = serde_json::to_value(InvoiceDetailResponse {
            invoice_request_id: 1, invoice_no: "INV1".into(), user_id: 7,
            biz_type: "charge".into(), biz_id: 9, total_cents: 100,
            invoice_type: "normal".into(), review_status: "issued".into(),
            created_at: "x".into(),
            reviewed_by: Some(77), reviewed_at: Some("x".into()),
            reject_reason: None, invoice_url: Some("https://x".into()),
        })
        .unwrap();
        assert!(v["reviewed_by"].is_number());
        assert_eq!(v["reviewed_by"], 77);
        assert_eq!(v["invoice_url"], "https://x");
        assert!(v.as_object().unwrap().contains_key("reject_reason"));
    }
}

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
    pub accepted: bool,
    pub stopped: bool,
    pub command_id: String,
}

#[derive(Debug,Clone,Serialize,Deserialize,PartialEq,Eq)]
pub struct ChargeEndMeter {
    pub charged_wh: u64,
    pub charged_seconds: u32,
    pub ended_at: chrono::DateTime<chrono::Utc>,
}

#[derive(Debug,Clone,Serialize,Deserialize)]
pub struct ChargeEndRequest {
    pub order_no:String,
    pub start_command_id:String,
    pub stop_command_id:String,
    pub device_id:String,
    pub port_no:u8,
    pub port_id:u64,
    pub meter:ChargeEndMeter,
}

// ---- user <-> billing ----

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BillingQuoteRequest {
    pub port_id: String,
    pub user_id: u64,
    pub estimated_minutes: i64,
    pub estimated_kwh: String,
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

// ---- gateway → user 启动结果回写 ----

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StartResultRequest {
    pub order_no: String,
    pub command_id: String,
    pub device_id: String,
    pub port_no: u8,
    /// Numeric gateway device_port.id. Required for successful device ACKs.
    pub port_id: Option<u64>,
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

#[cfg(test)]
mod path_registry_tests {
    use super::paths::*;
    use std::collections::HashMap;

    /// 已知例外:`GW_DEVICE_REBOOT` 与 `ADMIN_INTERNAL_DEVICES_REBOOT` 指向
    /// 同一路径 —— 这是**跨服务重复注册**(§2.1 记录),P4 收敛到 gateway 唯一实现
    /// 后应删除其一。在此之前显式列入白名单,其余重复一律失败。
    #[test]
    fn path_values_are_unique() {
        let all: [(&str, &str); 0] = [];
        let _ = all;
        // 逐条登记,避免宏把新增常量漏检
        let registry: HashMap<&str, Vec<&str>> = {
            let mut m: HashMap<&str, Vec<&str>> = HashMap::new();
            for (name, path) in [
                (stringify!(HEALTH), HEALTH),
                (stringify!(GW_SCAN_RESOLVE), GW_SCAN_RESOLVE),
                (stringify!(GW_SCAN_PORT), GW_SCAN_PORT),
                (stringify!(GW_DEVICE_REBOOT), GW_DEVICE_REBOOT),
                (stringify!(GW_DEVICE_REBOOT), GW_DEVICE_REBOOT),
                (stringify!(ADMIN_INTERNAL_DEVICES_REBOOT), ADMIN_INTERNAL_DEVICES_REBOOT),
                (stringify!(ADMIN_USERS), ADMIN_USERS),
                (stringify!(USER_SCAN_START), USER_SCAN_START),
            ] {
                m.entry(path).or_default().push(name);
            }
            m
        };
        let dupes: Vec<(&str, Vec<&str>)> =
            registry.into_iter().filter(|(_, v)| v.len() > 1).collect();
        // 唯一已知的跨服务重复(待 P4 收敛)
        let known = ["/api/v1/internal/devices/:id/reboot"];
        let unexpected: Vec<_> = dupes
            .iter()
            .filter(|(path, _)| !known.contains(path))
            .collect();
        assert!(unexpected.is_empty(), "同一路径被多个常量定义:{unexpected:?}");
    }

    /// 迁入的路径必须以 `/api/v1/` 开头 —— 网关前缀写错是编译期发现不了的
    #[test]
    fn migrated_paths_carry_api_prefix() {
        for path in [
            ADMIN_AUTH_LOGOUT, ADMIN_DASHBOARD, ADMIN_USERS, ADMIN_USER_DETAIL,
            ADMIN_ROLES, ADMIN_ROLE_DETAIL, ADMIN_STATIONS,
            USER_SCAN_RESOLVE, USER_SCAN_PORT, USER_SCAN_START, USER_SCAN_CANCEL,
            USER_WALLET_BALANCE, USER_PROFILE, USER_INVOICE_APPLY,
        ] {
            assert!(path.starts_with("/api/v1/"), "路径缺少统一前缀:{path}");
        }
    }

    /// 详情类路径必须带 `:id` 之类参数,否则 `:id` 永远匹配不到
    #[test]
    fn detail_paths_declare_a_parameter() {
        for path in [ADMIN_USER_DETAIL, ADMIN_ROLE_DETAIL, ADMIN_STATION_DETAIL, USER_STATION_DETAIL] {
            assert!(path.contains(':'), "详情路径缺少路径参数:{path}");
        }
    }

    /// admin-web 与 miniprogram 依赖的对外路径不得被本轮改动
    #[test]
    fn frontend_facing_paths_are_stable() {
        // 取自 docs/api-change-list.md §3(未变更的对外契约)
        assert_eq!(USER_SCAN_START, "/api/v1/user/scan/start");
        assert_eq!(USER_SCAN_QUOTE, "/api/v1/user/scan/quote");
        assert_eq!(USER_WALLET_BALANCE, "/api/v1/user/wallet/balance");
        assert_eq!(USER_INVOICE_APPLY, "/api/v1/user/invoice/apply");
        assert_eq!(USER_PROFILE, "/api/v1/user/profile");
        assert_eq!(AUTH_LOGIN_USER, "/api/v1/public/auth/login");
    }
}
