//! Dashboard charge metrics calculated from the user-owned charge_order table.

use crate::AppState;
use axum::{extract::State, Json};
use common_error::AppResult;
use serde_json::{json, Value};
use sqlx::Row;
use std::collections::HashMap;

pub async fn metrics(State(st): State<AppState>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let today = chrono::Utc::now().date_naive();
    let today_start = today.and_hms_opt(0, 0, 0).expect("midnight");
    let tomorrow_start = (today + chrono::Duration::days(1)).and_hms_opt(0, 0, 0).expect("midnight");
    let trend_start = (today - chrono::Duration::days(6)).and_hms_opt(0, 0, 0).expect("midnight");

    let charging_orders: i64 = sqlx::query_scalar(
        "SELECT COUNT(*) FROM charge_order WHERE status='charging' AND deleted_at IS NULL",
    ).fetch_one(st.db.pool()).await?;
    let today_order_users: i64 = sqlx::query_scalar(
        "SELECT COUNT(DISTINCT user_id) FROM charge_order WHERE created_at>=? AND created_at<? AND deleted_at IS NULL",
    ).bind(today_start).bind(tomorrow_start).fetch_one(st.db.pool()).await?;
    let today_summary = sqlx::query(
        "SELECT COUNT(*) AS completed_orders,CAST(COALESCE(SUM(total_cents),0) AS SIGNED) AS settled_cents
         FROM charge_order WHERE status='completed' AND ended_at>=? AND ended_at<? AND deleted_at IS NULL",
    ).bind(today_start).bind(tomorrow_start).fetch_one(st.db.pool()).await?;
    let trend_rows = sqlx::query(
        "SELECT DATE(ended_at) AS day,COUNT(*) AS completed_orders,CAST(COALESCE(SUM(total_cents),0) AS SIGNED) AS settled_cents
         FROM charge_order WHERE status='completed' AND ended_at>=? AND ended_at<? AND deleted_at IS NULL
         GROUP BY DATE(ended_at) ORDER BY day",
    ).bind(trend_start).bind(tomorrow_start).fetch_all(st.db.pool()).await?;
    let mut by_day = HashMap::with_capacity(trend_rows.len());
    for row in &trend_rows {
        by_day.insert(
            row.try_get::<chrono::NaiveDate, _>("day")?,
            (row.try_get::<i64, _>("completed_orders")?, row.try_get::<i64, _>("settled_cents")?),
        );
    }
    let mut trend = Vec::with_capacity(7);
    for offset in 0..7 {
        let day = today - chrono::Duration::days(6 - offset);
        let (completed_orders, settled_cents) = by_day.get(&day).copied().unwrap_or_default();
        trend.push(json!({
            "day":day.to_string(),
            "completed_orders":completed_orders.max(0) as u64,
            "settled_cents":settled_cents,
        }));
    }
    let data = json!({
        "charging_orders":charging_orders.max(0) as u64,
        "today_order_users":today_order_users.max(0) as u64,
        "today_completed_orders":today_summary.try_get::<i64,_>("completed_orders")?.max(0) as u64,
        "today_settled_cents":today_summary.try_get::<i64,_>("settled_cents")?,
        "daily_trend":trend,
        "updated_at":chrono::Utc::now().to_rfc3339(),
    });
    Ok(Json(common_error::ApiEnvelope::ok(data, common_error::current_request_id())))
}
