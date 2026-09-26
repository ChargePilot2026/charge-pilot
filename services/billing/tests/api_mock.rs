//! billing API mock 集成测试 —— 不依赖真实数据库
//!
//! 覆盖:
//!   - DTO 序列化往返
//!   - 路径常量冻结
//!   - 计费 + 分账纯函数组合

use billing::api_types;
use billing::engine;
use billing::split;
use serde_json;

// billing 是 bin crate,集成测试通过 `pub use` 暴露的子模块需在 lib.rs 重新导出;
// 这里通过 [lib] / [lib] name = "billing" 暴露。
// 不过当前 Cargo.toml 只有 [[bin]],所以这里只测试那些能从外部 crate 调用的类型。
// 若 CI 需要在 lib 里跑测试,改为双 target [[bin]] + [lib]。

#[test]
fn quote_response_serde() {
    let r = api_types::QuoteResponse { total_cents: 100, electric_cents: 64, service_cents: 36 };
    let s = serde_json::to_string(&r).unwrap();
    let back: api_types::QuoteResponse = serde_json::from_str(&s).unwrap();
    assert_eq!(back.total_cents, 100);
}

#[test]
fn paths_match_legacy_spec() {
    assert_eq!(api_types::paths::QUOTE, "/api/v1/internal/quote");
    assert_eq!(api_types::paths::CALCULATE, "/api/v1/internal/calculate");
    assert_eq!(api_types::paths::FEE_BREAKDOWN, "/api/v1/internal/orders/:order_id/fee-breakdown");
    assert_eq!(api_types::paths::SPLIT, "/api/v1/internal/split");
    assert_eq!(api_types::paths::ORDER_SPLIT, "/api/v1/internal/orders/:order_id/split");
    assert_eq!(api_types::paths::SETTLEMENT_DETAIL, "/api/v1/internal/settlements/:settlement_id");
    assert_eq!(api_types::paths::INVOICE_SETTLE_DETAIL, "/api/v1/internal/invoices/:invoice_id/settle-detail");
    assert_eq!(api_types::paths::REFUND_CALC, "/api/v1/internal/refunds/:refund_id/calc");
    assert_eq!(api_types::paths::WITHDRAW_REQUESTS, "/api/v1/internal/withdraw-requests");
}

#[test]
fn envelope_shape_invariant() {
    let env = common_error::ApiEnvelope::ok(api_types::QuoteResponse { total_cents: 1, electric_cents: 1, service_cents: 0 }, "rid-1");
    let v: serde_json::Value = serde_json::to_value(&env).unwrap();
    assert_eq!(v["code"], 0);
    assert_eq!(v["message"], "ok");
    assert_eq!(v["request_id"], "rid-1");
    assert_eq!(v["data"]["total_cents"], 1);
}

#[test]
fn calculate_fee_then_split() {
    // 完整业务链路 mock:算费 → 分账
    let rule = engine::PricingRule::default_default();
    let fee = engine::calculate_fee_compat(&rule, 2.0, 120, 1.2, 0.8);
    // 1.2*80 + 0.8*40 = 96 + 32 = 128;service = 2.0*30 = 60;raw = 188 (> 100)
    assert_eq!(fee.electric_cents, 128);
    assert_eq!(fee.service_cents, 60);
    assert_eq!(fee.total_cents, 188);

    // 全分账(模式 A):三方各 3334/3333/3333
    let parties = vec![
        split::Party { id: 1, party_code: "platform".into(), ratio_bp: 5000 },
        split::Party { id: 2, party_code: "operator".into(), ratio_bp: 3000 },
        split::Party { id: 3, party_code: "site".into(), ratio_bp: 2000 },
    ];
    let alloc = split::split_pool(fee.total_cents, &parties);
    assert_eq!(alloc.iter().map(|x| x.1).sum::<i64>(), fee.total_cents);
    assert_eq!(alloc[0].1, 94); // 188 * 5000 / 10000 = 94
    assert_eq!(alloc[2].1, fee.total_cents - 94 - 56); // 尾差归最后
}