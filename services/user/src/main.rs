//! user 服务:小程序侧 API + 微信支付 + 退款编排 + Stream 消费者

// 主模块文件(与 lib.rs 共用,各自 mod 声明各自一份,这样 bin 与 lib 都能独立编译)
mod api;
mod api_envelope;
mod api_types;
mod clients;
mod payment;
mod refund;
mod wallet;
mod coupon;
mod invoice;
mod station;
mod stream_consumer;
mod wechat;
mod repo;

use axum::{
    middleware,
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
    info!(service = "user", "starting");

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
        cfg: cfg.clone(),
        db: db.clone(),
        redis_cache: redis_cache.clone(),
        redis_stream: redis_stream.clone(),
        jwt: jwt.clone(),
        http: http.clone(),
        service_token: Arc::new(cfg.auth.service_token.clone()),
    };

    stream_consumer::spawn_all(state.clone()).await?;

    let app = build_router(state);
    let addr: SocketAddr = cfg.http_bind.parse().expect("bind addr");
    info!(%addr, "user listening");
    let listener = tokio::net::TcpListener::bind(addr).await?;
    axum::serve(listener, app.into_make_service()).await?;
    Ok(())
}

pub fn build_router(state: AppState) -> Router {
    let svc_token = state.service_token.clone();
    let jwt_codec = state.jwt.clone();

    let public_routes = Router::new()
        .route(api_types::paths::AUTH_LOGIN, post(api::login))
        .route(api_types::paths::AUTH_REFRESH, post(api::refresh))
        .route(api_types::paths::PAYMENT_WECHAT_CALLBACK, post(payment::wechat_callback));

    let internal_routes = Router::new()
        .route(api_types::paths::INTERNAL_START_RESULT, post(payment::start_result))
        .route(api_types::paths::INTERNAL_REFUND_CLAIM, post(refund::claim))
        .route(api_types::paths::INTERNAL_REFUND_RESULT, post(refund::result))
        .route(api_types::paths::INTERNAL_REFUND_DETAIL, get(refund::detail))
        .route(api_types::paths::INTERNAL_PAYMENT_DETAIL, get(payment::detail))
        .route(api_types::paths::INTERNAL_ORDER_DETAIL, get(api::internal_order_detail))
        .route(api_types::paths::INTERNAL_INVOICE_DETAIL, get(invoice::internal_detail))
        .route(api_types::paths::INTERNAL_COUPON_STATS, get(coupon::stats))
        .layer(middleware::from_fn_with_state(svc_token.clone(), common_auth::refs::internal_token_mw));

    let user_routes = Router::new()
        .route(api_types::paths::USER_SCAN_RESOLVE, post(api::scan_resolve))
        .route(api_types::paths::USER_SCAN_PORT, post(api::scan_port))
        .route(api_types::paths::USER_SCAN_START, post(api::scan_start))
        .route(api_types::paths::USER_SCAN_CANCEL, post(api::scan_cancel))
        .route(api_types::paths::USER_CHARGE_STOP, post(api::charge_stop))
        .route(api_types::paths::USER_CHARGE_ONGOING, get(api::charge_ongoing))
        .route(api_types::paths::USER_CHARGE_SNAPSHOT, get(api::charge_snapshot))
        .route(api_types::paths::USER_CHARGE_CURVE, get(api::charge_curve))
        .route(api_types::paths::USER_CHARGE_HISTORY, get(api::charge_history))
        .route(api_types::paths::USER_CHARGE_DETAIL, get(api::charge_detail))
        .route(api_types::paths::USER_CHARGE_HISTORICAL_CURVE, get(api::charge_historical_curve))
        .route(api_types::paths::USER_CHARGE_FEEDBACK, post(api::charge_feedback))
        .route(api_types::paths::USER_PROFILE, get(api::profile_get))
        .route(api_types::paths::USER_PHONE_BIND, post(api::phone_bind))
        .route(api_types::paths::USER_PHONE_UNBIND, post(api::phone_unbind))
        .route(api_types::paths::USER_WALLET_BALANCE, get(wallet::balance))
        .route(api_types::paths::USER_WALLET_RECHARGE, post(wallet::recharge))
        .route(api_types::paths::USER_WALLET_TXNS, get(wallet::txns))
        .route(api_types::paths::USER_WALLET_REFUND, post(wallet::refund))
        .route(api_types::paths::USER_STATION_NEARBY, get(station::nearby))
        .route(api_types::paths::USER_STATION_DETAIL, get(station::detail))
        .route(api_types::paths::USER_DEVICE_REPORT_FAULT, post(station::report_fault))
        .route(api_types::paths::USER_COUPON_MY, get(coupon::my))
        .route(api_types::paths::USER_COUPON_PREVIEW, post(coupon::preview))
        .route(api_types::paths::USER_INVOICE_APPLY, post(invoice::apply))
        .route(api_types::paths::USER_INVOICE_MY, get(invoice::my))
        .route(api_types::paths::USER_ANNOUNCEMENT_LIST, get(api::announcement_list))
        .route(api_types::paths::USER_CUSTOMER_SERVICE_ENTRY, post(api::customer_service_entry))
        .layer(middleware::from_fn_with_state(jwt_codec.clone(), common_auth::refs::require_user_jwt));

    Router::new()
        .merge(public_routes)
        .merge(internal_routes)
        .merge(user_routes)
        .route(api_types::paths::HEALTH, get(api::health))
        .layer(TraceLayer::new_for_http())
        .layer(middleware::from_fn(common_http::request_id_layer))
        .with_state(state)
}