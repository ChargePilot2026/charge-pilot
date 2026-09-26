//! 优惠券后台视角 CRUD

use crate::AppState;
use crate::api_types;
use api_contracts::paths as p;
use axum::{extract::{Path, State}, Json};
use common_auth::AdminClaims;
use common_db::IdGen;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use serde_json::{json, Value};

pub async fn list(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query(
        "SELECT id, code, name, discount_type, discount_value_cents, discount_percent,
                min_charge_cents, valid_hours, total_quota, per_user_quota, status, start_at, end_at
         FROM coupon WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "code": sqlx::Row::try_get::<String, _>(r, "code").unwrap_or_default(),
        "name": sqlx::Row::try_get::<String, _>(r, "name").unwrap_or_default(),
        "discount_type": sqlx::Row::try_get::<String, _>(r, "discount_type").unwrap_or_default(),
        "discount_value_cents": sqlx::Row::try_get::<Option<i64>, _>(r, "discount_value_cents").ok().flatten(),
        "discount_percent": sqlx::Row::try_get::<Option<f64>, _>(r, "discount_percent").ok().flatten(),
        "min_charge_cents": sqlx::Row::try_get::<i64, _>(r, "min_charge_cents").unwrap_or(0),
        "status": sqlx::Row::try_get::<String, _>(r, "status").unwrap_or_default(),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct CouponCreateReq {
    pub code: String,
    pub name: String,
    pub discount_type: String,
    pub discount_value_cents: Option<i64>,
    pub discount_percent: Option<f64>,
    pub min_charge_cents: Option<i64>,
    pub valid_hours: Option<i64>,
    pub total_quota: Option<i32>,
    pub per_user_quota: Option<i32>,
    pub start_at: Option<chrono::DateTime<chrono::Utc>>,
    pub end_at: Option<chrono::DateTime<chrono::Utc>>,
}

pub async fn create(State(st): State<AppState>, _c: AdminClaims, Json(req): Json<CouponCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let id: u64 = sqlx::query_scalar(
        "INSERT INTO coupon (code, name, discount_type, discount_value_cents, discount_percent,
                             min_charge_cents, valid_hours, total_quota, per_user_quota, start_at, end_at)
         VALUES (?, ?, ?, ?, ?, COALESCE(?, 0), COALESCE(?, 24), COALESCE(?, 0), COALESCE(?, 1), ?, ?)"
    )
    .bind(&req.code).bind(&req.name).bind(&req.discount_type)
    .bind(req.discount_value_cents).bind(req.discount_percent)
    .bind(req.min_charge_cents).bind(req.valid_hours).bind(req.total_quota).bind(req.per_user_quota)
    .bind(req.start_at).bind(req.end_at)
    .fetch_one(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": id}), common_error::current_request_id())))
}

pub async fn get(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r = sqlx::query("SELECT * FROM coupon WHERE id = ? AND deleted_at IS NULL")
        .bind(id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("coupon".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "id": sqlx::Row::try_get::<u64, _>(&r, "id")?,
        "code": sqlx::Row::try_get::<String, _>(&r, "code")?,
        "name": sqlx::Row::try_get::<String, _>(&r, "name")?,
        "discount_type": sqlx::Row::try_get::<String, _>(&r, "discount_type")?,
    }), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct CouponUpdateReq {
    pub name: Option<String>,
    pub status: Option<String>,
    pub end_at: Option<chrono::DateTime<chrono::Utc>>,
}

pub async fn update(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>, Json(req): Json<CouponUpdateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let n = sqlx::query(
        "UPDATE coupon SET name = COALESCE(?, name), status = COALESCE(?, status), end_at = COALESCE(?, end_at)
         WHERE id = ? AND deleted_at IS NULL"
    )
    .bind(req.name.as_deref()).bind(req.status.as_deref()).bind(req.end_at).bind(id)
    .execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("coupon".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"updated": true}), common_error::current_request_id())))
}

pub async fn delete(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let n = sqlx::query("UPDATE coupon SET deleted_at = NOW(3) WHERE id = ? AND deleted_at IS NULL")
        .bind(id).execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("coupon".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"deleted": true}), common_error::current_request_id())))
}

pub async fn stats(State(st): State<AppState>, _c: AdminClaims, Path(_id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    // 调 user 内部接口 — 类型化 client + 路径常量(本期固定路由;具体 id 通过 query 改 header)
    let cli = crate::clients::ServiceClient::new(st.http.clone(), st.service_token.clone());
    let v: Value = cli
        .get_typed(st.cfg.service_urls.user.as_deref(), p::USER_INTERNAL_COUPON_STATS)
        .await
        .unwrap_or_else(|_| json!({}));
    Ok(Json(common_error::ApiEnvelope::ok(v, common_error::current_request_id())))
}