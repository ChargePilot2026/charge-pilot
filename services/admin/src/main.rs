//! admin 服务: PC 后台 API(管理 / 角色 / 财务 / 告警 / OTA / Webhook / 公告)
//!
//! 端口: 8082(由 Caddy 反代 + 服务 PC 后台静态资源)

mod api;
mod api_types;
mod auth;
mod billing;
mod ota;
mod webhook;
mod alert;
mod clients;
mod stream_consumer;
mod refund_task;
mod static_serve;

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
    info!(service = "admin", "starting");

    let db = Db::connect(&cfg.mysql).await?;
    db.ensure_schema_metadata().await?;
    let redis_cache = RedisCache::connect(&cfg.redis_cache).await?;
    let redis_stream = RedisStream::connect(&cfg.redis_stream).await?;

    let jwt = Arc::new(JwtCodec::new(&cfg.auth));
    let http = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(15))
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
    refund_task::spawn(state.clone());
    api::device_import::spawn_recovery(state.clone());

    let app = build_router(state);
    let addr: SocketAddr = cfg.http_bind.parse().expect("bind addr");
    info!(%addr, "admin listening");
    let listener = tokio::net::TcpListener::bind(addr).await?;
    axum::serve(listener, app.into_make_service()).await?;
    Ok(())
}

pub fn build_router(state: AppState) -> Router {
    let svc_token = state.service_token.clone();
    let jwt_codec = state.jwt.clone();

    // ===== 公开路由 =====
    let public_routes = Router::new()
        .route(api_types::paths::AUTH_LOGIN, post(auth::login))
        .route(api_types::paths::AUTH_REFRESH, post(auth::refresh));

    // ===== 内部路由(其他服务调用)=====
    let internal_routes = Router::new()
        .route(api_contracts::paths::ADMIN_DEVICE_PRICING, get(api::pricing_reads::device))
        .route(api_types::paths::INTERNAL_ANNOUNCEMENTS_ACTIVE, get(api::internal::announcements_active))
        .route(api_types::paths::INTERNAL_STATIONS_NEARBY, get(api::station_reads::nearby))
        .route(api_types::paths::INTERNAL_STATIONS_DETAIL, get(api::station_reads::detail))
        .route(api_types::paths::INTERNAL_PRICING_RULES_GET, get(api::internal::pricing_rule_get))
        .route(api_types::paths::INTERNAL_SPLIT_TEMPLATES_GET, get(api::internal::split_template_get))
        .route(api_types::paths::INTERNAL_EXPORT_TASK_GET, get(api::internal::export_task_get))
        .route(api_types::paths::INTERNAL_ALERTS_ACTIVE, get(api::internal::alerts_active))
        .route(api_types::paths::INTERNAL_DEVICES_REBOOT, post(api::internal::device_reboot))
        .layer(middleware::from_fn_with_state(svc_token.clone(), common_auth::refs::internal_token_mw));

    // ===== PC 后台路由(需 JWT)=====
    let admin_routes = Router::new()
        .route(api_types::paths::ADMIN_AUTH_LOGOUT, post(auth::logout))
        .route(api_types::paths::ADMIN_USERS, get(api::users::list).post(api::users::create))
        .route(api_types::paths::ADMIN_USER_DETAIL, get(api::users::get).put(api::users::update).delete(api::users::delete))
        .route(api_types::paths::ADMIN_USER_RESET_PASSWORD, post(api::users::reset_password))
        .route(api_types::paths::ADMIN_ROLES, get(api::roles::list).post(api::roles::create))
        .route(api_types::paths::ADMIN_ROLE_DETAIL, get(api::roles::get).put(api::roles::update).delete(api::roles::delete))
        .route(api_types::paths::ADMIN_PERMISSIONS, get(api::roles::permissions))
        .route(api_types::paths::ADMIN_STATIONS, get(api::stations::list).post(api::stations::create))
        .route(api_types::paths::ADMIN_STATION_DETAIL, get(api::stations::get).put(api::stations::update).delete(api::stations::delete))
        .route(api_types::paths::ADMIN_DEVICES, get(api::devices::list))
        .route(api_types::paths::ADMIN_DEVICE_DETAIL, get(api::devices::get))
        .route(api_types::paths::ADMIN_DEVICE_ORDERS, get(api::devices::orders))
        .route(api_types::paths::ADMIN_ORDERS, get(api::orders::list))
        .route(api_types::paths::ADMIN_DEVICE_IMPORTS, get(api::device_import::list).post(api::device_import::create))
        .route(api_types::paths::ADMIN_DEVICE_IMPORT_RETRY, post(api::device_import::retry))
        .route(api_types::paths::ADMIN_ORDER_DETAIL, get(api::orders::get))
        .route(api_types::paths::ADMIN_ORDER_TIMELINE, get(api::orders::timeline))
        .route(api_types::paths::ADMIN_BILLING_SETTLEMENTS, get(billing::settlements))
        .route(api_types::paths::ADMIN_BILLING_WITHDRAW, get(billing::withdraw_list).post(billing::withdraw_create))
        .route(api_types::paths::ADMIN_BILLING_WITHDRAW_REVIEW, post(billing::withdraw_review))
        .route("/api/v1/admin/billing/wallet-risks/:request_id/release",post(billing::wallet_risk_release))
        .route("/api/v1/admin/billing/wallet-risks",get(billing::wallet_risks))
        .route("/api/v1/admin/billing/wallet-risks/:request_id/review",post(billing::wallet_risk_review))
        .route(api_types::paths::ADMIN_BILLING_REFUNDS, get(billing::refunds))
        .route(api_types::paths::ADMIN_BILLING_REFUND_RETRY, post(billing::refund_retry))
        .route("/api/v1/admin/billing/refunds/:id/approve",post(billing::refund_approve))
        .route("/api/v1/admin/billing/refunds/:id/reject",post(billing::refund_reject))
        .route("/api/v1/admin/orders/:id/refunds",post(billing::refund_create))
        .route(api_types::paths::ADMIN_BILLING_INVOICES, get(billing::invoices))
        .route(api_types::paths::ADMIN_BILLING_INVOICE_APPROVE, post(billing::invoice_approve))
        .route(api_types::paths::ADMIN_BILLING_INVOICE_REJECT, post(billing::invoice_reject))
        .route(api_types::paths::ADMIN_BILLING_RECONCILE_LOGS, get(billing::reconcile_logs))
        .route(api_types::paths::ADMIN_ALERTS, get(alert::list))
        .route(api_types::paths::ADMIN_ALERT_ACK, post(alert::ack))
        .route(api_types::paths::ADMIN_ALERT_RULES, get(alert::rules_list).post(alert::rules_create))
        .route(api_types::paths::ADMIN_ALERT_RULE_DETAIL, get(alert::rules_get).put(alert::rules_update).delete(alert::rules_delete))
        .route(api_types::paths::ADMIN_ALERT_SUBSCRIPTIONS, get(alert::subs_list).post(alert::subs_create))
        .route(api_types::paths::ADMIN_RISK_CONFIG, get(alert::risk_config_get).put(alert::risk_config_put))
        .route(api_types::paths::ADMIN_COUPONS, get(api::coupons::list).post(api::coupons::create))
        .route(api_types::paths::ADMIN_COUPON_DETAIL, get(api::coupons::get).put(api::coupons::update).delete(api::coupons::delete))
        .route(api_types::paths::ADMIN_COUPON_STATS, get(api::coupons::stats))
        .route(api_types::paths::ADMIN_MEMBERSHIP, get(api::membership::list).post(api::membership::create))
        .route(api_types::paths::ADMIN_CHARGE_RULES, get(api::settings::charge_rules).post(api::settings::charge_rule_create))
        .route(api_types::paths::ADMIN_PRICING_TEMPLATES, get(api::settings::pricing_templates).post(api::settings::pricing_template_create))
        .route(api_types::paths::ADMIN_SPLIT_TEMPLATES, get(api::settings::split_templates).post(api::settings::split_template_create))
        .route(api_types::paths::ADMIN_SPLIT_TEMPLATE_PARTIES, get(api::settings::split_parties).post(api::settings::split_party_create))
        .route(api_types::paths::ADMIN_OTA, get(api::settings::ota_get).put(api::settings::ota_put))
        .route(api_types::paths::ADMIN_ANNOUNCEMENTS, get(api::announcements::list).post(api::announcements::create))
        .route(api_types::paths::ADMIN_ANNOUNCEMENT_DETAIL, get(api::announcements::get).put(api::announcements::update).delete(api::announcements::delete))
        .route(api_types::paths::ADMIN_CUSTOMER_SERVICE, get(api::customer_service::list).post(api::customer_service::create))
        .route(api_types::paths::ADMIN_CUSTOMER_SERVICE_DETAIL, get(api::customer_service::get).put(api::customer_service::update).delete(api::customer_service::delete))
        .route(api_types::paths::ADMIN_WHITELABEL, get(api::whitelabel::get).put(api::whitelabel::put))
        .route(api_types::paths::ADMIN_WEBHOOKS, get(webhook::list).post(webhook::create))
        .route(api_types::paths::ADMIN_WEBHOOK_DETAIL, get(webhook::get).put(webhook::update).delete(webhook::delete))
        .route(api_types::paths::ADMIN_WEBHOOK_DELIVERIES, get(webhook::deliveries))
        .route(api_types::paths::ADMIN_OTA_PACKAGES, get(ota::packages_list).post(ota::packages_create))
        .route(api_types::paths::ADMIN_OTA_PACKAGE_DETAIL, get(ota::packages_get).delete(ota::packages_delete))
        .route(api_types::paths::ADMIN_OTA_SCHEDULES, get(ota::schedules_list).post(ota::schedules_create))
        .route(api_types::paths::ADMIN_OTA_SCHEDULE_DETAIL, get(ota::schedules_get).post(ota::schedules_trigger))
        .route(api_types::paths::ADMIN_EXPORT, post(api::export::create))
        .layer(middleware::from_fn_with_state(jwt_codec.clone(), common_auth::refs::require_admin_jwt));

    Router::new()
        .merge(public_routes)
        .merge(internal_routes)
        .merge(admin_routes)
        .route(api_types::paths::HEALTH, get(api::health))
        // PC 后台静态资源(SPA)
        .fallback(static_serve::serve_spa)
        .layer(TraceLayer::new_for_http())
        .layer(middleware::from_fn(common_http::request_id_layer))
        .with_state(state)
}
