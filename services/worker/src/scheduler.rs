//! 已接入后台循环。真实 cron/scheduled_task 驱动尚未接入。

use crate::{tasks, AppState};
use common_error::AppResult;
use tracing::info;

/// 启动已实现的后台循环。
///
/// D12 的生命周期规则同样适用于此：已注册循环若异常退出应可见。
/// 现状是 `tokio::spawn` 丢弃 `JoinHandle`，退出只会打一行 error
/// 而不撤销就绪状态 —— 属已知缺口，待与 gateway 的 D12 处理一并收口。
pub async fn start_all(state: AppState) -> AppResult<()> {
    tokio::spawn(tasks::announcement_expire::run(state.clone()));
    tokio::spawn(tasks::snapshot_warmer::run(state.clone()));
    tokio::spawn(tasks::device_session_clean::run(state.clone()));
    // D4 ③b:注册 DLQ 重放,否则失败事件永久滞留
    tokio::spawn(tasks::dlq_replay::run(state.clone()));
    info!("worker 4 background loops started");
    Ok(())
}
