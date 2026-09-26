//! gateway 服务 — 类型化跨服务 HTTP 客户端
//!
//! 与 user::clients / admin::clients 同形;禁止 handler 里拼 URL 或 `json!{}`。

use common_error::AppResult;
use serde::{de::DeserializeOwned, Serialize};
use std::sync::Arc;

#[derive(Clone)]
pub struct ServiceClient {
    inner: reqwest::Client,
    service_token: Arc<String>,
}

impl ServiceClient {
    pub fn new(http: reqwest::Client, token: Arc<String>) -> Self {
        Self { inner: http, service_token: token }
    }

    pub async fn get_typed<T: DeserializeOwned>(
        &self, base: Option<&str>, path: &str,
    ) -> AppResult<T> {
        let base = base.ok_or_else(|| common_error::AppError::Internal("service base url not configured".into()))?;
        let url = format!("{}{}", base.trim_end_matches('/'), path);
        let req_id = uuid::Uuid::new_v4().to_string();
        let resp = self.inner
            .get(&url)
            .header("x-service-token", self.service_token.as_str())
            .header("x-request-id", &req_id)
            .send().await?;
        if !resp.status().is_success() {
            return Err(common_error::AppError::HttpClient(format!(
                "GET {} failed: status={}", url, resp.status()
            )));
        }
        Ok(resp.json().await?)
    }

    pub async fn post_typed<B: Serialize, T: DeserializeOwned>(
        &self, base: Option<&str>, path: &str, body: &B,
    ) -> AppResult<T> {
        let base = base.ok_or_else(|| common_error::AppError::Internal("service base url not configured".into()))?;
        let url = format!("{}{}", base.trim_end_matches('/'), path);
        let req_id = uuid::Uuid::new_v4().to_string();
        let resp = self.inner
            .post(&url)
            .header("x-service-token", self.service_token.as_str())
            .header("x-request-id", &req_id)
            .json(body)
            .send().await?;
        if !resp.status().is_success() {
            return Err(common_error::AppError::HttpClient(format!(
                "POST {} failed: status={}", url, resp.status()
            )));
        }
        Ok(resp.json().await?)
    }
}