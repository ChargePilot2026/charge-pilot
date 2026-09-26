//! announcement_expire: 公告过期清理(把 end_at 过期且 status=published 的改为 expired)
//! 频率: 1 hour

use crate::AppState;
use std::time::Duration;
use tracing::info;

pub async fn run(state: AppState) {
    let mut iv = tokio::time::interval(Duration::from_secs(3600));
    loop {
        iv.tick().await;
        let res = sqlx::query(
            "UPDATE announcement SET status = 'expired' WHERE status = 'published' AND end_at IS NOT NULL AND end_at < NOW()"
        ).execute(state.db.pool()).await;
        info!(?res, "announcement_expire tick");
    }
}