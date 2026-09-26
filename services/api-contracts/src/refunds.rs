use serde::{Deserialize, Serialize};
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ManualRefundRequest {pub request_id:String,pub amount_cents:i64,pub reason:String}
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct RefundQuery {
    pub page: Option<u32>,
    pub page_size: Option<u32>,
    pub status: Option<String>,
    pub refund_no: Option<String>,
}
impl RefundQuery {
    pub fn valid(&self)->bool {
        (1..=100000).contains(&self.page.unwrap_or(1)) && (1..=100).contains(&self.page_size.unwrap_or(20))
        && self.status.as_deref().is_none_or(|s|["pending","processing","success","failed","rejected"].contains(&s))
        && self.refund_no.as_deref().is_none_or(|s|!s.is_empty() && s.len()<=64 && s.bytes().all(|b|b.is_ascii_alphanumeric() || b==b'-' || b==b'_'))
    }
}
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ExecutionRequest {
    pub refund_no: String,
}
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ExecutionDetail {
    pub refund_no: String,
    pub status: String,
    pub transaction_id: String,
    pub refund_cents: i32,
    pub total_cents: i32,
    pub wechat_refund_id: Option<String>,
}
