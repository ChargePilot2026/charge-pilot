//! 12 个定时任务调度器(简化:用 tokio interval 模拟 cron)

use crate::{tasks, AppState};
use common_error::AppResult;
use std::time::Duration;
use tracing::info;

/// 启动 12 个后台任务
pub async fn start_all(state: AppState) -> AppResult<()> {
    tokio::spawn(tasks::alert_scan::run(state.clone()));
    tokio::spawn(tasks::billing_cycle_daily::run(state.clone()));
    tokio::spawn(tasks::reconcile_daily::run(state.clone()));
    tokio::spawn(tasks::ota_schedule::run(state.clone()));
    tokio::spawn(tasks::webhook_retry::run(state.clone()));
    tokio::spawn(tasks::announcement_expire::run(state.clone()));
    tokio::spawn(tasks::data_retention::run(state.clone()));
    tokio::spawn(tasks::event_outbox_retry::run(state.clone()));
    tokio::spawn(tasks::snapshot_warmer::run(state.clone()));
    tokio::spawn(tasks::dlq_replay::run(state.clone()));
    tokio::spawn(tasks::device_session_clean::run(state.clone()));
    tokio::spawn(tasks::export_run::run(state.clone()));
    info!("worker 12 tasks scheduled");
    Ok(())
}

#[allow(dead_code)]
pub fn _every(_: Duration) {}