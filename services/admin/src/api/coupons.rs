//! Admin coupon routes proxy user-owned coupon data through the user service.
use crate::AppState;
use axum::{extract::{Path, State}, Json};
use crate::auth::ActiveAdmin;
use common_error::{ApiEnvelope, AppError, AppResult};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

async fn require_permission(st: &AppState, c: &ActiveAdmin, permission: &str) -> AppResult<()> {
    let allowed: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM admin_user_role a JOIN role r ON r.id=a.role_id AND r.deleted_at IS NULL
         JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id
         WHERE a.id=? AND a.username=? AND a.status='active' AND a.deleted_at IS NULL AND p.code=?)",
    ).bind(c.admin_user_id).bind(&c.sub).bind(permission).fetch_one(st.config.pool()).await?;
    if !allowed { return Err(AppError::Forbidden(format!("缺少 {permission} 权限"))); }
    Ok(())
}

fn user_client(st: &AppState) -> common_http::internal::ApiClient {
    common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone())
}

/// 审计载荷落 `audit_log.before_json` / `after_json`(JSON 列),此处用 Value 是写库不是出参。
async fn audit(st: &AppState, c: &ActiveAdmin, action: &str, target: &str, before: Option<Value>, after: Value, request_id: Option<&str>) -> AppResult<()> {
    sqlx::query("INSERT INTO audit_log(actor_id,module,action,target_type,target_id,request_id,before_json,after_json,created_month) VALUES (?,'coupon',?,'coupon',?,?,?, ?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
        .bind(c.admin_user_id).bind(action).bind(target).bind(request_id).bind(before).bind(after).execute(st.config.pool()).await?;
    Ok(())
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

pub async fn list(State(st): State<AppState>, c: ActiveAdmin) -> AppResult<Json<ApiEnvelope<api_contracts::common::ListResponse<api_contracts::charge::CouponTemplate>>>> {
    require_permission(&st, &c, "coupon.read").await?;
    let result: api_contracts::common::ListResponse<api_contracts::charge::CouponTemplate> = user_client(&st).get(st.cfg.service_urls.user.as_deref(), api_contracts::paths::USER_INTERNAL_COUPONS, &()).await?;
    Ok(Json(ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn create(State(st): State<AppState>, c: ActiveAdmin, Json(req): Json<CouponCreateReq>) -> AppResult<Json<ApiEnvelope<api_contracts::common::CreatedResponse>>> {
    require_permission(&st, &c, "coupon.create").await?;
    let created: api_contracts::common::CreatedResponse = user_client(&st).post(st.cfg.service_urls.user.as_deref(), api_contracts::paths::USER_INTERNAL_COUPONS, &req).await?;
    let id = created.id;
    audit(&st, &c, "coupon.create", &id.to_string(), None, json!({"request":req,"result":created}), None).await?;
    Ok(Json(ApiEnvelope::ok(created, common_error::current_request_id())))
}

pub async fn get(State(st): State<AppState>, c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<ApiEnvelope<api_contracts::charge::CouponTemplate>>> {
    require_permission(&st, &c, "coupon.read").await?;
    let result: api_contracts::charge::CouponTemplate = user_client(&st).get(st.cfg.service_urls.user.as_deref(), &api_contracts::paths::USER_INTERNAL_COUPON_DETAIL.replace(":id", &id.to_string()), &()).await?;
    Ok(Json(ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn update(State(st): State<AppState>, c: ActiveAdmin, Path(id): Path<u64>, Json(req): Json<CouponUpdateReq>) -> AppResult<Json<ApiEnvelope<api_contracts::common::UpdatedResponse>>> {
    require_permission(&st, &c, "coupon.update").await?;
    let path = api_contracts::paths::USER_INTERNAL_COUPON_DETAIL.replace(":id", &id.to_string());
    let before: api_contracts::charge::CouponTemplate = user_client(&st).get(st.cfg.service_urls.user.as_deref(), &path, &()).await?;
    let result: api_contracts::common::UpdatedResponse = user_client(&st).put(st.cfg.service_urls.user.as_deref(), &path, &req).await?;
    audit(&st, &c, "coupon.update", &id.to_string(), Some(serde_json::to_value(&before)?), json!({"request":req,"result":result}), None).await?;
    Ok(Json(ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn delete(State(st): State<AppState>, c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<ApiEnvelope<api_contracts::common::DeletedResponse>>> {
    require_permission(&st, &c, "coupon.delete").await?;
    let path = api_contracts::paths::USER_INTERNAL_COUPON_DETAIL.replace(":id", &id.to_string());
    let before: api_contracts::charge::CouponTemplate = user_client(&st).get(st.cfg.service_urls.user.as_deref(), &path, &()).await?;
    let result: api_contracts::common::DeletedResponse = user_client(&st).delete(st.cfg.service_urls.user.as_deref(), &path, &()).await?;
    audit(&st, &c, "coupon.delete", &id.to_string(), Some(serde_json::to_value(&before)?), serde_json::to_value(&result)?, None).await?;
    Ok(Json(ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn grant(State(st): State<AppState>, c: ActiveAdmin, Path(id): Path<u64>, Json(req): Json<CouponGrantReq>) -> AppResult<Json<ApiEnvelope<api_contracts::charge::CouponGrantResult>>> {
    require_permission(&st, &c, "coupon.grant").await?;
    if uuid::Uuid::parse_str(&req.request_id).is_err() || req.user_id == 0 {
        return Err(AppError::BadRequest("发券请求标识或用户编号无效".into()));
    }
    let path = api_contracts::paths::USER_INTERNAL_COUPON_GRANTS.replace(":id", &id.to_string());
    let result: api_contracts::charge::CouponGrantResult = user_client(&st).post(st.cfg.service_urls.user.as_deref(), &path, &req).await?;
    audit(&st, &c, "coupon.grant", &id.to_string(), None, json!({"request":req,"result":result}), Some(&req.request_id)).await?;
    Ok(Json(ApiEnvelope::ok(result, common_error::current_request_id())))
}

#[derive(Debug, Serialize)]
struct CouponStatsQuery { coupon_id: u64 }

pub async fn stats(State(st): State<AppState>, c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<ApiEnvelope<api_contracts::charge::CouponStats>>> {
    require_permission(&st, &c, "coupon.read").await?;
    let query = CouponStatsQuery { coupon_id: id };
    let result: api_contracts::charge::CouponStats = user_client(&st).get(st.cfg.service_urls.user.as_deref(), api_contracts::paths::USER_INTERNAL_COUPON_STATS, &query).await?;
    Ok(Json(ApiEnvelope::ok(result, common_error::current_request_id())))
}
