//! gateway 内部 HTTP API(17 个端点)

use crate::{protocol::Frame, AppState};
use api_contracts::gateway_devices as gd;
use axum::{
    extract::{Path, State},
    Json,
};
use common_error::{AppError, AppResult};
use serde::Deserialize;
use serde_json::{json, Value};

pub async fn health(State(st): State<AppState>) -> AppResult<&'static str> {
    st.device.ping().await?;
    st.redis_cache.ping().await?;
    st.redis_stream.ping().await?;
    Ok("ok")
}

/// Close stale sessions in gateway_db. This is exposed only through the service-token router.
pub async fn cleanup_idle_device_sessions(
    State(st): State<AppState>,
) -> AppResult<Json<common_error::ApiEnvelope<gd::CleanupSessionsResponse>>> {
    let closed_count = st.device.close_idle_sessions().await?;
    Ok(Json(common_error::ApiEnvelope::ok(
        gd::CleanupSessionsResponse { closed_count },
        common_error::current_request_id(),
    )))
}

pub use crate::scan::{scan_resolve, scan_port};

// ===== device =====

pub async fn device_get(
    State(st): State<AppState>,
    Path(id): Path<String>,
) -> AppResult<Json<common_error::ApiEnvelope<gd::DeviceSummary>>> {
    Ok(Json(common_error::ApiEnvelope::ok(
        st.device.device_summary(&id).await?,
        common_error::current_request_id(),
    )))
}

#[derive(Debug, Deserialize)]
pub struct SnapshotQuery {
    pub order_id: Option<String>,
    pub port_no: Option<u8>,
}

pub async fn device_snapshot(
    State(st): State<AppState>,
    Path(id): Path<String>,
    Query(q): Query<SnapshotQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<gd::DeviceSnapshotResponse>>> {
    let order_id = q.order_id.as_deref().filter(|value| !value.is_empty())
        .ok_or_else(|| AppError::BadRequest("order_id 必填".into()))?;
    let port_no = q.port_no.ok_or_else(|| AppError::BadRequest("port_no 必填".into()))?;
    let samples = st.device.recent_samples(&id, port_no).await?;
    let mut snap = gd::TelemetrySnapshot::default();
    let mut seen = std::collections::HashSet::new();
    for row in &samples {
        let Some(field) = metric_field(&row.metric) else { continue; };
        if !seen.insert(field) { continue; }
        if let Some(value) = row.value_num.filter(|value| value.is_finite()) {
            snap.set(field, value);
        }
        if snap.ts.is_none() { snap.ts = Some(row.ts.clone()); }
    }
    Ok(Json(common_error::ApiEnvelope::ok(
        gd::DeviceSnapshotResponse { device_id: id, order_id: order_id.to_string(), snapshot: snap },
        common_error::current_request_id(),
    )))
}

pub async fn device_ports(
    State(st): State<AppState>,
    Path(id): Path<String>,
) -> AppResult<Json<common_error::ApiEnvelope<gd::DevicePortsResponse>>> {
    let items = st.device.device_ports(&id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(
        gd::DevicePortsResponse { items },
        common_error::current_request_id(),
    )))
}

pub async fn device_orders(
    State(st): State<AppState>,
    Path(id): Path<String>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::orders::DeviceOrdersResponse>>> {
    // 透传 user 服务的类型化结果:客户端已解包信封,这里不再手工取 `data`
    let cli = crate::clients::ServiceClient::new(st.http.clone(), st.service_token.clone());
    let path = api_contracts::fill_path(api_contracts::paths::USER_INTERNAL_DEVICE_ORDERS, "device_id", &id);
    let orders: api_contracts::orders::DeviceOrdersResponse = cli
        .get_typed(st.cfg.service_urls.user.as_deref(), &path)
        .await?;
    Ok(Json(common_error::ApiEnvelope::ok(orders, common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct CurveQuery {
    pub order_id: Option<String>,
    pub window: Option<String>,
    pub port_no: Option<u8>,
    pub started_at: Option<String>,
}

pub async fn device_curve(
    State(st): State<AppState>,
    Path(id): Path<String>,
    Query(q): Query<CurveQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<gd::DeviceCurveResponse>>> {
    let order_id = q.order_id.as_deref().filter(|value| !value.is_empty())
        .ok_or_else(|| AppError::BadRequest("order_id 必填".into()))?;
    let port_no = q.port_no.ok_or_else(|| AppError::BadRequest("port_no 必填".into()))?;
    let window = q.window.as_deref().unwrap_or("last_30min");
    let now = chrono::Utc::now();
    let started_at = q.started_at.as_deref().map(chrono::DateTime::parse_from_rfc3339)
        .transpose().map_err(|_| AppError::BadRequest("started_at 格式无效".into()))?
        .map(|time| time.with_timezone(&chrono::Utc));
    let (mut from, sample_interval_seconds) = match window {
        "last_5min" => (now - chrono::Duration::minutes(5), 10),
        "last_30min" => (now - chrono::Duration::minutes(30), 60),
        "last_2h" => (now - chrono::Duration::hours(2), 300),
        "since_start" => {
            let start = started_at.ok_or_else(|| AppError::BadRequest("since_start 需要 started_at".into()))?;
            let seconds = (now - start).num_seconds().max(1);
            let interval = if seconds <= 30 * 60 { 10 } else if seconds <= 2 * 60 * 60 { 60 } else { 300 };
            (start, interval)
        }
        _ => return Err(AppError::BadRequest("window 无效".into())),
    };
    if let Some(started_at) = started_at { from = from.max(started_at); }
    let rows = st.device.curve_samples(&id, port_no, from, now, sample_interval_seconds).await?;
    let mut points: std::collections::BTreeMap<i64, gd::CurvePoint> = std::collections::BTreeMap::new();
    for (metric, value_num, ts) in &rows {
        let Some(field) = metric_field(metric) else { continue; };
        let Some(value) = value_num.filter(|value| value.is_finite()) else { continue; };
        let ts = *ts;
        let bucket = ts.timestamp();
        let point = points.entry(bucket).or_insert_with(|| gd::CurvePoint {
            ts: chrono::DateTime::<chrono::Utc>::from_timestamp(bucket, 0).unwrap_or(ts).to_rfc3339(),
            ..Default::default()
        });
        point.set(field, value);
    }
    let series: Vec<gd::CurvePoint> = points.into_values().collect();
    let summary = gd::CurveSummary {
        max_power_w: series.iter().filter_map(|p| p.power_w).reduce(f64::max),
        max_current_a: series.iter().filter_map(|p| p.current_a).reduce(f64::max),
        max_temperature_c: series.iter().filter_map(|p| p.temperature_c).reduce(f64::max),
    };
    Ok(Json(common_error::ApiEnvelope::ok(
        gd::DeviceCurveResponse {
            order_id: order_id.to_string(),
            window: window.to_string(),
            sample_interval_seconds: sample_interval_seconds as u32,
            series,
            summary,
        },
        common_error::current_request_id(),
    )))
}

#[derive(Debug, Deserialize)]
pub struct HistCurveQuery {
    pub order_id: Option<String>,
    pub granularity: Option<String>,
    pub port_no: Option<u8>,
    pub started_at: Option<String>,
    pub ended_at: Option<String>,
}

pub async fn device_historical_curve(
    State(st): State<AppState>,
    Path(id): Path<String>,
    Query(q): Query<HistCurveQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<gd::DeviceHistoricalCurveResponse>>> {
    let _order_id = q.order_id.as_deref().filter(|value| !value.is_empty())
        .ok_or_else(|| AppError::BadRequest("order_id 必填".into()))?;
    let port_no = q.port_no.ok_or_else(|| AppError::BadRequest("port_no 必填".into()))?;
    let started_at = q.started_at.as_deref().ok_or_else(|| AppError::BadRequest("started_at 必填".into()))?;
    let started_at = chrono::DateTime::parse_from_rfc3339(started_at)
        .map_err(|_| AppError::BadRequest("started_at 格式无效".into()))?.with_timezone(&chrono::Utc);
    let ended_at = q.ended_at.as_deref().map(chrono::DateTime::parse_from_rfc3339)
        .transpose().map_err(|_| AppError::BadRequest("ended_at 格式无效".into()))?
        .map(|time| time.with_timezone(&chrono::Utc)).unwrap_or_else(chrono::Utc::now);
    if started_at > ended_at { return Err(AppError::BadRequest("时间范围无效".into())); }
    let granularity = q.granularity.as_deref().unwrap_or("15min");
    let table = match granularity {
        "hourly" => "telemetry_aggregate_hourly",
        "15min" => "telemetry_aggregate_15min",
        _ => return Err(AppError::BadRequest("granularity 无效".into())),
    };
    let rows = st.device.historical_samples(table, &id, port_no, started_at, ended_at).await?;
    let mut series: std::collections::BTreeMap<String, gd::HistoricalCurvePoint> = std::collections::BTreeMap::new();
    let mut max_power_w: Option<f64> = None;
    let mut max_temperature_c: Option<f64> = None;
    let mut power_weighted_sum = 0.0;
    let mut power_sample_count = 0u64;
    for row in &rows {
        let Some(field) = metric_field(&row.metric) else { continue; };
        let avg = row.avg_v;
        let min = row.min_v;
        let max = row.max_v;
        let sample_count = row.sample_count;
        let ts = row.bucket_start.clone();
        let point = series.entry(ts.clone()).or_insert_with(|| gd::HistoricalCurvePoint {
            bucket_start: ts,
            ..Default::default()
        });
        // 电池取 avg、 电量取 max,其余 avg/min/max 三值——与旧实现逐项一致
        point.set_stat(field, avg, min, max);
        if row.metric == "power_w" {
            if let Some(value) = avg {
                power_weighted_sum += value * sample_count as f64;
                power_sample_count = power_sample_count.saturating_add(sample_count);
            }
            if let Some(value) = max { max_power_w = Some(max_power_w.map_or(value, |current| current.max(value))); }
        }
        if row.metric == "temperature_c" {
            if let Some(value) = max { max_temperature_c = Some(max_temperature_c.map_or(value, |current| current.max(value))); }
        }
    }
    let series: Vec<gd::HistoricalCurvePoint> = series.into_values().collect();
    let average_power_w = (power_sample_count > 0)
        .then_some(power_weighted_sum / power_sample_count as f64);
    Ok(Json(common_error::ApiEnvelope::ok(
        gd::DeviceHistoricalCurveResponse {
            granularity: granularity.to_string(),
            series,
            summary: gd::HistoricalCurveSummary {
                max_power_w,
                max_temperature_c,
                avg_power_w: average_power_w,
            },
        },
        common_error::current_request_id(),
    )))
}

fn metric_field(metric: &str) -> Option<&'static str> {
    match metric {
        "power_w" => Some("power_w"),
        "voltage_v" => Some("voltage_v"),
        "current_a" => Some("current_a"),
        "temperature_c" => Some("temperature_c"),
        "battery_soc" => Some("battery_soc"),
        "meter_kwh" => Some("meter_kwh"),
        _ => None,
    }
}

pub async fn device_reboot(
    State(_st): State<AppState>,
    Path(_id): Path<String>,
) -> AppResult<Json<common_error::ApiEnvelope<gd::NotImplementedResponse>>> {
    Err(AppError::ServiceUnavailable("设备重启指令未接入实际设备传输与 ACK，未创建或发送命令".into()))
}

pub async fn device_firmware_push(
    State(_st): State<AppState>,
    Path(_id): Path<String>,
    Json(_req): Json<Value>,
) -> AppResult<Json<common_error::ApiEnvelope<gd::NotImplementedResponse>>> {
    Err(AppError::ServiceUnavailable("OTA 固件传输、设备 ACK 与失败回滚尚未接入，未创建或发送命令".into()))
}

// ===== charge control =====

// ===== device register =====

#[derive(Debug, Deserialize)]
pub struct DeviceBackfillReq {
    pub frames: Vec<Frame>,
}

pub async fn device_backfill(
    State(st): State<AppState>,
    Path(id): Path<String>,
    Json(req): Json<DeviceBackfillReq>,
) -> AppResult<Json<common_error::ApiEnvelope<gd::BackfillResponse>>> {
    if req.frames.is_empty() || req.frames.len() > 1000 {
        return Err(AppError::BadRequest("补传帧数量必须在 1–1000 之间".into()));
    }
    let mut measurements = Vec::new();
    for f in &req.frames {
        if f.device_id != id { return Err(AppError::BadRequest("补传帧设备编号与路径不一致".into())); }
        let port_no = f.port_no.ok_or_else(|| AppError::BadRequest("补传帧缺少端口号".into()))?;
        let mut inserted = 0usize;
        for metric in ["power_w", "voltage_v", "current_a", "temperature_c", "battery_soc", "meter_kwh"] {
            let Some(value) = f.payload.get(metric).and_then(numeric_value) else { continue; };
            if metric == "battery_soc" && !(0.0..=100.0).contains(&value) {
                return Err(AppError::BadRequest("SOC 遥测值必须在 0–100 之间".into()));
            }
            measurements.push(crate::services::device::Measurement { device_id: f.device_id.as_str(), port_no, metric, value, ts: f.ts });
            inserted += 1;
        }
        if inserted == 0 { return Err(AppError::BadRequest("补传帧不含有效遥测值".into())); }
    }
    let n = st.device.record_measurements(&measurements).await?;
    Ok(Json(common_error::ApiEnvelope::ok(
        gd::BackfillResponse { inserted: n },
        common_error::current_request_id(),
    )))
}

fn numeric_value(value: &Value) -> Option<f64> {
    let number = value.as_f64().or_else(|| value.as_str().and_then(|text| text.parse::<f64>().ok()))?;
    number.is_finite().then_some(number)
}

#[derive(Debug, Deserialize)]
pub struct DeviceCommandReq {
    pub cmd: String,
    pub params: Option<Value>,
}

pub async fn device_command(
    State(_st): State<AppState>,
    Path(_id): Path<String>,
    Json(_req): Json<DeviceCommandReq>,
) -> AppResult<Json<common_error::ApiEnvelope<gd::NotImplementedResponse>>> {
    Err(AppError::ServiceUnavailable("通用设备指令传输与 ACK 尚未接入，未创建或发送命令".into()))
}

use axum::extract::Query;
