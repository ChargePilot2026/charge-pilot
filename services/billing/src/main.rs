//! billing 服务 — 计费引擎 + 价费分离 + 多方分账
//!
//! 端口: 8084(仅内部 HTTP)
//! 端点: 9 个内部 API + Stream 消费 charge_ended_stream.billing-cg

// 分层与序列化约束(P1a 建立;P5 收口完成,转 deny)
// 说明:配置在仓库根 clippy.toml,级别在这里。billing 同时有 `[lib]` 与
// `[[bin]]` 两个 crate root,二者都要声明 —— crate 级属性不跨 root 传递。
// repository 层与测试模块各自文件级豁免。
#![cfg_attr(
    not(test),
    deny(
        clippy::disallowed_macros,
        clippy::disallowed_types,
        clippy::disallowed_methods,
    )
)]

mod api;
mod charge_fee;
mod fee_delivery;
mod order_reads;
mod api_types;
mod engine;
mod quote_pricing;
mod metered_pricing;
mod split;
mod stream_consumer;
pub mod services;

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

/// ⚠️ P3:**不再有裸 `db` 字段**。handler 只能拿到 `fee` / `settlement` / `invoice`
/// 三个能力域服务对象,它们的 `base` 字段私有 —— 绕过事务与分层直写 SQL 的入口
/// 在类型层面被封死。
#[derive(Clone)]
pub struct AppState {
    pub cfg: Arc<AppConfig>,
    pub fee: services::FeeService,
    pub settlement: services::SettlementService,
    pub invoice: services::InvoiceService,
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

    let services::BillingServices { fee, settlement, invoice } = services::build(
        db,
        http.clone(),
        Arc::new(cfg.auth.service_token.clone()),
        cfg.clone(),
    );

    let state = AppState {
        cfg: cfg.clone(), fee, settlement, invoice,
        redis_cache: redis_cache.clone(), redis_stream: redis_stream.clone(),
        jwt: jwt.clone(), http: http.clone(),
        service_token: Arc::new(cfg.auth.service_token.clone()),
    };

    stream_consumer::spawn_all(state.clone()).await?;
    fee_delivery::spawn(state.clone());

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
        .layer(ax_middleware::from_fn_with_state(svc_token.clone(), common_auth::refs::internal_token_mw));

    Router::new()
        .merge(internal)
        .route(crate::api_types::paths::HEALTH, get(api::health))
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
