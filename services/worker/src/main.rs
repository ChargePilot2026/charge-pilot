//! worker 服务 — 后台任务 + Stream 消费者
//!
//! 端口: 8085(仅运维 API,可选)
//! 当前循环: announcement_expire / device_session_clean / snapshot_warmer
//! 当前 Stream 组: webhook_retry / ota_schedule(失败进入 DLQ) / comp_tx(结果审计)
//! scheduled_task cron/manual runner 与其他计划任务尚未接入

mod scheduler;
mod tasks;
mod streams;

use axum::{routing::get, Router};
use common_auth::JwtCodec;
use common_config::AppConfig;
use common_db::Db;
use common_error::AppResult;
use common_redis::{RedisCache, RedisStream};
use common_telemetry as telemetry;
use std::net::SocketAddr;
use std::sync::Arc;
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
    info!(service = "worker", "starting");

    let db = Db::connect(&cfg.mysql).await?;
    db.ensure_schema_metadata().await?;
    let redis_cache = RedisCache::connect(&cfg.redis_cache).await?;
    let redis_stream = RedisStream::connect(&cfg.redis_stream).await?;
    let jwt = Arc::new(JwtCodec::new(&cfg.auth));
    let http = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(15))
        .build().expect("reqwest");

    let state = AppState {
        cfg: cfg.clone(), db: db.clone(),
        redis_cache: redis_cache.clone(), redis_stream: redis_stream.clone(),
        jwt: jwt.clone(), http: http.clone(),
        service_token: Arc::new(cfg.auth.service_token.clone()),
    };

    // 后台任务
    scheduler::start_all(state.clone()).await?;
    // Stream 消费者
    streams::spawn_all(state.clone()).await?;

    // 健康端口(可选)
    let app = Router::new().route("/health", get(health)).with_state(state.clone());
    let addr: SocketAddr = cfg.http_bind.parse().unwrap_or_else(|_| "0.0.0.0:8085".parse().unwrap());
    info!(%addr, "worker listening");
    let listener = tokio::net::TcpListener::bind(addr).await?;
    axum::serve(listener, app.into_make_service()).await?;
    Ok(())
}

async fn health(axum::extract::State(state): axum::extract::State<AppState>) -> AppResult<&'static str> {
    state.db.ping().await?;
    state.redis_cache.ping().await?;
    state.redis_stream.ping().await?;
    Ok("ok")
}
