//! announcement_expire: 公告过期清理(把 end_at 过期且 status=published 的改为 expired)
//! 频率: 1 hour

use crate::AppState;
use std::time::Duration;
use serde::Deserialize;
use serde_json::json;
use tracing::{info, warn};

pub async fn run(state: AppState) {
    let mut iv = tokio::time::interval(Duration::from_secs(3600));
    loop {
        iv.tick().await;
        let result: common_error::AppResult<ExpiredCount> = common_http::internal::ApiClient::new(
            state.http.clone(), state.service_token.clone(),
        ).post(
            state.cfg.service_urls.admin.as_deref(),
            api_contracts::paths::ADMIN_INTERNAL_ANNOUNCEMENTS_EXPIRE,
            &json!({}),
        ).await;
        match result {
            Ok(data) => info!(expired_count = data.expired_count, "announcement_expire tick"),
            Err(error) => warn!(error = %error, "announcement_expire failed; will retry next interval"),
        }
    }
}

#[derive(Deserialize)]
struct ExpiredCount { expired_count: u64 }
