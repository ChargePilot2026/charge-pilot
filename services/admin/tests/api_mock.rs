//! admin API mock 集成测试 —— 不依赖真实数据库

use admin::api_types;
use serde_json;

#[test]
fn admin_login_round_trip() {
    let r = api_types::AdminLoginRequest {
        username: "admin".into(),
        password: "secret".into(),
    };
    let s = serde_json::to_string(&r).unwrap();
    let back: api_types::AdminLoginRequest = serde_json::from_str(&s).unwrap();
    assert_eq!(back.username, "admin");
}

#[test]
fn admin_login_response_round_trip() {
    let r = api_types::AdminLoginResponse {
        token: "jwt-a".into(),
        admin_user_id: 7,
        role: "customer_finance".into(),
        permissions: vec!["billing.read".into(), "billing.write".into()],
    };
    let s = serde_json::to_string(&r).unwrap();
    let back: api_types::AdminLoginResponse = serde_json::from_str(&s).unwrap();
    assert_eq!(back.admin_user_id, 7);
    assert_eq!(back.permissions.len(), 2);
}

#[test]
fn user_create_request_skip_none() {
    let r = api_types::UserCreateRequest {
        username: "alice".into(),
        display_name: Some("Alice".into()),
        password: "secret".into(),
        phone: None,
        email: Some("a@x.com".into()),
        role_id: None,
    };
    let s = serde_json::to_string(&r).unwrap();
    assert!(s.contains("alice"));
    assert!(s.contains("Alice"));
    assert!(!s.contains("\"phone\""));
    assert!(!s.contains("\"role_id\""));
}

#[test]
fn paths_match_legacy() {
    assert_eq!(api_types::paths::AUTH_LOGIN, "/api/v1/admin/auth/login");
    assert_eq!(api_types::paths::ADMIN_USERS, "/api/v1/admin/users");
    assert_eq!(api_types::paths::ADMIN_USER_DETAIL, "/api/v1/admin/users/:id");
    assert_eq!(api_types::paths::ADMIN_ORDERS, "/api/v1/admin/orders");
    assert_eq!(api_types::paths::ADMIN_BILLING_INVOICES, "/api/v1/admin/billing/invoices");
    assert_eq!(api_types::paths::ADMIN_EXPORT, "/api/v1/admin/export");
    assert_eq!(api_types::paths::ADMIN_OTA_PACKAGES, "/api/v1/admin/ota/packages");
}

#[test]
fn risk_config_skip_none() {
    let r = api_types::RiskConfig {
        max_concurrent_per_user: Some(3),
        suspicious_temperature_c: None,
        auto_refund_threshold_cents: Some(100),
        extra: None,
    };
    let s = serde_json::to_string(&r).unwrap();
    assert!(s.contains("max_concurrent_per_user"));
    assert!(!s.contains("suspicious_temperature_c"));
    assert!(!s.contains("\"extra\""));
}

#[test]
fn envelope_consistency() {
    let env = common_error::ApiEnvelope::<api_types::AdminLoginResponse>::err(1001, "未授权", "rid-3");
    let v: serde_json::Value = serde_json::to_value(&env).unwrap();
    assert_eq!(v["code"], 1001);
    assert_eq!(v["message"], "未授权");
}