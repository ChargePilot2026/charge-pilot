//! 跨服务 HTTP 客户端(类型化)—— 禁止散落 `ServiceClient::new().post_json(...)` 调用
//!
//! 集中:
//!   - 强类型请求/响应 DTO(由调用方传入)
//!   - 自动补充 `x-service-token` + `x-request-id` 头
//!
//! 调用示例:
//! ```ignore
//! let cli = ServiceClient::new(http, token_arc);
//! let resp: QuoteResponse = cli.post_typed(&st.cfg.service_urls.billing, QUOTE, &req).await?;
//! ```

use common_error::AppResult;
use serde::{de::DeserializeOwned, Serialize};
use std::sync::Arc;

/// ServiceClient 单例,内部 reqwest 连接池已设置超时 + 连接复用
#[derive(Clone)]
pub struct ServiceClient {
    inner: reqwest::Client,
    service_token: Arc<String>,
}

impl ServiceClient {
    pub fn new(http: reqwest::Client, token: Arc<String>) -> Self {
        Self { inner: http, service_token: token }
    }

    /// GET → typed response
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

    /// POST typed body → typed response
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn client_clone_shares_token() {
        let c = ServiceClient::new(
            reqwest::Client::new(),
            Arc::new("svc-token".into()),
        );
        assert_eq!(c.service_token.as_str(), "svc-token");
        let c2 = c.clone();
        assert!(Arc::ptr_eq(&c.service_token, &c2.service_token));
    }
}