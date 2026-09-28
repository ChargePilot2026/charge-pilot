//! admin 服务: PC 后台 API(管理 / 角色 / 财务 / 告警 / OTA / Webhook / 公告)
//!
//! 端口: 8082(由 Caddy 反代 + 服务 PC 后台静态资源)

mod api;
mod api_types;
mod clients;
mod password;
mod services;
mod static_serve;
mod stream_consumer;

mod capability;
use capability::{alert, cases, config, device, finance, identity, internal, order, webhook};

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
use std::time::Duration;
use tower_http::trace::TraceLayer;
use tracing::info;

/// outbox 发布循环的节拍与批量。1 秒一轮:事件落库到投递的延迟上限远小于
/// 运营能察觉的量级;批量 50 足够让一波积压在一轮内清完。
const OUTBOX_PUBLISH_INTERVAL: Duration = Duration::from_secs(1);
const OUTBOX_PUBLISH_BATCH: usize = 50;

#[derive(Clone)]
pub struct AppState {
    pub cfg: Arc<AppConfig>,
    /// P3:裸 `Db` 已从 AppState 移除。连接一律经具名能力域取得
    /// (`st.identity` / `st.finance` / `st.device` / …),见 `services.rs`。
    pub services: services::AdminServices,
    pub redis_cache: RedisCache,
    pub redis_stream: RedisStream,
    pub jwt: Arc<JwtCodec>,
    pub http: reqwest::Client,
    pub service_token: Arc<String>,
}

/// `AppState` 直接 `Deref` 到能力域集合,于是 `st.identity` / `st.finance` 这类
/// 字段访问成立。`cfg` / `redis_*` / `jwt` / `http` 等自身字段优先级更高,
/// 两者不冲突。
impl std::ops::Deref for AppState {
    type Target = services::AdminServices;
    fn deref(&self) -> &Self::Target {
        &self.services
    }
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
        services: services::build(
            db,
            http.clone(),
            Arc::new(cfg.auth.service_token.clone()),
            cfg.clone(),
            redis_cache.clone(),
            redis_stream.clone(),
        ),
        redis_cache: redis_cache.clone(),
        redis_stream: redis_stream.clone(),
        jwt: jwt.clone(),
        http: http.clone(),
        service_token: Arc::new(cfg.auth.service_token.clone()),
    };

    stream_consumer::spawn_all(state.clone()).await?;
    spawn_outbox_publisher(state.clone());
    finance::refund_task::spawn(state.clone());
    device::spawn_recovery(state.clone());

    let app = build_router(state);
    let addr: SocketAddr = cfg.http_bind.parse().expect("bind addr");
    info!(%addr, "admin listening");
    let listener = tokio::net::TcpListener::bind(addr).await?;
    axum::serve(listener, app.into_make_service()).await?;
    Ok(())
}

/// 启动 outbox 发布循环(D11)。
///
/// 事件已在业务事务里落库,这里只负责把它们送进 Redis Stream。
/// 失败只打日志:发布器是旁路,DB 或 Redis 抖动不应该拖垮 admin 的 HTTP 服务。
/// 与 worker 的后台循环同样丢弃 `JoinHandle` —— 已知缺口,留待 D12 一并收口。
fn spawn_outbox_publisher(state: AppState) {
    tokio::spawn(async move {
        let mut tick = tokio::time::interval(OUTBOX_PUBLISH_INTERVAL);
        loop {
            tick.tick().await;
            match state.outbox.publish_pending(OUTBOX_PUBLISH_BATCH).await {
                Ok(n) if n > 0 => info!(published = n, "admin outbox published"),
                Ok(_) => {}
                Err(e) => tracing::error!(error = %e, "admin outbox 发布失败,下个周期重试"),
            }
        }
    });
}

pub fn build_router(state: AppState) -> Router {
    let svc_token = state.service_token.clone();
    let jwt_codec = state.jwt.clone();

    // ===== 公开路由 =====
    let public_routes = Router::new()
        .route(api_types::paths::AUTH_LOGIN, post(identity::login))
        .route(api_types::paths::AUTH_REFRESH, post(identity::refresh));

    // ===== 内部路由(其他服务调用)=====
    let internal_routes = Router::new()
        .route(api_contracts::paths::ADMIN_DEVICE_PRICING, get(config::device_pricing))
        .route(api_types::paths::INTERNAL_ANNOUNCEMENTS_ACTIVE, get(internal::active_announcements))
        .route(api_contracts::paths::ADMIN_INTERNAL_ANNOUNCEMENTS_EXPIRE, post(internal::expire_announcements))
        .route(api_contracts::paths::ADMIN_INTERNAL_CUSTOMER_SERVICE_ENTRY, get(internal::customer_service_entry))
        .route(api_types::paths::INTERNAL_STATIONS_NEARBY, get(internal::stations_nearby))
        .route(api_types::paths::INTERNAL_STATIONS_DETAIL, get(internal::stations_detail))
        .route(api_types::paths::INTERNAL_PRICING_RULES_GET, get(internal::pricing_rule_get))
        .route(api_types::paths::INTERNAL_SPLIT_TEMPLATES_GET, get(internal::split_template_get))
        .route(api_types::paths::INTERNAL_ALERTS_ACTIVE, get(internal::alerts_active))
        .route(api_types::paths::INTERNAL_DEVICES_REBOOT, post(internal::device_reboot))
        // D11:worker 投递 webhook 后的明细回写(与 worker 同表归属,故在 admin 落库)
        .route(api_contracts::paths::ADMIN_INTERNAL_WEBHOOK_DELIVERIES, post(webhook::record_delivery))
        .layer(middleware::from_fn_with_state(svc_token.clone(), common_auth::refs::internal_token_mw));

    // ===== PC 后台路由(需 JWT)=====
    let admin_routes = Router::new()
        .route(api_types::paths::ADMIN_AUTH_LOGOUT, post(identity::logout))
        .route(api_types::paths::ADMIN_DASHBOARD, get(api::dashboard::get))
        .route(api_types::paths::ADMIN_USERS, get(identity::list).post(identity::create))
        .route(api_types::paths::ADMIN_USER_DETAIL, get(identity::get).put(identity::update).delete(identity::delete))
        .route(api_types::paths::ADMIN_USER_RESET_PASSWORD, post(identity::reset_password))
        .route(api_types::paths::ADMIN_ROLES, get(identity::roles_list).post(identity::roles_create))
        .route(api_types::paths::ADMIN_ROLE_DETAIL, get(identity::roles_get).put(identity::roles_update).delete(identity::roles_delete))
        .route(api_types::paths::ADMIN_PERMISSIONS, get(identity::permissions))
        .route(api_types::paths::ADMIN_STATIONS, get(device::stations).post(device::station_create))
        .route(api_types::paths::ADMIN_STATION_DETAIL, get(device::station_detail).put(device::station_update).delete(device::station_delete))
        .route(api_types::paths::ADMIN_DEVICES, get(device::device_list))
        .route(api_types::paths::ADMIN_DEVICE_DETAIL, get(device::device_get))
        .route(api_types::paths::ADMIN_DEVICE_ORDERS, get(device::device_orders))
        .route(api_types::paths::ADMIN_ORDERS, get(order::list))
        .route(api_types::paths::ADMIN_DEVICE_IMPORTS, get(device::import_list).post(device::import_create))
        .route(api_types::paths::ADMIN_DEVICE_IMPORT_RETRY, post(device::import_retry))
        .route(api_types::paths::ADMIN_ORDER_DETAIL, get(order::get))
        .route(api_types::paths::ADMIN_ORDER_TIMELINE, get(order::timeline))
        .route(api_types::paths::ADMIN_BILLING_SETTLEMENTS, get(finance::settlements))
        .route("/api/v1/admin/billing/wallet-risks/:request_id/release",post(finance::wallet_risk_release))
        .route("/api/v1/admin/billing/wallet-risks",get(finance::wallet_risks))
        .route("/api/v1/admin/billing/wallet-risks/:request_id/review",post(finance::wallet_risk_review))
        .route(api_types::paths::ADMIN_BILLING_REFUNDS, get(finance::refunds))
        .route(api_types::paths::ADMIN_BILLING_REFUND_RETRY, post(finance::refund_retry))
        .route("/api/v1/admin/billing/refunds/:id/approve",post(finance::refund_approve))
        .route("/api/v1/admin/billing/refunds/:id/reject",post(finance::refund_reject))
        .route("/api/v1/admin/orders/:id/refunds",post(finance::refund_create))
        .route(api_types::paths::ADMIN_BILLING_INVOICES, get(finance::invoices))
        .route(api_types::paths::ADMIN_BILLING_INVOICE_APPROVE, post(finance::invoice_approve))
        .route(api_types::paths::ADMIN_BILLING_INVOICE_REJECT, post(finance::invoice_reject))
        .route(api_types::paths::ADMIN_BILLING_RECONCILE_LOGS, get(finance::reconcile_logs))
        .route(api_types::paths::ADMIN_ALERTS, get(alert::list))
        .route(api_types::paths::ADMIN_ALERT_ACK, post(alert::ack))
        .route(api_types::paths::ADMIN_ALERT_RULES, get(alert::rules_list).post(alert::rules_create))
        .route(api_types::paths::ADMIN_ALERT_RULE_DETAIL, get(alert::rules_get).put(alert::rules_update).delete(alert::rules_delete))
        .route(api_types::paths::ADMIN_ALERT_SUBSCRIPTIONS, get(alert::subs_list).post(alert::subs_create))
        .route(api_types::paths::ADMIN_RISK_CONFIG, get(alert::risk_config_get).put(alert::risk_config_put))
        .route(api_types::paths::ADMIN_COUPONS, get(cases::coupon_list).post(cases::coupon_create))
        .route(api_types::paths::ADMIN_COUPON_DETAIL, get(cases::coupon_get).put(cases::coupon_update).delete(cases::coupon_delete))
        .route(api_types::paths::ADMIN_COUPON_STATS, get(cases::coupon_stats))
        .route(api_types::paths::ADMIN_COUPON_GRANTS, post(cases::coupon_grant))
        .route(api_types::paths::ADMIN_CHARGE_RULES, get(config::charge_rules).post(config::charge_rule_create))
        .route(api_types::paths::ADMIN_PRICING_TEMPLATES, get(config::pricing_templates).post(config::pricing_template_create))
        .route(api_types::paths::ADMIN_SPLIT_TEMPLATES, get(config::split_templates).post(config::split_template_create))
        .route(api_types::paths::ADMIN_SPLIT_TEMPLATE_PARTIES, get(config::split_parties).post(config::split_party_create))
        .route(api_types::paths::ADMIN_OTA, get(config::ota_get).put(config::ota_put))
        .route(api_types::paths::ADMIN_ANNOUNCEMENTS, get(config::announcements).post(config::announcement_create))
        .route(api_types::paths::ADMIN_ANNOUNCEMENT_DETAIL, get(config::announcement_get).put(config::announcement_update).delete(config::announcement_delete))
        .route(api_types::paths::ADMIN_CUSTOMER_SERVICE, get(config::customer_service_list).post(config::customer_service_create))
        .route(api_types::paths::ADMIN_CUSTOMER_SERVICE_DETAIL, get(config::customer_service_get).put(config::customer_service_update).delete(config::customer_service_delete))
        .route(api_types::paths::ADMIN_FEEDBACK, get(cases::feedback_list))
        .route(api_types::paths::ADMIN_FEEDBACK_REPLY, post(cases::feedback_reply))
        .route(api_types::paths::ADMIN_DEVICE_FAULT_REPORTS, get(cases::fault_list))
        .route(api_types::paths::ADMIN_DEVICE_FAULT_HISTORY, get(cases::fault_history))
        .route(api_types::paths::ADMIN_DEVICE_FAULT_DISPATCH, post(cases::fault_dispatch))
        .route(api_types::paths::ADMIN_DEVICE_FAULT_RESOLVE, post(cases::fault_resolve))
        .route(api_types::paths::ADMIN_WHITELABEL, get(config::whitelabel_get).put(config::whitelabel_put))
        .route(api_types::paths::ADMIN_WEBHOOKS, get(webhook::list).post(webhook::create))
        .route(api_types::paths::ADMIN_WEBHOOK_DETAIL, get(webhook::get).put(webhook::update).delete(webhook::delete))
        .route(api_types::paths::ADMIN_WEBHOOK_DELIVERIES, get(webhook::deliveries))
        .route(api_types::paths::ADMIN_OTA_PACKAGES, get(device::packages_list).post(device::packages_create))
        .route(api_types::paths::ADMIN_OTA_PACKAGE_DETAIL, get(device::packages_get).delete(device::packages_delete))
        .route(api_types::paths::ADMIN_OTA_SCHEDULES, get(device::schedules_list).post(device::schedules_create))
        .route(api_types::paths::ADMIN_OTA_SCHEDULE_DETAIL, get(device::schedules_get).post(device::schedules_trigger))
        .route(api_types::paths::ADMIN_EXPORT, post(api::export::create))
        .route(api_types::paths::ADMIN_EXPORT_TASKS, get(api::export::tasks))
        .route(api_types::paths::ADMIN_EXPORT_TASK, get(api::export::task))
        .route(api_types::paths::ADMIN_EXPORT_DOWNLOAD, get(api::export::download))
        .layer(middleware::from_fn_with_state(jwt_codec.clone(), common_auth::refs::require_admin_jwt));

    Router::new()
        .merge(public_routes)
        .merge(internal_routes)
        .merge(admin_routes)
        .route(api_types::paths::HEALTH, get(api::health))
        // PC 后台静态资源(SPA)
        .fallback(static_serve::serve_spa)
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
        .layer(middleware::from_fn(common_http::request_id_layer))
        .with_state(state)
}
