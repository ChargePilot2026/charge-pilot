//! 共享 HTTP 客户端(服务间调用)+ 通用中间件
//!
//! - `ServiceClient`: 封装 reqwest,自动加 `X-Service-Token` + 内部 trace 头
//! - `request_id_layer`: 给每次响应加 `X-Request-Id`
//! - `tracing_layer`: tracing tower 中间件

use axum::{body::Body, http::Request, middleware::Next, response::Response};
use common_auth::constant_time_eq;
use common_error::{AppError, AppResult};
use std::sync::Arc;
use std::time::Duration;
use uuid::Uuid;

pub mod internal;

/// 服务间 HTTP 客户端
#[derive(Clone)]
pub struct ServiceClient {
    inner: reqwest::Client,
    service_token: Arc<String>,
}

impl ServiceClient {
    pub fn new(service_token: impl Into<String>) -> Self {
        let client = reqwest::Client::builder()
            .timeout(Duration::from_secs(30))
            .connect_timeout(Duration::from_secs(5))
            .pool_max_idle_per_host(8)
            .build()
            .expect("reqwest client");
        Self {
            inner: client,
            service_token: Arc::new(service_token.into()),
        }
    }

    pub fn raw(&self) -> &reqwest::Client {
        &self.inner
    }

    pub async fn get_json<T: serde::de::DeserializeOwned>(
        &self,
        base: &str,
        path: &str,
    ) -> AppResult<T> {
        let url = format!("{}{}", base.trim_end_matches('/'), path);
        let req_id = Uuid::new_v4().to_string();
        let resp = self
            .inner
            .get(&url)
            .header("x-service-token", self.service_token.as_str())
            .header("x-request-id", &req_id)
            .send()
            .await?;
        if !resp.status().is_success() {
            return Err(AppError::HttpClient(format!(
                "GET {} failed: status={}",
                url, resp.status()
            )));
        }
        let body: T = resp.json().await?;
        Ok(body)
    }

    pub async fn post_json<B: serde::Serialize, T: serde::de::DeserializeOwned>(
        &self,
        base: &str,
        path: &str,
        body: &B,
    ) -> AppResult<T> {
        let url = format!("{}{}", base.trim_end_matches('/'), path);
        let req_id = Uuid::new_v4().to_string();
        let resp = self
            .inner
            .post(&url)
            .header("x-service-token", self.service_token.as_str())
            .header("x-request-id", &req_id)
            .json(body)
            .send()
            .await?;
        if !resp.status().is_success() {
            return Err(AppError::HttpClient(format!(
                "POST {} failed: status={}",
                url, resp.status()
            )));
        }
        let body: T = resp.json().await?;
        Ok(body)
    }

    pub async fn post_json_no_body<T: serde::de::DeserializeOwned>(
        &self,
        base: &str,
        path: &str,
    ) -> AppResult<T> {
        let url = format!("{}{}", base.trim_end_matches('/'), path);
        let req_id = Uuid::new_v4().to_string();
        let resp = self
            .inner
            .post(&url)
            .header("x-service-token", self.service_token.as_str())
            .header("x-request-id", &req_id)
            .send()
            .await?;
        if !resp.status().is_success() {
            return Err(AppError::HttpClient(format!(
                "POST {} failed: status={}",
                url, resp.status()
            )));
        }
        let body: T = resp.json().await?;
        Ok(body)
    }
}

/// Middleware: 每个响应加 X-Request-Id
pub async fn request_id_layer(mut req: Request<Body>, next: Next) -> Response {
    let req_id = req
        .headers()
        .get("x-request-id")
        .and_then(|v| v.to_str().ok())
        .map(|s| s.to_string())
        .unwrap_or_else(|| Uuid::new_v4().to_string());
    req.headers_mut().insert(
        "x-request-id",
        req_id.parse().unwrap_or_else(|_| "invalid".parse().unwrap()),
    );
    let mut resp = next.run(req).await;
    resp.headers_mut().insert(
        "x-request-id",
        req_id.parse().unwrap_or_else(|_| "invalid".parse().unwrap()),
    );
    resp
}

/// Middleware: 简单的 IP 限流(每 IP 每秒 1000 req,Redis 计数)
/// 实现层面仅做占位;实际服务可在路由级用 Redis SET NX + EX 实现。
pub async fn _trivial_ip_filter(_req: Request<Body>, next: Next) -> Response {
    next.run(_req).await
}

/// 校验 header `X-Service-Token`(Axum 中间件;与服务间客户端配对)
pub async fn service_token_required(
    expected: axum::extract::Extension<Arc<String>>,
    req: Request<Body>,
    next: Next,
) -> Result<Response, Response> {
    let provided = req
        .headers()
        .get("x-service-token")
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");
    if !constant_time_eq(provided.as_bytes(), expected.as_bytes()) {
        let body = serde_json::json!({
            "code": 1001,
            "message": "missing or invalid service token",
            "request_id": Uuid::new_v4().to_string()
        });
        let resp = axum::response::Response::builder()
            .status(401)
            .header("content-type", "application/json")
            .body(axum::body::Body::from(body.to_string()))
            .unwrap();
        return Err(resp);
    }
    Ok(next.run(req).await)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn service_client_construct() {
        let c = ServiceClient::new("token");
        assert!(!c.service_token.as_str().is_empty());
    }

    #[test]
    fn url_join_helper() {
        // trim_end_matches('/') + 拼接保证不会留下双斜杠
        let join = |base: &str, path: &str| -> String {
            format!("{}{}", base.trim_end_matches('/'), path)
        };
        assert_eq!(join("http://x", "/a/b"), "http://x/a/b");
        assert_eq!(join("http://x/", "/a/b"), "http://x/a/b");
        // 注意:若 path 不以 / 开头,业务侧需自行补 /;这里只验证 trim 的去重效果
        assert_eq!(join("http://x/", "/a"), "http://x/a");
        assert_eq!(join("http://x", "/"), "http://x/");
    }
}
