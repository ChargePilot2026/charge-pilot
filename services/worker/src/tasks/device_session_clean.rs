//! device_session_clean: 关闭 N 分钟无活跃的 device_session 行
//! 频率: 5 min

use crate::AppState;
use std::time::Duration;
use tracing::info;

pub async fn run(state: AppState) {
    let mut iv = tokio::time::interval(Duration::from_secs(300));
    loop {
        iv.tick().await;
        let n = sqlx::query(
            "UPDATE device_session SET ended_at = NOW(3), close_reason = 'idle_timeout'
             WHERE ended_at IS NULL AND last_active_at < NOW() - INTERVAL 10 MINUTE"
        ).execute(state.db.pool()).await;
        info!(?n, "device_session_clean tick");
    }
}