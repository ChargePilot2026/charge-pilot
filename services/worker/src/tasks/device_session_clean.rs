//! device_session_clean: 关闭 N 分钟无活跃的 device_session 行
//! 频率: 5 min

// 本任务循环**不碰本服务数据库** —— 清理动作发生在 gateway_db(P3 之前
// worker 有裸 `db` 时直接跨库 UPDATE;现在只经内部 HTTP 请 gateway 执行)。
// 下方 `json!({})` 是内部端点的空请求体,形状由契约固定,故按文件豁免。
#![allow(clippy::disallowed_macros)]

use crate::AppState;
use std::time::Duration;
use serde::Deserialize;
use serde_json::json;
use tracing::{info, warn};

pub async fn run(state: AppState) {
    let mut iv = tokio::time::interval(Duration::from_secs(300));
    loop {
        iv.tick().await;
        let result: common_error::AppResult<ClosedCount> = common_http::internal::ApiClient::new(
            state.http.clone(), state.service_token.clone(),
        ).post(
            state.cfg.service_urls.gateway.as_deref(),
            api_contracts::paths::GW_DEVICE_SESSIONS_CLEAN,
            &json!({}),
        ).await;
        match result {
            Ok(data) => info!(closed_count = data.closed_count, "device_session_clean tick"),
            Err(error) => warn!(error = %error, "device_session_clean failed; will retry next interval"),
        }
    }
}

#[derive(Deserialize)]
struct ClosedCount { closed_count: u64 }
