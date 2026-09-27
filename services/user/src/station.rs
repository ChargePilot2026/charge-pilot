//! 找桩(站点查询)+ 报修
//!
//! 跨服务调用走 [`crate::clients::ServiceClient`];路径与 DTO 来自 `api_contracts::*`。
//! 禁止在 handler 里拼 URL 或 `json!{}` 构造响应。

use crate::AppState;
use api_contracts::{paths as p, NearbyStationsResponse};
use axum::{
    extract::{Path, Query, State},
    Json,
};
use common_auth::UserClaims;
use common_error::{AppError, AppResult};
use serde::Deserialize;

// ===================== DTO =====================

#[derive(Debug, Deserialize)]
pub struct NearbyUserQuery {
    pub lat: f64,
    pub lng: f64,
    #[serde(default)]
    pub radius_km: Option<f64>,
}

#[derive(Debug, Deserialize)]
pub struct ReportFaultRequest {
    pub device_id: String,
    pub fault_type: String,
    #[serde(default)]
    pub description: Option<String>,
    #[serde(default)]
    pub images: Option<Vec<String>>,
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct ReportFaultResponse {
    pub submitted: bool,
    pub report_id: String,
}

#[derive(Debug, Deserialize)]
pub struct FaultHistoryQuery {
    pub page: Option<u32>,
    pub page_size: Option<u32>,
}

// ===================== handlers =====================

pub async fn nearby(
    State(st): State<AppState>,
    Query(q): Query<api_contracts::NearbyStationsQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<NearbyStationsResponse>>> {
    let cli = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone());
    let resp = cli.get(st.cfg.service_urls.admin.as_deref(), p::ADMIN_INTERNAL_STATIONS_NEARBY, &q).await?;
    Ok(Json(common_error::ApiEnvelope::ok(resp, common_error::current_request_id())))
}

pub async fn detail(
    State(st): State<AppState>,
    Path(station_id): Path<u64>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::StationPublicDetail>>> {
    let cli = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone());
    let path = p::ADMIN_INTERNAL_STATIONS_DETAIL.replace(":station_id", &station_id.to_string());
    let resp = cli.get(st.cfg.service_urls.admin.as_deref(), &path, &()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(resp, common_error::current_request_id())))
}
pub async fn report_fault(
    State(st): State<AppState>,
    claims: UserClaims,
    Json(req): Json<ReportFaultRequest>,
) -> AppResult<Json<common_error::ApiEnvelope<ReportFaultResponse>>> {
    let device_id = req.device_id.trim();
    if device_id.is_empty() || device_id.len() > 64 || !device_id.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-') {
        return Err(common_error::AppError::BadRequest("设备编号格式无效".into()));
    }
    if !["mechanical", "electrical", "communication", "display", "other"].contains(&req.fault_type.as_str()) {
        return Err(common_error::AppError::BadRequest("故障类型无效".into()));
    }
    if req.description.as_deref().is_some_and(|v| v.chars().count() > 2000 || v.chars().any(|c| c.is_control() && c != '\n' && c != '\r' && c != '\t')) {
        return Err(common_error::AppError::BadRequest("故障说明不得超过 2000 字或包含控制字符".into()));
    }
    if req.images.as_ref().is_some_and(|images| images.len() > 5 || images.iter().any(|url| url.len() > 512 || !url.starts_with("https://") || url.chars().any(char::is_control))) {
        return Err(common_error::AppError::BadRequest("故障图片链接无效".into()));
    }
    let device_path = p::GW_DEVICE_GET.replace(":id", device_id);
    let _: serde_json::Value = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone())
        .get(st.cfg.service_urls.gateway.as_deref(), &device_path, &()).await?;
    if !st.redis_cache.rate_limit(&format!("rate:device-fault:{}", claims.user_id), 5, 86_400).await? {
        return Err(common_error::AppError::RateLimited("今日报修次数已达上限".into()));
    }
    let mut tx = st.db.pool().begin().await?;
    let report_id = sqlx::query(
        "INSERT INTO device_fault_report (device_id, user_id, report_source, fault_type, description, images_json)
         VALUES (?, ?, 'user', ?, ?, ?)"
    )
    .bind(device_id)
    .bind(claims.user_id)
    .bind(&req.fault_type)
    .bind(req.description.as_deref())
    .bind(req.images.as_ref().map(serde_json::to_value).transpose()?)
    .execute(&mut *tx)
    .await?
    .last_insert_id();
    sqlx::query(
        "INSERT INTO device_fault_report_event(report_id,actor_id,event_type,to_status,note,user_visible,created_at)
         VALUES(?,?,'reported','open','用户提交报修',1,UTC_TIMESTAMP(3))",
    ).bind(report_id).bind(claims.user_id).execute(&mut *tx).await?;
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(ReportFaultResponse { submitted: true, report_id: report_id.to_string() }, common_error::current_request_id())))
}

pub async fn my_fault_reports(
    State(st): State<AppState>, claims: UserClaims, Query(q): Query<FaultHistoryQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::PagedResponse<api_contracts::charge::MyFaultReport>>> >{
    let page = q.page.unwrap_or(1);
    let page_size = q.page_size.unwrap_or(20);
    if page == 0 || page > 100_000 || !(1..=100).contains(&page_size) {
        return Err(common_error::AppError::BadRequest("page 必须为 1–100000，page_size 必须为 1–100".into()));
    }
    let offset = u64::from(page - 1) * u64::from(page_size);
    let total: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM device_fault_report WHERE user_id=? AND deleted_at IS NULL")
        .bind(claims.user_id).fetch_one(st.db.pool()).await?;
    let rows = sqlx::query(
        "SELECT id,device_id,fault_type,description,status,assigned_to,resolved_at,created_at,updated_at
         FROM device_fault_report WHERE user_id=? AND deleted_at IS NULL
         ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?",
    ).bind(claims.user_id).bind(page_size).bind(offset).fetch_all(st.db.pool()).await?;
    let items = rows.iter().map(|row| -> common_error::AppResult<api_contracts::charge::MyFaultReport> { Ok(api_contracts::charge::MyFaultReport {
        report_id: sqlx::Row::try_get::<u64,_>(row,"id")?.to_string(),
        device_id: sqlx::Row::try_get::<String,_>(row,"device_id")?,
        fault_type: sqlx::Row::try_get::<String,_>(row,"fault_type")?,
        description: sqlx::Row::try_get::<Option<String>,_>(row,"description")?,
        status: sqlx::Row::try_get::<String,_>(row,"status")?,
        resolved_at: sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>,_>(row,"resolved_at")?.map(|v|v.to_rfc3339()),
        created_at: sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>,_>(row,"created_at")?.to_rfc3339(),
        updated_at: sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>,_>(row,"updated_at")?.to_rfc3339(),
    }) }).collect::<common_error::AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::common::PagedResponse { items, total, page, page_size, permissions: vec![] },
        common_error::current_request_id(),
    )))
}

pub async fn my_fault_history(
    State(st): State<AppState>,
    claims: UserClaims,
    Path(id): Path<String>,
    Query(q): Query<FaultHistoryQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::PagedResponse<api_contracts::charge::MyFaultEvent>>> >{
    let report_id = id.parse::<u64>().map_err(|_| AppError::BadRequest("报修编号无效".into()))?;
    let page = q.page.unwrap_or(1);
    let page_size = q.page_size.unwrap_or(20);
    if page == 0 || page > 100_000 || !(1..=100).contains(&page_size) {
        return Err(AppError::BadRequest("page 必须为 1–100000，page_size 必须为 1–100".into()));
    }
    let owns_report: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM device_fault_report WHERE id=? AND user_id=? AND deleted_at IS NULL)",
    ).bind(report_id).bind(claims.user_id).fetch_one(st.db.pool()).await?;
    if !owns_report { return Err(AppError::NotFound("报修不存在".into())); }
    let offset = u64::from(page - 1) * u64::from(page_size);
    let total: i64 = sqlx::query_scalar(
        "SELECT COUNT(*) FROM device_fault_report_event WHERE report_id=? AND user_visible=1",
    ).bind(report_id).fetch_one(st.db.pool()).await?;
    let rows = sqlx::query(
        "SELECT id,event_type,from_status,to_status,note,created_at
         FROM device_fault_report_event WHERE report_id=? AND user_visible=1
         ORDER BY created_at,id LIMIT ? OFFSET ?",
    ).bind(report_id).bind(page_size).bind(offset).fetch_all(st.db.pool()).await?;
    let items = rows.iter().map(|row| -> AppResult<api_contracts::charge::MyFaultEvent> { Ok(api_contracts::charge::MyFaultEvent {
        event_id: sqlx::Row::try_get::<u64,_>(row,"id")?.to_string(),
        event_type: sqlx::Row::try_get::<String,_>(row,"event_type")?,
        from_status: sqlx::Row::try_get::<Option<String>,_>(row,"from_status")?,
        to_status: sqlx::Row::try_get::<Option<String>,_>(row,"to_status")?,
        note: sqlx::Row::try_get::<Option<String>,_>(row,"note")?,
        created_at: sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>,_>(row,"created_at")?.to_rfc3339(),
    }) }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::common::PagedResponse { items, total, page, page_size, permissions: vec![] },
        common_error::current_request_id(),
    )))
}
