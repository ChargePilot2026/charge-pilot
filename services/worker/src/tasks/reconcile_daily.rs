//! reconcile_daily: 每日 03:00 微信账单 vs 内部订单对账
//! 频率: 1 day(本期用 30 min 模拟)

use crate::AppState;
use std::time::Duration;
use tracing::info;

pub async fn run(state: AppState) {
    let mut iv = tokio::time::interval(Duration::from_secs(1800));
    loop {
        iv.tick().await;
        info!("reconcile_daily tick");
        let _ = state;
    }
}