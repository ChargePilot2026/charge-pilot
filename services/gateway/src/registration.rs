//! Physical registration never creates or reconfigures provisioned devices.
use crate::AppState;
use axum::{extract::State, Json};
use common_error::{ApiEnvelope, AppError, AppResult};
use serde::{Deserialize, Serialize};
use sqlx::Row;

fn tcp() -> String {
    "tcp".into()
}
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RegisterRequest {
    pub device_id: String,
    pub vendor_id: u64,
    pub station_id: Option<u64>,
    pub port_count: u8,
    pub model: Option<String>,
    pub firmware_version: Option<String>,
    pub mac_addr: Option<String>,
    #[serde(default = "tcp")]
    pub connect_type: String,
    pub client_ip: Option<String>,
}

#[derive(Debug, Serialize)]
pub struct RegisterResponse {
    pub registered: bool,
    pub session_id: u64,
    pub session_uuid: String,
    pub heartbeat_interval_sec: u32,
    pub server_time_ms: i64,
}

impl RegisterRequest {
    fn validate(&self) -> AppResult<()> {
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
    let mut tx = state.db.pool().begin().await?;
    let rows = sqlx::query("SELECT id,vendor_id,station_id,port_count,model,status FROM device WHERE device_id=? AND deleted_at IS NULL FOR UPDATE")
        .bind(&req.device_id).fetch_all(&mut *tx).await?;
    if rows.is_empty() {
        return Err(AppError::business(2001, "设备未建档"));
    }
    if rows.len() != 1 {
        return Err(AppError::Conflict("设备存在重复记录，需先清理".into()));
    }
    let device = &rows[0];
    let station_id: Option<u64> = device.try_get("station_id")?;
    if device.try_get::<String, _>("status")? != "enabled" {
        return Err(AppError::DeviceDisabled);
    }
    if device.try_get::<u64, _>("vendor_id")? != req.vendor_id
        || device.try_get::<u8, _>("port_count")? != req.port_count
        || req.station_id.is_some_and(|id| station_id != Some(id))
        || (req.model.is_some() && device.try_get::<Option<String>, _>("model")? != req.model)
    {
        return Err(AppError::Conflict("设备注册信息与运营配置不一致".into()));
    }
    let vendor: Option<(String, String)> = sqlx::query_as(
        "SELECT status,protocol FROM vendor WHERE id=? AND deleted_at IS NULL FOR SHARE",
    )
    .bind(req.vendor_id)
    .fetch_optional(&mut *tx)
    .await?;
    let (status, protocol) = vendor.ok_or_else(|| AppError::BadRequest("厂商不存在".into()))?;
    if status != "enabled" {
        return Err(AppError::BadRequest("厂商未启用".into()));
    }
    if protocol != "hybrid" && protocol != req.connect_type {
        return Err(AppError::BadRequest("连接协议与厂商配置不符".into()));
    }
    sqlx::query("UPDATE device_session SET ended_at=UTC_TIMESTAMP(3),close_reason='re_register' WHERE device_id=? AND ended_at IS NULL")
        .bind(&req.device_id).execute(&mut *tx).await?;
    let session_uuid = uuid::Uuid::new_v4().to_string();
    let session_id = sqlx::query("INSERT INTO device_session (session_id,device_id,protocol,remote_addr,started_at,last_active_at,created_month) VALUES (?,?,?,?,UTC_TIMESTAMP(3),UTC_TIMESTAMP(3),DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
        .bind(&session_uuid).bind(&req.device_id).bind(&req.connect_type).bind(&req.client_ip).execute(&mut *tx).await?.last_insert_id();
    sqlx::query("UPDATE device SET registered_at=UTC_TIMESTAMP(3),last_seen_at=UTC_TIMESTAMP(3),last_ip=?,firmware_version=COALESCE(?,firmware_version),mac_addr=COALESCE(?,mac_addr) WHERE id=?")
        .bind(&req.client_ip).bind(&req.firmware_version).bind(&req.mac_addr).bind(device.try_get::<u64,_>("id")?).execute(&mut *tx).await?;
    tx.commit().await?;
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
