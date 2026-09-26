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
use common_error::AppResult;
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
    sqlx::query(
        "INSERT INTO device_fault_report (device_id, user_id, report_source, fault_type, description, images_json)
         VALUES (?, ?, 'user', ?, ?, ?)"
    )
    .bind(&req.device_id)
    .bind(claims.user_id)
    .bind(&req.fault_type)
    .bind(req.description.as_deref())
    .bind(req.images.as_ref().and_then(|v| serde_json::to_value(v).ok()))
    .execute(st.db.pool())
    .await?;
    Ok(Json(common_error::ApiEnvelope::ok(ReportFaultResponse { submitted: true }, common_error::current_request_id())))
}
