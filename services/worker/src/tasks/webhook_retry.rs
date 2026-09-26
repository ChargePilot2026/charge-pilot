//! webhook_retry: Webhook 失败重试(消费 webhook_retry_stream.worker-cg)
//! 频率: 30 s

use crate::AppState;
use std::time::Duration;
use tracing::info;

pub async fn run(state: AppState) {
    let mut iv = tokio::time::interval(Duration::from_secs(30));
    loop {
        iv.tick().await;
        info!("webhook_retry tick");
        let _ = state;
    }
}