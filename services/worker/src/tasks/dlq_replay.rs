//! dlq_replay: DLQ 重放(每天 04:00)
//! 频率: 1 day

use crate::AppState;
use std::time::Duration;
use tracing::info;

pub async fn run(state: AppState) {
    let mut iv = tokio::time::interval(Duration::from_secs(86_400));
    loop {
        iv.tick().await;
        info!("dlq_replay tick");
        let _ = state;
    }
}