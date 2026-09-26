//! ota_schedule: 调度 OTA 推送(消费 ota_schedule_stream.worker-cg)
//! 频率: 5 min

use crate::AppState;
use std::time::Duration;
use tracing::info;

pub async fn run(state: AppState) {
    let mut iv = tokio::time::interval(Duration::from_secs(300));
    loop {
        iv.tick().await;
        info!("ota_schedule tick");
        let _ = state;
    }
}