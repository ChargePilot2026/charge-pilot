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

/// 单布尔确认。用于 `{"acked":true}` 这类"只回一个标志位"的确认型响应。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AckFlag {
    pub acked: bool,
}

impl AckFlag {
    pub fn new(acked: bool) -> Self {
        Self { acked }
    }
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

/// 通用列表响应(仅 `items`)。
///
/// 现存端点**两种形状都有**:
/// - 只回 `{items}`:roles / users / coupons / customer_service / announcements
/// - 回 `{items, total, page, page_size, permissions}`:stations(含分页与权限)
/// 因此提供 `ListResponse`(前者)与 `PagedResponse`(后者)两个类型,
/// **按现状固化**,不强行统一 —— 改形状会破坏 admin-web。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PagedResponse<T> {
    pub items: Vec<T>,
    pub total: i64,
    pub page: u32,
    pub page_size: u32,
    /// 调用者权限码,后台据此显示/隐藏操作按钮
    pub permissions: Vec<String>,
}
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
        assert!(v.get("total").is_none(), "非分页列表不应凭空多出 total");
    }

    /// 分页列表必须带 total/page/page_size/permissions
    #[test]
    fn paged_list_keeps_all_four_fields() {
        let p = PagedResponse::<u64> {
            items: vec![],
            total: 42,
            page: 2,
            page_size: 20,
            permissions: vec!["station.read".into()],
        };
        let v = serde_json::to_value(&p).unwrap();
        assert_eq!(v["total"], 42);
        assert_eq!(v["page"], 2);
        assert!(v["permissions"].is_array());
    }

    #[test]
    fn crud_acks_use_exact_field_names() {
        assert_eq!(serde_json::to_value(CreatedResponse { id: 7 }).unwrap()["id"], 7);
        assert_eq!(serde_json::to_value(UpdatedResponse::new()).unwrap()["updated"], true);
        assert_eq!(serde_json::to_value(DeletedResponse::new()).unwrap()["deleted"], true);
    }

    /// 回归护栏:字段名是 `acked` 不是 `ok`/`success`
    #[test]
    fn ack_flag_uses_acked_field() {
        let v = serde_json::to_value(AckFlag::new(true)).unwrap();
        assert_eq!(v["acked"], true);
        assert!(v.get("ok").is_none());
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

/// 单标志确认(键名不同于 `AckResponse`)。
///
/// 现有实现有两种并存的写法,按现状固化、**不强行统一**:
/// - `{"created":true}` / `{"reset":true}` —— admin 用户与密码端点
/// - `{"acked":true}` —— 告警确认端点
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CreatedFlag {
    pub created: bool,
}

impl CreatedFlag {
    pub fn new() -> Self {
        Self { created: true }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ResetFlag {
    pub reset: bool,
}

impl ResetFlag {
    pub fn new() -> Self {
        Self { reset: true }
    }
}

#[cfg(test)]
mod flag_tests {
    use super::*;

    /// 回归护栏:三种确认标志的键名各自独立,不可互换
    #[test]
    fn flag_field_names_are_distinct() {
        let created = serde_json::to_value(CreatedFlag::new()).unwrap();
        assert_eq!(created["created"], true);
        assert!(created.get("reset").is_none());
        assert!(created.get("ok").is_none());

        let reset = serde_json::to_value(ResetFlag::new()).unwrap();
        assert_eq!(reset["reset"], true);
        assert!(reset.get("created").is_none());

        let acked = serde_json::to_value(AckFlag::new(true)).unwrap();
        assert_eq!(acked["acked"], true);
        assert!(acked.get("created").is_none());
    }
}
