//! 已接入后台循环。真实 cron/scheduled_task 驱动尚未接入。

use crate::{tasks, AppState};
use common_error::AppResult;
use std::time::Duration;
use tracing::info;

/// 启动 3 个已实现的后台循环。
pub async fn start_all(state: AppState) -> AppResult<()> {
    tokio::spawn(tasks::announcement_expire::run(state.clone()));
    tokio::spawn(tasks::snapshot_warmer::run(state.clone()));
    tokio::spawn(tasks::device_session_clean::run(state.clone()));
    // D4 ③b:注册 DLQ 重放,否则失败事件永久滞留
    tokio::spawn(tasks::dlq_replay::run(state.clone()));
    info!("worker 4 implemented background loops started");
    Ok(())
}

#[allow(dead_code)]
pub fn _every(_: Duration) {}
