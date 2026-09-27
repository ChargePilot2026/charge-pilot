//! Authenticated dashboard aggregation across data owners.

use crate::AppState;
use axum::{extract::State, Json};
use common_auth::AdminClaims;
use common_error::{AppError, AppResult};
use serde_json::{json, Value};

pub async fn get(State(st): State<AppState>, c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let allowed: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM admin_user_role a JOIN role r ON r.id=a.role_id AND r.deleted_at IS NULL
         JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id
         WHERE a.id=? AND a.username=? AND a.status='active' AND a.deleted_at IS NULL AND p.code='dashboard.read')",
    ).bind(c.admin_user_id).bind(&c.sub).fetch_one(st.db.pool()).await?;
    if !allowed { return Err(AppError::Forbidden("缺少 dashboard.read 权限".into())); }
    let user_metrics: Value = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone())
        .get(st.cfg.service_urls.user.as_deref(), api_contracts::paths::USER_INTERNAL_DASHBOARD_METRICS, &()).await?;
    let active_alerts: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM alert_event WHERE status='active'")
        .fetch_one(st.db.pool()).await?;
    let mut data = user_metrics;
    data["active_alerts"] = json!(active_alerts.max(0) as u64);
    Ok(Json(common_error::ApiEnvelope::ok(data, common_error::current_request_id())))
}
