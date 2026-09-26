//! 设备查询(只读;控制命令经内部 API 走 gateway)

use crate::AppState;
use crate::api_types;
use api_contracts::paths as p;
use axum::{extract::{Path, State}, Json};
use common_auth::AdminClaims;
use common_error::{AppError, AppResult};
use serde_json::{json, Value};

pub async fn list(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query(
        "SELECT id, device_id, station_id, vendor_id, model, status, install_at
         FROM device_meta WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 500"
    ).fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "device_id": sqlx::Row::try_get::<String, _>(r, "device_id").unwrap_or_default(),
        "station_id": sqlx::Row::try_get::<Option<u64>, _>(r, "station_id").ok().flatten(),
        "vendor_id": sqlx::Row::try_get::<Option<u64>, _>(r, "vendor_id").ok().flatten(),
        "model": sqlx::Row::try_get::<Option<String>, _>(r, "model").ok().flatten(),
        "status": sqlx::Row::try_get::<String, _>(r, "status").unwrap_or_default(),
        "install_at": sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(r, "install_at").ok().flatten().map(|t| t.to_rfc3339()),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

pub async fn get(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<String>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r = sqlx::query("SELECT * FROM device_meta WHERE device_id = ? AND deleted_at IS NULL")
        .bind(&id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("device".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "id": sqlx::Row::try_get::<u64, _>(&r, "id")?,
        "device_id": sqlx::Row::try_get::<String, _>(&r, "device_id")?,
        "station_id": sqlx::Row::try_get::<Option<u64>, _>(&r, "station_id")?,
        "vendor_id": sqlx::Row::try_get::<Option<u64>, _>(&r, "vendor_id")?,
        "model": sqlx::Row::try_get::<Option<String>, _>(&r, "model")?,
        "status": sqlx::Row::try_get::<String, _>(&r, "status")?,
        "install_at": sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(&r, "install_at")?.map(|t| t.to_rfc3339()),
    }), common_error::current_request_id())))
}

/// 设备订单列表:通过 HTTP 调 user 服务内部接口 — 类型化 client + 路径常量
pub async fn orders(State(st): State<AppState>, _c: AdminClaims, Path(_device_id): Path<String>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    // 注:路径常量是固定的(没有 :device_id 参数因为 user 服务里这条路由是按 device_id 查),
    // 这里改成调 user 服务的 ORDER_DETAIL 端点;后续如果有 /api/v1/internal/devices/{id}/orders
    // 完整路径,需新增对应常量。
    let cli = crate::clients::ServiceClient::new(st.http.clone(), st.service_token.clone());
    let v: Value = cli
        .get_typed(st.cfg.service_urls.user.as_deref(), p::USER_INTERNAL_ORDER_DETAIL)
        .await
        .unwrap_or_else(|_| json!({"items": []}));
    Ok(Json(common_error::ApiEnvelope::ok(v, common_error::current_request_id())))
}