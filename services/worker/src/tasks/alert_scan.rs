//! alert_scan: 周期性扫描 gateway_db 写入的告警事件,推 Webhook
//! 频率: 60s

use crate::AppState;
use std::time::Duration;
use tracing::debug;

pub async fn run(state: AppState) {
    let mut iv = tokio::time::interval(Duration::from_secs(60));
    loop {
        iv.tick().await;
        // 通过 Webhook 投递(本期由 webhook_retry 任务统一调度)
        debug!("alert_scan tick (no-op in MVP)");
        let _ = state;
    }
}