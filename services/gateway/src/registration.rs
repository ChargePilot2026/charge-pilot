//! Physical registration never creates or reconfigures provisioned devices.
//!
//! P5:注册链路的全部 SQL 已下沉到 `DeviceService::register`(设备域
//! repository 层)。「锁设备行 → 校验厂商 → 关旧会话 → 建新会话 → 更新设备」
//! 跨 5 张表且必须原子,不能拆给 handler 编排。
use crate::AppState;
use axum::{extract::State, Json};
use common_error::{ApiEnvelope, AppError, AppResult};
use serde::Serialize;

pub use crate::services::device::RegisterRequest;

#[derive(Debug, Serialize)]
pub struct RegisterResponse {
    pub registered: bool,
    pub session_id: u64,
    pub session_uuid: String,
    pub heartbeat_interval_sec: u32,
    pub server_time_ms: i64,
}

impl RegisterRequest {
    /// 纯入参校验 —— 只看请求本身、不查库的部分(长度、字符集、取值域)。
    /// 需要数据库行才能判定的部分留在 `DeviceService::register` 里。
    pub fn validate(&self) -> AppResult<()> {
        if !(8..=32).contains(&self.device_id.len())
            || !self
                .device_id
                .bytes()
                .all(|c| c.is_ascii_alphanumeric() || c == b'-' || c == b'_')
            || self.vendor_id == 0
            || self.port_count == 0
        {
            return Err(AppError::BadRequest("设备标识、厂商或端口数无效".into()));
        }
        if !matches!(self.connect_type.as_str(), "tcp" | "mqtt") {
            return Err(AppError::BadRequest("连接类型无效".into()));
        }
        for (value, max) in [
            (&self.model, 128),
            (&self.firmware_version, 64),
            (&self.mac_addr, 32),
        ] {
            if value.as_ref().is_some_and(|v| {
                v.trim().is_empty() || v.chars().count() > max || v.chars().any(char::is_control)
            }) {
                return Err(AppError::BadRequest("注册信息格式无效".into()));
            }
        }
        if self
            .client_ip
            .as_ref()
            .is_some_and(|ip| ip.parse::<std::net::IpAddr>().is_err())
        {
            return Err(AppError::BadRequest("客户端 IP 格式无效".into()));
        }
        Ok(())
    }
}

pub async fn register(
    State(state): State<AppState>,
    Json(req): Json<RegisterRequest>,
) -> AppResult<Json<ApiEnvelope<RegisterResponse>>> {
    req.validate()?;
    let (session_id, session_uuid) = state.device.register(&req).await?;
    // Database is authoritative. A cache failure must not undo registration.
    if let Err(error) = state
        .redis_cache
        .set_ex(
            &format!("device:session:{}", req.device_id),
            &session_id,
            600,
        )
        .await
    {
        tracing::warn!(%error,device_id=%req.device_id,"session cache unavailable");
    }
    Ok(Json(ApiEnvelope::ok(
        RegisterResponse {
            registered: true,
            session_id,
            session_uuid,
            heartbeat_interval_sec: 60,
            server_time_ms: chrono::Utc::now().timestamp_millis(),
        },
        common_error::current_request_id(),
    )))
}
