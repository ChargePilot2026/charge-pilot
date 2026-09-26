//! gateway 服务 — 设备长连接接入(TCP/MQTT)+ 内部 HTTP API
//!
//! 端口分配(技术规格 § 2.1):
//!   - 9100: TCP 设备长连接
//!   - 1883: MQTT 设备长连接
//!   - 8083: 内部 HTTP(经 device-net/docker network)

mod api;
mod scan;
mod provision;
mod registration;
mod clients;
mod protocol;
mod device;
mod telemetry_obs;
mod alert;
mod ota;
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

#[derive(Clone)]
pub struct AppState {
    pub connections: protocol::connections::Connections,
    pub cfg: Arc<AppConfig>,
    pub db: Db,
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

    let state = AppState {
        connections: protocol::connections::Connections::default(),
        cfg: cfg.clone(),
        db: db.clone(),
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
    let tcp_state = state.clone();
    let tcp_bind = std::env::var("GATEWAY_TCP_BIND").unwrap_or_else(|_| "0.0.0.0:9100".into());
    tokio::spawn(async move {
        if let Err(e) = protocol::tcp::run_tcp_listener(&tcp_bind, tcp_state).await {
            tracing::error!(error=%e, "tcp listener exited");
        }
    });

    // ===== 启动 MQTT 监听(1883)— 本期简化 =====
    let mqtt_state = state.clone();
    let mqtt_bind = std::env::var("GATEWAY_MQTT_BIND").unwrap_or_else(|_| "0.0.0.0:1883".into());
    tokio::spawn(async move {
        if let Err(e) = protocol::mqtt::run_mqtt_listener(&mqtt_bind, mqtt_state).await {
            tracing::error!(error=%e, "mqtt listener exited");
        }
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
        .layer(TraceLayer::new_for_http())
        .layer(ax_middleware::from_fn(common_http::request_id_layer))
        .with_state(state)
}
