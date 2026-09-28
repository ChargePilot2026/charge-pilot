//! admin 服务 — 通用 API 容器
//!
//! 拆分:
//! - 子模块: users / stations / devices / orders / coupons /
//!   settings / announcements / customer_service / whitelabel / export / internal
//!
//! D3:会员卡与提现两个跨库视图的调用方已整块删除(admin-web 零调用),
//! `membership` 模块与相关 DTO/路径常量一并清除,不留编译期死代码。

pub mod users;
pub mod roles;
pub mod stations;
pub mod station_reads;
pub mod pricing_reads;
pub mod devices;
pub mod orders;
pub mod device_import;
pub mod coupons;
pub mod settings;
pub mod announcements;
pub mod customer_service;
pub mod whitelabel;
pub mod export;
pub mod internal;
pub mod casework;
pub mod dashboard;

use axum::{extract::State, Json};
use common_error::AppResult;

pub async fn health(State(st): State<crate::AppState>) -> AppResult<&'static str> {
    st.services.identity.ping().await?;
    st.redis_cache.ping().await?;
    st.redis_stream.ping().await?;
    Ok("ok")
}

#[allow(dead_code)]
pub fn ok_envelope<T: serde::Serialize>(data: T) -> Json<common_error::ApiEnvelope<T>> {
    Json(common_error::ApiEnvelope::ok(data, common_error::current_request_id()))
}

#[allow(dead_code)]
pub fn json_envelope(v: serde_json::Value) -> Json<common_error::ApiEnvelope<serde_json::Value>> {
    Json(common_error::ApiEnvelope::ok(v, common_error::current_request_id()))
}
