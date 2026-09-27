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

/// **D12**:把 bind 与 accept 循环拆开。
///
/// 原先 `run_tcp_listener` 自己在内部 bind,并被放进 `tokio::spawn` 且丢弃
/// `JoinHandle` —— 端口被占用或地址配置错误时只打一行日志就结束,HTTP 仍正常启动,
/// 健康检查只探 DB/Redis 因而照样返回 `ok`。设备连不上但服务看起来健康。
///
/// 现在由 `main` 在**就绪前**同步 bind,失败即启动失败;accept 循环单独运行。
pub async fn bind_tcp_listener(bind: &str) -> AppResult<TcpListener> {
    let listener = TcpListener::bind(bind).await?;
    info!(bind, "TCP device listener bound");
    Ok(listener)
}

pub async fn serve_tcp(listener: TcpListener, bind: &str, state: AppState) -> AppResult<()> {
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
                let enabled=state.device.device_vendor_enabled(&frame.device_id).await?;
                if enabled!=vec![true] || frame.msg_type!="heartbeat" {
                    writer.lock().await.write_all(b"{\"error\":\"unknown_device_or_missing_heartbeat\"}\n").await?;return Ok(());
                }
                let id=state.connections.register(&frame.device_id,writer.clone()).await;
                identity=Some((frame.device_id.clone(),id));
            }
            let session=&identity.as_ref().unwrap().1;
            // A replacement connection invalidates the old reader as well as its writer.
            if state.connections.get(&frame.device_id).await.map_or(true, |current| current.id != *session) {break;}
            state.device.touch_device(
                &frame.device_id,
                &peer.parse::<std::net::SocketAddr>().map(|addr|addr.ip().to_string()).unwrap_or_else(|_|peer.clone()),
            ).await?;
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
            let rows: Vec<crate::services::device::Measurement<'_>> = measurements
                .iter()
                .map(|(metric, value)| crate::services::device::Measurement {
                    device_id: &frame.device_id, port_no, metric, value: *value, ts: frame.ts,
                })
                .collect();
            state.device.record_measurements(&rows).await?;
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
            state.device.enqueue_event(common_redis::streams::DEVICE_EVENT, &env).await?;
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
            state.device.enqueue_event(common_redis::streams::ALERT, &env).await?;
        }
        _ => {
            warn!(msg_type = %frame.msg_type, "unknown frame type");
        }
    }
    Ok(())
}
