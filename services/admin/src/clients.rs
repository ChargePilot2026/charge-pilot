//! admin 服务 — 类型化跨服务 HTTP 客户端
//!
//! 与 user::clients 同形;禁止散落 `format!("/api/v1/...")` 拼 URL 或 `json!{}` 拼 body。

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
        Self::unwrap(resp, url, &req_id).await
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
        Self::unwrap(resp, url, &req_id).await
    }

    /// **D7**:解包单层业务信封,并把下游的业务错误码映射成本服务的错误类型。
    ///
    /// 原实现直接 `resp.json()`,拿到的是 `{code,data,message}` 整个信封,
    /// 调用方再包一层就变成 `{code,data:{code,data,…}}` 的双层信封。
    /// 这里与 `common_http::internal::ApiClient` 保持完全一致的语义:
    /// 409→Conflict、400→BadRequest、业务码 2000..3000→business、
    /// 其余非零码→ServiceUnavailable、`data` 缺失→ServiceUnavailable。
    async fn unwrap<T: DeserializeOwned>(
        resp: reqwest::Response,
        url: String,
        _req_id: &str,
    ) -> AppResult<T> {
        let status = resp.status();
        let envelope: common_error::ApiEnvelope<T> = resp.json().await.map_err(|_| {
            common_error::AppError::HttpClient(format!("{url} 响应不是合法 JSON"))
        })?;
        if status == reqwest::StatusCode::CONFLICT {
            return Err(common_error::AppError::Conflict(envelope.message));
        }
        if status == reqwest::StatusCode::BAD_REQUEST {
            return Err(common_error::AppError::BadRequest(envelope.message));
        }
        if !status.is_success() {
            return Err(common_error::AppError::HttpClient(format!(
                "{url} failed: status={status}"
            )));
        }
        if envelope.code != 0 {
            if (2000..3000).contains(&envelope.code) {
                return Err(common_error::AppError::business(envelope.code, envelope.message));
            }
            return Err(common_error::AppError::ServiceUnavailable("下游操作未成功".into()));
        }
        envelope
            .data
            .ok_or_else(|| common_error::AppError::ServiceUnavailable("下游响应缺少数据".into()))
    }
}

#[cfg(test)]
#[allow(clippy::disallowed_macros, clippy::disallowed_types)]
mod tests {
    use super::*;

    /// 回归护栏:**D7**。下游回单层信封时,客户端必须解出 `data`,
    /// 不得把整个 `{code,data,message}` 当成结果返回。
    ///
    /// 用一个只实现 `Deserialize` 的类型探测:若不解包,这里拿不到数据。
    #[tokio::test]
    async fn unwraps_single_envelope() {
        let server = axum::Router::new().route(
            "/x",
            axum::routing::post(|| async {
                axum::Json::<common_error::ApiEnvelope<serde_json::Value>>(
                    common_error::ApiEnvelope::ok(serde_json::json!({"id":7}), "req-1"),
                )
            }),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move { axum::serve(listener, server).await.unwrap(); });

        let client = ServiceClient::new(reqwest::Client::new(), Arc::new("svc".into()));
        let got: serde_json::Value = client
            .post_typed(Some(&format!("http://{addr}")), "/x", &serde_json::json!({}))
            .await
            .unwrap();
        // 若是双层信封,这里会是 {code:0,data:{id:7}} 而不是 {id:7}
        assert_eq!(got["id"], 7);
        assert!(got.get("code").is_none(), "不得把整个信封当结果返回");
        assert!(got.get("data").is_none(), "不得出现双层信封");
    }

    #[test]
    fn client_clone_shares_token() {
        let c = ServiceClient::new(reqwest::Client::new(), Arc::new("svc".into()));
        let c2 = c.clone();
        assert!(Arc::ptr_eq(&c.service_token, &c2.service_token));
    }
}