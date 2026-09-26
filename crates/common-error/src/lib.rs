//! ChargePilot 统一错误码 + 响应包装
//!
//! 错误码段位(参见 docs/技术规格.md § 7.2):
//!   0 = 成功
//!   1xxx = 通用 (1001 未授权 / 1003 禁止 / 1004 资源不存在)
//!   2xxx = 业务错误(按服务划子段,user 2001-2099,admin 2100-2199,...)
//!   3xxx = 第三方错误(微信支付/退款失败等)
//!   4xxx = 限流
//!   5xxx = 服务器内部错误

use axum::{
    http::StatusCode,
    response::{IntoResponse, Response},
    Json,
};
use serde::{Deserialize, Serialize};
use thiserror::Error;
use uuid::Uuid;

/// 统一响应包装(对齐 docs/api/user.md § 通用约定)
#[derive(Debug, Serialize, Deserialize)]
pub struct ApiEnvelope<T> {
    pub code: i32,
    pub message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<T>,
    pub request_id: String,
}

impl<T> ApiEnvelope<T> {
    pub fn ok(data: T, request_id: impl Into<String>) -> Self {
        Self {
            code: 0,
            message: "ok".into(),
            data: Some(data),
            request_id: request_id.into(),
        }
    }

    pub fn err(code: i32, message: impl Into<String>, request_id: impl Into<String>) -> ApiEnvelope<()> {
        ApiEnvelope {
            code,
            message: message.into(),
            data: None,
            request_id: request_id.into(),
        }
    }
}

/// 业务错误类型
#[derive(Debug, Error)]
pub enum AppError {
    #[error("unauthorized: {0}")]
    Unauthorized(String),
    #[error("forbidden: {0}")]
    Forbidden(String),
    #[error("not found: {0}")]
    NotFound(String),
    #[error("bad request: {0}")]
    BadRequest(String),
    #[error("conflict: {0}")]
    Conflict(String),
    #[error("rate limited: {0}")]
    RateLimited(String),
    #[error("port occupied")]
    PortOccupied,
    #[error("device disabled")]
    DeviceDisabled,
    #[error("insufficient balance")]
    InsufficientBalance,
    #[error("wechat pay failed: {0}")]
    WechatPayFailed(String),
    #[error("wechat refund failed: {0}")]
    WechatRefundFailed(String),
    #[error("third-party error: {0}")]
    ThirdParty(String),
    #[error("business error {code}: {message}")]
    Business { code: i32, message: String },
    #[error("internal error: {0}")]
    Internal(String),
    #[error("database error: {0}")]
    Database(#[from] sqlx::Error),
    #[error("redis error: {0}")]
    Redis(#[from] redis::RedisError),
    #[error("config error: {0}")]
    Config(String),
    #[error("io error: {0}")]
    Io(#[from] std::io::Error),
    #[error("http client error: {0}")]
    HttpClient(String),
    #[error("service unavailable: {0}")]
    ServiceUnavailable(String),
    #[error("serde error: {0}")]
    Serde(#[from] serde_json::Error),
}

impl AppError {
    pub fn business(code: i32, message: impl Into<String>) -> Self {
        Self::Business { code, message: message.into() }
    }

    pub fn code(&self) -> i32 {
        match self {
            AppError::Unauthorized(_) => 1001,
            AppError::Forbidden(_) => 1003,
            AppError::NotFound(_) => 1004,
            AppError::BadRequest(_) => 1000,
            AppError::Conflict(_) => 1002,
            AppError::RateLimited(_) => 4291,
            AppError::PortOccupied => 2001,
            AppError::DeviceDisabled => 2002,
            AppError::InsufficientBalance => 2003,
            AppError::WechatPayFailed(_) => 3001,
            AppError::WechatRefundFailed(_) => 3002,
            AppError::ThirdParty(_) => 3000,
            AppError::ServiceUnavailable(_) => 5003,
            AppError::Business { code, .. } => *code,
            AppError::Internal(_) | AppError::Database(_) | AppError::Redis(_)
            | AppError::Config(_) | AppError::Io(_) | AppError::HttpClient(_)
            | AppError::Serde(_) => 5001,
        }
    }

    pub fn http_status(&self) -> StatusCode {
        match self {
            AppError::Unauthorized(_) => StatusCode::UNAUTHORIZED,
            AppError::Forbidden(_) => StatusCode::FORBIDDEN,
            AppError::NotFound(_) => StatusCode::NOT_FOUND,
            AppError::BadRequest(_) => StatusCode::BAD_REQUEST,
            AppError::Conflict(_) => StatusCode::CONFLICT,
            AppError::RateLimited(_) => StatusCode::TOO_MANY_REQUESTS,
            AppError::ServiceUnavailable(_) => StatusCode::SERVICE_UNAVAILABLE,
            AppError::PortOccupied | AppError::DeviceDisabled | AppError::InsufficientBalance
            | AppError::WechatPayFailed(_) | AppError::WechatRefundFailed(_)
            | AppError::ThirdParty(_) | AppError::Business { .. } => StatusCode::OK,
            _ => StatusCode::INTERNAL_SERVER_ERROR,
        }
    }

    pub fn message(&self) -> String {
        match self {
            AppError::Database(e) => {
                tracing::error!(error = %e, "database error");
                "数据库错误".into()
            }
            AppError::Redis(e) => {
                tracing::error!(error = %e, "redis error");
                "缓存错误".into()
            }
            AppError::Internal(m) => {
                tracing::error!(error = %m, "internal error");
                "服务器内部错误".into()
            }
            AppError::HttpClient(m) => {
                tracing::error!(error = %m, "http client error");
                "上游服务错误".into()
            }
            other => other.to_string(),
        }
    }
}

impl IntoResponse for AppError {
    fn into_response(self) -> Response {
        let status = self.http_status();
        let code = self.code();
        let message = self.message();
        let request_id = Uuid::new_v4().to_string();

        // 5xx 始终写 error 日志
        if status.is_server_error() {
            tracing::error!(request_id = %request_id, code = code, "server error: {}", message);
        } else {
            tracing::debug!(request_id = %request_id, code = code, "client error: {}", message);
        }

        let envelope = ApiEnvelope::<()> {
            code,
            message,
            data: None,
            request_id,
        };

        (status, Json(envelope)).into_response()
    }
}

impl From<reqwest::Error> for AppError {
    fn from(e: reqwest::Error) -> Self {
        AppError::HttpClient(e.to_string())
    }
}

impl From<config::ConfigError> for AppError {
    fn from(e: config::ConfigError) -> Self {
        AppError::Config(e.to_string())
    }
}

impl From<dotenvy::Error> for AppError {
    fn from(e: dotenvy::Error) -> Self {
        AppError::Config(e.to_string())
    }
}

pub type AppResult<T> = std::result::Result<T, AppError>;

/// 提取当前请求的 trace/request id(若可用)
pub fn current_request_id() -> String {
    Uuid::new_v4().to_string()
}

/// 在日志/响应里展示代码段位的便利宏
pub fn business<T>(code: i32, msg: impl Into<String>) -> AppResult<T> {
    Err(AppError::Business { code, message: msg.into() })
}

/// 错误码段位常量(供外部引用,避免魔术数字)
pub mod codes {
    pub const SUCCESS: i32 = 0;
    pub const BAD_REQUEST: i32 = 1000;
    pub const UNAUTHORIZED: i32 = 1001;
    pub const CONFLICT: i32 = 1002;
    pub const FORBIDDEN: i32 = 1003;
    pub const NOT_FOUND: i32 = 1004;

    pub const USER_PORT_OCCUPIED: i32 = 2001;
    pub const USER_DEVICE_DISABLED: i32 = 2002;
    pub const USER_INSUFFICIENT_BALANCE: i32 = 2003;

    pub const WECHAT_PAY_FAILED: i32 = 3001;
    pub const WECHAT_REFUND_FAILED: i32 = 3002;

    pub const RATE_LIMITED: i32 = 4291;
    pub const INTERNAL: i32 = 5001;
    pub const UNAVAILABLE: i32 = 5003;
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn envelope_ok_shape() {
        let env: ApiEnvelope<i32> = ApiEnvelope::ok(42, "rid-1");
        assert_eq!(env.code, 0);
        assert_eq!(env.data, Some(42));
        assert_eq!(env.request_id, "rid-1");
    }

    #[test]
    fn error_codes() {
        assert_eq!(AppError::Unauthorized("x".into()).code(), 1001);
        assert_eq!(AppError::PortOccupied.code(), 2001);
        assert_eq!(AppError::RateLimited("x".into()).code(), 4291);
        assert_eq!(AppError::Internal("x".into()).code(), 5001);
    }

    #[test]
    fn business_error_keeps_code() {
        let e = AppError::Business { code: 2999, message: "x".into() };
        assert_eq!(e.code(), 2999);
    }
}
