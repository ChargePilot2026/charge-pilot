//! user API mock 集成测试 —— 不依赖真实数据库
//!
//! 覆盖:
//!   - DTO 序列化往返
//!   - 路径常量冻结
//!   - envelope 一致性

use serde_json;
use user::api_types;

#[test]
fn login_request_round_trip() {
    let r = api_types::LoginRequest {
        code: "code-123".into(),
        iv: None,
        encrypted_data: None,
    };
    let s = serde_json::to_string(&r).unwrap();
    let back: api_types::LoginRequest = serde_json::from_str(&s).unwrap();
    assert_eq!(back.code, "code-123");
}

#[test]
fn login_response_round_trip() {
    let r = api_types::LoginResponse {
        token: "jwt-x".into(),
        user_id: 42,
        openid: "o-x".into(),
        is_new_user: true,
    };
    let s = serde_json::to_string(&r).unwrap();
    let back: api_types::LoginResponse = serde_json::from_str(&s).unwrap();
    assert!(back.is_new_user);
    assert_eq!(back.user_id, 42);
}

#[test]
fn scan_start_response_skip_none() {
    let r = api_types::ScanStartResponse {
        order_no: "O1".into(),
        payment_order_no: "P1".into(),
        hold_expires_at: "2030-01-01T00:00:00Z".into(),
        payment_params: serde_json::json!({"prepay_id": "p_1"}),
    };
    let s = serde_json::to_string(&r).unwrap();
    let back: api_types::ScanStartResponse = serde_json::from_str(&s).unwrap();
    assert_eq!(back.order_no, "O1");
    assert_eq!(back.payment_params["prepay_id"], "p_1");
}

#[test]
fn paths_match_legacy() {
    assert_eq!(api_types::paths::AUTH_LOGIN, "/api/v1/public/auth/login");
    assert_eq!(api_types::paths::USER_SCAN_START, "/api/v1/user/scan/start");
    assert_eq!(api_types::paths::USER_CHARGE_STOP, "/api/v1/user/charge/stop");
    assert_eq!(api_types::paths::USER_WALLET_BALANCE, "/api/v1/user/wallet/balance");
    assert_eq!(api_types::paths::USER_INVOICE_APPLY, "/api/v1/user/invoice/apply");
    assert_eq!(api_types::paths::INTERNAL_START_RESULT, "/api/v1/internal/charge-orders/:order_id/start-result");
}

#[test]
fn envelope_consistency() {
    let env: common_error::ApiEnvelope<String> = common_error::ApiEnvelope::ok("ok".into(), "rid-1");
    let v: serde_json::Value = serde_json::to_value(&env).unwrap();
    assert_eq!(v["code"], 0);
    assert_eq!(v["message"], "ok");
    assert_eq!(v["request_id"], "rid-1");
    assert_eq!(v["data"], "ok");
}

#[test]
fn error_envelope_consistency() {
    let env = common_error::ApiEnvelope::<()>::err(1001, "未授权", "rid-2");
    let v: serde_json::Value = serde_json::to_value(&env).unwrap();
    assert_eq!(v["code"], 1001);
    assert_eq!(v["message"], "未授权");
    assert!(v["data"].is_null());
}

#[test]
fn charge_detail_skip_none() {
    let r = api_types::ChargeDetailResponse {
        order_no: "O1".into(),
        status: "paid".into(),
        electric_cents: Some(50),
        service_cents: None,
        total_cents: Some(50),
        device_id: "D1".into(),
        port_no: 1,
        started_at: Some("2026-01-01T00:00:00Z".into()),
        ended_at: None,
        failure_reason: None,
    };
    let s = serde_json::to_string(&r).unwrap();
    assert!(s.contains("electric_cents"));
    assert!(!s.contains("service_cents"));
    assert!(!s.contains("ended_at"));
    assert!(!s.contains("failure_reason"));
}