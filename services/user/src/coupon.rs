//! 优惠券:查询我的优惠券 / 预览折扣

use crate::AppState;
use axum::{extract::{Query, State}, Json};
use common_error::{AppError, AppResult};
use serde::Deserialize;

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct CouponQuery { pub status: Option<String>, pub page: Option<u32>, pub page_size: Option<u32> }

pub async fn my(
    State(st): State<AppState>,
    claims: common_auth::UserClaims,
    Query(q): Query<CouponQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::PagedResponse<api_contracts::charge::MyCoupon>>>> {
    let status = q.status.as_deref().unwrap_or("unused");
    let page = q.page.unwrap_or(1);
    let page_size = q.page_size.unwrap_or(20);
    if !["unused", "used", "expired"].contains(&status) || page == 0 || page > 100_000 || !(1..=100).contains(&page_size) {
        return Err(AppError::BadRequest("优惠券筛选参数无效".into()));
    }
    let filter = match status {
        "unused" => "cg.status = 'unused' AND cg.expired_at > UTC_TIMESTAMP(3)",
        "used" => "cg.status = 'used'",
        _ => "(cg.status = 'expired' OR (cg.status = 'unused' AND cg.expired_at <= UTC_TIMESTAMP(3)))",
    };
    let total: i64 = sqlx::query_scalar(&format!("SELECT COUNT(*) FROM coupon_grant cg JOIN coupon c ON c.id = cg.coupon_id WHERE cg.user_id = ? AND cg.deleted_at IS NULL AND {filter}"))
        .bind(claims.user_id).fetch_one(st.db.pool()).await?;
    let rows = sqlx::query(&format!(
        "SELECT cg.id, c.name, c.discount_type, c.discount_value_cents, CAST(c.discount_percent AS DOUBLE) AS discount_percent,
                c.min_charge_cents, cg.expired_at,
                CASE WHEN cg.status = 'unused' AND cg.expired_at <= UTC_TIMESTAMP(3) THEN 'expired' ELSE cg.status END AS status
         FROM coupon_grant cg JOIN coupon c ON c.id = cg.coupon_id
         WHERE cg.user_id = ? AND cg.deleted_at IS NULL AND {filter}
         ORDER BY cg.expired_at ASC, cg.id DESC LIMIT ? OFFSET ?"
    ))
    .bind(claims.user_id)
    .bind(page_size)
    .bind(u64::from(page - 1) * u64::from(page_size))
    .fetch_all(st.db.pool())
    .await?;
    let items = rows
        .iter()
        .map(|r| -> AppResult<api_contracts::charge::MyCoupon> {
            Ok(api_contracts::charge::MyCoupon {
                grant_id: sqlx::Row::try_get::<u64, _>(r, "id")?,
                name: sqlx::Row::try_get::<String, _>(r, "name")?,
                discount_type: sqlx::Row::try_get::<String, _>(r, "discount_type")?,
                discount_value_cents: sqlx::Row::try_get::<Option<i64>, _>(r, "discount_value_cents")?,
                discount_percent: sqlx::Row::try_get::<Option<f64>, _>(r, "discount_percent")?,
                min_charge_cents: sqlx::Row::try_get::<i64, _>(r, "min_charge_cents")?,
                expired_at: sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "expired_at")?.to_rfc3339(),
                status: sqlx::Row::try_get::<String, _>(r, "status")?,
            })
        })
        .collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::common::PagedResponse { items, total, page, page_size, permissions: vec![] },
        common_error::current_request_id(),
    )))
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
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::CouponPreview>>> {
    if req.coupon_grant_id == 0 || req.estimated_total_cents <= 0 || req.estimated_total_cents > 100_000_000 {
        return Err(AppError::BadRequest("优惠券预览金额无效".into()));
    }
    let r: Option<(u64, String, Option<i64>, Option<i64>, i64, String, chrono::DateTime<chrono::Utc>)> = sqlx::query_as(
        "SELECT c.id, c.discount_type, c.discount_value_cents, CAST(c.discount_percent * 100 AS SIGNED) AS discount_percent_bp,
                c.min_charge_cents, cg.status, cg.expired_at
         FROM coupon_grant cg JOIN coupon c ON c.id = cg.coupon_id
         WHERE cg.id = ? AND cg.user_id = ? AND cg.deleted_at IS NULL
           AND c.status = 'active' AND c.deleted_at IS NULL LIMIT 1"
    )
    .bind(req.coupon_grant_id)
    .bind(claims.user_id)
    .fetch_optional(st.db.pool())
    .await?;
    let (cid, dtype, val_cents, percent_bp, min, status, expired_at) = r.ok_or_else(|| AppError::NotFound("coupon".into()))?;
    if status != "unused" || expired_at <= chrono::Utc::now() {
        return Err(AppError::Conflict("coupon not unused".into()));
    }
    if req.estimated_total_cents < min {
        return Err(AppError::Conflict("below min charge".into()));
    }
    let discount_cents = match dtype.as_str() {
        "amount" => {
            let value = val_cents.unwrap_or(0);
            if value < 0 { return Err(AppError::Internal("优惠券金额配置无效".into())); }
            value.min(req.estimated_total_cents)
        },
        "percentage" => {
            let bp = percent_bp.unwrap_or(0);
            if !(0..=10_000).contains(&bp) { return Err(AppError::Internal("优惠券折扣比例配置无效".into())); }
            (req.estimated_total_cents * bp + 5_000) / 10_000
        }
        "time_free" => req.estimated_total_cents, // 全免
        _ => 0,
    };
    let final_cents = (req.estimated_total_cents - discount_cents).max(0);
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::charge::CouponPreview { coupon_id: cid, discount_type: dtype, discount_cents, final_cents },
        common_error::current_request_id(),
    )))
}

/// admin 调用: 优惠券统计
pub async fn stats(
    State(st): State<AppState>,
    axum::extract::Query(q): axum::extract::Query<StatsQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::MyCouponStats>>> {
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
        let s: String = sqlx::Row::try_get(r, "status")?;
        let c: i64 = sqlx::Row::try_get(r, "cnt")?;
        match s.as_str() {
            "used" => used = c,
            "unused" => unused = c,
            "expired" => expired = c,
            _ => {}
        }
    }
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::charge::MyCouponStats { coupon_id: q.coupon_id, used, unused, expired },
        common_error::current_request_id(),
    )))
}

#[derive(Debug, Deserialize)]
pub struct StatsQuery { pub coupon_id: u64 }
