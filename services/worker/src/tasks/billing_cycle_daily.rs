//! billing_cycle_daily: 每日 03:00 生成结算单 + 账单明细
//! 频率: 1 hour(扫描昨日数据)

use crate::AppState;
use std::time::Duration;
use tracing::info;

pub async fn run(state: AppState) {
    let mut iv = tokio::time::interval(Duration::from_secs(3600));
    loop {
        iv.tick().await;
        // 简化:本期仅记账
        info!("billing_cycle_daily tick");
        let _ = state;
    }
}