//! TCP 长连接监听
//!
//! 简化实现:每连接一个 task,接收 line-delimited JSON 帧(实际项目按厂商协议定制)
//! 端口: 9100

use crate::{protocol::Frame, AppState};
use common_error::AppResult;
use common_redis::StreamEnvelope;
use serde_json::json;
use std::sync::Arc;
use tokio::io::{AsyncBufReadExt, AsyncReadExt, AsyncWriteExt, BufReader};
use tokio::net::TcpListener;
use tokio::sync::Mutex;
use tracing::{error, info, warn};

pub async fn run_tcp_listener(bind: &str, state: AppState) -> AppResult<()> {
    let listener = TcpListener::bind(bind).await?;
    info!(bind, "TCP device listener ready");
    loop {
        let (socket, addr) = match listener.accept().await {
            Ok(v) => v,
            Err(e) => {
                error!(error=%e, "accept failed");
                continue;
            }
        };
        let state = state.clone();
        tokio::spawn(async move {
            if let Err(e) = handle_conn(socket, addr.to_string(), state).await {
                warn!(addr=%addr, error=%e, "conn ended");
            }
        });
    }
}

async fn handle_conn(
    socket: tokio::net::TcpStream,
    peer: String,
    state: AppState,
) -> AppResult<()> {
    let (read, write) = socket.into_split();
    let writer = Arc::new(Mutex::new(write));
    let mut reader = BufReader::new(read);
    let mut identity: Option<(String, String)> = None;
    let result:AppResult<()>=async {
        loop {
            let mut line=String::new();
            let mut limited=(&mut reader).take(65_537);
            let n=tokio::time::timeout(std::time::Duration::from_secs(120),limited.read_line(&mut line)).await
                .map_err(|_|common_error::AppError::ServiceUnavailable("设备心跳超时".into()))??;
            if n==0 {break;} if n>65_536 {return Err(common_error::AppError::BadRequest("设备帧过大".into()));}
            if line.trim().is_empty(){continue;}
            let frame:Frame=match serde_json::from_str(line.trim()) {
                Ok(frame)=>frame,
                Err(_)=>{writer.lock().await.write_all(b"{\"error\":\"bad_frame\"}\n").await?;continue;}
            };
            if let Some((device,_))=&identity {
                if device!=&frame.device_id {writer.lock().await.write_all(b"{\"error\":\"device_identity_mismatch\"}\n").await?;return Ok(());}
            } else {
                let enabled:Vec<bool>=sqlx::query_scalar("SELECT d.status='enabled' AND v.status='enabled' FROM device d JOIN vendor v ON v.id=d.vendor_id AND v.deleted_at IS NULL WHERE d.device_id=? AND d.deleted_at IS NULL")
                    .bind(&frame.device_id).fetch_all(state.db.pool()).await?;
                if enabled!=vec![true] || frame.msg_type!="heartbeat" {
                    writer.lock().await.write_all(b"{\"error\":\"unknown_device_or_missing_heartbeat\"}\n").await?;return Ok(());
                }
                let id=state.connections.register(&frame.device_id,writer.clone()).await;
                identity=Some((frame.device_id.clone(),id));
            }
            let session=&identity.as_ref().unwrap().1;
            // A replacement connection invalidates the old reader as well as its writer.
            if state.connections.get(&frame.device_id).await.map_or(true, |current| current.id != *session) {break;}
            sqlx::query("UPDATE device SET last_seen_at=UTC_TIMESTAMP(3),last_ip=? WHERE device_id=? AND deleted_at IS NULL")
                .bind(peer.parse::<std::net::SocketAddr>().map(|addr|addr.ip().to_string()).unwrap_or_else(|_|peer.clone())).bind(&frame.device_id).execute(state.db.pool()).await?;
            if let Err(e)=handle_frame(&frame,&state,session).await {
                error!(peer=%peer,error=%e,"frame handling failed");
                writer.lock().await.write_all(b"{\"error\":\"frame_processing_failed\"}\n").await?;continue;
            }
            writer.lock().await.write_all(b"{\"ack\":true}\n").await?;
        }
        Ok(())
    }.await;
    if let Some((device, session)) = identity {
        state.connections.remove(&device, &session).await;
    }
    result
}

async fn handle_frame(frame: &Frame, state: &AppState, session: &str) -> AppResult<()> {
    match frame.msg_type.as_str() {
        "heartbeat" => { /* 设备心跳 */ }
        "telemetry" => {
            // 持久化到 telemetry 表
            sqlx::query(
                "INSERT INTO telemetry (device_id, port_no, metric, value_num, ts)
                 VALUES (?, ?, 'power_w', ?, ?)",
            )
            .bind(&frame.device_id)
            .bind(frame.port_no.unwrap_or(0))
            .bind(
                frame
                    .payload
                    .get("power_w")
                    .and_then(|v| v.as_f64())
                    .unwrap_or(0.0),
            )
            .bind(frame.ts)
            .execute(state.db.pool())
            .await?;
        }
        "status" => {
            // 发布 device_event_stream
            let env = StreamEnvelope::new(
                "device_status",
                "gateway",
                json!({
                    "device_id": frame.device_id,
                    "port_no": frame.port_no,
                    "status": frame.payload.get("status").cloned().unwrap_or(serde_json::Value::Null),
                }),
            );
            let _ = state
                .redis_stream
                .xadd_envelope(common_redis::streams::DEVICE_EVENT, &env)
                .await;
        }
        "ack" => {
            crate::charge_command::acknowledge(state, session, frame).await?;
        }
        "alert" => {
            // 越界告警 → alert_stream
            let env = StreamEnvelope::new(
                "device_alert",
                "gateway",
                json!({
                    "device_id": frame.device_id,
                    "metric": frame.payload.get("metric").cloned().unwrap_or(serde_json::Value::Null),
                    "severity": frame.payload.get("severity").cloned().unwrap_or(json!("warning")),
                    "value": frame.payload.get("value").cloned().unwrap_or(serde_json::Value::Null),
                }),
            );
            let _ = state
                .redis_stream
                .xadd_envelope(common_redis::streams::ALERT, &env)
                .await;
        }
        _ => {
            warn!(msg_type = %frame.msg_type, "unknown frame type");
        }
    }
    Ok(())
}
