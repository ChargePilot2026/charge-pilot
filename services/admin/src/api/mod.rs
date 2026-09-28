//! admin 服务 — HTTP 层薄壳
//!
//! P5 迁移后,业务代码全部住在 [`crate::capability::<域>`]:
//! `capability::identity::list` / `capability::device::device_list` / ……
//!
//! 保留本模块的只有两类东西:
//! - **无业务归属的通用容器**:[`health`] / [`ok_envelope`]。
//! - **未接入的桩**:导出任务([`export`])四个端点全部是
//!   `ServiceUnavailable`,不属于任何已完成的能力域。

pub mod dashboard;
pub mod export;

use axum::{extract::State, Json};
use common_error::AppResult;

pub async fn health(State(st): State<crate::AppState>) -> AppResult<&'static str> {
    st.services.identity.ping().await?;
    st.redis_cache.ping().await?;
    st.redis_stream.ping().await?;
    Ok("ok")
}

/// 本文件四个端点全部是**未接入的桩**,永远走 `Err(ServiceUnavailable)`。
#[allow(dead_code)]
pub fn ok_envelope<T: serde::Serialize>(data: T) -> Json<common_error::ApiEnvelope<T>> {
    Json(common_error::ApiEnvelope::ok(data, common_error::current_request_id()))
}
