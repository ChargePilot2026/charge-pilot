//! snapshot_warmer: 主动路径填充充电中订单快照(消费 device_event_stream.worker-cg)
//! 频率: 2 s

use crate::AppState;
use common_redis::{read_snapshot, write_snapshot};
use serde_json::json;
use std::time::Duration;
use tracing::info;

pub async fn run(state: AppState) {
    let mut iv = tokio::time::interval(Duration::from_secs(2));
    loop {
        iv.tick().await;
        let rows_res = sqlx::query("SELECT order_no, device_id, port_no FROM charge_order WHERE status = 'charging' LIMIT 100")
            .fetch_all(state.db.pool()).await;
        let n = match &rows_res {
            Ok(rows) => {
                for r in rows.iter() {
                    let order_no: String = sqlx::Row::try_get(r, "order_no").unwrap_or_default();
                    let device_id: String = sqlx::Row::try_get(r, "device_id").unwrap_or_default();
                    if let Ok(Some(_)) = read_snapshot::<serde_json::Value>(&state.redis_cache, &order_no).await { continue; }
                    let snap = json!({
                        "order_id": order_no,
                        "device_id": device_id,
                        "charge_state": "charging",
                        "current_power_w": 0.0,
                        "charged_kwh": 0.0,
                        "poll_continue": true,
                        "next_poll_after_ms": 5000,
                    });
                    let _ = write_snapshot(&state.redis_cache, &order_no, &snap).await;
                }
                rows.len()
            }
            Err(_) => 0,
        };
        info!(n, "snapshot_warmer tick");
    }
}