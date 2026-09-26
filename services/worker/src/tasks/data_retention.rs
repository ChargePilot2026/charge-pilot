//! data_retention: 数据保留 — 原始遥测 1 月 / 聚合 3 年清理
//! 频率: 1 day

use crate::AppState;
use std::time::Duration;
use tracing::info;

pub async fn run(state: AppState) {
    let mut iv = tokio::time::interval(Duration::from_secs(86_400));
    loop {
        iv.tick().await;
        info!("data_retention tick");
        let _ = state;
    }
}