//! admin 服务所有 API 路径常量 + DTO 类型集中定义
//!
//! 路径常量、请求/响应类型集中在 [`paths`] 和顶层结构体;
//! handler 只做参数提取 + 业务调用,禁止直接拼字符串或 `json!{}` 宏。

use serde::{Deserialize, Serialize};

pub mod paths {
    // --- 公开路由(无需鉴权)---
    pub const AUTH_LOGIN: &str = "/api/v1/admin/auth/login";
    pub const AUTH_REFRESH: &str = "/api/v1/admin/auth/refresh";

    // --- 内部路由(其他服务调用)---
    pub const INTERNAL_ANNOUNCEMENTS_ACTIVE: &str = "/api/v1/internal/announcements/active";
    pub const INTERNAL_STATIONS_NEARBY: &str = "/api/v1/internal/stations/nearby";
    pub const INTERNAL_STATIONS_DETAIL: &str = "/api/v1/internal/stations/:station_id";
    pub const INTERNAL_PRICING_RULES_GET: &str = "/api/v1/internal/pricing-rules/:id";
    pub const INTERNAL_SPLIT_TEMPLATES_GET: &str = "/api/v1/internal/split-templates/:id";
    pub const INTERNAL_EXPORT_TASK_GET: &str = "/api/v1/internal/export/tasks/:id";
    pub const INTERNAL_ALERTS_ACTIVE: &str = "/api/v1/internal/alerts";
    pub const INTERNAL_DEVICES_REBOOT: &str = "/api/v1/internal/devices/:id/reboot";

    // --- PC 后台路由(需 AdminClaims JWT)---
    pub const ADMIN_AUTH_LOGOUT: &str = "/api/v1/admin/auth/logout";
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

    pub const ADMIN_WHITELABEL: &str = "/api/v1/admin/whitelabel";

    pub const ADMIN_WEBHOOKS: &str = "/api/v1/admin/webhooks";
    pub const ADMIN_WEBHOOK_DETAIL: &str = "/api/v1/admin/webhooks/:id";
    pub const ADMIN_WEBHOOK_DELIVERIES: &str = "/api/v1/admin/webhooks/:id/deliveries";

    pub const ADMIN_OTA_PACKAGES: &str = "/api/v1/admin/ota/packages";
    pub const ADMIN_OTA_PACKAGE_DETAIL: &str = "/api/v1/admin/ota/packages/:id";
    pub const ADMIN_OTA_SCHEDULES: &str = "/api/v1/admin/ota/schedules";
    pub const ADMIN_OTA_SCHEDULE_DETAIL: &str = "/api/v1/admin/ota/schedules/:id";

    pub const ADMIN_EXPORT: &str = "/api/v1/admin/export";

    pub const HEALTH: &str = "/api/v1/health";
}

// ===== DTO:Admin Auth =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AdminLoginRequest {
    pub username: String,
    pub password: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AdminLoginResponse {
    pub token: String,
    pub admin_user_id: u64,
    pub role: String,
    pub permissions: Vec<String>,
}

// ===== DTO:Users =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UserCreateRequest {
    pub username: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub display_name: Option<String>,
    pub password: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub phone: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub email: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub role_id: Option<u64>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UserUpdateRequest {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub display_name: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub role_id: Option<u64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub status: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub phone: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub email: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ResetPasswordRequest {
    pub new_password: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AdminUserSummary {
    pub id: u64,
    pub username: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub display_name: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub role_id: Option<u64>,
    pub status: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub last_login_at: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub created_at: Option<String>,
}

// ===== DTO:Export =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ExportCreateRequest {
    pub export_type: String,
    #[serde(default)]
    pub period_start: Option<chrono::DateTime<chrono::Utc>>,
    #[serde(default)]
    pub period_end: Option<chrono::DateTime<chrono::Utc>>,
    #[serde(default)]
    pub filters_json: Option<serde_json::Value>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ExportCreateResponse {
    pub export_no: String,
    pub status: String,
}

// ===== DTO:Risk Config =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RiskConfig {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub max_concurrent_per_user: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub suspicious_temperature_c: Option<f64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub auto_refund_threshold_cents: Option<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub extra: Option<serde_json::Value>,
}

// ===== 通用响应 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CreatedIdResponse {
    pub id: u64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OkFlagResponse {
    pub ok: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ItemListResponse<T> {
    pub items: Vec<T>,
}

impl<T> ItemListResponse<T> {
    pub fn empty() -> Self { Self { items: vec![] } }
}

// ===== Internal =====

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
pub struct AlertsQuery {
    #[serde(default)]
    pub device_id: Option<String>,
    #[serde(default)]
    pub status: Option<String>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn paths_match_legacy() {
        assert_eq!(paths::AUTH_LOGIN, "/api/v1/admin/auth/login");
        assert_eq!(paths::ADMIN_USERS, "/api/v1/admin/users");
        assert_eq!(paths::ADMIN_EXPORT, "/api/v1/admin/export");
        assert_eq!(paths::ADMIN_BILLING_WITHDRAW, "/api/v1/admin/billing/withdraw");
    }

    #[test]
    fn user_create_request_skip_none() {
        let r = UserCreateRequest {
            username: "u1".into(),
            display_name: Some("alice".into()),
            password: "secret".into(),
            phone: None,
            email: None,
            role_id: None,
        };
        let s = serde_json::to_string(&r).unwrap();
        assert!(s.contains("alice"));
        assert!(!s.contains("phone"));
    }

    #[test]
    fn export_request_round_trip() {
        let r = ExportCreateRequest {
            export_type: "orders".into(),
            period_start: None,
            period_end: None,
            filters_json: None,
        };
        let s = serde_json::to_string(&r).unwrap();
        let back: ExportCreateRequest = serde_json::from_str(&s).unwrap();
        assert_eq!(back.export_type, "orders");
    }
}
