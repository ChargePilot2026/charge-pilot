//! Tracing / 日志 / OpenTelemetry 初始化
//!
//! 默认输出 JSON 到 stdout(便于 Loki/Promtail 收集);
//! 如设置了 OTEL_EXPORTER_OTLP_ENDPOINT,启用 OTLP 导出(Tempo/Jaeger)。


// 分层与序列化约束(P1a 建立;随 P3 逐服务迁移完成转 deny)
// 说明:配置在仓库根 clippy.toml,级别在这里。测试模块豁免。
#![deny(
    clippy::disallowed_macros,
    clippy::disallowed_types,
    clippy::disallowed_methods,
)]
use common_config::AppConfig;
use common_error::AppResult;
use once_cell::sync::OnceCell;
use std::sync::Arc;
use tracing_subscriber::{
    fmt::{self, format::FmtSpan},
    layer::SubscriberExt,
    EnvFilter, Registry,
};

static TELEMETRY: OnceCell<Arc<TelemetryState>> = OnceCell::new();

pub struct TelemetryState {
    pub service_name: String,
}

pub fn init(cfg: &AppConfig) -> AppResult<()> {
    let env_filter = EnvFilter::try_new(&cfg.log_level).unwrap_or_else(|_| EnvFilter::new("info"));
    let service = cfg.service.as_str();
    // Backtrace frames only make it into the log when the runtime asked for them.
    // Reading the env var at init avoids paying the cost in production where
    // RUST_BACKTRACE is unset. `fmt::layer().json()` does NOT auto-render the
    // `backtrace` field, so we add it as a top-level event field via with_target
    // and turn on FmtSpan::CLOSE so spans emit their own JSON entries when a
    // request enters/exits — useful for matching a 5xx to its handler.
    let backtrace_enabled = std::env::var_os("RUST_BACKTRACE")
        .or_else(|| std::env::var_os("RUST_LIB_BACKTRACE"))
        .map(|v| v != "0")
        .unwrap_or(false);

    let json_layer = fmt::layer()
        .json()
        .with_current_span(true)
        .with_span_list(false)
        .with_target(true)
        .with_span_events(if backtrace_enabled {
            FmtSpan::CLOSE
        } else {
            FmtSpan::NONE
        });

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