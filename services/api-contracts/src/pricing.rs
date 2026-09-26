use serde::{Deserialize, Serialize};

#[derive(Debug,Clone,Serialize,Deserialize)]
pub struct DevicePricing {
    pub station_id:u64,
    pub station_name:String,
    pub rule_id:u64,
    pub name:String,
    pub version:u32,
    pub mode:String,
    pub time_of_use:serde_json::Value,
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
