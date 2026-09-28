//! admin 服务所有 API 路径常量 + DTO 类型集中定义
//!
//! 路径常量、请求/响应类型集中在 [`paths`] 和顶层结构体;
//! handler 只做参数提取 + 业务调用,禁止直接拼字符串或 `json!{}` 宏。
//!
//! **`serde_json::Value` 豁免理由**（方案 §三 例外清单第 2 类）：
//! 本文件的两处 `Value` 都是**数据库 JSON 列的原样透出**——
//! `filters_json`(导出任务的筛选条件)与 `risk_config.extra`(风控扩展项)
//! 都由运营在 PC 后台自由配置,**无固定 schema 可枚举**。
#![allow(clippy::disallowed_types)]

use serde::{Deserialize, Serialize};

pub mod paths {
    // P2/E5:路径**唯一真源**是 `api-contracts::paths`。
    // 本模块改为再导出/别名,不再持有定义 —— 消除各服务私有副本。
    // 迁移期保留本模块是为了不改 131 处 `.route()` 注册(P3 再收口)。
    pub use api_contracts::paths::ADMIN_ALERTS;
    pub use api_contracts::paths::ADMIN_ALERT_ACK;
    pub use api_contracts::paths::ADMIN_ALERT_RULES;
    pub use api_contracts::paths::ADMIN_ALERT_RULE_DETAIL;
    pub use api_contracts::paths::ADMIN_ALERT_SUBSCRIPTIONS;
    pub use api_contracts::paths::ADMIN_ANNOUNCEMENTS;
    pub use api_contracts::paths::ADMIN_ANNOUNCEMENT_DETAIL;
    pub use api_contracts::paths::ADMIN_AUTH_LOGOUT;
    pub use api_contracts::paths::ADMIN_BILLING_INVOICES;
    pub use api_contracts::paths::ADMIN_BILLING_INVOICE_APPROVE;
    pub use api_contracts::paths::ADMIN_BILLING_INVOICE_REJECT;
    pub use api_contracts::paths::ADMIN_BILLING_RECONCILE_LOGS;
    pub use api_contracts::paths::ADMIN_BILLING_REFUNDS;
    pub use api_contracts::paths::ADMIN_BILLING_REFUND_RETRY;
    pub use api_contracts::paths::ADMIN_BILLING_SETTLEMENTS;
    pub use api_contracts::paths::ADMIN_CHARGE_RULES;
    pub use api_contracts::paths::ADMIN_COUPONS;
    pub use api_contracts::paths::ADMIN_COUPON_DETAIL;
    pub use api_contracts::paths::ADMIN_COUPON_GRANTS;
    pub use api_contracts::paths::ADMIN_COUPON_STATS;
    pub use api_contracts::paths::ADMIN_CUSTOMER_SERVICE;
    pub use api_contracts::paths::ADMIN_CUSTOMER_SERVICE_DETAIL;
    pub use api_contracts::paths::ADMIN_DASHBOARD;
    pub use api_contracts::paths::ADMIN_DEVICES;
    pub use api_contracts::paths::ADMIN_DEVICE_DETAIL;
    pub use api_contracts::paths::ADMIN_DEVICE_FAULT_DISPATCH;
    pub use api_contracts::paths::ADMIN_DEVICE_FAULT_HISTORY;
    pub use api_contracts::paths::ADMIN_DEVICE_FAULT_REPORTS;
    pub use api_contracts::paths::ADMIN_DEVICE_FAULT_RESOLVE;
    pub use api_contracts::paths::ADMIN_DEVICE_IMPORTS;
    pub use api_contracts::paths::ADMIN_DEVICE_IMPORT_RETRY;
    pub use api_contracts::paths::ADMIN_DEVICE_ORDERS;
    pub use api_contracts::paths::ADMIN_EXPORT;
    pub use api_contracts::paths::ADMIN_EXPORT_DOWNLOAD;
    pub use api_contracts::paths::ADMIN_EXPORT_TASK;
    pub use api_contracts::paths::ADMIN_EXPORT_TASKS;
    pub use api_contracts::paths::ADMIN_FEEDBACK;
    pub use api_contracts::paths::ADMIN_FEEDBACK_REPLY;
    pub use api_contracts::paths::ADMIN_ORDERS;
    pub use api_contracts::paths::ADMIN_ORDER_DETAIL;
    pub use api_contracts::paths::ADMIN_ORDER_TIMELINE;
    pub use api_contracts::paths::ADMIN_OTA;
    pub use api_contracts::paths::ADMIN_OTA_PACKAGES;
    pub use api_contracts::paths::ADMIN_OTA_PACKAGE_DETAIL;
    pub use api_contracts::paths::ADMIN_OTA_SCHEDULES;
    pub use api_contracts::paths::ADMIN_OTA_SCHEDULE_DETAIL;
    pub use api_contracts::paths::ADMIN_PERMISSIONS;
    pub use api_contracts::paths::ADMIN_PRICING_TEMPLATES;
    pub use api_contracts::paths::ADMIN_RISK_CONFIG;
    pub use api_contracts::paths::ADMIN_ROLES;
    pub use api_contracts::paths::ADMIN_ROLE_DETAIL;
    pub use api_contracts::paths::ADMIN_SPLIT_TEMPLATES;
    pub use api_contracts::paths::ADMIN_SPLIT_TEMPLATE_PARTIES;
    pub use api_contracts::paths::ADMIN_STATIONS;
    pub use api_contracts::paths::ADMIN_STATION_DETAIL;
    pub use api_contracts::paths::ADMIN_USERS;
    pub use api_contracts::paths::ADMIN_USER_DETAIL;
    pub use api_contracts::paths::ADMIN_USER_RESET_PASSWORD;
    pub use api_contracts::paths::ADMIN_WEBHOOKS;
    pub use api_contracts::paths::ADMIN_WEBHOOK_DELIVERIES;
    pub use api_contracts::paths::ADMIN_WEBHOOK_DETAIL;
    pub use api_contracts::paths::ADMIN_WHITELABEL;
    // 命名对齐:值与 `AUTH_LOGIN_ADMIN` 相同,统一到契约名
    pub use api_contracts::paths::AUTH_LOGIN_ADMIN as AUTH_LOGIN;
    // 命名对齐:值与 `AUTH_REFRESH_ADMIN` 相同,统一到契约名
    pub use api_contracts::paths::AUTH_REFRESH_ADMIN as AUTH_REFRESH;
    pub use api_contracts::paths::HEALTH;
    // 命名对齐:值与 `ADMIN_INTERNAL_ALERTS_ACTIVE` 相同,统一到契约名
    pub use api_contracts::paths::ADMIN_INTERNAL_ALERTS_ACTIVE as INTERNAL_ALERTS_ACTIVE;
    // 命名对齐:值与 `ADMIN_INTERNAL_ANNOUNCEMENTS_ACTIVE` 相同,统一到契约名
    pub use api_contracts::paths::ADMIN_INTERNAL_ANNOUNCEMENTS_ACTIVE as INTERNAL_ANNOUNCEMENTS_ACTIVE;
    // 命名对齐:值与 `GW_DEVICE_REBOOT` 相同,统一到契约名
    pub use api_contracts::paths::GW_DEVICE_REBOOT as INTERNAL_DEVICES_REBOOT;
    // 命名对齐:值与 `ADMIN_INTERNAL_PRICING_RULES_GET` 相同,统一到契约名
    pub use api_contracts::paths::ADMIN_INTERNAL_PRICING_RULES_GET as INTERNAL_PRICING_RULES_GET;
    // 命名对齐:值与 `ADMIN_INTERNAL_SPLIT_TEMPLATES_GET` 相同,统一到契约名
    pub use api_contracts::paths::ADMIN_INTERNAL_SPLIT_TEMPLATES_GET as INTERNAL_SPLIT_TEMPLATES_GET;
    // 命名对齐:值与 `ADMIN_INTERNAL_STATIONS_DETAIL` 相同,统一到契约名
    pub use api_contracts::paths::ADMIN_INTERNAL_STATIONS_DETAIL as INTERNAL_STATIONS_DETAIL;
    // 命名对齐:值与 `ADMIN_INTERNAL_STATIONS_NEARBY` 相同,统一到契约名
    pub use api_contracts::paths::ADMIN_INTERNAL_STATIONS_NEARBY as INTERNAL_STATIONS_NEARBY;
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
