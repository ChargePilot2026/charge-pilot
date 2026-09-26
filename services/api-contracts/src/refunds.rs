use serde::{Deserialize, Serialize};
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
