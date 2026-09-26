//! TCP 长连接监听
//!
//! 简化实现:每连接一个 task,接收 line-delimited JSON 帧(实际项目按厂商协议定制)
//! 端口: 9100

use crate::{protocol::Frame, AppState};
use common_error::AppResult;
use common_redis::StreamEnvelope;
use serde_json::json;
use std::sync::Arc;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::TcpListener;
use tokio::sync::Mutex;
use tracing::{error, info, warn};

pub async fn run_tcp_listener(bind: &str, state: AppState) -> AppResult<()> {
    let listener = TcpListener::bind(bind).await?;
    info!(bind, "TCP device listener ready");
    loop {
        let (socket, addr) = match listener.accept().await {
            Ok(v) => v,
            Err(e) => { error!(error=%e, "accept failed"); continue; }
        };
        let state = state.clone();
        tokio::spawn(async move {
            if let Err(e) = handle_conn(socket, addr.to_string(), state).await {
                warn!(addr=%addr, error=%e, "conn ended");
            }
        });
    }
}

async fn handle_conn(socket: tokio::net::TcpStream, peer: String, state: AppState) -> AppResult<()> {
    let (read, mut write) = socket.into_split();
    let mut reader = BufReader::new(read);
    let mut line = String::new();
    let device_id_holder: Arc<Mutex<Option<String>>> = Arc::new(Mutex::new(None));

    loop {
        line.clear();
        let n = reader.read_line(&mut line).await?;
        if n == 0 { break; }
        let trimmed = line.trim();
        if trimmed.is_empty() { continue; }
        // 简化:每行一个 JSON frame
        let parsed: Result<Frame, _> = serde_json::from_str(trimmed);
        let frame = match parsed {
            Ok(f) => f,
            Err(e) => {
                warn!(peer=%peer, error=%e, body=%trimmed, "bad frame");
                let _ = write.write_all(b"{\"error\":\"bad_frame\"}\n").await;
                continue;
            }
        };
        // 首帧必须是 device_id 校验
        {
            let mut g = device_id_holder.lock().await;
            if g.is_none() {
                // 校验 device 是否在册
                let enabled: bool = sqlx::query_scalar("SELECT status = 'enabled' FROM device WHERE device_id = ? AND deleted_at IS NULL")
                    .bind(&frame.device_id)
                    .fetch_optional(state.db.pool())
                    .await
                    .ok()
                    .flatten()
                    .unwrap_or(false);
                if !enabled {
                    let _ = write.write_all(b"{\"error\":\"unknown_device\"}\n").await;
                    return Ok(());
                }
                *g = Some(frame.device_id.clone());
                sqlx::query("UPDATE device SET last_seen_at = NOW(3), last_ip = ? WHERE device_id = ?")
                    .bind(&peer).bind(&frame.device_id).execute(state.db.pool()).await?;
            }
        }
        // 处理
        if let Err(e) = handle_frame(&frame, &state).await {
            error!(peer=%peer, error=%e, "frame handling failed");
        }
        let _ = write.write_all(b"{\"ack\":true}\n").await;
    }
    Ok(())
}

async fn handle_frame(frame: &Frame, state: &AppState) -> AppResult<()> {
    match frame.msg_type.as_str() {
        "heartbeat" => { /* 设备心跳 */ }
        "telemetry" => {
            // 持久化到 telemetry 表
            sqlx::query(
                "INSERT INTO telemetry (device_id, port_no, metric, value_num, ts)
                 VALUES (?, ?, 'power_w', ?, ?)"
            )
            .bind(&frame.device_id)
            .bind(frame.port_no.unwrap_or(0))
            .bind(frame.payload.get("power_w").and_then(|v| v.as_f64()).unwrap_or(0.0))
            .bind(frame.ts)
            .execute(state.db.pool()).await?;
        }
        "status" => {
            // 发布 device_event_stream
            let env = StreamEnvelope::new("device_status", "gateway", json!({
                "device_id": frame.device_id,
                "port_no": frame.port_no,
                "status": frame.payload.get("status").cloned().unwrap_or(serde_json::Value::Null),
            }));
            let _ = state.redis_stream.xadd_envelope(common_redis::streams::DEVICE_EVENT, &env).await;
        }
        "ack" => {
            // 设备 ACK(OTA/启动指令)
            let cmd_id = frame.payload.get("command_id").and_then(|v| v.as_str()).unwrap_or("");
            if !cmd_id.is_empty() {
                sqlx::query("UPDATE ota_command SET status='acked', acked_at=NOW(3) WHERE command_id = ?")
                    .bind(cmd_id).execute(state.db.pool()).await?;
            }
        }
        "alert" => {
            // 越界告警 → alert_stream
            let env = StreamEnvelope::new("device_alert", "gateway", json!({
                "device_id": frame.device_id,
                "metric": frame.payload.get("metric").cloned().unwrap_or(serde_json::Value::Null),
                "severity": frame.payload.get("severity").cloned().unwrap_or(json!("warning")),
                "value": frame.payload.get("value").cloned().unwrap_or(serde_json::Value::Null),
            }));
            let _ = state.redis_stream.xadd_envelope(common_redis::streams::ALERT, &env).await;
        }
        _ => { warn!(msg_type = %frame.msg_type, "unknown frame type"); }
    }
    Ok(())
}