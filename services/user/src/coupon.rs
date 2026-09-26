//! 优惠券:查询我的优惠券 / 预览折扣

use crate::AppState;
use axum::{extract::{Query, State}, Json};
use common_error::{AppError, AppResult};
use serde::Deserialize;
use serde_json::{json, Value};

#[derive(Debug, Deserialize)]
pub struct CouponQuery { pub status: Option<String> }

pub async fn my(
    State(st): State<AppState>,
    claims: common_auth::UserClaims,
    Query(q): Query<CouponQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let status_filter = q.status.unwrap_or_else(|| "unused".to_string());
    let rows = sqlx::query(
        "SELECT cg.id, c.name, c.discount_type, c.discount_value_cents, c.discount_percent,
                c.min_charge_cents, cg.expired_at, cg.status, cg.created_at
         FROM coupon_grant cg JOIN coupon c ON c.id = cg.coupon_id
         WHERE cg.user_id = ? AND cg.status = ?
         ORDER BY cg.id DESC LIMIT 200"
    )
    .bind(claims.user_id)
    .bind(&status_filter)
    .fetch_all(st.db.pool())
    .await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "grant_id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "name": sqlx::Row::try_get::<String, _>(r, "name").unwrap_or_default(),
        "discount_type": sqlx::Row::try_get::<String, _>(r, "discount_type").unwrap_or_default(),
        "discount_value_cents": sqlx::Row::try_get::<Option<i64>, _>(r, "discount_value_cents").ok().flatten(),
        "discount_percent": sqlx::Row::try_get::<Option<f64>, _>(r, "discount_percent").ok().flatten(),
        "min_charge_cents": sqlx::Row::try_get::<i64, _>(r, "min_charge_cents").unwrap_or(0),
        "expired_at": sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "expired_at").ok().map(|t| t.to_rfc3339()),
        "status": sqlx::Row::try_get::<String, _>(r, "status").unwrap_or_default(),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct CouponPreviewReq {
    pub coupon_grant_id: u64,
    pub estimated_total_cents: i64,
}

pub async fn preview(
    State(st): State<AppState>,
    claims: common_auth::UserClaims,
    Json(req): Json<CouponPreviewReq>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r: Option<(u64, String, Option<i64>, Option<f64>, i64, String)> = sqlx::query_as(
        "SELECT c.id, c.discount_type, c.discount_value_cents, c.discount_percent,
                c.min_charge_cents, cg.status
         FROM coupon_grant cg JOIN coupon c ON c.id = cg.coupon_id
         WHERE cg.id = ? AND cg.user_id = ? LIMIT 1"
    )
    .bind(req.coupon_grant_id)
    .bind(claims.user_id)
    .fetch_optional(st.db.pool())
    .await?;
    let (cid, dtype, val_cents, percent, min, status) = r.ok_or_else(|| AppError::NotFound("coupon".into()))?;
    if status != "unused" {
        return Err(AppError::Conflict("coupon not unused".into()));
    }
    if req.estimated_total_cents < min {
        return Err(AppError::Conflict("below min charge".into()));
    }
    let discount_cents = match dtype.as_str() {
        "amount" => val_cents.unwrap_or(0).min(req.estimated_total_cents),
        "percentage" => {
            let pct = percent.unwrap_or(0.0);
            ((req.estimated_total_cents as f64) * pct / 100.0) as i64
        }
        "time_free" => req.estimated_total_cents, // 全免
        _ => 0,
    };
    let final_cents = (req.estimated_total_cents - discount_cents).max(0);
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "coupon_id": cid,
        "discount_type": dtype,
        "discount_cents": discount_cents,
        "final_cents": final_cents,
    }), common_error::current_request_id())))
}

/// admin 调用: 优惠券统计
pub async fn stats(
    State(st): State<AppState>,
    axum::extract::Query(q): axum::extract::Query<StatsQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query(
        "SELECT status, COUNT(*) cnt FROM coupon_grant WHERE coupon_id = ? GROUP BY status"
    )
    .bind(q.coupon_id)
    .fetch_all(st.db.pool())
    .await?;
    let mut used = 0i64;
    let mut unused = 0i64;
    let mut expired = 0i64;
    for r in &rows {
        let s: String = sqlx::Row::try_get(r, "status").unwrap_or_default();
        let c: i64 = sqlx::Row::try_get(r, "cnt").unwrap_or(0);
        match s.as_str() {
            "used" => used = c,
            "unused" => unused = c,
            "expired" => expired = c,
            _ => {}
        }
    }
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "coupon_id": q.coupon_id,
        "used": used,
        "unused": unused,
        "expired": expired,
    }), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct StatsQuery { pub coupon_id: u64 }
