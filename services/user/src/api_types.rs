//! user 服务所有 API 路径常量 + DTO 类型集中定义
//!
//! 设计原则(对齐 老杨师傅 修订):
//!   - 所有路径 / 方法 / 请求体 / 响应体**先在这里集中定义**,handler 只做参数提取 + 业务调用。
//!   - 禁止在 handler 里直接拼字符串路径或 `serde_json::json!{}` 宏构造响应。
//!   - 跨服务调用统一走 [`crate::clients`],自动补充 service token + request id。

// ── 豁免:微信 `wx.requestPayment` 签名参数透传 ──
// `ScanStartResponse.payment_params` 是微信 SDK 要求的**小驼峰**定形载荷
// (`appId` / `timeStamp` / `nonceStr` / `package` / `signType` / `paySign`),
// 由 `common_wechat::sign_jsapi_pay` 生成后原样透传给小程序。字段集由微信定义,
// 不能改成具名 DTO(会破坏小驼峰契约),也不能复用 `api_contracts::charge::JsapiPaySign`
// —— 两者 serde 归一化方式不同,混用会让签名参数在两端解析不一致。
// 全文件仅此一处使用 `serde_json::Value`。
#![allow(clippy::disallowed_types)]

use serde::{Deserialize, Serialize};

// ===== 路径常量 =====

pub mod paths {
    // P2/E5:路径**唯一真源**是 `api-contracts::paths`。
    // 本模块改为再导出/别名,不再持有定义 —— 消除各服务私有副本。
    // 迁移期保留本模块是为了不改 131 处 `.route()` 注册(P3 再收口)。
    // 命名对齐:值与 `AUTH_LOGIN_USER` 相同,统一到契约名
    pub use api_contracts::paths::AUTH_LOGIN_USER as AUTH_LOGIN;
    pub use api_contracts::paths::AUTH_LOGOUT;
    // 命名对齐:值与 `AUTH_REFRESH_USER` 相同,统一到契约名
    pub use api_contracts::paths::AUTH_REFRESH_USER as AUTH_REFRESH;
    pub use api_contracts::paths::HEALTH;
    // 命名对齐:值与 `USER_INTERNAL_COUPON_STATS` 相同,统一到契约名
    pub use api_contracts::paths::USER_INTERNAL_COUPON_STATS as INTERNAL_COUPON_STATS;
    // 命名对齐:值与 `USER_INTERNAL_DASHBOARD_METRICS` 相同,统一到契约名
    pub use api_contracts::paths::USER_INTERNAL_DASHBOARD_METRICS as INTERNAL_DASHBOARD_METRICS;
    // 命名对齐:值与 `USER_INTERNAL_INVOICE_DETAIL` 相同,统一到契约名
    pub use api_contracts::paths::USER_INTERNAL_INVOICE_DETAIL as INTERNAL_INVOICE_DETAIL;
    // 命名对齐:值与 `USER_INTERNAL_ORDER_DETAIL` 相同,统一到契约名
    pub use api_contracts::paths::USER_INTERNAL_ORDER_DETAIL as INTERNAL_ORDER_DETAIL;
    // 命名对齐:值与 `USER_INTERNAL_PAYMENT_DETAIL` 相同,统一到契约名
    pub use api_contracts::paths::USER_INTERNAL_PAYMENT_DETAIL as INTERNAL_PAYMENT_DETAIL;
    // 命名对齐:值与 `USER_INTERNAL_REFUND_CLAIM` 相同,统一到契约名
    pub use api_contracts::paths::USER_INTERNAL_REFUND_CLAIM as INTERNAL_REFUND_CLAIM;
    // 命名对齐:值与 `USER_INTERNAL_REFUND_DETAIL` 相同,统一到契约名
    pub use api_contracts::paths::USER_INTERNAL_REFUND_DETAIL as INTERNAL_REFUND_DETAIL;
    // 命名对齐:值与 `USER_INTERNAL_REFUND_RESULT` 相同,统一到契约名
    pub use api_contracts::paths::USER_INTERNAL_REFUND_RESULT as INTERNAL_REFUND_RESULT;
    // 命名对齐:值与 `USER_INTERNAL_START_RESULT` 相同,统一到契约名
    pub use api_contracts::paths::USER_INTERNAL_START_RESULT as INTERNAL_START_RESULT;
    pub use api_contracts::paths::PAYMENT_WECHAT_CALLBACK;
    pub use api_contracts::paths::USER_ANNOUNCEMENT_LIST;
    pub use api_contracts::paths::USER_CHARGE_CURVE;
    pub use api_contracts::paths::USER_CHARGE_DETAIL;
    pub use api_contracts::paths::USER_CHARGE_FEEDBACK;
    pub use api_contracts::paths::USER_CHARGE_HISTORICAL_CURVE;
    pub use api_contracts::paths::USER_CHARGE_HISTORY;
    pub use api_contracts::paths::USER_CHARGE_ONGOING;
    pub use api_contracts::paths::USER_CHARGE_SNAPSHOT;
    pub use api_contracts::paths::USER_CHARGE_STOP;
    pub use api_contracts::paths::USER_COUPON_MY;
    pub use api_contracts::paths::USER_COUPON_PREVIEW;
    pub use api_contracts::paths::USER_CUSTOMER_SERVICE_ENTRY;
    pub use api_contracts::paths::USER_DEVICE_FAULT_HISTORY;
    pub use api_contracts::paths::USER_DEVICE_FAULT_REPORTS;
    pub use api_contracts::paths::USER_DEVICE_REPORT_FAULT;
    pub use api_contracts::paths::USER_INVOICE_APPLY;
    pub use api_contracts::paths::USER_INVOICE_MY;
    pub use api_contracts::paths::USER_PHONE_BIND;
    pub use api_contracts::paths::USER_PHONE_UNBIND;
    pub use api_contracts::paths::USER_PROFILE;
    pub use api_contracts::paths::USER_SCAN_CANCEL;
    pub use api_contracts::paths::USER_SCAN_PORT;
    pub use api_contracts::paths::USER_SCAN_QUOTE;
    pub use api_contracts::paths::USER_SCAN_RESOLVE;
    pub use api_contracts::paths::USER_SCAN_START;
    pub use api_contracts::paths::USER_STATION_DETAIL;
    pub use api_contracts::paths::USER_STATION_NEARBY;
    pub use api_contracts::paths::USER_WALLET_BALANCE;
    pub use api_contracts::paths::USER_WALLET_RECHARGE;
    pub use api_contracts::paths::USER_WALLET_RECHARGES;
    pub use api_contracts::paths::USER_WALLET_REFUND;
    pub use api_contracts::paths::USER_WALLET_TXNS;
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
    pub amount_cents:i64,
    pub order_no: String,
    pub payment_order_no: String,
    pub hold_expires_at: String,
    // 方案 §三 正用途:微信 `wx.requestPayment` 的签名参数由微信 SDK 定义
    // (appId / timeStamp / nonceStr / package / signType / paySign,小驼峰,
    //  不可改名),本服务只做 `to_value` 后原样透传给小程序。
    // 该字段与 `api_contracts::charge::JsapiPaySign` 形状相同,但**不能**直接
    // 复用那个类型:此处字段名是契约冻结的小驼峰,与仓内 snake_case 规范不同。
    #[serde(default)]
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

pub use api_contracts::{ChargeStopCommand,ChargeStopResponse};

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

pub use api_contracts::StartResultRequest;

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
