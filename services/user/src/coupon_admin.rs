//! Internal coupon-template management and idempotent operator grants.
use crate::AppState;
use axum::{extract::{Path, State}, Json};
use common_error::{ApiEnvelope, AppError, AppResult};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use sqlx::Row;

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct CouponCreate {
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
pub struct CouponUpdate {
    pub name: Option<String>,
    pub status: Option<String>,
    pub end_at: Option<chrono::DateTime<chrono::Utc>>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct CouponGrantRequest {
    pub request_id: String,
    pub user_id: u64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CouponGrantResult {
    pub request_id: String,
    pub coupon_id: u64,
    pub coupon_grant_id: u64,
    pub user_id: u64,
    pub status: String,
    pub expired_at: String,
}

fn validate_coupon(req: &CouponCreate) -> AppResult<()> {
    let code = req.code.trim();
    let name = req.name.trim();
    let valid_hours = req.valid_hours.unwrap_or(24);
    if code.is_empty() || code.len() > 64 || code.chars().any(char::is_control)
        || name.is_empty() || name.chars().count() > 128 || name.chars().any(char::is_control)
    {
        return Err(AppError::BadRequest("优惠券编码或名称无效".into()));
    }
    let valid_discount = match req.discount_type.as_str() {
        "amount" => req.discount_value_cents.is_some_and(|v| v > 0 && v <= 100_000_000)
            && req.discount_percent.is_none(),
        "percentage" => req.discount_value_cents.is_none()
            && req.discount_percent.is_some_and(|v| v.is_finite() && v > 0.0 && v <= 100.0),
        "time_free" => req.discount_value_cents.is_none() && req.discount_percent.is_none(),
        _ => false,
    };
    if !valid_discount
        || req.min_charge_cents.unwrap_or(0) < 0
        || req.min_charge_cents.unwrap_or(0) > 100_000_000
        || !(1..=8760).contains(&valid_hours)
        || req.total_quota.unwrap_or(0) < 0 || req.total_quota.unwrap_or(0) > i64::from(u32::MAX)
        || req.per_user_quota.unwrap_or(1) < 1
        || req.per_user_quota.unwrap_or(1) > 100_000
        || req.start_at.as_ref().zip(req.end_at.as_ref()).is_some_and(|(start, end)| start >= end)
    {
        return Err(AppError::BadRequest("优惠券折扣、额度、有效期或时间范围无效".into()));
    }
    Ok(())
}

fn coupon_json(row: &sqlx::mysql::MySqlRow) -> AppResult<Value> {
    Ok(json!({
        "id": row.try_get::<u64, _>("id")?,
        "code": row.try_get::<String, _>("code")?,
        "name": row.try_get::<String, _>("name")?,
        "discount_type": row.try_get::<String, _>("discount_type")?,
        "discount_value_cents": row.try_get::<Option<i64>, _>("discount_value_cents")?,
        "discount_percent": row.try_get::<Option<f64>, _>("discount_percent")?,
        "min_charge_cents": row.try_get::<i64, _>("min_charge_cents")?,
        "valid_hours": row.try_get::<i64, _>("valid_hours")?,
        "total_quota": row.try_get::<i64, _>("total_quota")?,
        "per_user_quota": row.try_get::<i64, _>("per_user_quota")?,
        "status": row.try_get::<String, _>("status")?,
        "start_at": row.try_get::<Option<chrono::NaiveDateTime>, _>("start_at")?.map(|v| v.and_utc().to_rfc3339()),
        "end_at": row.try_get::<Option<chrono::NaiveDateTime>, _>("end_at")?.map(|v| v.and_utc().to_rfc3339()),
    }))
}

pub async fn list(State(st): State<AppState>) -> AppResult<Json<ApiEnvelope<Value>>> {
    let rows = sqlx::query("SELECT id,code,name,discount_type,discount_value_cents,CAST(discount_percent AS DOUBLE) AS discount_percent,min_charge_cents,valid_hours,total_quota,per_user_quota,status,start_at,end_at FROM coupon WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 200")
        .fetch_all(st.db.pool()).await?;
    let items = rows.iter().map(coupon_json).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(ApiEnvelope::ok(json!({"items":items}), common_error::current_request_id())))
}

pub async fn create(State(st): State<AppState>, Json(req): Json<CouponCreate>) -> AppResult<Json<ApiEnvelope<Value>>> {
    validate_coupon(&req)?;
    let result = sqlx::query("INSERT INTO coupon (code,name,discount_type,discount_value_cents,discount_percent,min_charge_cents,valid_hours,total_quota,per_user_quota,start_at,end_at) VALUES (?,?,?,?,?,COALESCE(?,0),COALESCE(?,24),COALESCE(?,0),COALESCE(?,1),?,?)")
        .bind(req.code.trim()).bind(req.name.trim()).bind(&req.discount_type).bind(req.discount_value_cents)
        .bind(req.discount_percent).bind(req.min_charge_cents).bind(req.valid_hours).bind(req.total_quota)
        .bind(req.per_user_quota).bind(req.start_at.as_ref().map(|v| v.naive_utc())).bind(req.end_at.as_ref().map(|v| v.naive_utc()))
        .execute(st.db.pool()).await?;
    Ok(Json(ApiEnvelope::ok(json!({"id":result.last_insert_id()}), common_error::current_request_id())))
}

pub async fn get(State(st): State<AppState>, Path(id): Path<u64>) -> AppResult<Json<ApiEnvelope<Value>>> {
    let row = sqlx::query("SELECT id,code,name,discount_type,discount_value_cents,CAST(discount_percent AS DOUBLE) AS discount_percent,min_charge_cents,valid_hours,total_quota,per_user_quota,status,start_at,end_at FROM coupon WHERE id=? AND deleted_at IS NULL")
        .bind(id).fetch_optional(st.db.pool()).await?.ok_or_else(|| AppError::NotFound("coupon".into()))?;
    Ok(Json(ApiEnvelope::ok(coupon_json(&row)?, common_error::current_request_id())))
}

pub async fn update(State(st): State<AppState>, Path(id): Path<u64>, Json(req): Json<CouponUpdate>) -> AppResult<Json<ApiEnvelope<Value>>> {
    if req.name.as_deref().is_some_and(|v| v.trim().is_empty() || v.chars().count() > 128 || v.chars().any(char::is_control))
        || req.status.as_deref().is_some_and(|v| !["active", "disabled"].contains(&v))
    {
        return Err(AppError::BadRequest("优惠券更新内容无效".into()));
    }
    let changed = sqlx::query("UPDATE coupon SET name=COALESCE(?,name),status=COALESCE(?,status),end_at=COALESCE(?,end_at) WHERE id=? AND deleted_at IS NULL")
        .bind(req.name.as_deref().map(str::trim)).bind(req.status.as_deref()).bind(req.end_at.as_ref().map(|v| v.naive_utc())).bind(id)
        .execute(st.db.pool()).await?;
    if changed.rows_affected() == 0 { return Err(AppError::NotFound("coupon".into())); }
    Ok(Json(ApiEnvelope::ok(json!({"updated":true}), common_error::current_request_id())))
}

pub async fn delete(State(st): State<AppState>, Path(id): Path<u64>) -> AppResult<Json<ApiEnvelope<Value>>> {
    let mut tx = st.db.pool().begin().await?;
    let exists: Option<u64> = sqlx::query_scalar("SELECT id FROM coupon WHERE id=? AND deleted_at IS NULL FOR UPDATE")
        .bind(id).fetch_optional(&mut *tx).await?;
    if exists.is_none() { return Err(AppError::NotFound("coupon".into())); }
    let issued: bool = sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM coupon_grant WHERE coupon_id=? AND deleted_at IS NULL)")
        .bind(id).fetch_one(&mut *tx).await?;
    if issued { return Err(AppError::Conflict("优惠券已有发放记录，请停用模板".into())); }
    let changed = sqlx::query("UPDATE coupon SET deleted_at=UTC_TIMESTAMP(3) WHERE id=? AND deleted_at IS NULL")
        .bind(id).execute(&mut *tx).await?;
    if changed.rows_affected() == 0 { return Err(AppError::NotFound("coupon".into())); }
    tx.commit().await?;
    Ok(Json(ApiEnvelope::ok(json!({"deleted":true}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct StatsQuery { pub coupon_id: u64 }

pub async fn stats(State(st): State<AppState>, axum::extract::Query(q): axum::extract::Query<StatsQuery>) -> AppResult<Json<ApiEnvelope<Value>>> {
    let row = sqlx::query("SELECT c.total_quota,COUNT(g.id) AS granted_count,
       COALESCE(SUM(g.status='used'),0) AS used_count,
       COALESCE(SUM(g.status='unused' AND g.expired_at>UTC_TIMESTAMP(3)),0) AS unused_count,
       COALESCE(SUM(g.status='expired' OR (g.status='unused' AND g.expired_at<=UTC_TIMESTAMP(3))),0) AS expired_count
       FROM coupon c LEFT JOIN coupon_grant g ON g.coupon_id=c.id AND g.deleted_at IS NULL
       WHERE c.id=? AND c.deleted_at IS NULL GROUP BY c.id,c.total_quota")
        .bind(q.coupon_id).fetch_optional(st.db.pool()).await?.ok_or_else(|| AppError::NotFound("coupon".into()))?;
    let granted: i64 = row.try_get("granted_count")?;
    let used: i64 = row.try_get("used_count")?;
    let total_quota: i64 = row.try_get("total_quota")?;
    let counts = json!({"coupon_id":q.coupon_id,"total_quota":total_quota,"granted_count":granted,
        "used_count":used,"unused_count":row.try_get::<i64,_>("unused_count")?,
        "expired_count":row.try_get::<i64,_>("expired_count")?,
        "usage_rate":if granted>0 {used as f64/granted as f64} else {0.0}});
    Ok(Json(ApiEnvelope::ok(counts, common_error::current_request_id())))
}

/// A single grant transaction locks the template, so both total and per-user quotas
/// remain correct when activity and operator grants arrive concurrently.
async fn create_grant(
    pool: &sqlx::MySqlPool,
    request_id: &str,
    user_id: u64,
    coupon_id: u64,
    source: &str,
    source_event_id: Option<&str>,
) -> AppResult<CouponGrantResult> {
    let request_uuid = uuid::Uuid::parse_str(request_id)
        .map_err(|_| AppError::BadRequest("发券请求标识必须为 UUID".into()))?.to_string();
    if user_id == 0 || coupon_id == 0 || !["manual", "register", "activity", "invite", "invite_reward"].contains(&source) {
        return Err(AppError::BadRequest("发券用户、模板或来源无效".into()));
    }
    let mut tx = pool.begin().await?;
    if let Some((saved_coupon, saved_user, grant_id)) = sqlx::query_as::<_, (u64,u64,u64)>("SELECT coupon_id,user_id,coupon_grant_id FROM coupon_grant_request WHERE request_id=?")
        .bind(&request_uuid).fetch_optional(&mut *tx).await? {
        if saved_coupon != coupon_id || saved_user != user_id { return Err(AppError::Conflict("发券幂等标识已用于其他用户或模板".into())); }
        let expired_at: chrono::NaiveDateTime = sqlx::query_scalar("SELECT expired_at FROM coupon_grant WHERE id=? AND user_id=? AND coupon_id=?")
            .bind(grant_id).bind(user_id).bind(coupon_id).fetch_one(&mut *tx).await?;
        tx.commit().await?;
        return Ok(CouponGrantResult {request_id:request_uuid,coupon_id,coupon_grant_id:grant_id,user_id,status:"issued".into(),expired_at:expired_at.and_utc().to_rfc3339()});
    }
    let template = sqlx::query("SELECT status,valid_hours,total_quota,per_user_quota,start_at,end_at FROM coupon WHERE id=? AND deleted_at IS NULL FOR UPDATE")
        .bind(coupon_id).fetch_optional(&mut *tx).await?.ok_or_else(|| AppError::NotFound("coupon".into()))?;
    let status: String = template.try_get("status")?;
    let valid_hours: i64 = template.try_get("valid_hours")?;
    let total_quota: i64 = template.try_get("total_quota")?;
    let per_user_quota: i64 = template.try_get("per_user_quota")?;
    let start_at: Option<chrono::NaiveDateTime> = template.try_get("start_at")?;
    let end_at: Option<chrono::NaiveDateTime> = template.try_get("end_at")?;
    if status != "active" || valid_hours <= 0 || start_at.is_some_and(|v| v > chrono::Utc::now().naive_utc())
        || end_at.is_some_and(|v| v <= chrono::Utc::now().naive_utc())
    { return Err(AppError::Conflict("优惠券未启用或不在发放时间范围内".into())); }
    let user_active: bool = sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM user WHERE id=? AND status='active' AND deleted_at IS NULL)")
        .bind(user_id).fetch_one(&mut *tx).await?;
    if !user_active { return Err(AppError::NotFound("有效用户不存在".into())); }
    let total: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM coupon_grant WHERE coupon_id=? AND deleted_at IS NULL")
        .bind(coupon_id).fetch_one(&mut *tx).await?;
    let per_user: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM coupon_grant WHERE coupon_id=? AND user_id=? AND deleted_at IS NULL")
        .bind(coupon_id).bind(user_id).fetch_one(&mut *tx).await?;
    if (total_quota > 0 && total >= total_quota) || per_user >= per_user_quota {
        return Err(AppError::Conflict("优惠券发放额度已用尽".into()));
    }
    if !(1..=8760).contains(&valid_hours) { return Err(AppError::Conflict("优惠券有效小时配置无效".into())); }
    let now = chrono::Utc::now().naive_utc();
    let expires_at = now.checked_add_signed(chrono::Duration::hours(valid_hours))
        .ok_or_else(|| AppError::Conflict("优惠券有效期超出范围".into()))?;
    let expires_at = end_at.map_or(expires_at, |end| end.min(expires_at));
    if expires_at <= now { return Err(AppError::Conflict("优惠券有效期已结束".into())); }
    let grant_id = sqlx::query("INSERT INTO coupon_grant (coupon_id,user_id,grant_source,source_event_id,expired_at) VALUES (?,?,?,?,?)")
        .bind(coupon_id).bind(user_id).bind(source).bind(source_event_id).bind(expires_at).execute(&mut *tx).await?.last_insert_id();
    sqlx::query("INSERT INTO coupon_grant_request (request_id,coupon_id,user_id,coupon_grant_id) VALUES (?,?,?,?)")
        .bind(&request_uuid).bind(coupon_id).bind(user_id).bind(grant_id).execute(&mut *tx).await?;
    tx.commit().await?;
    Ok(CouponGrantResult {request_id:request_uuid,coupon_id,coupon_grant_id:grant_id,user_id,status:"issued".into(),expired_at:expires_at.and_utc().to_rfc3339()})
}

pub async fn grant(State(st): State<AppState>, Path(coupon_id): Path<u64>, Json(req): Json<CouponGrantRequest>) -> AppResult<Json<ApiEnvelope<CouponGrantResult>>> {
    let result = create_grant(st.db.pool(), &req.request_id, req.user_id, coupon_id, "manual", None).await?;
    Ok(Json(ApiEnvelope::ok(result, common_error::current_request_id())))
}

pub async fn grant_activity_event(st: &AppState, entry: &common_redis::StreamEntry) -> AppResult<CouponGrantResult> {
    let payload = &entry.envelope.payload;
    let user_id = payload.get("user_id").and_then(Value::as_u64).ok_or_else(|| AppError::BadRequest("发券事件缺少有效 user_id".into()))?;
    let coupon_id = payload.get("coupon_id").and_then(Value::as_u64).ok_or_else(|| AppError::BadRequest("发券事件缺少有效 coupon_id".into()))?;
    let source = payload.get("source").and_then(Value::as_str).ok_or_else(|| AppError::BadRequest("发券事件缺少 source".into()))?;
    if source == "manual" { return Err(AppError::BadRequest("人工发券不能通过活动事件执行".into())); }
    create_grant(st.db.pool(), &entry.envelope.event_id, user_id, coupon_id, source, Some(&entry.envelope.event_id)).await
}
