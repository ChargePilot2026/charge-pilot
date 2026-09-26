//! admin 内部 API(供其他服务调用,需要 ServiceToken)

use crate::AppState;
use crate::api_types;
use axum::{extract::{Path, State}, Json};
use common_error::{AppError, AppResult};
use serde_json::{json, Value};

pub async fn announcements_active(State(st): State<AppState>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query(
        "SELECT id, title, content, priority, start_at, end_at
         FROM announcement
         WHERE status = 'published' AND start_at <= NOW() AND (end_at IS NULL OR end_at >= NOW())
         ORDER BY priority DESC, id DESC LIMIT 50"
    ).fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "title": sqlx::Row::try_get::<String, _>(r, "title").unwrap_or_default(),
        "content": sqlx::Row::try_get::<String, _>(r, "content").unwrap_or_default(),
        "priority": sqlx::Row::try_get::<u8, _>(r, "priority").unwrap_or(0),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, serde::Deserialize)]
pub struct NearbyQuery {
    pub lat: f64,
    pub lng: f64,
    pub radius_km: Option<f64>,
}

pub async fn stations_nearby(State(st): State<AppState>, axum::extract::Query(q): axum::extract::Query<NearbyQuery>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let radius = q.radius_km.unwrap_or(5.0);
    let lat = q.lat;
    let lng = q.lng;
    // 简单边界:经纬度 ± 0.1 度近似筛选
    let rows = sqlx::query(
        "SELECT id, code, name, address, longitude, latitude
         FROM station
         WHERE status = 'active' AND deleted_at IS NULL
           AND latitude BETWEEN ? AND ?
           AND longitude BETWEEN ? AND ?
         LIMIT 100"
    )
    .bind(lat - 1.0).bind(lat + 1.0)
    .bind(lng - 1.0).bind(lng + 1.0)
    .fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().filter_map(|r| {
        let slat = sqlx::Row::try_get::<f64, _>(r, "latitude").ok()?;
        let slng = sqlx::Row::try_get::<f64, _>(r, "longitude").ok()?;
        let dist = ((slat - lat).powi(2) + (slng - lng).powi(2)).sqrt() * 111.0; // 近似 km
        if dist > radius { return None; }
        Some(json!({
            "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
            "code": sqlx::Row::try_get::<String, _>(r, "code").unwrap_or_default(),
            "name": sqlx::Row::try_get::<String, _>(r, "name").unwrap_or_default(),
            "address": sqlx::Row::try_get::<Option<String>, _>(r, "address").ok().flatten(),
            "longitude": slng,
            "latitude": slat,
            "distance_km": dist,
        }))
    }).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

pub async fn stations_detail(State(st): State<AppState>, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r: Option<(u64, String, String, Option<String>, f64, f64, String)> = sqlx::query_as(
        "SELECT id, code, name, address, longitude, latitude, status FROM station WHERE id = ? AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("station".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "id": r.0, "code": r.1, "name": r.2, "address": r.3, "longitude": r.5, "latitude": r.4, "status": r.6,
    }), common_error::current_request_id())))
}

pub async fn pricing_rule_get(State(st): State<AppState>, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r: Option<(u64, String, String, i64, i64, i64, u32)> = sqlx::query_as(
        "SELECT id, name, mode, service_fee_cents_per_kwh, service_fee_cents_per_min, min_charge_cents, version
         FROM pricing_rule WHERE id = ? AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("pricing_rule".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "id": r.0, "name": r.1, "mode": r.2,
        "service_fee_cents_per_kwh": r.3,
        "service_fee_cents_per_min": r.4,
        "min_charge_cents": r.5,
        "version": r.6,
    }), common_error::current_request_id())))
}

pub async fn split_template_get(State(st): State<AppState>, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r: Option<(u64, String, String, String)> = sqlx::query_as(
        "SELECT id, code, name, mode FROM split_template WHERE id = ? AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("split_template".into()))?;
    let parties: Vec<Value> = sqlx::query("SELECT party_code, party_name, ratio_bp FROM split_party WHERE split_template_id = ?")
        .bind(id).fetch_all(st.db.pool()).await
        .ok()
        .map(|rows| rows.iter().map(|p| json!({
            "party_code": sqlx::Row::try_get::<String, _>(p, "party_code").unwrap_or_default(),
            "party_name": sqlx::Row::try_get::<String, _>(p, "party_name").unwrap_or_default(),
            "ratio_bp": sqlx::Row::try_get::<u32, _>(p, "ratio_bp").unwrap_or(0),
        })).collect()).unwrap_or_default();
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "id": r.0, "code": r.1, "name": r.2, "mode": r.3, "parties": parties,
    }), common_error::current_request_id())))
}

pub async fn export_task_get(State(st): State<AppState>, Path(id): Path<String>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r = sqlx::query("SELECT task_code, name, last_run_at, next_run_at, config_json FROM scheduled_task WHERE task_code = 'export_run'")
        .fetch_optional(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "task_id": id,
        "found": r.is_some(),
        "info": r.map(|r| json!({
            "task_code": sqlx::Row::try_get::<String, _>(&r, "task_code").unwrap_or_default(),
            "name": sqlx::Row::try_get::<String, _>(&r, "name").unwrap_or_default(),
            "last_run_at": sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(&r, "last_run_at").ok().flatten().map(|t| t.to_rfc3339()),
        })),
    }), common_error::current_request_id())))
}

#[derive(Debug, serde::Deserialize)]
pub struct AlertsQuery { pub device_id: Option<String>, pub status: Option<String> }

pub async fn alerts_active(State(st): State<AppState>, axum::extract::Query(q): axum::extract::Query<AlertsQuery>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let mut sql = String::from("SELECT id, device_id, severity, metric, status, created_at FROM alert_event WHERE status = 'active'");
    if q.device_id.is_some() { sql.push_str(" AND device_id = ?"); }
    sql.push_str(" ORDER BY id DESC LIMIT 100");
    let mut query = sqlx::query(&sql);
    if let Some(d) = &q.device_id { query = query.bind(d); }
    let rows = query.fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "device_id": sqlx::Row::try_get::<String, _>(r, "device_id").unwrap_or_default(),
        "severity": sqlx::Row::try_get::<String, _>(r, "severity").unwrap_or_default(),
        "metric": sqlx::Row::try_get::<String, _>(r, "metric").unwrap_or_default(),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

pub async fn device_reboot(State(st): State<AppState>, Path(id): Path<String>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    // 调 gateway 内部接口 — 类型化 client + 路径常量,禁止拼 URL
    let cli = crate::clients::ServiceClient::new(st.http.clone(), st.service_token.clone());
    let v: Value = cli
        .post_typed(st.cfg.service_urls.gateway.as_deref(), api_types::paths::INTERNAL_DEVICES_REBOOT, &serde_json::json!({}))
        .await?;
    Ok(Json(common_error::ApiEnvelope::ok(v, common_error::current_request_id())))
}