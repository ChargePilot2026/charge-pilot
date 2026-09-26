//! 站点 CRUD

use crate::AppState;
use axum::{extract::{Path, State}, Json};
use common_auth::AdminClaims;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use serde_json::{json, Value};

pub async fn list(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query(
        "SELECT id, code, name, address, longitude, latitude, status, created_at
         FROM station WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "code": sqlx::Row::try_get::<String, _>(r, "code").unwrap_or_default(),
        "name": sqlx::Row::try_get::<String, _>(r, "name").unwrap_or_default(),
        "address": sqlx::Row::try_get::<Option<String>, _>(r, "address").ok().flatten(),
        "longitude": sqlx::Row::try_get::<f64, _>(r, "longitude").unwrap_or(0.0),
        "latitude": sqlx::Row::try_get::<f64, _>(r, "latitude").unwrap_or(0.0),
        "status": sqlx::Row::try_get::<String, _>(r, "status").unwrap_or_default(),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct StationCreateReq {
    pub code: String,
    pub name: String,
    pub address: Option<String>,
    pub longitude: f64,
    pub latitude: f64,
    pub open_hours: Option<String>,
    pub contact_phone: Option<String>,
    pub pricing_template_id: Option<u64>,
    pub split_template_id: Option<u64>,
}

pub async fn create(State(st): State<AppState>, _c: AdminClaims, Json(req): Json<StationCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let id: u64 = sqlx::query_scalar(
        "INSERT INTO station (code, name, address, longitude, latitude, open_hours, contact_phone, pricing_template_id, split_template_id)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)"
    )
    .bind(&req.code).bind(&req.name).bind(req.address.as_deref())
    .bind(req.longitude).bind(req.latitude)
    .bind(req.open_hours.as_deref()).bind(req.contact_phone.as_deref())
    .bind(req.pricing_template_id).bind(req.split_template_id)
    .fetch_one(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": id}), common_error::current_request_id())))
}

pub async fn get(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r: Option<(u64, String, String, Option<String>, f64, f64, String)> = sqlx::query_as(
        "SELECT id, code, name, address, longitude, latitude, status FROM station WHERE id = ? AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("station".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "id": r.0, "code": r.1, "name": r.2, "address": r.3,
        "longitude": r.4, "latitude": r.5, "status": r.6,
    }), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct StationUpdateReq {
    pub name: Option<String>,
    pub address: Option<String>,
    pub longitude: Option<f64>,
    pub latitude: Option<f64>,
    pub status: Option<String>,
    pub open_hours: Option<String>,
    pub pricing_template_id: Option<u64>,
    pub split_template_id: Option<u64>,
}

pub async fn update(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>, Json(req): Json<StationUpdateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let n = sqlx::query(
        "UPDATE station SET
            name = COALESCE(?, name),
            address = COALESCE(?, address),
            longitude = COALESCE(?, longitude),
            latitude = COALESCE(?, latitude),
            status = COALESCE(?, status),
            open_hours = COALESCE(?, open_hours),
            pricing_template_id = COALESCE(?, pricing_template_id),
            split_template_id = COALESCE(?, split_template_id)
         WHERE id = ? AND deleted_at IS NULL"
    )
    .bind(req.name.as_deref()).bind(req.address.as_deref())
    .bind(req.longitude).bind(req.latitude)
    .bind(req.status.as_deref()).bind(req.open_hours.as_deref())
    .bind(req.pricing_template_id).bind(req.split_template_id)
    .bind(id).execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("station".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"updated": true}), common_error::current_request_id())))
}

pub async fn delete(State(st): State<AppState>, actor: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let n = sqlx::query("UPDATE station SET deleted_at = NOW(3), deleted_by = ? WHERE id = ? AND deleted_at IS NULL")
        .bind(actor.admin_user_id).bind(id).execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("station".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"deleted": true}), common_error::current_request_id())))
}