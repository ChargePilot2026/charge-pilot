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

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AnnouncementDetail {
    pub id: u64,
    pub title: String,
    pub content: String,
    pub priority: u8,
    pub status: String,
    pub start_at: Option<String>,
    pub end_at: Option<String>,
    pub created_at: String,
}

// ===== 客服配置 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CustomerServiceConfig {
    pub id: u64,
    pub agent_wechat: String,
    pub agent_name: Option<String>,
    pub path: String,
    pub enabled: bool,
    pub priority: i32,
    pub created_at: String,
}

pub type CustomerServiceList = ListResponse<CustomerServiceConfig>;

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
