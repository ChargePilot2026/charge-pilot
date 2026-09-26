//! 导出任务(简化为创建一个 worker 任务)

use crate::AppState;
use axum::{extract::State, Json};
use common_auth::AdminClaims;
use common_db::IdGen;
use common_error::AppResult;
use serde::Deserialize;
use serde_json::{json, Value};

#[derive(Debug, Deserialize)]
pub struct ExportCreateReq {
    pub export_type: String,    // orders / settlements / invoices / alerts
    pub period_start: Option<chrono::DateTime<chrono::Utc>>,
    pub period_end: Option<chrono::DateTime<chrono::Utc>>,
    pub filters_json: Option<Value>,
}

pub async fn create(
    State(st): State<AppState>,
    actor: AdminClaims,
    Json(req): Json<ExportCreateReq>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let task_code = "export_run";
    let code = IdGen::new("EXP").next();
    sqlx::query(
        "INSERT INTO scheduled_task (task_code, name, cron_expr, enabled, next_run_at, config_json)
         VALUES (?, ?, '* * * * *', 1, NOW(3), ?)
         ON DUPLICATE KEY UPDATE config_json = VALUES(config_json)"
    )
    .bind(task_code).bind(format!("export_{}", req.export_type))
    .bind(serde_json::to_value(&serde_json::json!({
        "export_no": code,
        "type": req.export_type,
        "period_start": req.period_start,
        "period_end": req.period_end,
        "filters": req.filters_json,
        "operator_id": actor.admin_user_id,
    }))?)
    .execute(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"export_no": code, "status": "queued"}), common_error::current_request_id())))
}