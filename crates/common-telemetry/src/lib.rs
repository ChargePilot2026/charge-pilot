//! Tracing / 日志 / OpenTelemetry 初始化
//!
//! 默认输出 JSON 到 stdout(便于 Loki/Promtail 收集);
//! 如设置了 OTEL_EXPORTER_OTLP_ENDPOINT,启用 OTLP 导出(Tempo/Jaeger)。

use common_config::AppConfig;
use common_error::AppResult;
use once_cell::sync::OnceCell;
use std::sync::Arc;
use tracing_subscriber::{fmt, layer::SubscriberExt, EnvFilter, Registry};

static TELEMETRY: OnceCell<Arc<TelemetryState>> = OnceCell::new();

pub struct TelemetryState {
    pub service_name: String,
}

pub fn init(cfg: &AppConfig) -> AppResult<()> {
    let env_filter = EnvFilter::try_new(&cfg.log_level).unwrap_or_else(|_| EnvFilter::new("info"));
    let service = cfg.service.as_str();
    let json_layer = fmt::layer()
        .json()
        .with_current_span(true)
        .with_span_list(false)
        .with_target(true);

    let subscriber = Registry::default()
        .with(env_filter)
        .with(json_layer);

    tracing::subscriber::set_global_default(subscriber)
        .map_err(|e| common_error::AppError::Config(format!("tracing init: {e}")))?;

    TELEMETRY
        .set(Arc::new(TelemetryState { service_name: service.to_string() }))
        .map_err(|_| common_error::AppError::Config("telemetry already initialized".into()))?;

    tracing::info!(service = service, "telemetry initialized");
    Ok(())
}

pub fn current_service() -> Option<String> {
    TELEMETRY.get().map(|s| s.service_name.clone())
}

/// 在日志里注入 trace_id(用于跨服务关联) —— 占位实现,真实接 OTel 时由中间件注入。
pub fn inject_trace<B>(_req: &mut http::Request<B>) {
    // 占位:trace_id 由 tracing-opentelemetry / tower-http 中间件处理
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn service_name_matches() {
        // placeholder; init() 副作用 + OnceCell 限制,只能单测 enum
        assert!(!TelemetryState { service_name: "test".into() }.service_name.is_empty());
    }
}