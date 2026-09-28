//! 运营仪表盘 —— 跨数据源的聚合读
//!
//! 指标本体归 user 服务,admin 只做三件事:复核 `dashboard.read` 权限、
//! 取 user 侧指标、追加 admin 库自己的活跃告警数。
//! 聚合本身没有业务归属(它横跨 order / alert),故留在 [`crate::api`] 下,
//! 不硬塞进某一个能力域。

use crate::AppState;
use axum::{extract::State, Json};
use crate::capability::identity::ActiveAdmin;
use common_error::{AppError, AppResult};

pub async fn get(State(st): State<AppState>, c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::AdminDashboard>>> {
    if !crate::capability::alert::repository_sql::has_dashboard_permission(&st, &c).await? {
        return Err(AppError::Forbidden("缺少 dashboard.read 权限".into()));
    }
    let user_metrics: api_contracts::admin::UserChargeMetrics = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone())
        .get(st.cfg.service_urls.user.as_deref(), api_contracts::paths::USER_INTERNAL_DASHBOARD_METRICS, &()).await?;
    // 告警数由 admin 侧查 alert_event 后追加,user 侧不提供
    let active_alerts = crate::capability::alert::repository_sql::active_alert_count(&st).await?;
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
