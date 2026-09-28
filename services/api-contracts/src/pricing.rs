use serde::{Deserialize, Serialize};

#[derive(Debug,Clone,Serialize,Deserialize)]
pub struct FeeResult {
    pub calculation_no:String,
    pub source:MeteredOrder,
    pub electric_cents:i64,
    pub service_cents:i64,
    pub total_cents:i64,
}

#[derive(Debug,Clone,Serialize,Deserialize)]
pub struct MeteredOrder {
    pub charge_order_id:u64,
    pub order_no:String,
    pub user_id:u64,
    pub started_at:chrono::DateTime<chrono::Utc>,
    pub meter:crate::ChargeEndMeter,
    pub quote:PriceQuote,
}

#[derive(Debug,Clone,Serialize,Deserialize)]
pub struct DevicePricing {
    pub station_id:u64,
    pub station_name:String,
    pub rule_id:u64,
    pub name:String,
    pub version:u32,
    pub mode:String,
    /// ⚠️ 豁免:DB 列 `pricing_rule.time_of_use_json`(JSON 列,方案 §三 正用途第 2 类)。
    /// 表结构本身由后台配置(admin 写入、billing/user/admin 各家解析),
    /// 契约层无权威 schema,类型化等于替全部服务固化一套结构。
    pub time_of_use:crate::OpaqueJson,
    pub service_fee_cents_per_kwh:i64,
    pub service_fee_cents_per_min:i64,
    pub min_charge_cents:i64,
}

#[derive(Debug,Clone,Serialize,Deserialize)]
pub struct PriceQuote {
    #[serde(flatten)]
    pub amount:crate::QuoteResponse,
    pub pricing:DevicePricing,
    pub estimated_kwh:String,
    pub estimated_minutes:i64,
    pub quote_expires_at:String,
    pub estimation_basis:String,
}
