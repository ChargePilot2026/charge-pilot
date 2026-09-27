//! admin 内部 API(供其他服务调用,需要 ServiceToken)

use crate::AppState;
use crate::api_types;
use axum::{extract::{Path, State}, Json};
use common_error::{AppError, AppResult};
use serde_json::{json, Value};

pub async fn announcements_active(State(st): State<AppState>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::ActiveAnnouncementsResponse>>> {
    let rows = sqlx::query(
        "SELECT id, title, content, priority, start_at, end_at
         FROM announcement
         WHERE status = 'published' AND deleted_at IS NULL AND start_at <= NOW() AND (end_at IS NULL OR end_at >= NOW())
         ORDER BY priority DESC, id DESC LIMIT 50"
    ).fetch_all(st.db.pool()).await?;
    let items = rows.iter().map(|r| -> AppResult<api_contracts::charge::Announcement> {
        Ok(api_contracts::charge::Announcement {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            title: sqlx::Row::try_get::<String, _>(r, "title")?,
            content: sqlx::Row::try_get::<String, _>(r, "content")?,
            priority: sqlx::Row::try_get::<u8, _>(r, "priority")?,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::charge::ActiveAnnouncementsResponse { items },
        common_error::current_request_id(),
    )))
}

#[derive(Debug, serde::Deserialize)]
pub struct NearbyQuery {
    pub lat: f64,
    pub lng: f64,
    pub radius_km: Option<f64>,
}

pub async fn stations_nearby(State(st): State<AppState>, axum::extract::Query(q): axum::extract::Query<NearbyQuery>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::NearbyStations>>> {
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
    let items: Vec<Option<api_contracts::admin::NearbyStation>> = rows.iter().map(|r| -> AppResult<Option<api_contracts::admin::NearbyStation>> {
        let slat = sqlx::Row::try_get::<f64, _>(r, "latitude")?;
        let slng = sqlx::Row::try_get::<f64, _>(r, "longitude")?;
        let dist = ((slat - lat).powi(2) + (slng - lng).powi(2)).sqrt() * 111.0; // 近似 km
        if dist > radius { return Ok(None); }
        Ok(Some(api_contracts::admin::NearbyStation {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            code: sqlx::Row::try_get::<String, _>(r, "code")?,
            name: sqlx::Row::try_get::<String, _>(r, "name")?,
            address: sqlx::Row::try_get::<Option<String>, _>(r, "address")?,
            longitude: slng,
            latitude: slat,
            distance_km: dist,
        }))
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::admin::NearbyStations { items: items.into_iter().flatten().collect() },
        common_error::current_request_id(),
    )))
}

pub async fn stations_detail(State(st): State<AppState>, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::StationForUser>>> {
    let r: Option<(u64, String, String, Option<String>, f64, f64, String)> = sqlx::query_as(
        "SELECT id, code, name, address, longitude, latitude, status FROM station WHERE id = ? AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("station".into()))?;
    // D18 修复:SQL 列序为 (…, longitude, latitude, …) 即 r.4=longitude、r.5=latitude,
    // 原实现写成 longitude: r.5 / latitude: r.4,经纬度颠倒。这里显式按名赋值。
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::admin::StationForUser {
            id: r.0,
            code: r.1,
            name: r.2,
            address: r.3,
            longitude: r.4,
            latitude: r.5,
            status: r.6,
        },
        common_error::current_request_id(),
    )))
}

pub async fn pricing_rule_get(State(st): State<AppState>, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::PricingRuleForBilling>>> {
    let r: Option<(u64, String, String, i64, i64, i64, u32)> = sqlx::query_as(
        "SELECT id, name, mode, service_fee_cents_per_kwh, service_fee_cents_per_min, min_charge_cents, version
         FROM pricing_rule WHERE id = ? AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("pricing_rule".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::admin::PricingRuleForBilling {
            id: r.0, name: r.1, mode: r.2,
            service_fee_cents_per_kwh: r.3,
            service_fee_cents_per_min: r.4,
            min_charge_cents: r.5,
            version: r.6,
        },
        common_error::current_request_id(),
    )))
}

pub async fn split_template_get(State(st): State<AppState>, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::SplitTemplateWithParties>>> {
    let r: Option<(u64, String, String, String)> = sqlx::query_as(
        "SELECT id, code, name, mode FROM split_template WHERE id = ? AND status = 'active' AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("split_template".into()))?;
    let party_rows = sqlx::query("SELECT id, party_code, party_name, ratio_bp FROM split_party WHERE split_template_id = ? ORDER BY id")
        .bind(id).fetch_all(st.db.pool()).await?;
    let parties = party_rows.iter().map(|p| -> AppResult<api_contracts::admin::SplitParty> { Ok(api_contracts::admin::SplitParty {
        id: sqlx::Row::try_get::<u64, _>(p, "id")?,
        party_code: sqlx::Row::try_get::<String, _>(p, "party_code")?,
        party_name: sqlx::Row::try_get::<String, _>(p, "party_name")?,
        ratio_bp: sqlx::Row::try_get::<u32, _>(p, "ratio_bp")?,
    }) }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::admin::SplitTemplateWithParties {
            id: r.0, code: r.1, name: r.2, mode: r.3, parties,
        },
        common_error::current_request_id(),
    )))
}

#[derive(Debug, serde::Deserialize)]
pub struct AlertsQuery { pub device_id: Option<String>, pub status: Option<String> }

pub async fn alerts_active(State(st): State<AppState>, axum::extract::Query(q): axum::extract::Query<AlertsQuery>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::ActiveAlert>>>>{
    let mut sql = String::from("SELECT id, device_id, severity, metric, status, created_at FROM alert_event WHERE status = 'active'");
    if q.device_id.is_some() { sql.push_str(" AND device_id = ?"); }
    sql.push_str(" ORDER BY id DESC LIMIT 100");
    let mut query = sqlx::query(&sql);
    if let Some(d) = &q.device_id { query = query.bind(d); }
    let rows = query.fetch_all(st.db.pool()).await?;
    let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::ActiveAlert> { Ok(api_contracts::admin::ActiveAlert {
        id: sqlx::Row::try_get::<u64, _>(r, "id")?,
        device_id: sqlx::Row::try_get::<String, _>(r, "device_id")?,
        severity: sqlx::Row::try_get::<String, _>(r, "severity")?,
        metric: sqlx::Row::try_get::<String, _>(r, "metric")?,
    }) }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::common::ListResponse::new(items),
        common_error::current_request_id(),
    )))
}

pub async fn device_reboot(State(st): State<AppState>, Path(id): Path<String>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::gateway_devices::NotImplementedResponse>>> {
    // 调 gateway 内部接口 — 类型化 client + 路径常量,禁止拼 URL
    let cli = crate::clients::ServiceClient::new(st.http.clone(), st.service_token.clone());
    let path = api_types::paths::INTERNAL_DEVICES_REBOOT.replace(":id", &id);
    // gateway 侧该端点是未接入的桩,响应类型即 NotImplementedResponse。
    let v: api_contracts::gateway_devices::NotImplementedResponse = cli
        .post_typed(st.cfg.service_urls.gateway.as_deref(), &path, &serde_json::json!({}))
        .await?;
    Ok(Json(common_error::ApiEnvelope::ok(v, common_error::current_request_id())))
}

/// Worker calls this endpoint so only the admin service writes announcement state.
pub async fn announcements_expire(State(st): State<AppState>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::AnnouncementExpireResult>>> {
    let result = sqlx::query(
        "UPDATE announcement SET status = 'expired'
         WHERE status = 'published' AND end_at IS NOT NULL AND end_at < UTC_TIMESTAMP(3) AND deleted_at IS NULL"
    ).execute(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::admin::AnnouncementExpireResult { expired_count: result.rows_affected() },
        common_error::current_request_id(),
    )))
}

#[derive(Debug, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct CustomerServiceEntryQuery {
    pub scene: String,
}

pub async fn customer_service_entry(
    State(st): State<AppState>,
    axum::extract::Query(q): axum::extract::Query<CustomerServiceEntryQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::charge::CustomerServiceEntry>>> {
    if !["general", "refund", "complaint"].contains(&q.scene.as_str()) {
        return Err(AppError::BadRequest("客服场景无效".into()));
    }
    let row = sqlx::query(
        "SELECT agent_wechat, agent_name, path FROM customer_service_config
         WHERE enabled = 1 ORDER BY priority DESC, id ASC LIMIT 1",
    )
    .fetch_optional(st.db.pool())
    .await?
    .ok_or_else(|| AppError::NotFound("当前暂无可用客服".into()))?;
    let entry_url: Option<String> = sqlx::Row::try_get(&row, "path")?;
    let entry_url = entry_url.filter(|url| url.starts_with("https://") && !url.chars().any(char::is_control));
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::charge::CustomerServiceEntry {
            agent_wechat: sqlx::Row::try_get::<String, _>(&row, "agent_wechat")?,
            agent_name: sqlx::Row::try_get::<Option<String>, _>(&row, "agent_name")?,
            entry_url,
            scene: q.scene,
        },
        common_error::current_request_id(),
    )))
}
