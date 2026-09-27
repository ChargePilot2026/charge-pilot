//! snapshot_warmer: 通过 user / gateway 内部 HTTP 轮询并缓存有真实遥测的充电中订单快照
//! 频率: 2 s

use crate::AppState;
use common_redis::write_snapshot;
use common_error::{AppError, AppResult};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::time::Duration;
use tracing::{info, warn};

#[derive(Serialize)]
struct SnapshotQuery { order_id: String, port_no: u8 }

#[derive(Deserialize)]
struct OrderSnapshotRow {
    order_no: String,
    device_id: String,
    port_no: u8,
    charged_kwh: Option<String>,
    charged_seconds: Option<u32>,
}

pub async fn run(state: AppState) {
    let mut iv = tokio::time::interval(Duration::from_secs(2));
    loop {
        iv.tick().await;
        let user_path = api_contracts::paths::USER_INTERNAL_CHARGING_ORDERS;
        let rows_res: AppResult<Value> = common_http::internal::ApiClient::new(state.http.clone(), state.service_token.clone())
            .get(state.cfg.service_urls.user.as_deref(), user_path, &()).await;
        let rows_data = match rows_res {
            Ok(rows) => rows,
            Err(error) => {
                warn!(error = %error, "snapshot warmer could not read user charging orders");
                continue;
            }
        };
        let rows: Vec<OrderSnapshotRow> = match rows_data.get("items").cloned()
            .ok_or_else(|| AppError::ServiceUnavailable("user snapshot list omitted items".into()))
            .and_then(|items| serde_json::from_value(items).map_err(AppError::from)) {
            Ok(rows) => rows,
            Err(error) => {
                warn!(error = %error, "snapshot warmer received invalid user order data");
                continue;
            }
        };
        let mut n = 0usize;
        for order in rows {
            let order_no = order.order_no;
            let device_id = order.device_id;
            let port_no = order.port_no;
            let path = api_contracts::paths::GW_DEVICE_SNAPSHOT.replace(":id", &device_id);
            let query = SnapshotQuery { order_id: order_no.clone(), port_no };
            let result: AppResult<Value> = common_http::internal::ApiClient::new(state.http.clone(), state.service_token.clone())
                .get(state.cfg.service_urls.gateway.as_deref(), &path, &query).await;
            let data = match result {
                Ok(data) => data,
                Err(error) => {
                    warn!(order_no, error = %error, "snapshot warmer could not read gateway telemetry");
                    continue;
                }
            };
            let readings = data.get("snapshot").cloned().unwrap_or_else(|| Value::Object(Default::default()));
            if !readings.as_object().is_some_and(|values| !values.is_empty()) { continue; }
            let snapshot = serde_json::json!({
                "order_id": order_no,
                "device_id": device_id,
                "charge_state": "charging",
                "charged_kwh": order.charged_kwh,
                "elapsed_seconds": order.charged_seconds,
                "poll_continue": true,
                "next_poll_after_ms": 5000,
                "telemetry_available": true,
                "current_power_w": readings.get("power_w"),
                "power_w": readings.get("power_w"),
                "current_a": readings.get("current_a"),
                "voltage_v": readings.get("voltage_v"),
                "temperature_c": readings.get("temperature_c"),
                "battery_soc": readings.get("battery_soc"),
                "telemetry_ts": readings.get("ts"),
            });
            if let Err(error) = write_snapshot(&state.redis_cache, &order_no, &snapshot).await {
                warn!(order_no, error = %error, "snapshot cache write failed");
            } else {
                n += 1;
            }
        }
        info!(n, "snapshot_warmer tick");
    }
}
