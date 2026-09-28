//! admin Webhook 订阅 + 投递日志

use crate::AppState;
use axum::{extract::{Path, State}, Json};
use crate::auth::ActiveAdmin;
use common_error::{AppError, AppResult};
use common_redis::StreamEnvelope;
use serde::Deserialize;
use serde_json::{json, Value};

pub async fn list(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::WebhookSubscription>>>> {
    let rows = sqlx::query(
        "SELECT id, name, url, secret, event_types, enabled, created_at FROM webhook_subscription WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.webhook.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::WebhookSubscription> {
        Ok(api_contracts::admin::WebhookSubscription {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            name: sqlx::Row::try_get::<String, _>(r, "name")?,
            url: sqlx::Row::try_get::<String, _>(r, "url")?,
            // 完整密钥不外泄:只回前 8 个字符作为前缀
            secret_prefix: sqlx::Row::try_get::<String, _>(r, "secret")?.chars().take(8).collect::<String>(),
            // event_types 是 JSON 数组列,sqlx 的 try_get 不支持 Vec<String> 直线解码
            event_types: serde_json::from_value(
                sqlx::Row::try_get::<serde_json::Value, _>(r, "event_types")?,
            )
            .map_err(|e| AppError::Internal(format!("webhook event_types 解析失败: {e}")))?,
            enabled: sqlx::Row::try_get::<i8, _>(r, "enabled")? != 0,
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::common::ListResponse::new(items),
        common_error::current_request_id(),
    )))
}

#[derive(Debug, Deserialize)]
pub struct WebhookCreateReq {
    pub name: String,
    pub url: String,
    pub event_types: Vec<String>,
    pub headers_json: Option<Value>,
}

pub async fn create(State(st): State<AppState>, _c: ActiveAdmin, Json(req): Json<WebhookCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::WebhookCreated>>> {
    crate::auth::require_permission(&st,&_c,"webhook.create").await?;
    let secret = format!("whsec_{}", uuid::Uuid::new_v4().simple());
    let event_types = serde_json::to_value(&req.event_types)?;
    let result = sqlx::query(
        "INSERT INTO webhook_subscription (name, url, secret, event_types, headers_json, enabled) VALUES (?, ?, ?, ?, ?, 1)"
    )
    .bind(&req.name).bind(&req.url).bind(&secret).bind(event_types).bind(req.headers_json)
    .execute(st.webhook.pool()).await?;
    let id = result.last_insert_id();
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::admin::WebhookCreated { id, secret },
        common_error::current_request_id(),
    )))
}

pub async fn get(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::WebhookSubscriptionDetail>>> {
    let r: Option<(u64, String, String, String, serde_json::Value)> = sqlx::query_as(
        "SELECT id, name, url, secret, event_types FROM webhook_subscription WHERE id = ? AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.webhook.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("webhook".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::admin::WebhookSubscriptionDetail {
            id: r.0,
            name: r.1,
            url: r.2,
            // 完整密钥不外泄:只回前 8 个字符
            secret_prefix: r.3.chars().take(8).collect::<String>(),
            event_types: serde_json::from_value(r.4)
                .map_err(|e| AppError::Internal(format!("webhook event_types 解析失败: {e}")))?,
        },
        common_error::current_request_id(),
    )))
}

#[derive(Debug, Deserialize)]
pub struct WebhookUpdateReq {
    pub name: Option<String>,
    pub url: Option<String>,
    pub event_types: Option<Vec<String>>,
    pub enabled: Option<bool>,
}

pub async fn update(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>, Json(req): Json<WebhookUpdateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::UpdatedResponse>>> {
    crate::auth::require_permission(&st,&_c,"webhook.update").await?;
    let event_types = req.event_types.as_ref().map(serde_json::to_value).transpose()?;
    let n = sqlx::query(
        "UPDATE webhook_subscription
         SET name = COALESCE(?, name), url = COALESCE(?, url),
             event_types = COALESCE(?, event_types), enabled = COALESCE(?, enabled)
         WHERE id = ? AND deleted_at IS NULL"
    )
    .bind(req.name.as_deref()).bind(req.url.as_deref()).bind(event_types).bind(req.enabled).bind(id)
    .execute(st.webhook.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("webhook".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::UpdatedResponse::new(), common_error::current_request_id())))
}

pub async fn delete(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::DeletedResponse>>> {
    crate::auth::require_permission(&st,&_c,"webhook.delete").await?;
    let n = sqlx::query("UPDATE webhook_subscription SET deleted_at = NOW(3) WHERE id = ? AND deleted_at IS NULL")
        .bind(id).execute(st.webhook.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("webhook".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::DeletedResponse::new(), common_error::current_request_id())))
}

pub async fn deliveries(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::WebhookDelivery>>> >{
    let rows = sqlx::query(
        "SELECT id, event_type, response_status, attempt_count, duration_ms, delivered_at
         FROM webhook_delivery_log WHERE subscription_id = ? ORDER BY id DESC LIMIT 100"
    ).bind(id).fetch_all(st.webhook.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::WebhookDelivery> {
        Ok(api_contracts::admin::WebhookDelivery {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            event_type: sqlx::Row::try_get::<String, _>(r, "event_type")?,
            response_status: sqlx::Row::try_get::<Option<i32>, _>(r, "response_status")?,
            attempt_count: sqlx::Row::try_get::<u32, _>(r, "attempt_count")?,
            duration_ms: sqlx::Row::try_get::<Option<u32>, _>(r, "duration_ms")?,
            delivered_at: sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(r, "delivered_at")?
                .map(|t| t.to_rfc3339()),
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::common::ListResponse::new(items),
        common_error::current_request_id(),
    )))
}

/// **D11**:worker 投递 webhook 后的明细回写(内部服务间调用)。
///
/// `webhook_delivery_log` 在 admin_db,worker 无权跨库访问,故经本端点回写。
/// 鉴权由 `internal_token_mw` 中间件负责(`x-service-token`),与其它
/// `internal_routes` 一致 —— 本 handler **不取** `ActiveAdmin`。
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct DeliveryReportReq {
    pub subscription_id: u64,
    pub event_id: String,
    pub event_type: String,
    /// 实际发出的请求体(已不含 `secret` 与 `url`)。
    pub request_body: Value,
    /// `None` = 网络层失败(未拿到任何响应码,如超时/连接被拒)。
    #[serde(default)]
    pub response_status: Option<i32>,
    /// 订阅方响应体,已截断。
    #[serde(default)]
    pub response_body: Option<String>,
    #[serde(default)]
    pub error_msg: Option<String>,
    #[serde(default = "one")]
    pub attempt_count: u32,
    #[serde(default)]
    pub duration_ms: Option<u32>,
}

fn one() -> u32 { 1 }

pub async fn record_delivery(
    State(st): State<AppState>,
    Json(req): Json<DeliveryReportReq>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::AckFlag>>> {
    if req.subscription_id == 0 || req.event_id.is_empty() || req.event_id.len() > 64 {
        return Err(AppError::BadRequest("投递记录缺少订阅或事件标识".into()));
    }
    // 列宽对齐:migrations/admin_db/0001_init.sql:272-286
    //   event_type VARCHAR(64) / error_msg VARCHAR(255) / response_body TEXT
    let event_type: String = req.event_type.chars().take(64).collect();
    let error_msg: Option<String> = req.error_msg.map(|e| e.chars().take(255).collect());
    // 订阅方可能回巨大 HTML 错误页;整段写库会撑爆 TEXT 与 worker 内存。
    // 2 KiB 足够人工排障,超出部分丢弃。
    let response_body: Option<String> = req
        .response_body
        .map(|b| b.chars().take(2048).collect::<String>().into());
    sqlx::query(
        "INSERT INTO webhook_delivery_log
           (subscription_id, event_id, event_type, request_body, response_status,
            response_body, error_msg, attempt_count, duration_ms)
         VALUES (?,?,?,?,?,?,?,?,?)",
    )
    .bind(req.subscription_id)
    .bind(&req.event_id)
    .bind(event_type)
    .bind(&req.request_body)
    .bind(req.response_status)
    .bind(response_body)
    .bind(error_msg)
    .bind(req.attempt_count.max(1))
    .bind(req.duration_ms)
    .execute(st.webhook.pool())
    .await?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::common::AckFlag::new(true),
        common_error::current_request_id(),
    )))
}

#[allow(dead_code)]
fn _trigger_via_stream(env: StreamEnvelope) {
    let _ = env;
}
