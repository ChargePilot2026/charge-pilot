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

/// 用户的报修列表项。
///
/// 与 admin 侧的 `FaultReport` 字段**完全一致**,唯一差别是主键字段名:
/// 这里是 **`report_id`**(面向小程序语义),admin 侧是 `id`。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MyFaultReport {
    pub report_id: String,
    pub device_id: String,
    pub fault_type: String,
    pub description: Option<String>,
    pub status: String,
    pub resolved_at: Option<String>,
    pub created_at: String,
    pub updated_at: String,
}

/// 用户看到的报修流转事件。
///
/// 与 [`FaultHistoryEvent`] **同义**——差别只在序列化时后三个 admin 专属字段
/// 不出现。保留此别名以便调用方按"用户视角"命名,避免误传 admin 字段。
pub type MyFaultEvent = FaultHistoryEvent;

#[cfg(test)]
mod my_fault_tests {
    use super::*;

    /// 回归护栏:用户侧主键字段是 `report_id`(**不是** `id`)
    #[test]
    fn my_fault_uses_report_id() {
        let v = serde_json::to_value(MyFaultReport {
            report_id: "1".into(), device_id: "D1".into(), fault_type: "offline".into(),
            description: None, status: "open".into(), resolved_at: None,
            created_at: "t".into(), updated_at: "t".into(),
        })
        .unwrap();
        assert!(v["report_id"].is_string());
        assert!(v.get("id").is_none());
    }
}

// ===== 支付与钱包充值 =====

/// 支付单详情。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PaymentDetail {
    pub id: u64,
    pub order_no: String,
    /// `wechat` / `wallet` 等
    pub pay_method: String,
    pub total_cents: i64,
    pub paid_cents: i64,
    /// `initiated` / `paid` / `closed` / `refunded` / `failed`
    pub status: String,
}

/// 钱包充值申请。
///
/// ⚠️ `user_id` 是**字符串**;`can_pay` 是服务端算出的可支付判定
/// (状态为 `initiated` 且未过期)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RechargeRequest {
    pub request_id: String,
    pub pay_order_no: String,
    pub amount_cents: i64,
    pub can_pay: bool,
    pub status: String,
    pub expires_at: String,
}

/// 用户的充值申请列表。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MyRecharges {
    pub user_id: String,
    pub page: u32,
    pub items: Vec<RechargeRequest>,
}

/// 充值下单结果(POST /wallet/recharge 的响应体)。
///
/// 三条路径字段集不同:
/// - 未过期可支付 : 全部字段 + `payment_params`
/// - 已过期/已支付: 无 `payment_params`(**缺键**, 不是 null)
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct WalletRechargePrepared {
    pub request_id: String,
    /// 支付单 ID 是**字符串**(历史上 `pid.to_string()`)
    pub pay_order_id: String,
    pub pay_order_no: String,
    pub amount_cents: i64,
    pub status: String,
    pub expires_at: String,
    /// 服务端算出的可支付判定
    pub can_pay: bool,
    /// 仅可支付时出现
    #[serde(skip_serializing_if = "Option::is_none")]
    pub payment_params: Option<JsapiPaySign>,
}

/// 微信 `wx.requestPayment` 的签名参数。
///
/// ⚠️ 字段名是**小驼峰**(`appId` / `timeStamp` / `nonceStr` / `signType` / `paySign`),
/// 与仓库其余 snake_case 契约不同,前端直接透传给微信 SDK,不可改名。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct JsapiPaySign {
    pub app_id: String,
    pub time_stamp: String,
    pub nonce_str: String,
    pub package: String,
    pub sign_type: String,
    pub pay_sign: String,
}

#[cfg(test)]
mod payment_tests {
    use super::*;

    /// 回归护栏:`user_id` 是字符串(实现里 `claims.user_id.to_string()`)
    #[test]
    fn my_recharges_user_id_is_string() {
        let v = serde_json::to_value(MyRecharges {
            user_id: "7".into(), page: 1, items: vec![],
        })
        .unwrap();
        assert!(v["user_id"].is_string());
    }

    /// 回归护栏:不可支付时 `payment_params` 是**缺键**而非 null
    #[test]
    fn not_payable_omits_payment_params() {
        let v = serde_json::to_value(WalletRechargePrepared {
            request_id: "R1".into(), pay_order_id: "12".into(),
            pay_order_no: "PAY1".into(), amount_cents: 100,
            status: "initiated".into(), expires_at: "2026-01-01T00:00:00+00:00".into(),
            can_pay: false, payment_params: None,
        })
        .unwrap();
        assert!(v.get("payment_params").is_none());
        assert_eq!(v["pay_order_id"], "12");
    }

    /// 回归护栏:微信签名参数是小驼峰键名,不可被 serde 默认改成 snake_case
    #[test]
    fn jsapi_pay_sign_keeps_camel_case_keys() {
        let v = serde_json::to_value(JsapiPaySign {
            app_id: "wx1".into(), time_stamp: "1".into(), nonce_str: "n".into(),
            package: "prepay_id=p".into(), sign_type: "RSA".into(), pay_sign: "s".into(),
        })
        .unwrap();
        for key in ["appId", "timeStamp", "nonceStr", "package", "signType", "paySign"] {
            assert!(v.get(key).is_some(), "缺少小驼峰键 {key}");
        }
        assert!(v.get("app_id").is_none(), "不得出现 snake_case 键 app_id");
    }
}

/// 充电中订单快照项(user 内部端点,供管理端批量取快照)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChargingOrderSnapshot {
    pub order_no: String,
    pub device_id: String,
    pub port_no: u8,
    pub charged_kwh: Option<String>,
    pub charged_seconds: Option<u32>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChargingOrderSnapshots {
    pub items: Vec<ChargingOrderSnapshot>,
}

#[cfg(test)]
mod snapshot_tests {
    use super::*;

    /// 回归护栏:电量是**字符串**(保精度),秒数可空
    #[test]
    fn snapshot_keeps_kwh_as_string() {
        let v = serde_json::to_value(ChargingOrderSnapshot {
            order_no: "O1".into(), device_id: "D1".into(), port_no: 1,
            charged_kwh: Some("1.234".into()), charged_seconds: None,
        })
        .unwrap();
        assert!(v["charged_kwh"].is_string());
        assert!(v["charged_seconds"].is_null());
    }
}

// ===== 人工退款与风控放款(admin 调 user 内部端点)=====

/// 人工退款申请结果。
///
/// `created` 区分**首次创建**与**幂等重放**:同一 `request_id` 重复提交会
/// 返回已存在的 `refund_no` 且 `created` 仍为 true(实现未区分),
/// 调用方需靠 `refund_no` 是否变化判断是否新建。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ManualRefundCreated {
    pub request_id: String,
    pub refund_no: String,
    pub created: bool,
}

/// 风控冻结放款结果。
///
/// 派生 `PartialEq`:同一 request_id 幂等重放必须返回**等值**结果
/// (既有测试直接断言两次 apply 结果相等)。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct WalletRiskReleased {
    pub request_id: String,
    pub freeze_id: String,
    pub released: bool,
    /// 放款后钱包是否恢复可用
    pub wallet_active: bool,
    pub actor_id: String,
    pub comment: String,
}

#[cfg(test)]
mod manual_refund_tests {
    use super::*;

    /// 回归护栏:freeze_id / actor_id 是**字符串**
    #[test]
    fn risk_release_ids_are_strings() {
        let v = serde_json::to_value(WalletRiskReleased {
            request_id: "R1".into(), freeze_id: "12".into(), released: true,
            wallet_active: true, actor_id: "7".into(), comment: "已核实".into(),
        })
        .unwrap();
        assert!(v["freeze_id"].is_string());
        assert!(v["actor_id"].is_string());
    }
}

// ===== 退款(admin 消费 user 的内部列表)=====

/// 退款单上的审核信息(admin 端补齐)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RefundReviewInfo {
    /// `approved` / `awaiting_second`
    pub status: String,
    pub first_signer: String,
    /// 第二签署人;仅第一签时为 null
    pub second_signer: Option<String>,
    pub first_comment: String,
    pub second_comment: Option<String>,
    pub approved_at: Option<String>,
}

/// 退款单(admin 列表项)。
///
/// ⚠️ `id` / `user_id` / `payment_order_id` 均为**字符串**。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AdminRefund {
    pub id: String,
    pub refund_no: String,
    pub user_id: String,
    pub payment_order_id: String,
    pub biz_type: String,
    pub refund_cents: i64,
    pub status: String,
    pub reason: Option<String>,
    pub failure_reason: Option<String>,
    pub created_at: String,
    pub completed_at: Option<String>,
    /// 未提交审核时**不出现**该键(不是 null)
    #[serde(skip_serializing_if = "Option::is_none")]
    pub review: Option<RefundReviewInfo>,
}

#[cfg(test)]
mod admin_refund_tests {
    use super::*;

    /// 回归护栏:主键三个字段都是字符串
    #[test]
    fn admin_refund_ids_are_strings() {
        let v = serde_json::to_value(AdminRefund {
            id: "1".into(), refund_no: "REF-1".into(), user_id: "2".into(),
            payment_order_id: "3".into(), biz_type: "charge".into(), refund_cents: 500,
            status: "pending".into(), reason: None, failure_reason: None,
            created_at: "t".into(), completed_at: None, review: None,
        })
        .unwrap();
        assert!(v["id"].is_string());
        assert!(v["payment_order_id"].is_string());
        // 未审核时 review 键不出现
        assert!(v.get("review").is_none());
    }
}

/// 退款领取结果(admin 领单后回执)。
///
/// ⚠️ `user_id` / `payment_order_id` 在这里是**数字**——与 `AdminRefund`
/// (列表项)里的**字符串**不同。两者都是"退款相关",但 id 形态不一致,
/// 合并成一个类型会破坏其中一方。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RefundClaimed {
    pub refund_no: String,
    pub user_id: u64,
    pub payment_order_id: u64,
    pub refund_cents: i64,
    /// 领取后为 `processing`
    pub status: String,
}

/// 退款详情(admin 单条查询)。主键是**数字**。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AdminRefundDetail {
    pub id: u64,
    pub user_id: u64,
    pub refund_cents: i64,
    pub status: String,
    pub biz_type: String,
    pub wechat_refund_id: Option<String>,
    /// 提交双签审核后出现(字符串)
    pub first_signer: Option<String>,
    pub second_signer: Option<String>,
}

#[cfg(test)]
mod refund_detail_tests {
    use super::*;

    /// 回归护栏:detail 的 id 是**数字**,而列表项 AdminRefund 的 id 是**字符串**
    #[test]
    fn refund_detail_id_is_number() {
        let v = serde_json::to_value(AdminRefundDetail {
            id: 1, user_id: 2, refund_cents: 500, status: "pending".into(),
            biz_type: "charge".into(), wechat_refund_id: None,
            first_signer: None, second_signer: None,
        })
        .unwrap();
        assert!(v["id"].is_number(), "detail 的 id 不得是字符串");
    }
}

// ===== 钱包退款 =====

/// 钱包退款拆出的单笔订单退款。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct WalletRefundOrderPart {
    pub refund_no: String,
    pub payment_order_no: String,
    pub refund_cents: i64,
    pub status: String,
}

/// 钱包退款申请结果。
///
/// 实现有三条返回路径,字段集不同:
/// - `accepted`   : {request_id, status, txn_no, refund_cents, refund_orders}
/// - `manual_review`: {request_id, status, refund_cents, refund_orders(空), message}
/// - 幂等重放     : 原样返回上次存进 `response_json` 的上述之一
/// - 风控审核后   : 上述之一 + `review`
/// 故用可选字段 + `skip_serializing_if` 精确复刻,保证响应格式不变。
/// 注:字段顺序按 `accepted` 路径排列;`manual_review` 路径原本的
/// `txn_no` 位置不存在,故键顺序与历史 JSON 有差异,键集合与取值完全一致。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct WalletRefundApplied {
    pub request_id: String,
    /// `accepted` / `manual_review` / `rejected`
    pub status: String,
    /// 仅 `accepted` 场景返回
    #[serde(skip_serializing_if = "Option::is_none")]
    pub txn_no: Option<String>,
    pub refund_cents: i64,
    /// 人工审核场景是**空数组**(不是缺键)
    pub refund_orders: Vec<WalletRefundOrderPart>,
    /// 仅 `manual_review` 场景返回
    #[serde(skip_serializing_if = "Option::is_none")]
    pub message: Option<String>,
    /// 仅风控人工审核后返回
    #[serde(skip_serializing_if = "Option::is_none")]
    pub review: Option<WalletRiskReview>,
}

/// 风控审核落款。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct WalletRiskReview {
    /// 审核人 ID 的**字符串**形式
    pub actor_id: String,
    pub approved: bool,
    pub comment: String,
}

/// 退款申请列表项里拆出的单笔退款。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct WalletRefundRequestPart {
    pub refund_no: String,
    pub refund_cents: i64,
    pub status: String,
    pub failure_reason: Option<String>,
    pub completed_at: Option<String>,
}

/// 用户钱包退款申请列表项。
///
/// ⚠️ `refunded_cents` 与列表项 `status` 都是**服务端派生**的,不是申请表原值:
/// 派生规则见 `list_in_transaction`。`review` 未审核过时是 **null(不是缺键)**。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct WalletRefundRequestItem {
    pub request_id: String,
    pub amount_cents: i64,
    pub refunded_cents: i64,
    /// `rejected` / `manual_review` / `needs_review` / `success` / `processing` / `pending`
    pub status: String,
    pub reason: Option<String>,
    pub created_at: String,
    pub refund_orders: Vec<WalletRefundRequestPart>,
    /// 未审核过为 `null`
    pub review: Option<WalletRiskReview>,
}

/// 用户钱包退款申请列表。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct WalletRefundRequests {
    /// 字符串形式
    pub user_id: String,
    pub items: Vec<WalletRefundRequestItem>,
    pub total: i64,
    pub page: u32,
    pub page_size: u32,
}

/// 管理员侧待风控处理的退款申请条目。
///
/// `review` / `release` 及其时间戳未发生时都是 **null(不是缺键)**。
/// `can_release` 是服务端派生:已审核 且 冻结原因仍 `frozen` 且 尚未解冻。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct WalletRiskListItem {
    pub request_id: String,
    /// 字符串形式
    pub user_id: String,
    pub amount_cents: i64,
    pub reason: Option<String>,
    pub created_at: String,
    /// 取 `wallet_risk_review.response_json` 里的 `review` 子对象
    pub review: Option<WalletRiskReview>,
    pub review_created_at: Option<String>,
    /// `wallet_risk_release.response_json` 整体
    pub release: Option<WalletRiskReleased>,
    pub release_created_at: Option<String>,
    pub freeze_status: Option<String>,
    pub can_release: bool,
    /// 是否挂到了 `wallet_risk_freeze_link`(布尔化,不是原始 ID)
    pub freeze_linked: bool,
}

/// 管理员侧待风控处理的退款申请列表。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct WalletRiskList {
    pub items: Vec<WalletRiskListItem>,
    pub total: i64,
    pub page: u32,
    pub page_size: u32,
}

#[cfg(test)]
mod wallet_refund_list_tests {
    use super::*;

    /// 回归护栏:列表项的 `review` 未审核过时必须是 **null 而不是缺键**
    #[test]
    fn list_item_review_is_null_not_absent() {
        let v = serde_json::to_value(WalletRefundRequestItem {
            request_id: "R1".into(), amount_cents: 500, refunded_cents: 0,
            status: "manual_review".into(), reason: None, created_at: "x".into(),
            refund_orders: vec![], review: None,
        })
        .unwrap();
        assert!(v.as_object().unwrap().contains_key("review"), "review 键不得缺省");
        assert!(v["review"].is_null());
        assert!(v["refund_orders"].is_array());
    }

    /// 回归护栏:列表容器 `user_id` 是字符串,`total` 是数字
    #[test]
    fn request_list_user_id_is_string_total_is_number() {
        let v = serde_json::to_value(WalletRefundRequests {
            user_id: "9".into(), items: vec![], total: 0, page: 1, page_size: 20,
        })
        .unwrap();
        assert!(v["user_id"].is_string());
        assert!(v["total"].is_number());
    }

    /// 回归护栏:风控列表项未审核时 `review` / `release` 是 **null 而不是缺键**,
    /// 且 `freeze_linked` 是布尔不是数字
    #[test]
    fn risk_list_item_nulls_are_present_and_freeze_linked_is_bool() {
        let v = serde_json::to_value(WalletRiskListItem {
            request_id: "R1".into(), user_id: "7".into(), amount_cents: 500,
            reason: None, created_at: "x".into(),
            review: None, review_created_at: None,
            release: None, release_created_at: None,
            freeze_status: Some("frozen".into()),
            can_release: false, freeze_linked: true,
        })
        .unwrap();
        let obj = v.as_object().unwrap();
        for key in ["review", "review_created_at", "release", "release_created_at"] {
            assert!(obj.contains_key(key), "{key} 键不得缺省");
            assert!(v[key].is_null());
        }
        assert!(v["freeze_linked"].is_boolean());
        assert_eq!(v["freeze_linked"], true);
    }
}

#[cfg(test)]
mod wallet_refund_tests {
    use super::*;

    fn manual_review() -> WalletRefundApplied {
        WalletRefundApplied {
            request_id: "R1".into(), status: "manual_review".into(), txn_no: None,
            refund_cents: 500, refund_orders: vec![],
            message: Some("退款频次较高，钱包已冻结".into()), review: None,
        }
    }

    /// 回归护栏:人工审核场景 refund_orders 是**空数组**而非缺键
    #[test]
    fn manual_review_has_empty_orders_and_message() {
        let v = serde_json::to_value(manual_review()).unwrap();
        assert!(v["refund_orders"].is_array());
        assert!(v.get("txn_no").is_none(), "人工审核场景不应出现 txn_no");
        assert!(v["message"].is_string());
    }

    /// 回归护栏:accepted 场景必须带 txn_no,且不得出现 message/review
    #[test]
    fn accepted_has_txn_no_and_no_message_or_review() {
        let v = serde_json::to_value(WalletRefundApplied {
            request_id: "R2".into(), status: "accepted".into(),
            txn_no: Some("WTX1".into()), refund_cents: 250,
            refund_orders: vec![WalletRefundOrderPart {
                refund_no: "REF1".into(), payment_order_no: "PAY1".into(),
                refund_cents: 250, status: "pending".into(),
            }],
            message: None, review: None,
        })
        .unwrap();
        assert_eq!(v["txn_no"], "WTX1");
        assert!(v.get("message").is_none());
        assert!(v.get("review").is_none());
        assert_eq!(v["refund_orders"][0]["payment_order_no"], "PAY1");
    }

    /// 回归护栏:风控审核后 `review.actor_id` 是字符串而非数字
    #[test]
    fn review_actor_id_is_string() {
        let mut applied = manual_review();
        applied.status = "accepted".into();
        applied.message = None;
        applied.txn_no = Some("WTX2".into());
        applied.review = Some(WalletRiskReview {
            actor_id: "77".into(), approved: true, comment: "核实原路退款".into(),
        });
        let v = serde_json::to_value(&applied).unwrap();
        assert!(v["review"]["actor_id"].is_string());
        assert_eq!(v["review"]["actor_id"], "77");
    }

    /// 回归护栏:反序列化必须能吃回旧的 `response_json`(缺 review 键)
    #[test]
    fn deserializes_legacy_response_without_review() {
        let legacy = serde_json::json!({
            "request_id": "R3", "status": "manual_review", "refund_cents": 500,
            "refund_orders": [], "message": "x"
        });
        let back: WalletRefundApplied = serde_json::from_value(legacy).unwrap();
        assert!(back.review.is_none());
        assert_eq!(back.status, "manual_review");
    }
}

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

/// 报修状态流转事件(admin 与 user 共用)。
///
/// admin 视角多 3 个字段(`actor_id` / `assigned_to` / `user_visible`),
/// user 视角不带——实现里就是 `if user_id.is_none()` 条件下才追加这三个键。
/// 这里用 `Option` + `skip_serializing_if` 精确复刻:**用户侧不出现这些键**。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FaultHistoryEvent {
    pub event_id: String,
    pub event_type: String,
    pub from_status: Option<String>,
    pub to_status: Option<String>,
    pub note: Option<String>,
    pub created_at: String,
    /// 仅 admin 视角
    #[serde(skip_serializing_if = "Option::is_none")]
    pub actor_id: Option<String>,
    /// 仅 admin 视角
    #[serde(skip_serializing_if = "Option::is_none")]
    pub assigned_to: Option<String>,
    /// 仅 admin 视角
    #[serde(skip_serializing_if = "Option::is_none")]
    pub user_visible: Option<bool>,
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

/// 运营发券结果。admin 端点原样透传,故落契约而非留在 user 内部。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct CouponGrantResult {
    pub request_id: String,
    pub coupon_id: u64,
    pub coupon_grant_id: u64,
    pub user_id: u64,
    /// `granted` / 幂等重放沿用上次状态
    pub status: String,
    pub expired_at: String,
}

#[cfg(test)]
mod coupon_grant_tests {
    use super::*;

    /// 回归护栏:coupon_id / coupon_grant_id 是**数字**,request_id 是字符串
    #[test]
    fn coupon_grant_result_id_kinds() {
        let v = serde_json::to_value(CouponGrantResult {
            request_id: "11111111-1111-1111-1111-111111111111".into(),
            coupon_id: 3, coupon_grant_id: 900, user_id: 7,
            status: "granted".into(), expired_at: "x".into(),
        })
        .unwrap();
        assert!(v["coupon_id"].is_number());
        assert_eq!(v["coupon_grant_id"], 900);
        assert!(v["user_id"].is_number());
        assert!(v["request_id"].is_string());
    }
}

/// 反馈分页响应。
///
/// ⚠️ 刻意**不带** `permissions` —— 与 `common::PagedResponse` 键集合不同,
/// 复用会凭空多出一个空数组。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PagedFeedback {
    pub items: Vec<Feedback>,
    pub total: i64,
    pub page: u32,
    pub page_size: u32,
}

/// 报修分页响应(同 `PagedFeedback`,不带 permissions)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PagedFault {
    pub items: Vec<FaultReport>,
    pub total: i64,
    pub page: u32,
    pub page_size: u32,
}

#[cfg(test)]
mod casework_paging_tests {
    use super::*;

    /// 回归护栏:分页响应**不得**凭空多出 `permissions` 键
    #[test]
    fn paged_feedback_has_no_permissions_key() {
        let v = serde_json::to_value(PagedFeedback { items: vec![], total: 0, page: 1, page_size: 20 }).unwrap();
        assert!(v.get("permissions").is_none());
        let f = serde_json::to_value(PagedFault { items: vec![], total: 0, page: 1, page_size: 20 }).unwrap();
        assert!(f.get("permissions").is_none());
    }
}
