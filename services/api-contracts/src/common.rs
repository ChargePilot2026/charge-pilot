//! 跨服务复用的通用响应形状(P2)
//!
//! 这些形状在 user/admin 两侧反复出现(`{"ok":true}`、幂等短路返回
//! `{"status":…,"already_processed":true}`、列表 `{"items":[…]}`),
//! 各自 `json!` 拼装。抽成契约后,字段名与语义由测试锁死,不再靠复制。

use serde::{Deserialize, Serialize};

/// 纯确认型响应。用于"接收即成功、无需回传业务数据"的内部回调端点。
///
/// 字段是 `ok`(不是 `success`),与原实现一致。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AckResponse {
    pub ok: bool,
}

impl AckResponse {
    pub fn new() -> Self {
        Self { ok: true }
    }
}

/// 幂等处理结果。
///
/// 内部消息消费端点普遍**先查回执**:已处理过就直接原样返回上一轮的结果,
/// 并带 `already_processed: true` —— 消费者据此不会重复执行副作用。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProcessedResponse {
    pub status: String,
    pub already_processed: bool,
}

/// 派单结果(带受理人)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DispatchedResponse {
    pub status: String,
    pub assigned_to: String,
    pub already_processed: bool,
}

/// 创建成功:回传新记录 id。
///
/// PC 后台多处创建端点都是这个形状(公告 / 客服配置 / 角色 / 站点 / 优惠券 …),
/// 逐个 `json!` 拼装。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CreatedResponse {
    pub id: u64,
}

/// 更新成功。注意是**软删除**语义下的"已更新",不返回行数。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UpdatedResponse {
    pub updated: bool,
}

/// 删除成功(软删除,`deleted_at` 置位)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DeletedResponse {
    pub deleted: bool,
}

impl UpdatedResponse {
    pub fn new() -> Self {
        Self { updated: true }
    }
}

impl DeletedResponse {
    pub fn new() -> Self {
        Self { deleted: true }
    }
}

/// 通用列表响应。
///
/// ⚠️ 技术规格 §7.5 期望的字段是 `total` / `page` / `page_size` / `data[]`,
/// 但现存端点多用 `{items}` 且不返回 `total`。本类型**按现状固化**,
/// 统一分页形状属独立议题,不在本轮改动 —— 改动会破坏前端。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ListResponse<T> {
    pub items: Vec<T>,
}

impl<T> ListResponse<T> {
    pub fn new(items: Vec<T>) -> Self {
        Self { items }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ack_uses_ok_field_not_success() {
        let v = serde_json::to_value(AckResponse::new()).unwrap();
        assert_eq!(v["ok"], true);
        assert!(v.get("success").is_none());
    }

    #[test]
    fn processed_reports_idempotent_replay() {
        let v = serde_json::to_value(ProcessedResponse {
            status: "processed".into(),
            already_processed: true,
        })
        .unwrap();
        assert_eq!(v["status"], "processed");
        assert_eq!(v["already_processed"], true);
    }

    /// 列表即使为空也必须有 `items` 数组 —— 前端按 `Array.isArray` 判断
    #[test]
    fn empty_list_still_has_items_array() {
        let v = serde_json::to_value(ListResponse::<u64>::new(vec![])).unwrap();
        assert!(v["items"].is_array());
    }

    #[test]
    fn crud_acks_use_exact_field_names() {
        assert_eq!(serde_json::to_value(CreatedResponse { id: 7 }).unwrap()["id"], 7);
        assert_eq!(serde_json::to_value(UpdatedResponse::new()).unwrap()["updated"], true);
        assert_eq!(serde_json::to_value(DeletedResponse::new()).unwrap()["deleted"], true);
    }

    #[test]
    fn dispatched_keeps_assigned_to_as_string() {
        let v = serde_json::to_value(DispatchedResponse {
            status: "dispatched".into(),
            assigned_to: "7".into(),
            already_processed: false,
        })
        .unwrap();
        assert_eq!(v["assigned_to"], "7");
    }
}
