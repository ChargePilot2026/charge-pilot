//! MQTT 监听占位
//!
//! 真实部署使用 rumqttd 嵌入或独立 Broker。本期先提供 TCP-level 简版(行分隔 JSON),
//! 端口 1883(可通过 GATEWAY_MQTT_BIND 覆盖)。生产环境务必替换为 rumqttd + topic 解析。

use crate::{protocol::Frame, AppState};
use common_error::{AppError, AppResult};
use serde_json::json;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::TcpListener;
use tracing::{error, info};

pub async fn run_mqtt_listener(bind: &str, state: AppState) -> AppResult<()> {
    let listener = TcpListener::bind(bind).await.map_err(|e| AppError::Io(e))?;
    info!(bind, "MQTT (compat) listener ready (line-delimited JSON over TCP; replace with rumqttd in production)");
    loop {
        let (socket, addr) = match listener.accept().await {
            Ok(v) => v,
            Err(e) => { error!(error=%e, "mqtt accept failed"); continue; }
        };
        let state = state.clone();
        tokio::spawn(async move {
            let (read, mut write) = socket.into_split();
            let mut reader = BufReader::new(read);
            let mut line = String::new();
            loop {
                line.clear();
                let n = match reader.read_line(&mut line).await {
                    Ok(v) => v,
                    Err(_) => break,
                };
                if n == 0 { break; }
                let body = line.trim();
                if body.is_empty() { continue; }
                let parsed: Result<Frame, _> = serde_json::from_str(body);
                match parsed {
                    Ok(frame) => {
                        // 简化处理:同 tcp
                        let env_body = json!({"device_id": frame.device_id, "port_no": frame.port_no});
                        let _ = state.redis_stream.xadd_envelope(
                            common_redis::streams::DEVICE_EVENT,
                            &common_redis::StreamEnvelope::new(
                                "mqtt_telemetry",
                                "gateway",
                                env_body,
                            )
                        ).await;
                        let _ = write.write_all(b"{\"ack\":true}\n").await;
                    }
                    Err(_) => {
                        let _ = write.write_all(b"{\"error\":\"bad_mqtt_frame\"}\n").await;
                    }
                }
            }
            tracing::info!(addr=%addr, "mqtt conn closed");
        });
    }
}