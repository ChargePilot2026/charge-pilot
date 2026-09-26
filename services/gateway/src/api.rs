//! gateway 内部 HTTP API(17 个端点)

use crate::{protocol::Frame, AppState};
use axum::{
    extract::{Path, State},
    Json,
};
use common_error::{AppError, AppResult};
use common_redis::StreamEnvelope;
use serde::Deserialize;
use serde_json::{json, Value};

pub async fn health() -> &'static str { "ok" }

pub use crate::scan::{scan_resolve, scan_port};

// ===== device =====

pub async fn device_get(
    State(st): State<AppState>,
    Path(id): Path<String>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r: Option<(String, u64, u8, String, Option<String>)> = sqlx::query_as(
        "SELECT device_id, vendor_id, port_count, status, firmware_version FROM device WHERE device_id = ? AND deleted_at IS NULL"
    ).bind(&id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("device".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "device_id": r.0, "vendor_id": r.1, "port_count": r.2, "status": r.3, "firmware_version": r.4,
    }), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct SnapshotQuery {
    pub order_id: Option<String>,
}

pub async fn device_snapshot(
    State(st): State<AppState>,
    Path(id): Path<String>,
    Query(q): Query<SnapshotQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r: Option<(f64, f64, f64, Option<chrono::DateTime<chrono::Utc>>)> = sqlx::query_as(
        "SELECT power_w, voltage_v, current_a, ts FROM telemetry WHERE device_id = ? ORDER BY ts DESC LIMIT 1"
    ).bind(&id).fetch_optional(st.db.pool()).await.ok().flatten();
    let snap = match r {
        None => json!({}),
        Some((p, v, c, ts)) => json!({
            "power_w": p, "voltage_v": v, "current_a": c,
            "ts": ts.map(|t| t.to_rfc3339()),
        }),
    };
    let v = json!({
        "device_id": id,
        "order_id": q.order_id,
        "snapshot": snap,
    });
    Ok(Json(common_error::ApiEnvelope::ok(v, common_error::current_request_id())))
}

pub async fn device_ports(
    State(st): State<AppState>,
    Path(id): Path<String>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query("SELECT id, port_no, port_code, status FROM device_port WHERE device_id = ? AND deleted_at IS NULL")
        .bind(&id).fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "port_no": sqlx::Row::try_get::<u8, _>(r, "port_no").unwrap_or(0),
        "port_code": sqlx::Row::try_get::<String, _>(r, "port_code").unwrap_or_default(),
        "status": sqlx::Row::try_get::<String, _>(r, "status").unwrap_or_default(),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

pub async fn device_orders(
    State(st): State<AppState>,
    Path(id): Path<String>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    // 通过 HTTP 调 user 服务查订单 — 类型化 client + 路径常量
    let cli = crate::clients::ServiceClient::new(st.http.clone(), st.service_token.clone());
    let v: Value = cli
        .get_typed(st.cfg.service_urls.user.as_deref(), api_contracts::paths::USER_INTERNAL_ORDER_DETAIL)
        .await
        .unwrap_or_else(|_| json!({"items": []}));
    Ok(Json(common_error::ApiEnvelope::ok(v, common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct CurveQuery {
    pub order_id: Option<String>,
    pub window: Option<String>,
}

pub async fn device_curve(
    State(st): State<AppState>,
    Path(id): Path<String>,
    Query(_q): Query<CurveQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query("SELECT metric, value_num, ts FROM telemetry WHERE device_id = ? AND ts >= NOW() - INTERVAL 5 MINUTE ORDER BY ts ASC LIMIT 200")
        .bind(&id).fetch_all(st.db.pool()).await?;
    let buckets: Vec<Value> = rows.iter().map(|r| json!({
        "metric": sqlx::Row::try_get::<String, _>(r, "metric").unwrap_or_default(),
        "value": sqlx::Row::try_get::<f64, _>(r, "value_num").unwrap_or(0.0),
        "ts": sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "ts").ok().map(|t| t.to_rfc3339()),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"buckets": buckets}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct HistCurveQuery { pub granularity: Option<String> }

pub async fn device_historical_curve(
    State(st): State<AppState>,
    Path(id): Path<String>,
    Query(q): Query<HistCurveQuery>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let granularity = q.granularity.as_deref().unwrap_or("15min");
    let table = match granularity {
        "hourly" => "telemetry_aggregate_hourly",
        _ => "telemetry_aggregate_15min",
    };
    let sql = format!(
        "SELECT metric, AVG(avg_value) avg_v, MIN(min_value) min_v, MAX(max_value) max_v, bucket_start
         FROM {} WHERE device_id = ? AND bucket_start >= NOW() - INTERVAL 24 HOUR
         GROUP BY metric, bucket_start ORDER BY bucket_start ASC LIMIT 200",
        table
    );
    let rows = sqlx::query(&sql).bind(&id).fetch_all(st.db.pool()).await?;
    let buckets: Vec<Value> = rows.iter().map(|r| json!({
        "metric": sqlx::Row::try_get::<String, _>(r, "metric").unwrap_or_default(),
        "avg": sqlx::Row::try_get::<f64, _>(r, "avg_v").unwrap_or(0.0),
        "min": sqlx::Row::try_get::<f64, _>(r, "min_v").unwrap_or(0.0),
        "max": sqlx::Row::try_get::<f64, _>(r, "max_v").unwrap_or(0.0),
        "ts": sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "bucket_start").ok().map(|t| t.to_rfc3339()),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"buckets": buckets}), common_error::current_request_id())))
}

pub async fn device_reboot(
    State(st): State<AppState>,
    Path(id): Path<String>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    // 下发 REBOOT 命令:写入 ota_command,真实下发由 device 端监听
    let cmd_id = uuid::Uuid::new_v4().to_string();
    let now_month = chrono::Utc::now().format("%Y-%m-01").to_string();
    sqlx::query("INSERT INTO ota_command (command_id, device_id, package_id, status, sent_at, created_month) VALUES (?, ?, 'REBOOT', 'sent', NOW(3), ?)")
        .bind(&cmd_id).bind(&id).bind(&now_month).execute(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"command_id": cmd_id, "status": "queued"}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct FirmwarePushReq {
    pub package_id: String,
}

pub async fn device_firmware_push(
    State(st): State<AppState>,
    Path(id): Path<String>,
    Json(req): Json<FirmwarePushReq>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let cmd_id = uuid::Uuid::new_v4().to_string();
    let now_month = chrono::Utc::now().format("%Y-%m-01").to_string();
    sqlx::query("INSERT INTO ota_command (command_id, device_id, package_id, status, sent_at, created_month) VALUES (?, ?, ?, 'sent', NOW(3), ?)")
        .bind(&cmd_id).bind(&id).bind(&req.package_id).bind(&now_month).execute(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"command_id": cmd_id}), common_error::current_request_id())))
}

// ===== charge control =====

// ===== device register =====

#[derive(Debug, Deserialize)]
pub struct DeviceBackfillReq {
    pub frames: Vec<Frame>,
}

pub async fn device_backfill(
    State(st): State<AppState>,
    Path(_id): Path<String>,
    Json(req): Json<DeviceBackfillReq>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let n = req.frames.len();
    for f in &req.frames {
        sqlx::query(
            "INSERT INTO telemetry (device_id, port_no, metric, value_num, ts) VALUES (?, ?, 'power_w', ?, ?)"
        )
        .bind(&f.device_id)
        .bind(f.port_no.unwrap_or(0))
        .bind(f.payload.get("power_w").and_then(|v| v.as_f64()).unwrap_or(0.0))
        .bind(f.ts)
        .execute(st.db.pool()).await?;
    }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"inserted": n}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct DeviceCommandReq {
    pub cmd: String,
    pub params: Option<Value>,
}

pub async fn device_command(
    State(st): State<AppState>,
    Path(id): Path<String>,
    Json(req): Json<DeviceCommandReq>,
) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let cmd_id = uuid::Uuid::new_v4().to_string();
    let now_month = chrono::Utc::now().format("%Y-%m-01").to_string();
    sqlx::query("INSERT INTO ota_command (command_id, device_id, package_id, status, sent_at, created_month) VALUES (?, ?, ?, 'sent', NOW(3), ?)")
        .bind(&cmd_id).bind(&id).bind(&req.cmd).bind(&now_month).execute(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"command_id": cmd_id}), common_error::current_request_id())))
}

use axum::extract::Query;
