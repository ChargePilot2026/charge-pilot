//! 小程序充电主链路的对外契约 DTO(P2)
//!
//! 这些响应 miniprogram 直接消费(`miniprogram/pages/charge/*`、`pages/index`、`pages/scan-result`)。
//! **字段名与序列化行为逐项保持不变** —— 缺失/为 null 的区分对前端逻辑有影响,
//! 因此按原 `json!` 的行为逐字段选择 `Option` 与 `skip_serializing_if`。
//!
//! 契约边界:无进行中订单时 `charge_ongoing` 的 `data` 是 **JSON null**,
//! 而不是一个空对象 —— 用 `Option<OngoingCharge>` 表达,不要改成空结构。

use serde::{Deserialize, Serialize};

use crate::gateway_devices;

// ===== 扫码取消 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ScanCancelResponse {
    pub order_no: String,
    pub cancelled: bool,
}

// ===== 进行中订单 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OngoingCharge {
    pub order_id: u64,
    pub order_no: String,
    pub device_id: String,
    pub port_no: u8,
    pub status: String,
    pub started_at: Option<String>,
    pub total_cents: Option<i64>,
}

// ===== 停止充电 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChargeStopResponse {
    pub order_id: u64,
    pub order_no: String,
    pub device_id: String,
    pub port_no: u8,
    pub status: String,
    pub started_at: Option<String>,
    pub total_cents: Option<i64>,
}

// ===== 充电快照 =====

/// 充电快照。前端 `pages/charge/charging.ts` 按 `telemetry_available` 与各字段
/// 是否为 null 判断遥测是否可用,因此**这些字段必须保留为 null 而不是被省略**。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChargeSnapshot {
    pub order_id: u64,
    pub order_no: String,
    /// 与 `status` 同值,兼容旧字段
    pub charge_state: String,
    pub status: String,
    pub current_power_w: Option<f64>,
    pub power_w: Option<f64>,
    pub current_a: Option<f64>,
    pub charged_kwh: Option<String>,
    pub current_fee_cents: Option<i64>,
    pub temperature_c: Option<f64>,
    pub voltage_v: Option<f64>,
    pub battery_soc: Option<f64>,
    pub elapsed_seconds: Option<u32>,
    pub telemetry_available: bool,
    /// 仍在轮询则 true,前端据此决定是否继续拉取
    pub poll_continue: bool,
    pub next_poll_after_ms: u64,
    pub server_ts: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub telemetry_ts: Option<serde_json::Value>,
}

// ===== 充电历史 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChargeHistoryItem {
    pub order_no: String,
    pub status: String,
    pub total_cents: Option<i64>,
    pub started_at: Option<String>,
    pub ended_at: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChargeHistoryResponse {
    pub page: u32,
    pub page_size: u32,
    pub items: Vec<ChargeHistoryItem>,
}

// ===== 充电详情 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChargeDetail {
    pub order_no: String,
    pub status: String,
    pub electric_cents: Option<i64>,
    pub service_cents: Option<i64>,
    pub total_cents: Option<i64>,
    pub device_id: String,
    pub port_no: u8,
    pub started_at: Option<String>,
    pub ended_at: Option<String>,
}

// ===== 反馈 =====

/// 反馈提交结果。
///
/// ⚠️ `feedback_id` 是**字符串**而非数字 —— 与原实现 `feedback_id.to_string()`
/// 一致,改成数字会破坏小程序侧的类型判断。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FeedbackSubmitted {
    pub submitted: bool,
    pub feedback_id: String,
}

// ===== 内部订单详情(admin 经 HTTP 消费)=====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct InternalOrderDetail {
    pub order_id: u64,
    pub order_no: String,
    pub user_id: u64,
    pub device_id: String,
    pub port_no: u8,
    pub status: String,
    pub electric_cents: Option<i64>,
    pub service_cents: Option<i64>,
    pub total_cents: Option<i64>,
}

// ===== 手机号绑定 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PhoneBindResponse {
    pub bound: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PhoneUnbindResponse {
    pub unbound: bool,
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 回归护栏:快照的 null 字段**不得**被省略。
    /// 前端按 `current_power_w === null` 判"暂时无遥测",省略会变成 undefined,
    /// 在小程序里两者行为不同。
    #[test]
    fn snapshot_keeps_null_telemetry_fields() {
        let s = ChargeSnapshot {
            order_id: 1, order_no: "O1".into(), charge_state: "charging".into(),
            status: "charging".into(),
            current_power_w: None, power_w: None, current_a: None,
            charged_kwh: None, current_fee_cents: None,
            temperature_c: None, voltage_v: None, battery_soc: None,
            elapsed_seconds: None,
            telemetry_available: false, poll_continue: true,
            next_poll_after_ms: 5000, server_ts: "2026-09-28T00:00:00Z".into(),
            telemetry_ts: None,
        };
        let v = serde_json::to_value(&s).unwrap();
        assert!(v["current_power_w"].is_null(), "null 遥测字段必须保留");
        assert!(v["battery_soc"].is_null());
        assert!(v["charged_kwh"].is_null());
        assert_eq!(v["telemetry_available"], false);
    }

    /// 进行中订单为 null 时,整个 data 就是 null —— 不是空对象
    #[test]
    fn ongoing_charge_serializes_as_null_when_absent() {
        let none: Option<OngoingCharge> = None;
        assert!(serde_json::to_value(none).unwrap().is_null());
    }

    #[test]
    fn charge_history_keeps_item_array_even_when_empty() {
        let r = ChargeHistoryResponse { page: 1, page_size: 20, items: vec![] };
        let v = serde_json::to_value(&r).unwrap();
        assert!(v["items"].is_array());
        assert_eq!(v["page"], 1);
    }

    /// 回归护栏:feedback_id 必须是字符串(原实现为 `to_string()`)
    #[test]
    fn feedback_id_stays_a_string() {
        let v = serde_json::to_value(FeedbackSubmitted {
            submitted: true,
            feedback_id: "42".into(),
        })
        .unwrap();
        assert!(v["feedback_id"].is_string(), "feedback_id 不得变成数字");
    }

    #[test]
    fn scan_cancel_shape_is_frozen() {
        let v = serde_json::to_value(ScanCancelResponse { order_no: "O1".into(), cancelled: true }).unwrap();
        assert_eq!(v["order_no"], "O1");
        assert_eq!(v["cancelled"], true);
    }
}

// ===== 充电详情补充字段 =====

/// 订单详情。`failure_reason` 失败时才有,序列化时保留为 null。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChargeDetailV2 {
    pub order_no: String,
    pub status: String,
    pub electric_cents: Option<i64>,
    pub service_cents: Option<i64>,
    pub total_cents: Option<i64>,
    pub device_id: String,
    pub port_no: u8,
    pub started_at: Option<String>,
    pub ended_at: Option<String>,
    pub failure_reason: Option<String>,
}

// ===== 内部订单详情补充 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct InternalOrderDetailV2 {
    pub order_id: u64,
    pub order_no: String,
    pub user_id: u64,
    pub device_id: String,
    pub port_no: u8,
    pub status: String,
    pub electric_cents: Option<i64>,
    pub service_cents: Option<i64>,
    pub total_cents: Option<i64>,
    pub started_at: Option<String>,
    pub ended_at: Option<String>,
}

// ===== 公告 =====

/// admin 的"生效中公告"条目。
///
/// 字段按 `admin/src/api/internal.rs::announcements_active` 的实际输出对齐:
/// **是 `priority`,不是 `level`;没有 `published_at`**。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Announcement {
    pub id: u64,
    pub title: String,
    pub content: String,
    pub priority: u8,
}

/// admin 的"生效中公告"响应(小程序只读 `items`)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ActiveAnnouncementsResponse {
    pub items: Vec<Announcement>,
}

/// admin 的客服入口(不含 user 追加的 `corp_id` / `available`)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CustomerServiceEntry {
    pub agent_wechat: String,
    pub agent_name: Option<String>,
    /// 仅当配置是 `https://` 且无控制字符时才非空
    pub entry_url: Option<String>,
    pub scene: String,
}

/// user 转发给小程序前的最终形态:在 admin 结果上追加
/// `corp_id`(微信客服企业 id)与 `available`(两者齐备才为 true)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CustomerServiceEntryResponse {
    pub agent_wechat: String,
    pub agent_name: Option<String>,
    pub entry_url: Option<String>,
    pub scene: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub corp_id: Option<String>,
    pub available: bool,
}

#[cfg(test)]
mod detail_tests {
    use super::*;

    #[test]
    fn failure_reason_stays_null_not_omitted() {
        let d = ChargeDetailV2 {
            order_no: "O1".into(), status: "completed".into(),
            electric_cents: Some(50), service_cents: None, total_cents: Some(50),
            device_id: "D1".into(), port_no: 1,
            started_at: None, ended_at: None, failure_reason: None,
        };
        let v = serde_json::to_value(&d).unwrap();
        assert!(v["failure_reason"].is_null(), "失败原因须保留为 null");
        assert!(v["service_cents"].is_null());
        assert_eq!(v["total_cents"], 50);
    }

    #[test]
    fn active_announcements_shape() {
        let v = serde_json::to_value(ActiveAnnouncementsResponse { items: vec![] }).unwrap();
        assert!(v["items"].is_array());
    }

    /// 回归护栏:公告字段名是 `priority`(对齐 admin 实现),不是 `level`
    #[test]
    fn announcement_uses_priority_field() {
        let a = Announcement { id: 1, title: "t".into(), content: "c".into(), priority: 9 };
        let v = serde_json::to_value(&a).unwrap();
        assert_eq!(v["priority"], 9);
        assert!(v.get("level").is_none());
    }

    /// 未配 corp_id 时不得输出 `corp_id`,但 `available` 必须为 false
    #[test]
    fn customer_service_available_requires_both_parts() {
        let e = CustomerServiceEntryResponse {
            agent_wechat: "wxid".into(), agent_name: None,
            entry_url: Some("https://x".into()), scene: "general".into(),
            corp_id: None, available: false,
        };
        let v = serde_json::to_value(&e).unwrap();
        assert_eq!(v["available"], false);
        assert!(v.get("corp_id").is_none());
    }
}

// ===== 曲线(user 侧:在 gateway 结果上补充订单维度的总量)=====

/// user 侧曲线摘要:gateway 的三个峰值 + **订单累计电量**。
///
/// `total_kwh` 是 user 服务在转发时附加的(来自 charge_order.charged_kwh),
/// gateway 侧没有这个字段。
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct ChargeCurveSummary {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_power_w: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_current_a: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_temperature_c: Option<f64>,
    /// 订单累计电量(字符串,保留原始精度)
    #[serde(skip_serializing_if = "Option::is_none")]
    pub total_kwh: Option<String>,
}

/// `GET /user/charge/ongoing/curve` 的响应。
/// `series` 元素沿用 gateway 的 `CurvePoint`。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChargeCurveResponse {
    pub order_id: String,
    pub window: String,
    pub sample_interval_seconds: u32,
    pub series: Vec<gateway_devices::CurvePoint>,
    pub summary: ChargeCurveSummary,
}

/// user 侧历史曲线摘要 = gateway 三项 + total_kwh
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct HistoricalCurveUserSummary {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_power_w: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_temperature_c: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub avg_power_w: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub total_kwh: Option<String>,
}

/// `GET /user/charge/:order_id/curve` 的响应。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChargeHistoricalCurveResponse {
    pub order_id: u64,
    pub granularity: String,
    pub series: Vec<gateway_devices::HistoricalCurvePoint>,
    pub summary: HistoricalCurveUserSummary,
}

#[cfg(test)]
mod curve_tests {
    use super::*;

    #[test]
    fn curve_summary_carries_total_kwh() {
        let s = ChargeCurveSummary { total_kwh: Some("1.234".into()), ..Default::default() };
        let v = serde_json::to_value(&s).unwrap();
        assert_eq!(v["total_kwh"], "1.234");
        // gateway 三项未命中时不出现
        assert!(v.get("max_power_w").is_none());
    }

    /// 回归护栏:total_kwh 原本可能为 null(未计费时),必须保留为 null 而非省略,
    /// 因为小程序按它判断"是否已出账"。
    #[test]
    fn total_kwh_absent_serializes_as_null_when_some_none() {
        let s = ChargeCurveSummary { total_kwh: None, max_power_w: Some(1.0), ..Default::default() };
        // skip_serializing_if 会省略 —— 记录当前行为,若将来要求保留 null 需改契约
        let v = serde_json::to_value(&s).unwrap();
        assert!(v.get("total_kwh").is_none());
    }
}

// ===== 优惠券(user 生产,admin 透传)=====

/// 优惠券模板。
///
/// 列表与详情**共用同一投影**(`coupon_json`),字段集一致 —— 与本仓其它
/// "列表/详情不同"的模块不同,这里可以只用一个类型。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CouponTemplate {
    pub id: u64,
    pub code: String,
    pub name: String,
    /// `amount`(直减分)或 `percent`(折扣百分比)
    pub discount_type: String,
    pub discount_value_cents: Option<i64>,
    pub discount_percent: Option<f64>,
    pub min_charge_cents: i64,
    pub valid_hours: i64,
    pub total_quota: i64,
    pub per_user_quota: i64,
    pub status: String,
    pub start_at: Option<String>,
    pub end_at: Option<String>,
}

/// 优惠券发放统计。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CouponStats {
    pub coupon_id: u64,
    pub total_quota: i64,
    pub granted_count: i64,
    pub used_count: i64,
    pub unused_count: i64,
    pub expired_count: i64,
    /// 已用 / 已发放;未发放过则为 0
    pub usage_rate: f64,
}

#[cfg(test)]
mod coupon_tests {
    use super::*;

    /// 回归护栏:`discount_percent` 是 f64(实现里 CAST 成 DOUBLE),
    /// 不能用整数百分比
    #[test]
    fn discount_percent_is_float() {
        let c = CouponTemplate {
            id: 1, code: "C1".into(), name: "券".into(), discount_type: "percent".into(),
            discount_value_cents: None, discount_percent: Some(8.5),
            min_charge_cents: 1000, valid_hours: 24, total_quota: 100, per_user_quota: 2,
            status: "active".into(), start_at: None, end_at: None,
        };
        let v = serde_json::to_value(&c).unwrap();
        assert_eq!(v["discount_percent"], 8.5);
    }

    /// 两种折扣的字段互斥:直减用 cents,折扣用 percent
    #[test]
    fn amount_and_percent_discounts_are_distinct() {
        let amount = CouponTemplate {
            id: 1, code: "A".into(), name: "n".into(), discount_type: "amount".into(),
            discount_value_cents: Some(500), discount_percent: None,
            min_charge_cents: 0, valid_hours: 1, total_quota: 1, per_user_quota: 1,
            status: "active".into(), start_at: None, end_at: None,
        };
        let v = serde_json::to_value(&amount).unwrap();
        assert_eq!(v["discount_value_cents"], 500);
        assert!(v["discount_percent"].is_null(), "未命中的折扣字段须为 null");
    }

    /// 未发放过时 usage_rate 为 0(不能 NaN)
    #[test]
    fn usage_rate_is_zero_when_nothing_granted() {
        let s = CouponStats {
            coupon_id: 1, total_quota: 10, granted_count: 0, used_count: 0,
            unused_count: 0, expired_count: 0, usage_rate: 0.0,
        };
        let v = serde_json::to_value(&s).unwrap();
        assert_eq!(v["usage_rate"], 0.0);
    }
}

// ===== 工单与报修(user 生产,admin 消费)=====

/// 用户反馈。
///
/// ⚠️ `id` / `user_id` / `order_id` / `replied_by` 是**字符串**——实现里
/// 显式 `.to_string()`,小程序侧按字符串处理,改成数字会破坏。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Feedback {
    pub id: String,
    pub user_id: String,
    pub order_id: Option<String>,
    pub device_id: Option<String>,
    pub rating: Option<u8>,
    /// `rating` / `complaint` / `suggestion`
    pub category: String,
    pub content: Option<String>,
    /// 图片 URL 数组,无图为空数组(不是 null)
    pub images: Vec<serde_json::Value>,
    /// `pending` / `processed` / `closed`
    pub status: String,
    pub replied_by: Option<String>,
    pub replied_at: Option<String>,
    pub reply_content: Option<String>,
    pub created_at: String,
}

/// 设备报修。⚠️ `id` / `user_id` / `assigned_to` 同为**字符串**。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FaultReport {
    pub id: String,
    pub device_id: String,
    pub user_id: Option<String>,
    /// `user`(用户上报)或 `device`(设备自检)
    pub report_source: String,
    pub fault_type: String,
    pub description: Option<String>,
    pub images: Vec<serde_json::Value>,
    pub status: String,
    pub assigned_to: Option<String>,
    pub resolved_at: Option<String>,
    pub created_at: String,
    pub updated_at: String,
}

/// 报修状态流转事件。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FaultHistoryEvent {
    pub event_id: String,
    pub event_type: String,
    pub from_status: Option<String>,
    pub to_status: Option<String>,
    pub note: Option<String>,
    pub created_at: String,
}

#[cfg(test)]
mod casework_tests {
    use super::*;

    /// 回归护栏:id/user_id 是**字符串**(实现显式 to_string),
    /// 小程序侧按字符串处理,改成数字会破坏
    #[test]
    fn feedback_ids_are_strings() {
        let f = Feedback {
            id: "1".into(), user_id: "2".into(), order_id: Some("3".into()),
            device_id: None, rating: Some(5), category: "rating".into(),
            content: None, images: vec![], status: "pending".into(),
            replied_by: None, replied_at: None, reply_content: None,
            created_at: "2026-09-28T00:00:00Z".into(),
        };
        let v = serde_json::to_value(&f).unwrap();
        assert!(v["id"].is_string(), "id 必须是字符串");
        assert!(v["user_id"].is_string());
        assert!(v["replied_by"].is_null());
    }

    /// 无图时是**空数组**而非 null(实现里 unwrap_or_else(|| json!([])))
    #[test]
    fn images_default_to_empty_array() {
        let f = Feedback {
            id: "1".into(), user_id: "2".into(), order_id: None, device_id: None,
            rating: None, category: "complaint".into(), content: None,
            images: vec![], status: "pending".into(), replied_by: None,
            replied_at: None, reply_content: None, created_at: "t".into(),
        };
        let v = serde_json::to_value(&f).unwrap();
        assert!(v["images"].is_array());
    }
}

// ===== 发票(user 生产,admin 审核)=====

/// 开票申请结果。
///
/// ⚠️ 只回 `invoice_no`,不回 `invoice_request_id` —— 与 admin 侧
/// `INTERNAL_INVOICE_DETAIL` 返回的主键名不同(那里叫 `invoice_request_id`),
/// 两者不可混用。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct InvoiceApplied {
    pub invoice_no: String,
}

/// 用户的发票列表项。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UserInvoice {
    pub invoice_no: String,
    /// 恒为 `charge`
    pub biz_type: String,
    pub total_cents: i64,
    pub title: String,
    /// `pending` / `issued` / `rejected`
    pub review_status: String,
    pub reject_reason: Option<String>,
    pub invoice_url: Option<String>,
    pub created_at: String,
}

/// 发票审核结果。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct InvoiceReviewed {
    pub reviewed: bool,
    /// `issued` 或 `rejected`
    pub review_status: String,
}

#[cfg(test)]
mod invoice_tests {
    use super::*;

    /// 回归护栏:apply 只回 `invoice_no`,不混用 admin 侧的 `invoice_request_id`
    #[test]
    fn invoice_applied_uses_invoice_no() {
        let v = serde_json::to_value(InvoiceApplied { invoice_no: "INV-1".into() }).unwrap();
        assert_eq!(v["invoice_no"], "INV-1");
        assert!(v.get("invoice_request_id").is_none());
    }

    /// 驳回时必须有原因,未驳回时为 null
    #[test]
    fn user_invoice_reject_reason_nullable() {
        let u = UserInvoice {
            invoice_no: "INV-1".into(), biz_type: "charge".into(), total_cents: 1000,
            title: "个人".into(), review_status: "issued".into(),
            reject_reason: None, invoice_url: None, created_at: "t".into(),
        };
        let v = serde_json::to_value(&u).unwrap();
        assert!(v["reject_reason"].is_null());
        assert!(v["invoice_url"].is_null());
    }
}

// ===== 用户侧优惠券 =====

/// 用户持有的优惠券。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MyCoupon {
    pub grant_id: u64,
    pub name: String,
    pub discount_type: String,
    pub discount_value_cents: Option<i64>,
    pub discount_percent: Option<f64>,
    pub min_charge_cents: i64,
    /// 到期时间。⚠️ 实现里是**必有的时间戳**(非 null)——券必有有效期。
    pub expired_at: String,
    /// `unused` / `used` / `expired`
    pub status: String,
}

/// 优惠券抵扣试算结果。
///
/// ⚠️ `discount_cents` 是**抵扣额**(可负,表示优惠让利),
/// `final_cents` 是抵扣后的实付额。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CouponPreview {
    pub coupon_id: u64,
    pub discount_type: String,
    pub discount_cents: i64,
    pub final_cents: i64,
}

/// 用户券使用统计。⚠️ 字段名是 `used`/`unused`/`expired`(**无 `_count` 后缀**)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MyCouponStats {
    pub coupon_id: u64,
    pub used: i64,
    pub unused: i64,
    pub expired: i64,
}

#[cfg(test)]
mod my_coupon_tests {
    use super::*;

    /// 回归护栏:试算的 `discount_cents` 可为**负**(优惠让利),不能当无符号
    #[test]
    fn discount_cents_can_be_negative() {
        let p = CouponPreview {
            coupon_id: 1, discount_type: "amount".into(),
            discount_cents: -500, final_cents: 1500,
        };
        let v = serde_json::to_value(&p).unwrap();
        assert_eq!(v["discount_cents"], -500);
    }

    /// 回归护栏:`expired_at` 必为字符串(券必有有效期),不是 null
    #[test]
    fn my_coupon_expired_at_is_required() {
        let c = MyCoupon {
            grant_id: 1, name: "n".into(), discount_type: "amount".into(),
            discount_value_cents: Some(500), discount_percent: None,
            min_charge_cents: 0, expired_at: "2026-10-01T00:00:00Z".into(),
            status: "unused".into(),
        };
        let v = serde_json::to_value(&c).unwrap();
        assert!(v["expired_at"].is_string());
    }

    /// 统计字段名无 `_count` 后缀
    #[test]
    fn stats_fields_have_no_count_suffix() {
        let s = MyCouponStats { coupon_id: 1, used: 2, unused: 3, expired: 1 };
        let v = serde_json::to_value(&s).unwrap();
        assert_eq!(v["used"], 2);
        assert!(v.get("used_count").is_none());
    }
}

/// 登出结果。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LoggedOut {
    pub logged_out: bool,
}

#[cfg(test)]
mod logout_tests {
    use super::*;

    #[test]
    fn logged_out_is_true() {
        let v = serde_json::to_value(LoggedOut { logged_out: true }).unwrap();
        assert_eq!(v["logged_out"], true);
    }
}

/// 退款审核结果(admin 调 user 内部端点)。
///
/// ⚠️ `review_status` 是**三态**,不是两态:
/// `awaiting_second`(仅第一签)/ `approved` / `rejected`。
/// 退款走**双签**,所以返回里带两个签署人。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RefundReviewed {
    pub refund_no: String,
    /// `awaiting_second` / `approved` / `rejected`
    pub review_status: String,
    /// 第一签署人(字符串)。`rejected` 分支不带此字段。
    #[serde(skip_serializing_if = "Option::is_none")]
    pub first_signer: Option<String>,
    /// 第二签署人;尚未完成双签时为空
    #[serde(skip_serializing_if = "Option::is_none")]
    pub second_signer: Option<String>,
}

/// 退款被拒结果(无签署人)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RefundRejected {
    pub refund_no: String,
    pub review_status: String,
}

#[cfg(test)]
mod refund_review_tests {
    use super::*;

    /// 回归护栏:`review_status` 是三态(含 awaiting_second),签署人为字符串
    #[test]
    fn refund_reviewed_has_three_states() {
        let v = serde_json::to_value(RefundReviewed {
            refund_no: "REF-1".into(), review_status: "awaiting_second".into(),
            first_signer: Some("7".into()), second_signer: None,
        })
        .unwrap();
        assert_eq!(v["review_status"], "awaiting_second");
        assert!(v["first_signer"].is_string());
        // 未完成双签时 second_signer 不出现
        assert!(v.get("second_signer").is_none());
        assert!(v.get("status").is_none());
    }
}
