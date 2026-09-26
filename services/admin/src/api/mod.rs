//! admin 服务 — 通用 API 容器
//!
//! 拆分:
//! - 子模块: users / stations / devices / orders / coupons / membership /
//!   settings / announcements / customer_service / whitelabel / export / internal

pub mod users;
pub mod roles;
pub mod stations;
pub mod devices;
pub mod orders;
pub mod coupons;
pub mod membership;
pub mod settings;
pub mod announcements;
pub mod customer_service;
pub mod whitelabel;
pub mod export;
pub mod internal;

use axum::Json;
use common_error::AppResult;

pub async fn health() -> &'static str { "ok" }

#[allow(dead_code)]
pub fn ok_envelope<T: serde::Serialize>(data: T) -> Json<common_error::ApiEnvelope<T>> {
    Json(common_error::ApiEnvelope::ok(data, common_error::current_request_id()))
}

#[allow(dead_code)]
pub fn json_envelope(v: serde_json::Value) -> Json<common_error::ApiEnvelope<serde_json::Value>> {
    Json(common_error::ApiEnvelope::ok(v, common_error::current_request_id()))
}