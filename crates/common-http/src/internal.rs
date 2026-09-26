//! Strict internal API transport. A failed upstream is never an empty success.
use common_error::{ApiEnvelope, AppError, AppResult};
use serde::{de::DeserializeOwned, Serialize};
use std::{sync::Arc, time::Duration};

#[derive(Clone)]
pub struct ApiClient {
    http: reqwest::Client,
    token: Arc<String>,
}

impl ApiClient {
    /// POST is not retried implicitly; callers retain a durable request for recovery.
    pub async fn post<T: DeserializeOwned, B: Serialize + ?Sized>(
        &self,
        base: Option<&str>,
        path: &str,
        body: &B,
    ) -> AppResult<T> {
        let base = base.ok_or_else(|| AppError::ServiceUnavailable("下游服务未配置".into()))?;
        let response = self
            .http
            .post(format!("{}{}", base.trim_end_matches('/'), path))
            .header("x-service-token", self.token.as_str())
            .header("x-request-id", uuid::Uuid::new_v4().to_string())
            .json(body)
            .timeout(Duration::from_secs(15))
            .send()
            .await
            .map_err(|_| AppError::ServiceUnavailable("下游服务连接失败，可重试同步".into()))?;
        let status = response.status();
        if status == reqwest::StatusCode::NOT_FOUND {
            return Err(AppError::NotFound("资源不存在".into()));
        }
        if !status.is_success()
            && status != reqwest::StatusCode::BAD_REQUEST
            && status != reqwest::StatusCode::CONFLICT
        {
            return Err(AppError::ServiceUnavailable("下游服务暂时不可用".into()));
        }
        let envelope: ApiEnvelope<T> = response
            .json()
            .await
            .map_err(|_| AppError::ServiceUnavailable("下游响应格式错误".into()))?;
        if status == reqwest::StatusCode::CONFLICT {
            return Err(AppError::Conflict(envelope.message));
        }
        if status == reqwest::StatusCode::BAD_REQUEST {
            return Err(AppError::BadRequest(envelope.message));
        }
        if envelope.code != 0 {
            return Err(AppError::ServiceUnavailable("下游操作未成功".into()));
        }
        envelope
            .data
            .ok_or_else(|| AppError::ServiceUnavailable("下游响应缺少数据".into()))
    }
    pub fn new(http: reqwest::Client, token: Arc<String>) -> Self {
        Self { http, token }
    }

    pub async fn get<T: DeserializeOwned, Q: Serialize + ?Sized>(
        &self,
        base: Option<&str>,
        path: &str,
        query: &Q,
    ) -> AppResult<T> {
        let base = base.ok_or_else(|| AppError::ServiceUnavailable("下游服务未配置".into()))?;
        let url = format!("{}{}", base.trim_end_matches('/'), path);
        let request_id = uuid::Uuid::new_v4().to_string();
        // GET is idempotent. Retry transport failures and 5xx once, never 4xx.
        for attempt in 0..2 {
            let response = self
                .http
                .get(&url)
                .header("x-service-token", self.token.as_str())
                .header("x-request-id", &request_id)
                .query(query)
                .timeout(Duration::from_secs(5))
                .send()
                .await;
            let response = match response {
                Ok(response) => response,
                Err(error) if attempt == 0 => {
                    tracing::warn!(%error, %path, "retrying internal GET");
                    continue;
                }
                Err(error) => {
                    tracing::error!(%error, %path, "internal GET failed");
                    return Err(AppError::ServiceUnavailable("下游服务连接失败".into()));
                }
            };
            let status = response.status();
            if status.is_server_error() && attempt == 0 {
                continue;
            }
            if status == reqwest::StatusCode::NOT_FOUND {
                return Err(AppError::NotFound("资源不存在".into()));
            }
            // An internal service-token failure must not log the operator out.
            if !status.is_success() && status != reqwest::StatusCode::BAD_REQUEST {
                tracing::error!(%status, %path, "internal API rejected request");
                return Err(AppError::ServiceUnavailable("下游服务暂时不可用".into()));
            }
            let envelope: ApiEnvelope<T> = response.json().await.map_err(|error| {
                tracing::error!(%error, %path, "invalid internal API envelope");
                AppError::ServiceUnavailable("下游响应格式错误".into())
            })?;
            return match envelope.code {
                0 if status.is_success() => envelope
                    .data
                    .ok_or_else(|| AppError::ServiceUnavailable("下游响应缺少数据".into())),
                1004 => Err(AppError::NotFound(envelope.message)),
                1000 | 1005 => Err(AppError::BadRequest(envelope.message)),
                1001 | 1003 | 5000..=5999 => {
                    Err(AppError::ServiceUnavailable("下游服务暂时不可用".into()))
                }
                code => Err(AppError::business(code, envelope.message)),
            };
        }
        unreachable!("both attempts return or retry")
    }
}
