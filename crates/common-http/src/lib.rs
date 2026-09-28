//! 共享 HTTP 客户端(服务间调用)+ 通用中间件
//!
//! - `ServiceClient`: 封装 reqwest,自动加 `X-Service-Token` + 内部 trace 头
//! - `request_id_layer`: 给每次响应加 `X-Request-Id`
//! - `tracing_layer`: tracing tower 中间件


// 分层与序列化约束(P1a 建立;P5 收口完成,转 deny)
// 说明:配置在仓库根 clippy.toml,级别在这里。测试模块豁免。
#![deny(
    clippy::disallowed_macros,
    clippy::disallowed_types,
    clippy::disallowed_methods,
)]
use axum::{body::Body, http::Request, middleware::Next, response::Response};
use common_auth::constant_time_eq;
use common_error::{ApiEnvelope, AppError, AppResult};
use tracing::Instrument;
use std::sync::Arc;
use std::time::Duration;
use uuid::Uuid;

pub mod internal;
pub mod routes;
pub mod contract_baseline;
pub mod contract_baseline_data;

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

/// Middleware:建立请求作用域 + 写回 X-Request-Id 响应头。
///
/// **D8**:此前只改 header、不进作用域也不进 body,而 `current_request_id()` 又每次
/// 现生成随机值,导致响应体/响应头/日志三处 id 互不相干。
///
/// **中间件不得改响应 body**:本层覆盖全部路由,其中包括
/// `/api/v1/health`(纯文本)、admin 的 `fallback`(静态资源)、微信支付回调(可能 204 空响应)。
/// 只有 `ApiEnvelope` 构造器与 `IntoResponse for AppError` 读作用域,其余响应体原样透传。
pub async fn request_id_layer(req: Request<Body>, next: Next) -> Response {
    let req_id = req
        .headers()
        .get("x-request-id")
        .and_then(|v| v.to_str().ok())
        .filter(|v| !v.is_empty())
        .map(|s| s.to_string())
        .unwrap_or_else(|| Uuid::new_v4().to_string());

    // 日志关联:task-local 不会自动给 tracing 附加字段,这里显式建 span
    let span = tracing::info_span!("request", trace_id = %req_id);

    let mut req = req;
    if let Ok(value) = req_id.parse() {
        req.headers_mut().insert("x-request-id", value);
    }

    let mut resp = common_error::scope_trace_id(req_id.clone(), next.run(req)).instrument(span).await;
    if let Ok(value) = req_id.parse() {
        resp.headers_mut().insert("x-request-id", value);
    }
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
        // 与全站响应形状对齐:原实现裸 `json!` 手写 `{code,message,request_id}`,
        // 形状与 `ApiEnvelope::err` 一致(缺 `data`、content-type 同为
        // application/json),但绕过了信封构造器 —— 契约一改这里就会漏。
        let body = ApiEnvelope::<()>::err(
            common_error::codes::UNAUTHORIZED,
            "missing or invalid service token",
            Uuid::new_v4().to_string(),
        );
        let resp = Response::builder()
            .status(401)
            .header("content-type", "application/json")
            .body(Body::from(serde_json::to_string(&body).unwrap()))
            .unwrap();
        return Err(resp);
    }
    Ok(next.run(req).await)
}

#[cfg(test)]
// 断言直接比对 `serde_json::Value`:被测对象就是响应体字节本身,类型化
// 断言看不到「多了一个字段」这类形状回归。
#[allow(clippy::disallowed_types)]
mod tests {
    use super::*;

    /// 401 响应**形状**锁定:与全站 `ApiEnvelope` 信封一致 ——
    /// `code=1001`、`message` 文案、`request_id` 透传、**不得带 `data` 字段**。
    /// 此前这里是裸 `json!` 手写体,信封契约变了也不会失败,现由本测试兜住。
    #[test]
    fn service_token_rejection_body_matches_api_envelope() {
        let body = ApiEnvelope::<()>::err(
            common_error::codes::UNAUTHORIZED,
            "missing or invalid service token",
            "rid-fixed",
        );
        let v: serde_json::Value = serde_json::to_value(&body).unwrap();
        assert_eq!(v["code"], 1001);
        assert_eq!(v["message"], "missing or invalid service token");
        assert_eq!(v["request_id"], "rid-fixed");
        assert!(v.get("data").is_none(), "错误响应不得带 data 字段");
    }

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
