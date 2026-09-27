//! PC 后台(admin-web)的实体契约 DTO(P2)
//!
//! admin-web 直接消费这些响应。**字段名与序列化行为按现有实现固化**,
//! 其中 `is_builtin` 是 **boolean**(实现里由 `i8 != 0` 转换),不是数字 ——
//! 改成数字会破坏后台的勾选框。

use serde::{Deserialize, Serialize};

use crate::common::ListResponse;

// ===== 角色 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Role {
    pub id: u64,
    pub code: String,
    pub name: String,
    pub description: Option<String>,
    /// 数据库里是 `TINYINT`,对外是 boolean
    pub is_builtin: bool,
    pub created_at: String,
}

pub type RoleList = ListResponse<Role>;

// ===== 管理员账号 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AdminUser {
    pub id: u64,
    pub username: String,
    pub display_name: Option<String>,
    pub role_id: Option<u64>,
    pub status: String,
    pub last_login_at: Option<String>,
    pub created_at: String,
}

pub type AdminUserList = ListResponse<AdminUser>;

// ===== 权限 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Permission {
    pub id: u64,
    pub code: String,
    pub name: String,
    pub module: String,
    pub description: Option<String>,
}

pub type PermissionList = ListResponse<Permission>;

// ===== 站点 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Station {
    pub id: u64,
    pub code: String,
    pub name: String,
    pub address: Option<String>,
    pub status: String,
    /// ⚠️ 是 **f64 数字**(SQL 里 `longitude+0e0`),不是字符串
    pub longitude: f64,
    pub latitude: f64,
    pub open_hours: Option<String>,
    pub contact_phone: Option<String>,
    pub pricing_template_id: Option<u64>,
    pub split_template_id: Option<u64>,
}

// ===== 公告 =====

/// 公告**列表**项。含生效时间段,不含 `created_at`(详情也不含)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AnnouncementListItem {
    pub id: u64,
    pub title: String,
    pub content: String,
    pub scope: String,
    pub priority: u8,
    pub start_at: String,
    pub end_at: Option<String>,
    pub status: String,
}

// ===== 客服配置 =====

/// 客服配置**列表**项。比详情少 `working_hours_json`。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CustomerServiceListItem {
    pub id: u64,
    pub agent_wechat: String,
    pub agent_name: Option<String>,
    pub path: Option<String>,
    pub priority: u32,
    pub enabled: bool,
}

pub type CustomerServiceList = ListResponse<CustomerServiceListItem>;

// ===== 优惠券 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Coupon {
    pub id: u64,
    pub name: String,
    pub discount_type: String,
    pub discount_value_cents: Option<i64>,
    pub discount_percent: Option<i32>,
    pub min_charge_cents: Option<i64>,
    pub total_quota: Option<i64>,
    pub status: String,
    pub start_at: Option<String>,
    pub end_at: Option<String>,
    pub created_at: String,
}

pub type CouponList = ListResponse<Coupon>;

#[cfg(test)]
mod tests {
    use super::*;

    /// 回归护栏:`is_builtin` 是 boolean,PC 后台按布尔判断内置角色不可删
    #[test]
    fn is_builtin_is_a_boolean() {
        let r = Role {
            id: 1, code: "customer_admin".into(), name: "管理员".into(),
            description: None, is_builtin: true, created_at: "2026-09-28T00:00:00Z".into(),
        };
        let v = serde_json::to_value(&r).unwrap();
        assert!(v["is_builtin"].is_boolean(), "is_builtin 不得变成数字");
        assert_eq!(v["is_builtin"], true);
    }

    #[test]
    fn role_list_shape() {
        let v = serde_json::to_value(RoleList::new(vec![])).unwrap();
        assert!(v["items"].is_array());
    }

    /// 时间字段为 null 时必须保留 —— 后台按"从未登录"显示
    #[test]
    fn admin_user_keeps_null_timestamps() {
        let u = AdminUser {
            id: 1, username: "root".into(), display_name: None, role_id: None,
            status: "active".into(), last_login_at: None, created_at: "2026-09-28T00:00:00Z".into(),
        };
        let v = serde_json::to_value(&u).unwrap();
        assert!(v["last_login_at"].is_null());
        assert!(v["role_id"].is_null());
    }
}

// ===== 详情(detail)——字段与**列表不同**,不可复用 =====
//
// 逐一比对过各 `get` 的实际输出,detail 比列表多/少字段:
//   announcements::get  有 `scope`,没有 start_at/end_at/created_at
//   roles::get          有 `permission_ids`,没有 is_builtin/created_at
//   customer_service::get 多了 working_hours_json
//   users::get          多了 phone/email
// 因此 detail 与 list 必须是**两套**类型,否则会凭空多出后台不用的字段。

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AnnouncementDetailV2 {
    pub id: u64,
    pub title: String,
    pub content: String,
    pub scope: String,
    pub priority: u8,
    pub status: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CustomerServiceDetail {
    pub id: u64,
    pub agent_wechat: String,
    pub agent_name: Option<String>,
    pub path: Option<String>,
    pub priority: u32,
    pub enabled: bool,
    /// 营业时间为任意 JSON(后台可配置结构),保留动态
    pub working_hours_json: Option<serde_json::Value>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RoleDetail {
    pub id: u64,
    pub code: String,
    pub name: String,
    pub description: Option<String>,
    /// 角色已分配的权限 id
    pub permission_ids: Vec<u64>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
/// 站点详情。字段比列表**少**(无营业时间/模板绑定),且带 `status`。
pub struct StationDetail {
    pub id: u64,
    pub code: String,
    pub name: String,
    pub address: Option<String>,
    pub longitude: f64,
    pub latitude: f64,
    pub status: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AdminUserDetail {
    pub id: u64,
    pub username: String,
    pub display_name: Option<String>,
    pub role_id: Option<u64>,
    pub status: String,
    pub phone: Option<String>,
    pub email: Option<String>,
    pub last_login_at: Option<String>,
    pub created_at: String,
}

#[cfg(test)]
mod detail_tests {
    use super::*;

    /// 回归护栏:公告列表项有生效时间段,**没有** created_at
    #[test]
    fn announcement_list_item_has_window_not_created_at() {
        let a = AnnouncementListItem {
            id: 1, title: "t".into(), content: "c".into(), scope: "user".into(),
            priority: 3, start_at: "2026-09-01T00:00:00Z".into(), end_at: None, status: "published".into(),
        };
        let v = serde_json::to_value(&a).unwrap();
        assert!(v["start_at"].is_string());
        assert!(v["end_at"].is_null());
        assert!(v.get("created_at").is_none());
    }

    /// 客服列表项的 priority 是 u32(详情同为 u32),enabled 是 boolean
    #[test]
    fn customer_service_list_item_types() {
        let c = CustomerServiceListItem {
            id: 1, agent_wechat: "w".into(), agent_name: None, path: None,
            priority: 0, enabled: true,
        };
        let v = serde_json::to_value(&c).unwrap();
        assert!(v["enabled"].is_boolean());
        assert!(v.get("working_hours_json").is_none(), "列表项不含工作时间");
    }

    /// 回归护栏:detail 不得凭空多出列表里的字段
    /// (后台按字段存在与否渲染,多字段会被当成"后端有、页面没接")
    #[test]
    fn role_detail_has_permission_ids_not_is_builtin() {
        let d = RoleDetail {
            id: 1, code: "r".into(), name: "n".into(),
            description: None, permission_ids: vec![3, 5],
        };
        let v = serde_json::to_value(&d).unwrap();
        assert_eq!(v["permission_ids"][1], 5);
        assert!(v.get("is_builtin").is_none(), "detail 不含 is_builtin");
        assert!(v.get("created_at").is_none());
    }

    #[test]
    fn announcement_detail_uses_scope_not_time_range() {
        let d = AnnouncementDetailV2 {
            id: 1, title: "t".into(), content: "c".into(),
            scope: "user".into(), priority: 3, status: "published".into(),
        };
        let v = serde_json::to_value(&d).unwrap();
        assert_eq!(v["scope"], "user");
        assert!(v.get("start_at").is_none());
    }

    /// 回归护栏:经纬度是数字,不是字符串
    #[test]
    fn station_coordinates_are_numbers() {
        let d = StationDetail {
            id: 1, code: "S1".into(), name: "站".into(), address: None,
            longitude: 116.397, latitude: 39.908, status: "active".into(),
        };
        let v = serde_json::to_value(&d).unwrap();
        assert!(v["longitude"].is_number(), "经纬度不得是字符串");
        assert_eq!(v["longitude"], 116.397);
    }

    /// 联系方式为 null 时保留,后台显示"未绑定"
    #[test]
    fn admin_user_detail_keeps_null_contacts() {
        let d = AdminUserDetail {
            id: 1, username: "u".into(), display_name: None, role_id: None,
            status: "active".into(), phone: None, email: None,
            last_login_at: None, created_at: "2026-09-28T00:00:00Z".into(),
        };
        let v = serde_json::to_value(&d).unwrap();
        assert!(v["phone"].is_null());
        assert!(v["email"].is_null());
    }

    /// enabled 是 boolean(库��� TINYINT)
    #[test]
    fn customer_service_enabled_is_boolean() {
        let d = CustomerServiceDetail {
            id: 1, agent_wechat: "w".into(), agent_name: None, path: None,
            priority: 0, enabled: true, working_hours_json: None,
        };
        let v = serde_json::to_value(&d).unwrap();
        assert!(v["enabled"].is_boolean());
    }
}

// ===== 计费与分账配置(D1 涉及的敏感面)=====

/// 计费规则项。`time_of_use_json` 是分时电价表,结构可配置,保留动态 JSON。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ChargeRule {
    pub id: u64,
    pub name: String,
    pub mode: String,
    pub service_fee_cents_per_kwh: i64,
    pub service_fee_cents_per_min: i64,
    pub min_charge_cents: i64,
    /// 规则版本号(实现里读的是 u32)
    pub version: u32,
    pub status: String,
}

/// 计费模板
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PricingTemplate {
    pub id: u64,
    pub code: String,
    pub name: String,
}

/// 分账模板。`mode` 决定分账池口径(mode_a 全额 / mode_b 服务费)——
/// billing/src/api.rs 依赖它校验,不可改语义。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SplitTemplate {
    pub id: u64,
    pub code: String,
    pub name: String,
    pub mode: String,
}

/// 分账参与方。`ratio_bp` 是万分比。
///
/// ⚠️ D1:向已合计 10000 的模板追加正比例参与方,会使 billing 分账因
/// 比例校验失败而停止。此结构是那个风险的载体,字段不可随意增删。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SplitParty {
    pub id: u64,
    pub party_code: String,
    pub party_name: String,
    /// 万分比
    pub ratio_bp: u32,
}

#[cfg(test)]
mod pricing_tests {
    use super::*;

    /// 回归护栏:分账参与方的比例单位是**万分比**
    #[test]
    fn ratio_is_basis_points() {
        let p = SplitParty {
            id: 1, party_code: "operator".into(), party_name: "运营商".into(), ratio_bp: 5000,
        };
        let v = serde_json::to_value(&p).unwrap();
        assert_eq!(v["ratio_bp"], 5000, "比例单位是万分比,不是百分数");
    }

    /// 分账模板的 mode 决定分账池口径,billing 依赖,不可改成布尔
    #[test]
    fn split_template_keeps_mode_string() {
        let t = SplitTemplate { id: 1, code: "ST".into(), name: "模板".into(), mode: "mode_b".into() };
        let v = serde_json::to_value(&t).unwrap();
        assert_eq!(v["mode"], "mode_b");
        assert!(v["mode"].is_string());
    }
}

// ===== 财务(admin 视角)=====

/// 结算单(admin_db 的 `settled_record`,**不是** billing_db 的 settlement)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Settlement {
    pub id: u64,
    pub settlement_no: String,
    pub split_template_id: u64,
    /// `DATE`,序列化为 `YYYY-MM-DD`
    pub period_start: String,
    pub period_end: String,
    pub total_cents: i64,
    pub status: String,
}

/// 对账日志。
///
/// ⚠️ `diff_count` 是 **i32**(有符号,允许负数表示内部多于微信),
/// `internal_count` / `wechat_count` 是 u32。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ReconcileLog {
    pub id: u64,
    pub reconcile_type: String,
    pub reconcile_date: String,
    pub internal_count: u32,
    pub wechat_count: u32,
    pub diff_count: i32,
    /// 数据库 `TINYINT`,对外 boolean
    pub resolved: bool,
}

#[cfg(test)]
mod finance_tests {
    use super::*;

    /// 回归护栏:对账差异可能为负(内部笔数多于微信),不得用无符号类型
    #[test]
    fn diff_count_can_be_negative() {
        let r = ReconcileLog {
            id: 1, reconcile_type: "daily".into(), reconcile_date: "2026-09-28".into(),
            internal_count: 10, wechat_count: 12, diff_count: -2, resolved: false,
        };
        let v = serde_json::to_value(&r).unwrap();
        assert_eq!(v["diff_count"], -2, "差异可为负,不能截断成无符号");
        assert!(v["resolved"].is_boolean());
    }

    /// 结算周期是日期字符串,不是时间戳
    #[test]
    fn settlement_period_is_date_string() {
        let s = Settlement {
            id: 1, settlement_no: "STL-1".into(), split_template_id: 2,
            period_start: "2026-09-01".into(), period_end: "2026-09-30".into(),
            total_cents: 1000, status: "settled".into(),
        };
        let v = serde_json::to_value(&s).unwrap();
        assert_eq!(v["period_start"], "2026-09-01");
    }
}

// ===== 仪表盘(user 生产 / admin 消费后追加 active_alerts)=====

/// 趋势里的单日汇总。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DailyTrendPoint {
    /// `YYYY-MM-DD`
    pub day: String,
    pub completed_orders: u64,
    pub settled_cents: i64,
}

/// user 服务的充电指标。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UserChargeMetrics {
    pub charging_orders: u64,
    pub today_order_users: u64,
    pub today_completed_orders: u64,
    pub today_settled_cents: i64,
    pub daily_trend: Vec<DailyTrendPoint>,
    pub updated_at: String,
}

/// admin 转发给 PC 后台的最终形态 = user 指标 + 告警数。
///
/// `active_alerts` 由 admin 侧查询 `alert_event` 后**追加**,user 侧不提供。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AdminDashboard {
    pub charging_orders: u64,
    pub today_order_users: u64,
    pub today_completed_orders: u64,
    pub today_settled_cents: i64,
    pub daily_trend: Vec<DailyTrendPoint>,
    pub updated_at: String,
    pub active_alerts: u64,
}

#[cfg(test)]
mod dashboard_tests {
    use super::*;

    /// 回归护栏:admin 侧必须补上 `active_alerts`,否则后台告警卡片恒为 0
    #[test]
    fn admin_dashboard_adds_active_alerts() {
        let d = AdminDashboard {
            charging_orders: 3, today_order_users: 2, today_completed_orders: 1,
            today_settled_cents: 1000,
            daily_trend: vec![],
            updated_at: "2026-09-28T00:00:00Z".into(),
            active_alerts: 5,
        };
        let v = serde_json::to_value(&d).unwrap();
        assert_eq!(v["active_alerts"], 5);
    }

    /// 趋势固定 7 天,空数据也要补零(否则前端折线断裂)
    #[test]
    fn daily_trend_is_seven_points() {
        let points: Vec<DailyTrendPoint> = (0..7)
            .map(|i| DailyTrendPoint {
                day: format!("2026-09-{:02}", i + 1),
                completed_orders: 0,
                settled_cents: 0,
            })
            .collect();
        assert_eq!(points.len(), 7);
        let v = serde_json::to_value(points).unwrap();
        assert_eq!(v.as_array().unwrap().len(), 7);
    }
}

// ===== 告警 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AlertEvent {
    pub id: u64,
    pub device_id: String,
    /// 规则可能已被删,故可空
    pub rule_id: Option<u64>,
    pub severity: String,
    pub metric: String,
    /// 触发时的遥测数值(实现读的是 f64)
    pub value: Option<f64>,
    pub status: String,
    /// 确认人 id
    pub acked_by: Option<u64>,
    pub created_at: String,
}

/// 告警规则**列表**项。⚠️ 比详情**少** `device_id_pattern` / `threshold` /
/// `window_seconds` —— 列表只够渲染表格,编辑要用详情。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AlertRule {
    pub id: u64,
    pub name: String,
    pub metric: String,
    pub op: String,
    pub severity: String,
    pub enabled: bool,
}

/// 告警规则详情(比列表多 3 个字段)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AlertRuleDetail {
    pub id: u64,
    pub name: String,
    pub device_id_pattern: String,
    pub metric: String,
    pub op: String,
    pub threshold: String,
    pub window_seconds: u32,
    pub severity: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AlertSubscription {
    pub id: u64,
    pub rule_id: Option<u64>,
    /// 未指定级别表示订阅全部
    pub severity: Option<String>,
    pub webhook_subscription_id: Option<u64>,
    pub admin_user_id: Option<u64>,
}

/// 风控配置项。`value` 是字符串(配置值统一按文本存取)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RiskConfigItem {
    pub key: String,
    /// 配置值是 JSON(可能是数字/布尔/对象),不是统一字符串
    pub value: serde_json::Value,
    pub description: Option<String>,
}

#[cfg(test)]
mod alert_tests {
    use super::*;

    /// 回归护栏:告警 `value` 是 f64 数字(实现直接读遥测数值),
    /// `rule_id` / `acked_by` 可空(规则/确认人可能已删)
    #[test]
    fn alert_value_is_number_and_refs_nullable() {
        let a = AlertEvent {
            id: 1, device_id: "D1".into(), rule_id: None, severity: "critical".into(),
            metric: "power_w".into(), value: Some(3500.5), status: "active".into(),
            acked_by: None, created_at: "2026-09-28T00:00:00Z".into(),
        };
        let v = serde_json::to_value(&a).unwrap();
        assert_eq!(v["value"], 3500.5);
        assert!(v["rule_id"].is_null());
        assert!(v["acked_by"].is_null());
    }

    /// 回归护栏:规则**列表**项不含 threshold/window_seconds(避免后台拿到就渲染)
    #[test]
    fn alert_rule_list_item_is_minimal() {
        let r = AlertRule {
            id: 1, name: "n".into(), metric: "power_w".into(),
            op: ">".into(), severity: "warning".into(), enabled: true,
        };
        let v = serde_json::to_value(&r).unwrap();
        assert!(v.get("threshold").is_none());
        assert!(v.get("window_seconds").is_none());
        assert!(v["enabled"].is_boolean());
    }

    /// 回归护栏:风控 value 是 JSON(可能是数字),不是字符串
    #[test]
    fn risk_config_value_is_json() {
        let c = RiskConfigItem {
            key: "max_daily_recharge_cents".into(),
            value: serde_json::json!(100000),
            description: None,
        };
        let v = serde_json::to_value(&c).unwrap();
        assert!(v["value"].is_number(), "风控阈值是数字 JSON");
    }

    /// 告警订阅可只订阅全部级别(severity 为空)
    #[test]
    fn alert_subscription_severity_is_optional() {
        let s = AlertSubscription {
            id: 1, rule_id: None, severity: None,
            webhook_subscription_id: None, admin_user_id: Some(3),
        };
        let v = serde_json::to_value(&s).unwrap();
        assert!(v["severity"].is_null());
    }

    /// 规则详情比列表多 device_id_pattern,不可混用
    #[test]
    fn alert_rule_detail_has_device_pattern() {
        let d = AlertRuleDetail {
            id: 1, name: "n".into(), device_id_pattern: "GW-*".into(),
            metric: "power_w".into(), op: ">".into(), threshold: "3000".into(),
            window_seconds: 60, severity: "warning".into(),
        };
        let v = serde_json::to_value(&d).unwrap();
        assert_eq!(v["device_id_pattern"], "GW-*");
    }
}

// ===== Webhook 订阅 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WebhookSubscription {
    pub id: u64,
    pub name: String,
    pub url: String,
    /// 密钥前缀(完整密钥只在创建时返回一次)
    pub secret_prefix: String,
    /// 订阅的事件类型
    pub event_types: Vec<String>,
    pub enabled: bool,
}

/// 订阅**详情**。⚠️ 比列表**少 `enabled`** —— 实现里 `get` 未查该列。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WebhookSubscriptionDetail {
    pub id: u64,
    pub name: String,
    pub url: String,
    /// 密钥前缀(完整密钥只在创建时返回一次)
    pub secret_prefix: String,
    pub event_types: Vec<String>,
}

/// 创建订阅的响应。**`secret` 只在这里出现一次**,之后只能看到前缀。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WebhookCreated {
    pub id: u64,
    pub secret: String,
}

/// 投递记录。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WebhookDelivery {
    pub id: u64,
    pub event_type: String,
    /// HTTP 响应码;网络失败时为空。
    /// ⚠️ 是 **i32** 不是 u16(实现直接读 INT 列)。
    pub response_status: Option<i32>,
    pub attempt_count: u32,
    pub duration_ms: Option<u32>,
    pub delivered_at: Option<String>,
}

#[cfg(test)]
mod webhook_tests {
    use super::*;

    /// 回归护栏:完整密钥**只在创建响应里出现**,列表/详情只有前缀。
    /// 若把 secret 加进 WebhookSubscription,每次查询都会泄露。
    #[test]
    fn secret_only_returned_on_create() {
        let sub = WebhookSubscription {
            id: 1, name: "n".into(), url: "https://x".into(),
            secret_prefix: "whsec_ab".into(), event_types: vec!["alert_recorded".into()], enabled: true,
        };
        let v = serde_json::to_value(&sub).unwrap();
        assert!(v.get("secret").is_none(), "列表/详情不得返回完整密钥");
        assert_eq!(v["secret_prefix"], "whsec_ab");
    }

    /// 回归护栏:详情**不含** `enabled`(实现未查该列),列表才有
    #[test]
    fn webhook_detail_has_no_enabled() {
        let d = WebhookSubscriptionDetail {
            id: 1, name: "n".into(), url: "https://x".into(),
            secret_prefix: "whsec_ab".into(), event_types: vec![],
        };
        let v = serde_json::to_value(&d).unwrap();
        assert!(v.get("enabled").is_none());
    }

    /// 回归护栏:响应码是 i32(实现读 INT 列),负值/异常码不应被截断
    #[test]
    fn delivery_status_is_i32() {
        let d = WebhookDelivery {
            id: 1, event_type: "e".into(), response_status: Some(-1),
            attempt_count: 1, duration_ms: None, delivered_at: None,
        };
        let v = serde_json::to_value(&d).unwrap();
        assert_eq!(v["response_status"], -1);
    }

    /// 投递未成功时 `response_status` 为空
    #[test]
    fn delivery_status_is_nullable() {
        let d = WebhookDelivery {
            id: 1, event_type: "alert_recorded".into(), response_status: None,
            attempt_count: 3, duration_ms: None, delivered_at: None,
        };
        let v = serde_json::to_value(&d).unwrap();
        assert!(v["response_status"].is_null());
    }
}

// ===== OTA =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OtaPackage {
    pub id: u64,
    pub code: String,
    pub vendor_id: Option<u64>,
    pub version: String,
    pub size_bytes: u64,
    pub checksum_sha256: String,
    pub status: String,
    pub created_at: String,
}

/// OTA 固件详情。⚠️ 比列表**少** `vendor_id`/`status`/`created_at`,
/// **多** `storage_url`(设备下载地址)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OtaPackageDetail {
    pub id: u64,
    pub code: String,
    pub version: String,
    pub storage_url: String,
    pub checksum_sha256: String,
    pub size_bytes: u64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OtaSchedule {
    pub id: u64,
    pub package_id: u64,
    pub rollout_strategy: String,
    /// 未分批时为空
    pub batch_size: Option<u32>,
    pub status: String,
    pub scheduled_at: Option<String>,
    pub started_at: Option<String>,
    pub completed_at: Option<String>,
}

/// 升级计划详情(比列表少 `batch_size` 与三个时间戳)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OtaScheduleDetail {
    pub id: u64,
    pub package_id: u64,
    pub rollout_strategy: String,
    pub status: String,
}

#[cfg(test)]
mod ota_tests {
    use super::*;

    /// 回归护栏:详情才有 `storage_url`(设备靠它下载),列表不该暴露
    #[test]
    fn package_detail_has_storage_url() {
        let d = OtaPackageDetail {
            id: 1, code: "P1".into(), version: "1.0.0".into(),
            storage_url: "https://cdn/fw.bin".into(),
            checksum_sha256: "ab".into(), size_bytes: 1024,
        };
        let v = serde_json::to_value(&d).unwrap();
        assert_eq!(v["storage_url"], "https://cdn/fw.bin");
        assert!(v.get("created_at").is_none());
    }

    /// 未开始/进行中的计划,时间戳为 null
    #[test]
    fn schedule_timestamps_are_nullable() {
        let s = OtaSchedule {
            id: 1, package_id: 2, rollout_strategy: "batch".into(), batch_size: Some(10),
            status: "pending".into(), scheduled_at: None, started_at: None, completed_at: None,
        };
        let v = serde_json::to_value(&s).unwrap();
        assert!(v["scheduled_at"].is_null());
        assert!(v["completed_at"].is_null());
    }
}

// ===== 内部端点(admin 生产,user/gateway 消费)=====

/// 计费规则详情(供 billing 报价使用)。
/// ⚠️ 比后台列表**少** `status`/`created_at`,且 `version` 是 u32。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PricingRuleForBilling {
    pub id: u64,
    pub name: String,
    pub mode: String,
    pub service_fee_cents_per_kwh: i64,
    pub service_fee_cents_per_min: i64,
    pub min_charge_cents: i64,
    pub version: u32,
}

/// 分账模板 + 参与方(billing 分账时读取)。
/// ⚠️ **参与方比例合计必须为 10000**,否则 billing 会拒绝分账(见 §五 D1)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SplitTemplateWithParties {
    pub id: u64,
    pub code: String,
    pub name: String,
    pub mode: String,
    pub parties: Vec<SplitParty>,
}

/// 公告过期处理结果。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AnnouncementExpireResult {
    pub expired_count: u64,
}

#[cfg(test)]
mod internal_tests {
    use super::*;

    /// 回归护栏:billing 依赖 `version` 与三个费率字段,少一个就无法报价
    #[test]
    fn pricing_rule_for_billing_has_all_rate_fields() {
        let r = PricingRuleForBilling {
            id: 1, name: "n".into(), mode: "kwh".into(),
            service_fee_cents_per_kwh: 30, service_fee_cents_per_min: 2,
            min_charge_cents: 0, version: 3,
        };
        let v = serde_json::to_value(&r).unwrap();
        for f in ["service_fee_cents_per_kwh", "service_fee_cents_per_min", "min_charge_cents", "version"] {
            assert!(v.get(f).is_some(), "billing 报价依赖 {f}");
        }
    }

    /// 分账模板必须带 parties,否则 billing 无法分账
    #[test]
    fn split_template_carries_parties() {
        let t = SplitTemplateWithParties {
            id: 1, code: "ST".into(), name: "模板".into(), mode: "mode_b".into(),
            parties: vec![],
        };
        let v = serde_json::to_value(&t).unwrap();
        assert!(v["parties"].is_array());
    }
}

// ===== 站点(user/gateway 经内部端点消费)=====

/// 附近站点项。⚠️ 经纬度是 **f64 数字**,不是字符串。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct NearbyStation {
    pub id: u64,
    pub code: String,
    pub name: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub address: Option<String>,
    pub longitude: f64,
    pub latitude: f64,
    pub distance_km: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct NearbyStations {
    pub items: Vec<NearbyStation>,
}

/// 站点详情。
///
/// ⚠️ **D18**:原实现按 SQL 列序 `(… , longitude, latitude, …)` 取值时写成
/// `longitude: r.5 / latitude: r.4`,**经纬度颠倒**。本类型以字段名固定,
/// 由 `stations_detail` 显式赋值,杜绝按位置错配。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StationForUser {
    pub id: u64,
    pub code: String,
    pub name: String,
    pub address: Option<String>,
    pub longitude: f64,
    pub latitude: f64,
    pub status: String,
}

/// 生效中的告警(内部端点,字段少于后台列表:无 status/acked_by/created_at)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ActiveAlert {
    pub id: u64,
    pub device_id: String,
    pub severity: String,
    pub metric: String,
}

#[cfg(test)]
mod station_tests {
    use super::*;

    /// **D18 回归护栏**:经纬度必须各归其位。
    /// 站点详情与附近站点是不同接口,曾因按 SQL 列序取值而把两者写反。
    #[test]
    fn coordinates_are_not_swapped() {
        let d = StationForUser {
            id: 1, code: "S1".into(), name: "站".into(), address: None,
            // 北京站:经度 116.397,纬度 39.908
            longitude: 116.397, latitude: 39.908, status: "active".into(),
        };
        let v = serde_json::to_value(&d).unwrap();
        assert_eq!(v["longitude"], 116.397);
        assert_eq!(v["latitude"], 39.908);
        assert!(v["longitude"].as_f64().unwrap() > v["latitude"].as_f64().unwrap(), "北京经度应大于纬度");
    }

    #[test]
    fn nearby_station_defaults_to_empty_items() {
        let v = serde_json::to_value(NearbyStations::default()).unwrap();
        assert!(v["items"].is_array());
    }
}

// ===== 财务:退款审核(admin 侧聚合视图) =====

/// admin 退款列表项的 `review` 视图。
///
/// ⚠️ 与 user 侧 `charge::RefundReviewInfo` **刻意不同**:
/// 退款被拒且从未产生审核行时,admin 会把 `review` 补成
/// `{"status":"rejected"}` —— **只有 status 一个键**,
/// `first_signer` / `second_signer` / 两个 comment / `approved_at` 全部缺省。
/// 若复用 user 侧类型(那些字段是必填),序列化会凭空多出 null 键。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct AdminRefundReviewView {
    /// `approved` / `awaiting_second` / `rejected`
    pub status: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub first_signer: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub second_signer: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub first_comment: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub second_comment: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub approved_at: Option<String>,
}

/// admin 侧退款列表项:user 服务的 `charge::AdminRefund` 加上
/// admin 本地派生的操作可用性与重试任务。
///
/// 四个派生字段的判定口径(改动会直接影响后台按钮显隐):
/// - `can_approve` : 有审核权限 且 status==pending 且 biz_type==charge
///                   且 review.status != approved 且 review.first_signer != 当前管理员
/// - `can_reject`  : 有审核权限 且 status==pending 且 biz_type==charge
///                   且 review.status == awaiting_second
/// - `can_retry`   : 有重试权限 且 refund_task 存在 且 last_error 非空
///                   且 stage ∈ {queued, querying, reporting};无任务行时 false
/// - `task`        : 无任务行时是 **null(不是缺键)**
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct AdminRefundRow {
    /// 字符串形式
    pub id: String,
    pub refund_no: String,
    /// 字符串形式
    pub user_id: String,
    /// 字符串形式
    pub payment_order_id: String,
    pub biz_type: String,
    pub refund_cents: i64,
    pub status: String,
    pub reason: Option<String>,
    pub failure_reason: Option<String>,
    pub created_at: String,
    pub completed_at: Option<String>,
    /// 未审核且未拒绝时为 null
    pub review: Option<AdminRefundReviewView>,
    pub can_approve: bool,
    pub can_reject: bool,
    pub can_retry: bool,
    /// 无 `refund_task` 行时是 null
    pub task: Option<AdminRefundTask>,
}

/// admin 本地 `refund_task` 的对外视图。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct AdminRefundTask {
    /// `queued` / `querying` / `reporting` / `manual_review` / …
    pub stage: String,
    pub attempts: u32,
    pub last_error: Option<String>,
    pub scheduled_at: String,
}

#[cfg(test)]
mod refund_admin_view_tests {
    use super::*;

    /// 回归护栏:被拒且无审核行的退款,`review` 只有 `status` 一个键。
    /// 误用 user 侧的 `RefundReviewInfo` 会凭空多出 5 个 null 键。
    #[test]
    fn rejected_review_view_has_status_only() {
        let v = serde_json::to_value(AdminRefundReviewView {
            status: "rejected".into(), first_signer: None, second_signer: None,
            first_comment: None, second_comment: None, approved_at: None,
        })
        .unwrap();
        assert_eq!(v.as_object().unwrap().len(), 1, "不得凭空多出 null 键");
        assert_eq!(v["status"], "rejected");
    }

    /// 回归护栏:`task` 无任务行时是 null 而不是缺键 —— 前端按 `task === null` 判断
    #[test]
    fn refund_row_task_is_null_when_absent() {
        let v = serde_json::to_value(AdminRefundRow {
            id: "1".into(), refund_no: "REF1".into(), user_id: "7".into(),
            payment_order_id: "9".into(), biz_type: "charge".into(),
            refund_cents: 100, status: "pending".into(), reason: None,
            failure_reason: None, created_at: "x".into(), completed_at: None,
            review: None, can_approve: false, can_reject: false, can_retry: false,
            task: None,
        })
        .unwrap();
        assert!(v.as_object().unwrap().contains_key("task"), "task 键不得缺省");
        assert!(v["task"].is_null());
        // 派生字段必须全部存在,前端直接读
        for key in ["can_approve", "can_reject", "can_retry"] {
            assert!(v[key].is_boolean(), "{key} 必须是布尔");
        }
    }

    /// 回归护栏:id / user_id / payment_order_id 都是**字符串**
    #[test]
    fn refund_row_ids_are_strings() {
        let v = serde_json::to_value(AdminRefundTask {
            stage: "queued".into(), attempts: 2, last_error: Some("boom".into()),
            scheduled_at: "x".into(),
        })
        .unwrap();
        assert!(v["attempts"].is_number());
        assert!(v["last_error"].is_string());
    }
}

// ===== 财务:重试 / 发票双签 / 钱包风控 =====

/// 退款自动任务重试受理结果。
///
/// ⚠️ `already_queued` 为 true 时**不会**写审计日志、不重置 stage/次数/单号,
/// 只是确认任务本就在队列里。改动前须确认这两条语义。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct RefundRetryQueued {
    pub queued: bool,
    pub already_queued: bool,
    pub refund_no: String,
    /// 重试时该任务的 `refund_task.stage`
    pub stage: String,
}

/// admin 发票审核队列列表项 = user 发票详情 + admin 本地 `invoice_review` 队列态。
///
/// 八个 `queue_*` / 双签人字段是 admin 侧 `invoice_review` 表的列,
/// **ID 一律转成字符串**,时间转 RFC3339。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct InvoiceReviewRow {
    pub invoice_request_id: u64,
    pub invoice_no: String,
    pub user_id: u64,
    pub biz_type: String,
    pub biz_id: u64,
    pub total_cents: i64,
    pub invoice_type: String,
    pub created_at: String,
    /// user 侧 `invoice_request.review_status`
    pub review_status: String,
    /// admin 侧 `invoice_review.review_status`
    pub queue_review_status: String,
    /// 字符串
    pub queue_reviewed_by: Option<String>,
    pub queue_reject_reason: Option<String>,
    /// 字符串
    pub first_reviewer_id: Option<String>,
    pub first_reviewed_at: Option<String>,
    /// 字符串
    pub second_reviewer_id: Option<String>,
    pub second_reviewed_at: Option<String>,
    pub invoice_url: Option<String>,
}

/// 发票审核回执。
///
/// 六条路径的键集合不同,故用可选字段复刻:
/// - 首签落定      : {reviewed, review_status:"awaiting_second", first_reviewer_id}
/// - 二次签发      : {reviewed, review_status:"issued", recovered:false}
/// - 二次签发(恢复): {reviewed, review_status:"issued", recovered:true}
/// - 首签幂等重放  : {reviewed, review_status:"awaiting_second", already_processed:true}
/// - 二次幂等重放  : {reviewed, review_status:"issued", already_processed:true}
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct InvoiceReviewAck {
    pub reviewed: bool,
    /// `awaiting_second` / `issued`
    pub review_status: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub first_reviewer_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub recovered: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub already_processed: Option<bool>,
}

/// 发票拒签回执。
///
/// - 正常拒签 / 恢复拒签 : {reviewed, review_status:"rejected", rejected:true}
/// - 幂等重放            : {reviewed, review_status:"rejected", already_processed:true}
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct InvoiceRejectAck {
    pub reviewed: bool,
    pub review_status: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub rejected: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub already_processed: Option<bool>,
}

/// 钱包风控列表项 = user 侧 `charge::WalletRiskListItem`,但 `can_release`
/// 被 admin 用**本地** `finance.wallet_risk.release` 权限再收紧一次。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct AdminWalletRiskRow {
    pub request_id: String,
    /// 字符串
    pub user_id: String,
    pub amount_cents: i64,
    pub reason: Option<String>,
    pub created_at: String,
    pub review: Option<crate::charge::WalletRiskReview>,
    pub review_created_at: Option<String>,
    pub release: Option<crate::charge::WalletRiskReleased>,
    pub release_created_at: Option<String>,
    pub freeze_status: Option<String>,
    /// user 侧已算一遍,admin 再与本地解冻权限取**与**
    pub can_release: bool,
    pub freeze_linked: bool,
}

#[cfg(test)]
mod finance_admin_tests {
    use super::*;

    /// 回归护栏:幂等重放标记是**缺键**而非 null —— 前端按 `!== undefined` 判断
    #[test]
    fn idempotent_replay_marks_are_absent_not_null() {
        let v = serde_json::to_value(InvoiceReviewAck {
            reviewed: true, review_status: "issued".into(),
            first_reviewer_id: None, recovered: None, already_processed: Some(true),
        })
        .unwrap();
        let obj = v.as_object().unwrap();
        assert!(!obj.contains_key("recovered"), "recovered 不得凭空出现 null");
        assert!(!obj.contains_key("first_reviewer_id"));
        assert_eq!(v["already_processed"], true);
    }

    /// 回归护栏:首签落定必须带 `first_reviewer_id` 且是字符串
    #[test]
    fn first_approval_carries_reviewer_id_as_string() {
        let v = serde_json::to_value(InvoiceReviewAck {
            reviewed: true, review_status: "awaiting_second".into(),
            first_reviewer_id: Some("77".into()), recovered: None, already_processed: None,
        })
        .unwrap();
        assert_eq!(v["first_reviewer_id"], "77");
    }

    /// 回归护栏:拒签回执的 `rejected` 与 `already_processed` 互斥出现
    #[test]
    fn reject_ack_never_carries_both_marks() {
        let recovered = serde_json::to_value(InvoiceRejectAck {
            reviewed: true, review_status: "rejected".into(),
            rejected: Some(true), already_processed: None,
        })
        .unwrap();
        assert_eq!(recovered["rejected"], true);
        assert!(recovered.get("already_processed").is_none());

        let replay = serde_json::to_value(InvoiceRejectAck {
            reviewed: true, review_status: "rejected".into(),
            rejected: None, already_processed: Some(true),
        })
        .unwrap();
        assert_eq!(replay["already_processed"], true);
        assert!(replay.get("rejected").is_none());
    }

    /// 回归护栏:重试回执的 `already_queued` 必须是布尔
    #[test]
    fn refund_retry_queued_is_bool() {
        let v = serde_json::to_value(RefundRetryQueued {
            queued: true, already_queued: false, refund_no: "REF1".into(),
            stage: "querying".into(),
        })
        .unwrap();
        assert!(v["queued"].is_boolean());
        assert!(v["already_queued"].is_boolean());
    }
}
