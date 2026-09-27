//! Authenticated dashboard aggregation across data owners.

use crate::AppState;
use axum::{extract::State, Json};
use crate::auth::ActiveAdmin;
use common_error::{AppError, AppResult};
use serde_json::{json, Value};

pub async fn get(State(st): State<AppState>, c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::AdminDashboard>>> {
    let allowed: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM admin_user_role a JOIN role r ON r.id=a.role_id AND r.deleted_at IS NULL
         JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id
         WHERE a.id=? AND a.username=? AND a.status='active' AND a.deleted_at IS NULL AND p.code='dashboard.read')",
    ).bind(c.admin_user_id).bind(&c.sub).fetch_one(st.db.pool()).await?;
    if !allowed { return Err(AppError::Forbidden("缺少 dashboard.read 权限".into())); }
    let user_metrics: api_contracts::admin::UserChargeMetrics = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone())
        .get(st.cfg.service_urls.user.as_deref(), api_contracts::paths::USER_INTERNAL_DASHBOARD_METRICS, &()).await?;
    let active_alerts: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM alert_event WHERE status='active'")
        .fetch_one(st.db.pool()).await?;
    // 告警数由 admin 侧查 alert_event 后追加,user 侧不提供
    let data = api_contracts::admin::AdminDashboard {
        charging_orders: user_metrics.charging_orders,
        today_order_users: user_metrics.today_order_users,
        today_completed_orders: user_metrics.today_completed_orders,
        today_settled_cents: user_metrics.today_settled_cents,
        daily_trend: user_metrics.daily_trend,
        updated_at: user_metrics.updated_at,
        active_alerts: active_alerts.max(0) as u64,
    };
    Ok(Json(common_error::ApiEnvelope::ok(data, common_error::current_request_id())))
}
