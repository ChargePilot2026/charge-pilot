//! gateway 服务 — 9100 TCP/JSON 设备接入 + 内部 HTTP API
//!
//! 端口分配(技术规格 § 2.1):
//!   - 9100: TCP 设备长连接
//!   - 8083: 内部 HTTP(经 device-net/docker network)

mod api;
mod scan;
mod provision;
mod registration;
pub mod services;
mod clients;
mod protocol;
mod telemetry_obs;
mod stream_consumer;
mod charge_command;
mod charge_stop;
mod outbox;

use axum::{
    middleware as ax_middleware,
    routing::{get, post},
    Router,
};
use common_auth::JwtCodec;
use common_config::AppConfig;
use common_db::Db;
use common_error::AppResult;
use common_redis::{RedisCache, RedisStream};
use common_telemetry as telemetry;
use std::net::SocketAddr;
use std::sync::Arc;
use tokio::net::TcpListener;
use tower_http::trace::TraceLayer;
use tracing::info;

/// ⚠️ P3:**没有 `pub db`**。handler 只能拿到 `device` / `command` 两个
/// 能力域服务对象,它们的 `db` 字段私有 —— 绕过事务与分层直写 SQL 的入口
/// 在类型层面被封死。
#[derive(Clone)]
pub struct AppState {
    pub connections: protocol::connections::Connections,
    pub cfg: Arc<AppConfig>,
    pub device: services::DeviceService,
    pub command: services::ChargeCommandService,
    pub stop: services::ChargeStopService,
    pub outbox: services::OutboxService,
    pub redis_cache: RedisCache,
    pub redis_stream: RedisStream,
    pub jwt: Arc<JwtCodec>,
    pub http: reqwest::Client,
    pub service_token: Arc<String>,
}

#[tokio::main]
async fn main() -> AppResult<()> {
    let cfg = Arc::new(AppConfig::load()?);
    telemetry::init(&cfg)?;
    info!(service = "gateway", "starting");

    let db = Db::connect(&cfg.mysql).await?;
    db.ensure_schema_metadata().await?;
    let redis_cache = RedisCache::connect(&cfg.redis_cache).await?;
    let redis_stream = RedisStream::connect(&cfg.redis_stream).await?;
    let jwt = Arc::new(JwtCodec::new(&cfg.auth));
    let http = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(10))
        .build()
        .expect("reqwest");

    let services::GatewayServices { device, command, stop, outbox } =
        services::build(db, http.clone(), Arc::new(cfg.auth.service_token.clone()), cfg.clone(), redis_cache.clone(), redis_stream.clone());
    let state = AppState {
        connections: protocol::connections::Connections::default(),
        cfg: cfg.clone(),
        device,
        command,
        stop,
        outbox,
        redis_cache: redis_cache.clone(),
        redis_stream: redis_stream.clone(),
        jwt: jwt.clone(),
        http: http.clone(),
        service_token: Arc::new(cfg.auth.service_token.clone()),
    };

    stream_consumer::spawn_all(state.clone()).await?;
    charge_command::spawn_recovery(state.clone());
    charge_stop::spawn(state.clone());
    outbox::spawn(state.clone());

    // ===== 启动 TCP 监听(9100)=====
    // D12:**先 bind 再 spawn**。端口被占用 / 地址配置错误时必须让启动失败,
    // 而不是打一行日志后继续以"设备连不上但健康检查返回 ok"的状态运行。
    let tcp_state = state.clone();
    let tcp_bind = std::env::var("GATEWAY_TCP_BIND").unwrap_or_else(|_| "0.0.0.0:9100".into());
    let tcp_listener = protocol::tcp::bind_tcp_listener(&tcp_bind)
        .await
        .map_err(|e| {
            common_error::AppError::Config(format!("设备 TCP 监听绑定失败 {tcp_bind}: {e}"))
        })?;
    let tcp_bind_for_task = tcp_bind.clone();
    // 关键任务生命周期:accept 循环异常退出必须可见 —— 撤销就绪并结束进程,
    // 不允许静默降级成"设备接入已死但服务仍报健康"。
    tokio::spawn(async move {
        match protocol::tcp::serve_tcp(tcp_listener, &tcp_bind_for_task, tcp_state).await {
            Ok(()) => tracing::error!("TCP 监听循环意外结束"),
            Err(e) => tracing::error!(
                error = %e,
                backtrace = %common_error::backtrace(),
                "TCP 监听循环异常退出"
            ),
        }
        tracing::error!("设备接入已不可用,撤销就绪状态并退出进程");
        std::process::exit(1);
    });

    // ===== HTTP API(8083)=====
    let app = build_router(state);
    let addr: SocketAddr = cfg.http_bind.parse().expect("bind addr");
    info!(%addr, "gateway http listening");
    let listener = TcpListener::bind(addr).await?;
    axum::serve(listener, app.into_make_service()).await?;
    Ok(())
}

pub fn build_router(state: AppState) -> Router {
    let svc_token = state.service_token.clone();

    let internal_routes = Router::new()
        .route(api_contracts::paths::GATEWAY_DEVICE_PROVISION, post(provision::provision))
        .route(api_contracts::paths::GW_DEVICE_SESSIONS_CLEAN, post(api::cleanup_idle_device_sessions))
        // 扫码解析
        .route("/api/v1/internal/scan/resolve", post(api::scan_resolve))
        .route("/api/v1/internal/scan/port", post(api::scan_port))
        // 设备控制
        .route("/api/v1/internal/devices/:id/snapshot", get(api::device_snapshot))
        .route("/api/v1/internal/devices/:id", get(api::device_get))
        .route("/api/v1/internal/devices/:id/ports", get(api::device_ports))
        .route("/api/v1/internal/devices/:id/orders", get(api::device_orders))
        .route("/api/v1/internal/devices/:id/curve", get(api::device_curve))
        .route("/api/v1/internal/devices/:id/historical-curve", get(api::device_historical_curve))
        .route("/api/v1/internal/devices/:id/reboot", post(api::device_reboot))
        .route("/api/v1/internal/devices/:id/firmware-push", post(api::device_firmware_push))
        // 充电控制
        .route("/api/v1/internal/charge-orders/stop", post(charge_stop::request))
        // 设备注册 / 查询
        .route(api_contracts::paths::GW_DEVICE_REGISTER, post(registration::register))
        .route("/api/v1/internal/devices/:id/backfill", post(api::device_backfill))
        .route("/api/v1/internal/devices/:id/command", post(api::device_command))
        .layer(ax_middleware::from_fn_with_state(svc_token.clone(), common_auth::refs::internal_token_mw));

    Router::new()
        .merge(internal_routes)
        .route("/api/v1/health", get(api::health))
        .layer(
            TraceLayer::new_for_http().make_span_with(|req: &axum::http::Request<axum::body::Body>| {
                let id = req
                    .headers()
                    .get("x-request-id")
                    .and_then(|v| v.to_str().ok())
                    .unwrap_or("-");
                tracing::info_span!("http", method = %req.method(), uri = %req.uri(), trace_id = %id)
            }),
        )
        .layer(ax_middleware::from_fn(common_http::request_id_layer))
        .with_state(state)
}
