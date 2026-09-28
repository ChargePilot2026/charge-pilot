//! cases 域的 repository 层 —— SQL 只允许出现在这里
//!
//! (方案 §三:handler / usecase / domain 层禁 SQL,由 clippy disallowed-methods 保证)
//! 覆盖 `audit_log`(客服 / 优惠券 / 派单回执)与优惠券的授权查询。

#![allow(clippy::disallowed_methods, clippy::disallowed_types)]

// `disallowed_types` 只服务 `audit_log.before_json` / `after_json` ——
// 数据库 JSON 列的**原样落库**(方案 §三例外清单第 2 条)。

use crate::AppState;
use crate::capability::identity::ActiveAdmin;
use common_error::{AppError, AppResult};
use serde::{Deserialize, Serialize};

/// 客服域的权限校验(带 username 复核)。
pub async fn has_case_permission(
    st: &AppState,
    c: &ActiveAdmin,
    code: &str,
) -> AppResult<bool> {
    let allowed: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM admin_user_role a
         JOIN role r ON r.id=a.role_id AND r.deleted_at IS NULL
         JOIN role_permission rp ON rp.role_id=r.id
         JOIN permission p ON p.id=rp.permission_id
         WHERE a.id=? AND a.username=? AND a.status='active' AND a.deleted_at IS NULL AND p.code=?)",
    ).bind(c.admin_user_id).bind(&c.sub).bind(code).fetch_one(st.cases.pool()).await?;
    Ok(allowed)
}

/// 幂等重放不重复写审计 —— 判定依据是上游显式的 `already_processed` 标记。
pub async fn audit_feedback_reply(
    st: &AppState,
    c: &ActiveAdmin,
    id: &str,
    action: &str,
    payload: &serde_json::Value,
) -> AppResult<()> {
    sqlx::query("INSERT INTO audit_log(actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'customer_service',?,'feedback',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
        .bind(c.admin_user_id).bind(action).bind(id).bind(payload).execute(st.cases.pool()).await?;
    Ok(())
}

pub async fn audit_fault(
    st: &AppState,
    c: &ActiveAdmin,
    id: &str,
    action: &str,
    payload: &serde_json::Value,
) -> AppResult<()> {
    sqlx::query("INSERT INTO audit_log(actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'inspection',?,'device_fault_report',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
        .bind(c.admin_user_id).bind(action).bind(id).bind(payload).execute(st.cases.pool()).await?;
    Ok(())
}

pub async fn target_admin_active(st: &AppState, assigned_to: u64) -> AppResult<bool> {
    let target_active: bool = sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM admin_user_role WHERE id=? AND status='active' AND deleted_at IS NULL)")
        .bind(assigned_to).fetch_one(st.cases.pool()).await?;
    Ok(target_active)
}

// ===== 优惠券(数据归 user 服务,admin 只做授权 + 审计) =====

pub async fn has_coupon_permission(
    st: &AppState,
    c: &ActiveAdmin,
    permission: &str,
) -> AppResult<bool> {
    let allowed: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM admin_user_role a JOIN role r ON r.id=a.role_id AND r.deleted_at IS NULL
         JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id
         WHERE a.id=? AND a.username=? AND a.status='active' AND a.deleted_at IS NULL AND p.code=?)",
    ).bind(c.admin_user_id).bind(&c.sub).bind(permission).fetch_one(st.config.pool()).await?;
    Ok(allowed)
}

/// 审计载荷落 `audit_log.before_json` / `after_json`(JSON 列),此处用 Value 是写库不是出参。
pub async fn audit_coupon(
    st: &AppState,
    c: &ActiveAdmin,
    action: &str,
    target: &str,
    before: Option<serde_json::Value>,
    after: serde_json::Value,
    request_id: Option<&str>,
) -> AppResult<()> {
    sqlx::query("INSERT INTO audit_log(actor_id,module,action,target_type,target_id,request_id,before_json,after_json,created_month) VALUES (?,'coupon',?,'coupon',?,?,?, ?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
        .bind(c.admin_user_id).bind(action).bind(target).bind(request_id).bind(before).bind(after).execute(st.config.pool()).await?;
    Ok(())
}

// ===== 请求 / 载荷类型 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct QueueQuery {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub status: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub page: Option<u32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub page_size: Option<u32>,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct FeedbackReply { pub action: String, pub reply_content: Option<String> }

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct FaultDispatch {
    pub assigned_to: u64,
    #[serde(default)]
    pub note: Option<String>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct FaultResolve {
    pub status: String,
    #[serde(default)]
    pub note: Option<String>,
}

/// 反馈回复载荷(user 内部端点)。
#[derive(Serialize)]
pub struct FeedbackReplyBody<'a> {
    pub actor_id: u64,
    pub action: &'a str,
    pub reply_content: &'a Option<String>,
}

/// 派单载荷。
#[derive(Serialize)]
pub struct FaultDispatchBody<'a> {
    pub actor_id: u64,
    pub assigned_to: u64,
    pub note: &'a Option<String>,
}

/// 处理报修载荷。
#[derive(Serialize)]
pub struct FaultResolveBody<'a> {
    pub actor_id: u64,
    pub status: &'a str,
    pub note: &'a Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct CouponCreateReq {
    pub code: String,
    pub name: String,
    pub discount_type: String,
    pub discount_value_cents: Option<i64>,
    pub discount_percent: Option<f64>,
    pub min_charge_cents: Option<i64>,
    pub valid_hours: Option<i64>,
    pub total_quota: Option<i64>,
    pub per_user_quota: Option<i64>,
    pub start_at: Option<chrono::DateTime<chrono::Utc>>,
    pub end_at: Option<chrono::DateTime<chrono::Utc>>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct CouponUpdateReq { pub name: Option<String>, pub status: Option<String>, pub end_at: Option<chrono::DateTime<chrono::Utc>> }

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct CouponGrantReq { pub request_id: String, pub user_id: u64 }

#[derive(Debug, Serialize)]
pub struct CouponStatsQuery { pub coupon_id: u64 }

/// 审计载荷:`{"request":…, "result":…}`。
#[derive(Serialize)]
pub struct AuditRequestResult<'a, R, S> {
    pub request: &'a R,
    pub result: &'a S,
}

/// 审计载荷:`{"assigned_to":…, "result":…}`。
#[derive(Serialize)]
pub struct AuditAssigned<'a, S> {
    pub assigned_to: u64,
    pub result: &'a S,
}

/// 审计载荷:`{"result":…}`。
#[derive(Serialize)]
pub struct AuditResult<'a, S> {
    pub result: &'a S,
}

/// 权限不足时的统一文案。
pub fn forbidden(permission: &str) -> AppError {
    AppError::Forbidden(format!("缺少 {permission} 权限"))
}
