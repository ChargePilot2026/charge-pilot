//! event_outbox_retry: 重试 event_outbox 中 pending 状态的事件
//! 频率: 10 s

use crate::AppState;
use common_redis::StreamEnvelope;
use std::time::Duration;
use tracing::{info, warn};

pub async fn run(state: AppState) {
    let mut iv = tokio::time::interval(Duration::from_secs(10));
    loop {
        iv.tick().await;
        let rows = sqlx::query("SELECT id, event_id, stream, envelope_json, retry_count FROM event_outbox WHERE status = 'pending' AND scheduled_at <= NOW(3) LIMIT 50")
            .fetch_all(state.db.pool()).await;
        if let Ok(rows) = rows {
            for r in &rows {
                let id: u64 = sqlx::Row::try_get(r, "id").unwrap_or(0);
                let event_id: String = sqlx::Row::try_get(r, "event_id").unwrap_or_default();
                let stream: String = sqlx::Row::try_get(r, "stream").unwrap_or_default();
                let env_str: String = sqlx::Row::try_get(r, "envelope_json").unwrap_or_default();
                let retry_count: u32 = sqlx::Row::try_get(r, "retry_count").unwrap_or(0);
                let env: Result<StreamEnvelope, _> = serde_json::from_str(&env_str);
                match env {
                    Ok(env) => {
                        let res = state.redis_stream.xadd_envelope(&stream, &env).await;
                        match res {
                            Ok(_) => {
                                let _ = sqlx::query("UPDATE event_outbox SET status='published', published_at=NOW(3) WHERE id=?")
                                    .bind(id).execute(state.db.pool()).await;
                            }
                            Err(e) => {
                                warn!(event_id, error=%e, retry_count, "outbox publish failed");
                                let _ = sqlx::query("UPDATE event_outbox SET retry_count = retry_count + 1, scheduled_at = DATE_ADD(NOW(3), INTERVAL 30 SECOND), last_error = ? WHERE id = ?")
                                    .bind(e.to_string()).bind(id).execute(state.db.pool()).await;
                            }
                        }
                    }
                    Err(e) => {
                        warn!(event_id, error=%e, "outbox envelope parse failed; marking failed");
                        let _ = sqlx::query("UPDATE event_outbox SET status='failed', last_error=? WHERE id=?")
                            .bind(e.to_string()).bind(id).execute(state.db.pool()).await;
                    }
                }
            }
            if !rows.is_empty() { info!(n = rows.len(), "outbox republished"); }
        }
    }
}