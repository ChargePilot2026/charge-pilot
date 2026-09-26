//! billing 服务 — 计费引擎 + 价费分离 + 多方分账
//!
//! 端口: 8084(仅内部 HTTP)
//! 端点: 9 个内部 API + Stream 消费 charge_ended_stream.billing-cg

mod api;
mod order_reads;
mod api_types;
mod engine;
mod quote_pricing;
mod split;
mod stream_consumer;

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
use tower_http::trace::TraceLayer;
use tracing::info;

#[derive(Clone)]
pub struct AppState {
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
    info!(service = "billing", "starting");

    let db = Db::connect(&cfg.mysql).await?;
    db.ensure_schema_metadata().await?;
    let redis_cache = RedisCache::connect(&cfg.redis_cache).await?;
    let redis_stream = RedisStream::connect(&cfg.redis_stream).await?;
    let jwt = Arc::new(JwtCodec::new(&cfg.auth));
    let http = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(10))
        .build().expect("reqwest");

    let state = AppState {
        cfg: cfg.clone(), db: db.clone(),
        redis_cache: redis_cache.clone(), redis_stream: redis_stream.clone(),
        jwt: jwt.clone(), http: http.clone(),
        service_token: Arc::new(cfg.auth.service_token.clone()),
    };

    stream_consumer::spawn_all(state.clone()).await?;

    let app = build_router(state);
    let addr: SocketAddr = cfg.http_bind.parse().expect("bind addr");
    info!(%addr, "billing listening");
    let listener = tokio::net::TcpListener::bind(addr).await?;
    axum::serve(listener, app.into_make_service()).await?;
    Ok(())
}

pub fn build_router(state: AppState) -> Router {
    let svc_token = state.service_token.clone();

    let internal = Router::new()
        .route(api_contracts::paths::BILLING_ORDER_SUMMARY, get(order_reads::summary))
        .route(crate::api_types::paths::QUOTE, post(api::quote))
        .route(crate::api_types::paths::CALCULATE, post(api::calculate))
        .route(crate::api_types::paths::FEE_BREAKDOWN, get(api::fee_breakdown))
        .route(crate::api_types::paths::SPLIT, post(api::split))
        .route(crate::api_types::paths::ORDER_SPLIT, get(api::order_split))
        .route(crate::api_types::paths::SETTLEMENT_DETAIL, get(api::settlement_detail))
        .route(crate::api_types::paths::INVOICE_SETTLE_DETAIL, get(api::invoice_settle_detail))
        .route(crate::api_types::paths::REFUND_CALC, post(api::refund_calc))
        .route(crate::api_types::paths::WITHDRAW_REQUESTS, post(api::withdraw_create))
        .layer(ax_middleware::from_fn_with_state(svc_token.clone(), common_auth::refs::internal_token_mw));

    Router::new()
        .merge(internal)
        .route(crate::api_types::paths::HEALTH, get(api::health))
        .layer(TraceLayer::new_for_http())
        .layer(ax_middleware::from_fn(common_http::request_id_layer))
        .with_state(state)
}
