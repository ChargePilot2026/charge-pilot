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
            let port_no = frame.port_no.ok_or_else(|| common_error::AppError::BadRequest("遥测帧缺少端口号".into()))?;
            let mut measurements = Vec::new();
            for metric in ["power_w", "voltage_v", "current_a", "temperature_c", "battery_soc", "meter_kwh"] {
                let Some(value) = frame.payload.get(metric).and_then(|value| {
                    value.as_f64().or_else(|| value.as_str().and_then(|text| text.parse::<f64>().ok()))
                }).filter(|value| value.is_finite()) else { continue; };
                if metric == "battery_soc" && !(0.0..=100.0).contains(&value) {
                    return Err(common_error::AppError::BadRequest("SOC 遥测值必须在 0–100 之间".into()));
                }
                measurements.push((metric, value));
            }
            if measurements.is_empty() { return Err(common_error::AppError::BadRequest("遥测帧不含有效测量值".into())); }
            let mut tx = state.db.pool().begin().await?;
            for (metric, value) in measurements {
                sqlx::query(
                    "INSERT INTO telemetry (device_id, port_no, metric, value_num, ts) VALUES (?, ?, ?, ?, ?)",
                )
                .bind(&frame.device_id).bind(port_no).bind(metric).bind(value).bind(frame.ts)
                .execute(&mut *tx).await?;
                crate::telemetry_obs::aggregate_measurement(
                    &mut tx, &frame.device_id, port_no, metric, value, frame.ts,
                ).await?;
            }
            tx.commit().await?;
        }
        "status" => {
            let status = frame.payload.get("status").and_then(|v| v.as_str())
                .filter(|value| !value.is_empty() && value.len() <= 64 && !value.chars().any(char::is_control))
                .ok_or_else(|| common_error::AppError::BadRequest("设备状态帧缺少有效 status".into()))?;
            let env = StreamEnvelope::new(
                "device_status",
                "gateway",
                json!({
                    "device_id": frame.device_id,
                    "port_no": frame.port_no,
                    "status": status,
                }),
            );
            let mut tx = state.db.pool().begin().await?;
            crate::outbox::enqueue(&mut tx, common_redis::streams::DEVICE_EVENT, &env).await?;
            tx.commit().await?;
        }
        "ack" => {
            crate::charge_command::acknowledge(state, session, frame).await?;
        }
        "alert" => {
            let metric = frame.payload.get("metric").and_then(|v| v.as_str())
                .filter(|value| !value.is_empty() && value.len() <= 64 && !value.chars().any(char::is_control))
                .ok_or_else(|| common_error::AppError::BadRequest("设备告警帧缺少有效 metric".into()))?;
            let severity = frame.payload.get("severity").and_then(|v| v.as_str()).unwrap_or("warning");
            if !["warning", "critical", "fatal"].contains(&severity) {
                return Err(common_error::AppError::BadRequest("设备告警 severity 无效".into()));
            }
            let env = StreamEnvelope::new(
                "device_alert",
                "gateway",
                json!({
                    "device_id": frame.device_id,
                    "metric": metric,
                    "severity": severity,
                    "value": frame.payload.get("value"),
                }),
            );
            let mut tx = state.db.pool().begin().await?;
            crate::outbox::enqueue(&mut tx, common_redis::streams::ALERT, &env).await?;
            tx.commit().await?;
        }
        _ => {
            warn!(msg_type = %frame.msg_type, "unknown frame type");
        }
    }
    Ok(())
}
