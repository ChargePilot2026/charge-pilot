//! user 服务所有 API 路径常量 + DTO 类型集中定义
//!
//! 设计原则(对齐 老杨师傅 修订):
//!   - 所有路径 / 方法 / 请求体 / 响应体**先在这里集中定义**,handler 只做参数提取 + 业务调用。
//!   - 禁止在 handler 里直接拼字符串路径或 `serde_json::json!{}` 宏构造响应。
//!   - 跨服务调用统一走 [`crate::clients`],自动补充 service token + request id。

use serde::{Deserialize, Serialize};

// ===== 路径常量 =====

pub mod paths {
    pub const USER_SCAN_QUOTE: &str = "/api/v1/user/scan/quote";
    // --- 公开路由(无需 JWT)---
    pub const AUTH_LOGIN: &str = "/api/v1/public/auth/login";
    pub const AUTH_LOGOUT: &str = "/api/v1/public/auth/logout";
    pub const AUTH_REFRESH: &str = "/api/v1/public/auth/refresh";
    pub const PAYMENT_WECHAT_CALLBACK: &str = "/api/v1/public/payment/wechat/callback";

    // --- 内部路由(其他服务调用,需 ServiceToken)---
    pub const INTERNAL_START_RESULT: &str = "/api/v1/internal/charge-orders/:order_id/start-result";
    pub const INTERNAL_REFUND_CLAIM: &str = "/api/v1/internal/refund-records/claim";
    pub const INTERNAL_REFUND_RESULT: &str = "/api/v1/internal/refund-records/:refund_id/result";
    pub const INTERNAL_REFUND_DETAIL: &str = "/api/v1/internal/refunds/:refund_id";
    pub const INTERNAL_PAYMENT_DETAIL: &str = "/api/v1/internal/payment-orders/:payment_order_id";
    pub const INTERNAL_ORDER_DETAIL: &str = "/api/v1/internal/orders/:order_id";
    pub const INTERNAL_INVOICE_DETAIL: &str = "/api/v1/internal/invoices/:invoice_id";
    pub const INTERNAL_COUPON_STATS: &str = "/api/v1/internal/coupons/stats";

    // --- 用户路由(需 UserClaims JWT)---
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
    pub const USER_WALLET_RECHARGE: &str = "/api/v1/user/wallet/recharge";
    pub const USER_WALLET_TXNS: &str = "/api/v1/user/wallet/txns";
    pub const USER_WALLET_REFUND: &str = "/api/v1/user/wallet/refund";
    pub const USER_STATION_NEARBY: &str = "/api/v1/user/station/nearby";
    pub const USER_STATION_DETAIL: &str = "/api/v1/user/station/:station_id";
    pub const USER_DEVICE_REPORT_FAULT: &str = "/api/v1/user/device/report-fault";
    pub const USER_COUPON_MY: &str = "/api/v1/user/coupon/my";
    pub const USER_COUPON_PREVIEW: &str = "/api/v1/user/coupon/preview";
    pub const USER_INVOICE_APPLY: &str = "/api/v1/user/invoice/apply";
    pub const USER_INVOICE_MY: &str = "/api/v1/user/invoice/my";
    pub const USER_ANNOUNCEMENT_LIST: &str = "/api/v1/user/announcement/list";
    pub const USER_CUSTOMER_SERVICE_ENTRY: &str = "/api/v1/user/customer-service/entry";

    pub const HEALTH: &str = "/api/v1/health";
}

// ===== DTO:扫码 / 充电 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LoginRequest {
    pub code: String,
    #[serde(default)]
    pub iv: Option<String>,
    #[serde(default)]
    pub encrypted_data: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LoginResponse {
    pub token: String,
    pub user_id: u64,
    pub openid: String,
    pub is_new_user: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RefreshRequest {
    pub token: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RefreshResponse {
    pub token: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ScanResolveRequest {
    pub code: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ScanPortRequest {
    pub port_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ScanStartRequest {
    pub port_id: String,
    pub quote_id: Option<String>,
    pub estimated_kwh: String,
    pub estimated_minutes: i64,
    #[serde(default)]
    pub coupon_grant_id: Option<u64>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ScanStartResponse {
    pub order_no: String,
    pub payment_order_no: String,
    pub hold_expires_at: String,
    pub payment_params: serde_json::Value,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ScanCancelRequest {
    pub order_no: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChargeStopRequest {
    pub order_no: String,
}

// ===== 内部 DTO:charge_stop → gateway 的请求体 =====

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

// ===== DTO:订单详情 / 历史 / 反馈 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChargeHistoryItem {
    pub order_no: String,
    pub status: String,
    pub total_cents: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub started_at: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ended_at: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChargeHistoryResponse {
    pub page: u32,
    pub page_size: u32,
    pub items: Vec<ChargeHistoryItem>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OrderListResponse {
    #[serde(default)]
    pub items: Vec<ChargeDetailResponse>,
}

impl OrderListResponse {
    pub fn empty() -> Self { Self { items: vec![] } }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChargeDetailResponse {
    pub order_no: String,
    pub status: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub electric_cents: Option<i64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub service_cents: Option<i64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub total_cents: Option<i64>,
    pub device_id: String,
    pub port_no: u8,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub started_at: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ended_at: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub failure_reason: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FeedbackRequest {
    #[serde(default)]
    pub rating: Option<u8>,
    pub category: String,
    #[serde(default)]
    pub content: Option<String>,
    #[serde(default)]
    pub images: Option<Vec<String>>,
}

// ===== DTO:个人中心 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProfileResponse {
    pub user_id: u64,
    pub openid: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub nickname: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub avatar_url: Option<String>,
    pub gender: String,
    pub balance_cents: i64,
}

// ===== 内部 DTO:start-result / 跨服务 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StartResultRequest {
    pub order_no: String,
    pub success: bool,
    #[serde(default)]
    pub error: Option<String>,
}

// ===== 内部 DTO:scan_start 调 billing 用的 Quote 请求 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BillingQuoteRequest {
    pub port_id: String,
    pub user_id: u64,
    pub estimated_minutes: i64,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn paths_match_legacy() {
        assert_eq!(paths::AUTH_LOGIN, "/api/v1/public/auth/login");
        assert_eq!(paths::USER_SCAN_START, "/api/v1/user/scan/start");
        assert_eq!(paths::USER_CHARGE_DETAIL, "/api/v1/user/charge/:order_id");
        assert_eq!(paths::INTERNAL_START_RESULT, "/api/v1/internal/charge-orders/:order_id/start-result");
    }

    #[test]
    fn login_request_serde() {
        let r = LoginRequest { code: "abc".into(), iv: None, encrypted_data: None };
        let s = serde_json::to_string(&r).unwrap();
        let back: LoginRequest = serde_json::from_str(&s).unwrap();
        assert_eq!(back.code, "abc");
    }

    #[test]
    fn charge_detail_response_skip_none() {
        let r = ChargeDetailResponse {
            order_no: "O1".into(),
            status: "paid".into(),
            electric_cents: Some(50),
            service_cents: None,
            total_cents: Some(50),
            device_id: "D1".into(),
            port_no: 1,
            started_at: None,
            ended_at: None,
            failure_reason: None,
        };
        let s = serde_json::to_string(&r).unwrap();
        assert!(s.contains("electric_cents"));
        assert!(!s.contains("service_cents"));
        assert!(!s.contains("started_at"));
    }
}
