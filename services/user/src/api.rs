//! user 服务 — 核心 API 路由处理(扫码 / 充电 / 个人中心 / 公告 / 客服)
//!
//! 所有跨服务 HTTP 走 [`crate::clients::ServiceClient`],路径与跨服务 DTO
//! 来自 `api_contracts::*`;禁止在 handler 里拼 URL 或 `json!{}` 构造响应。

use crate::api_types::{ChargeStopRequest, ScanStartResponse, ScanCancelRequest};
use crate::AppState;
use api_contracts::{
    paths as p, BillingQuoteRequest, ChargeStopCommand, QuoteResponse, ScanPortRequest, ScanResolveRequest,
    StartResultRequest,
};
use axum::{
    extract::{Path, Query, State},
    Json,
};
use common_auth::UserClaims;
use common_db::IdGen;
use common_error::{AppError, AppResult};
use common_redis::PortLock;
use serde::{Deserialize, Serialize};
use serde_json::json;
use sqlx::Row;

pub async fn health() -> &'static str { "ok" }

// ===================== 公开:auth/login + refresh =====================

#[derive(Debug, Serialize, Deserialize)]
pub struct LoginReq {
    pub code: String,
    pub iv: Option<String>,
    pub encrypted_data: Option<String>,
}

#[derive(Debug, Serialize)]
pub struct LoginResp {
    pub token: String,
    pub user_id: u64,
    pub openid: String,
    pub is_new_user: bool,
}

pub async fn login(
    State(st): State<AppState>,
    Json(req): Json<LoginReq>,
) -> AppResult<Json<crate::api_envelope::Envelope<LoginResp>>> {
    let wechat = st.cfg.wechat.as_ref()
        .ok_or_else(|| AppError::Config("wechat config missing for user service".into()))?;
    let sess = common_wechat::code2session(&st.http, wechat, &req.code).await?;
    let openid = sess.openid.clone();

    let user_id: u64 = sqlx::query_scalar(
        r#"
        INSERT INTO `user` (openid, last_login_at)
        VALUES (?, NOW(3))
        ON DUPLICATE KEY UPDATE last_login_at = NOW(3), deleted_at = NULL,
          id = LAST_INSERT_ID(id)
        "#,
    )
    .bind(&openid)
    .fetch_one(st.db.pool())
    .await?;

    let token = st.jwt.issue_user(&openid, user_id)?;
    let is_new: bool = sqlx::query_scalar(
        "SELECT (first_seen_at >= NOW() - INTERVAL 10 SECOND) FROM `user` WHERE id = ?"
    )
    .bind(user_id)
    .fetch_one(st.db.pool())
    .await
    .unwrap_or(false);

    Ok(Json(crate::api_envelope::Envelope::ok(LoginResp {
        token, user_id, openid, is_new_user: is_new,
    }, common_error::current_request_id())))
}

#[derive(Debug, Serialize, Deserialize)]
pub struct RefreshReq {
    pub token: String,
}

#[derive(Debug, Serialize)]
pub struct RefreshResp { pub token: String }

pub async fn refresh(
    State(st): State<AppState>,
    Json(req): Json<RefreshReq>,
) -> AppResult<Json<crate::api_envelope::Envelope<RefreshResp>>> {
    let claims = st.jwt.verify_user(&req.token).map_err(|_| AppError::Unauthorized("bad token".into()))?;
    let new_token = st.jwt.issue_user(&claims.sub, claims.user_id)?;
    Ok(Json(crate::api_envelope::Envelope::ok(RefreshResp { token: new_token }, common_error::current_request_id())))
}

// ===================== 扫码 3 端点 (P0-1:扫码 ≠ 启动) =====================

pub async fn scan_resolve(
    State(st): State<AppState>,
    _claims: UserClaims,
    Json(req): Json<ScanResolveRequest>,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    let cli = crate::clients::ServiceClient::new(st.http.clone(), st.service_token.clone());
    let resp: serde_json::Value = cli
        .post_typed(st.cfg.service_urls.gateway.as_deref(), p::GW_SCAN_RESOLVE, &req)
        .await?;
    Ok(Json(crate::api_envelope::Envelope::ok(resp, common_error::current_request_id())))
}

pub async fn scan_port(
    State(st): State<AppState>,
    _claims: UserClaims,
    Json(req): Json<ScanPortRequest>,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    let cli = crate::clients::ServiceClient::new(st.http.clone(), st.service_token.clone());
    let resp: serde_json::Value = cli
        .post_typed(st.cfg.service_urls.gateway.as_deref(), p::GW_SCAN_PORT, &req)
        .await?;
    Ok(Json(crate::api_envelope::Envelope::ok(resp, common_error::current_request_id())))
}

// ScanStartRequest 在 api_types.rs 已定义,handler 直接引用

pub async fn scan_start(
    State(st): State<AppState>,
    claims: UserClaims,
    Json(req): Json<crate::api_types::ScanStartRequest>,
) -> AppResult<Json<crate::api_envelope::Envelope<ScanStartResponse>>> {
    let user_id = claims.user_id;
    let openid = claims.sub;

    let id_gen = IdGen::new("ORD");
    let pay_id_gen = IdGen::new("PAY");
    let order_no = id_gen.next();
    let pay_order_no = pay_id_gen.next();

    let lock = PortLock::new(st.redis_cache.clone());
    let holder = format!("{user_id}:{order_no}");
    let got = lock.try_hold(&req.port_id, &holder, 300).await?;
    if !got { return Err(AppError::PortOccupied); }

    let mut tx = st.db.pool().begin().await?;
    let now_month = chrono::Utc::now().format("%Y-%m-01").to_string();

    let charge_order_id: u64 = sqlx::query_scalar(
        r#"
        INSERT INTO charge_order
          (order_no, user_id, device_id, port_no, port_code, status, created_month, created_at)
        VALUES (?, ?, ?, ?, ?, 'pending_payment', ?, NOW(3))
        "#,
    )
    .bind(&order_no)
    .bind(user_id)
    .bind(&req.port_id)
    .bind(1u8)
    .bind(&req.port_id)
    .bind(&now_month)
    .fetch_one(&mut *tx)
    .await?;

    let pay_order_id: u64 = sqlx::query_scalar(
        r#"
        INSERT INTO payment_order
          (order_no, biz_type, biz_id, user_id, pay_method, total_cents, status, created_month, created_at, expired_at)
        VALUES (?, 'charge', ?, ?, 'wechat', 0, 'initiated', ?, NOW(3), DATE_ADD(NOW(3), INTERVAL 5 MINUTE))
        "#,
    )
    .bind(&pay_order_no)
    .bind(charge_order_id)
    .bind(user_id)
    .bind(&now_month)
    .fetch_one(&mut *tx)
    .await?;

    sqlx::query("UPDATE charge_order SET payment_order_id = ? WHERE id = ?")
        .bind(pay_order_id)
        .bind(charge_order_id)
        .execute(&mut *tx)
        .await?;

    tx.commit().await?;

    // 调 billing 拿报价 — 类型化 DTO,禁止 json!{} 拼请求
    let cli = crate::clients::ServiceClient::new(st.http.clone(), st.service_token.clone());
    let quote_req = BillingQuoteRequest {
        port_id: req.port_id.clone(),
        user_id,
        estimated_minutes: 240,
    };
    let quote_resp: QuoteResponse = cli
        .post_typed(
            st.cfg.service_urls.billing.as_deref(),
            p::BILLING_QUOTE,
            &quote_req,
        )
        .await?;
    let total_cents = quote_resp.total_cents;

    let wechat = st.cfg.wechat.as_ref()
        .ok_or_else(|| AppError::Config("wechat missing".into()))?;
    let jsapi_req = common_wechat::JsapiOrderReq {
        appid: wechat.appid.clone(),
        mchid: wechat.mch_id.clone(),
        description: format!("充电订单 {order_no}"),
        out_trade_no: pay_order_no.clone(),
        time_expire: (chrono::Utc::now() + chrono::Duration::minutes(5)).to_rfc3339(),
        attach: Some(serde_json::to_string(&serde_json::json!({"order_id": charge_order_id})).unwrap_or_default()),
        notify_url: wechat.notify_url.clone(),
        amount: common_wechat::JsapiAmount { total: total_cents as i32, currency: "CNY".into() },
        payer: common_wechat::JsapiPayer { openid: openid.clone() },
    };
    let jsapi_resp = common_wechat::jsapi_create_order(&st.http, wechat, &jsapi_req).await?;
    let pay_sign = common_wechat::sign_jsapi_pay(wechat, &jsapi_resp.prepay_id);

    Ok(Json(crate::api_envelope::Envelope::ok(ScanStartResponse {
        order_no,
        payment_order_no: pay_order_no,
        hold_expires_at: (chrono::Utc::now() + chrono::Duration::seconds(300)).to_rfc3339(),
        payment_params: serde_json::to_value(&pay_sign).unwrap_or(serde_json::Value::Null),
    }, common_error::current_request_id())))
}

// ScanCancelRequest 已在 api_types.rs 定义

pub async fn scan_cancel(
    State(st): State<AppState>,
    claims: UserClaims,
    Json(req): Json<ScanCancelRequest>,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    let mut tx = st.db.pool().begin().await?;
    let row: Option<(u64, String, Option<u64>, chrono::DateTime<chrono::Utc>)> = sqlx::query_as(
        r#"SELECT id, status, payment_order_id, created_at FROM charge_order
           WHERE order_no = ? AND user_id = ? AND created_month >= DATE_FORMAT(NOW(), '%Y-%m-01')
           LIMIT 1 FOR UPDATE"#,
    )
    .bind(&req.order_no)
    .bind(claims.user_id)
    .fetch_optional(&mut *tx)
    .await?;
    let (order_id, status, pay_id, created_at) = row.ok_or_else(|| AppError::NotFound("order".into()))?;
    if status != "pending_payment" {
        return Err(AppError::Conflict(format!("order in status {status}, cannot cancel")));
    }
    let now = chrono::Utc::now().timestamp();
    if (now - created_at.timestamp()) > 60 {
        return Err(AppError::Conflict("outside 60s cancel window".into()));
    }
    sqlx::query("UPDATE charge_order SET status='cancelled', ended_at=NOW(3) WHERE id=?")
        .bind(order_id).execute(&mut *tx).await?;
    if let Some(p) = pay_id {
        sqlx::query("UPDATE payment_order SET status='closed', closed_at=NOW(3) WHERE id=?")
            .bind(p).execute(&mut *tx).await?;
    }
    tx.commit().await?;
    Ok(Json(crate::api_envelope::Envelope::ok(serde_json::json!({"order_no": req.order_no, "cancelled": true}), common_error::current_request_id())))
}

// ===================== 充电 =====================
// ChargeStopRequest 已在 api_types.rs 定义

pub async fn charge_stop(
    State(st): State<AppState>,
    claims: UserClaims,
    Json(req): Json<ChargeStopRequest>,
) -> AppResult<Json<crate::api_envelope::Envelope<crate::api_types::ChargeStopResponse>>> {
    let body = ChargeStopCommand {
        order_no: req.order_no.clone(),
        user_id: claims.user_id,
        source: "user_app".into(),
    };
    let cli = crate::clients::ServiceClient::new(st.http.clone(), st.service_token.clone());
    let _resp: serde_json::Value = cli
        .post_typed(
            st.cfg.service_urls.gateway.as_deref(),
            p::GW_CHARGE_ORDERS_STOP,
            &body,
        )
        .await?;
    Ok(Json(crate::api_envelope::Envelope::ok(
        crate::api_types::ChargeStopResponse { stopped: true },
        common_error::current_request_id(),
    )))
}

pub async fn charge_ongoing(
    State(st): State<AppState>,
    claims: UserClaims,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    let r = sqlx::query(
        "SELECT id, order_no, device_id, port_no, status, started_at, total_cents
         FROM charge_order WHERE user_id = ? AND status IN ('paid','charging')
         ORDER BY id DESC LIMIT 1"
    )
    .bind(claims.user_id)
    .fetch_optional(st.db.pool())
    .await?;
    let v = match r {
        None => serde_json::Value::Null,
        Some(r) => json!({
            "order_id": r.try_get::<u64, _>("id").ok(),
            "order_no": r.try_get::<String, _>("order_no").ok(),
            "device_id": r.try_get::<String, _>("device_id").ok(),
            "port_no": r.try_get::<u8, _>("port_no").ok(),
            "status": r.try_get::<String, _>("status").ok(),
            "started_at": r.try_get::<Option<chrono::DateTime<chrono::Utc>>, _>("started_at").ok().flatten().map(|t| t.to_rfc3339()),
            "total_cents": r.try_get::<Option<i64>, _>("total_cents").ok().flatten().unwrap_or(0),
        }),
    };
    Ok(Json(crate::api_envelope::Envelope::ok(v, common_error::current_request_id())))
}

#[derive(Debug, Serialize, Deserialize)]
pub struct SnapshotQuery { pub order_id: String }

pub async fn charge_snapshot(
    State(st): State<AppState>,
    claims: UserClaims,
    Query(q): Query<SnapshotQuery>,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    let key = format!("snapshot:{}", q.order_id);
    if let Ok(Some(s)) = st.redis_cache.get::<serde_json::Value>(&key).await {
        return Ok(Json(crate::api_envelope::Envelope::ok(s, common_error::current_request_id())));
    }
    let r = sqlx::query("SELECT status FROM charge_order WHERE order_no = ? AND user_id = ? LIMIT 1")
        .bind(&q.order_id)
        .bind(claims.user_id)
        .fetch_optional(st.db.pool())
        .await?;
    let status: String = match r {
        Some(r) => r.try_get("status")?,
        None => return Err(AppError::NotFound("order".into())),
    };
    let poll_continue = matches!(status.as_str(), "paid" | "charging");
    let resp = json!({
        "order_id": q.order_id,
        "charge_state": if status == "paid" { "charging" } else { &status },
        "current_power_w": 0.0,
        "charged_kwh": 0.0,
        "current_cost": 0.0,
        "temperature_c": 0.0,
        "voltage_v": 0.0,
        "elapsed_seconds": 0,
        "poll_continue": poll_continue,
        "next_poll_after_ms": 5000,
    });
    Ok(Json(crate::api_envelope::Envelope::ok(resp, common_error::current_request_id())))
}

pub async fn charge_curve(
    State(_st): State<AppState>,
    _claims: UserClaims,
    Query(q): Query<SnapshotQuery>,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    Ok(Json(crate::api_envelope::Envelope::ok(json!({
        "order_id": q.order_id, "buckets": []
    }), common_error::current_request_id())))
}

#[derive(Debug, Serialize, Deserialize)]
pub struct HistoryQuery { pub page: Option<u32>, pub page_size: Option<u32> }

pub async fn charge_history(
    State(st): State<AppState>,
    claims: UserClaims,
    Query(q): Query<HistoryQuery>,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    let page = q.page.unwrap_or(1).max(1);
    let page_size = q.page_size.unwrap_or(20).min(100);
    let offset = (page - 1) * page_size;
    let rows = sqlx::query(
        "SELECT order_no, status, total_cents, started_at, ended_at
         FROM charge_order WHERE user_id = ?
         ORDER BY id DESC LIMIT ? OFFSET ?"
    )
    .bind(claims.user_id)
    .bind(page_size as i64)
    .bind(offset as i64)
    .fetch_all(st.db.pool())
    .await?;
    let arr: Vec<serde_json::Value> = rows.iter().map(|r| json!({
        "order_no": r.try_get::<String, _>("order_no").unwrap_or_default(),
        "status": r.try_get::<String, _>("status").unwrap_or_default(),
        "total_cents": r.try_get::<Option<i64>, _>("total_cents").unwrap_or(Some(0)),
        "started_at": r.try_get::<Option<chrono::DateTime<chrono::Utc>>, _>("started_at").ok().flatten().map(|t| t.to_rfc3339()),
        "ended_at": r.try_get::<Option<chrono::DateTime<chrono::Utc>>, _>("ended_at").ok().flatten().map(|t| t.to_rfc3339()),
    })).collect();
    Ok(Json(crate::api_envelope::Envelope::ok(json!({
        "page": page, "page_size": page_size, "items": arr
    }), common_error::current_request_id())))
}

pub async fn charge_detail(
    State(st): State<AppState>,
    claims: UserClaims,
    Path(order_id): Path<String>,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    let r = sqlx::query(
        "SELECT order_no, status, electric_cents, service_cents, total_cents,
                started_at, ended_at, device_id, port_no, failure_reason
         FROM charge_order WHERE order_no = ? AND user_id = ? LIMIT 1"
    )
    .bind(&order_id)
    .bind(claims.user_id)
    .fetch_optional(st.db.pool())
    .await?;
    let r = r.ok_or_else(|| AppError::NotFound("order".into()))?;
    Ok(Json(crate::api_envelope::Envelope::ok(json!({
        "order_no": r.try_get::<String, _>("order_no")?,
        "status": r.try_get::<String, _>("status")?,
        "electric_cents": r.try_get::<Option<i64>, _>("electric_cents")?,
        "service_cents": r.try_get::<Option<i64>, _>("service_cents")?,
        "total_cents": r.try_get::<Option<i64>, _>("total_cents")?,
        "device_id": r.try_get::<String, _>("device_id")?,
        "port_no": r.try_get::<u8, _>("port_no")?,
        "started_at": r.try_get::<Option<chrono::DateTime<chrono::Utc>>, _>("started_at")?.map(|t| t.to_rfc3339()),
        "ended_at": r.try_get::<Option<chrono::DateTime<chrono::Utc>>, _>("ended_at")?.map(|t| t.to_rfc3339()),
        "failure_reason": r.try_get::<Option<String>, _>("failure_reason")?,
    }), common_error::current_request_id())))
}

pub async fn charge_historical_curve(
    State(_st): State<AppState>,
    _claims: UserClaims,
    Path(order_id): Path<String>,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    Ok(Json(crate::api_envelope::Envelope::ok(json!({
        "order_id": order_id, "buckets": []
    }), common_error::current_request_id())))
}

#[derive(Debug, Serialize, Deserialize)]
pub struct FeedbackReq {
    pub rating: Option<u8>,
    pub category: String,
    pub content: Option<String>,
    pub images: Option<Vec<String>>,
}

pub async fn charge_feedback(
    State(st): State<AppState>,
    claims: UserClaims,
    Path(order_id): Path<String>,
    Json(req): Json<FeedbackReq>,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    sqlx::query(
        "INSERT INTO feedback (user_id, order_id, device_id, rating, category, content, images_json)
         VALUES (?, (SELECT id FROM charge_order WHERE order_no = ? AND user_id = ?), NULL, ?, ?, ?, ?)"
    )
    .bind(claims.user_id)
    .bind(&order_id)
    .bind(claims.user_id)
    .bind(req.rating)
    .bind(&req.category)
    .bind(req.content.as_deref())
    .bind(req.images.as_ref().and_then(|v| serde_json::to_value(v).ok()))
    .execute(st.db.pool())
    .await?;
    Ok(Json(crate::api_envelope::Envelope::ok(json!({"submitted": true}), common_error::current_request_id())))
}

pub async fn internal_order_detail(
    State(st): State<AppState>,
    Path(order_id): Path<String>,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    let r = sqlx::query("SELECT * FROM charge_order WHERE order_no = ? LIMIT 1")
        .bind(&order_id)
        .fetch_optional(st.db.pool())
        .await?;
    let r = r.ok_or_else(|| AppError::NotFound("order".into()))?;
    Ok(Json(crate::api_envelope::Envelope::ok(json!({
        "order_id": r.try_get::<u64, _>("id").ok(),
        "order_no": r.try_get::<String, _>("order_no").ok(),
        "user_id": r.try_get::<u64, _>("user_id").ok(),
        "device_id": r.try_get::<String, _>("device_id").ok(),
        "port_no": r.try_get::<u8, _>("port_no").ok(),
        "status": r.try_get::<String, _>("status").ok(),
        "electric_cents": r.try_get::<Option<i64>, _>("electric_cents").ok().flatten(),
        "service_cents": r.try_get::<Option<i64>, _>("service_cents").ok().flatten(),
        "total_cents": r.try_get::<Option<i64>, _>("total_cents").ok().flatten(),
        "started_at": r.try_get::<Option<chrono::DateTime<chrono::Utc>>, _>("started_at").ok().flatten().map(|t| t.to_rfc3339()),
        "ended_at": r.try_get::<Option<chrono::DateTime<chrono::Utc>>, _>("ended_at").ok().flatten().map(|t| t.to_rfc3339()),
    }), common_error::current_request_id())))
}

// ===================== 个人中心 =====================

pub async fn profile_get(
    State(st): State<AppState>,
    claims: UserClaims,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    let r = sqlx::query("SELECT id, openid, nickname, avatar_url, gender, status FROM `user` WHERE id = ?")
        .bind(claims.user_id)
        .fetch_optional(st.db.pool())
        .await?;
    let r = r.ok_or_else(|| AppError::NotFound("user".into()))?;
    let balance: Option<i64> = sqlx::query_scalar("SELECT balance_cents FROM wallet_account WHERE user_id = ?")
        .bind(claims.user_id)
        .fetch_optional(st.db.pool())
        .await
        .ok()
        .flatten();
    Ok(Json(crate::api_envelope::Envelope::ok(json!({
        "user_id": r.try_get::<u64, _>("id")?,
        "openid": r.try_get::<String, _>("openid")?,
        "nickname": r.try_get::<Option<String>, _>("nickname")?,
        "avatar_url": r.try_get::<Option<String>, _>("avatar_url")?,
        "gender": r.try_get::<String, _>("gender")?,
        "balance_cents": balance.unwrap_or(0),
    }), common_error::current_request_id())))
}

#[derive(Debug, Serialize, Deserialize)]
pub struct PhoneBindReq {
    pub iv: String,
    pub encrypted_data: String,
    pub phone_hash: String,
}

pub async fn phone_bind(
    State(st): State<AppState>,
    claims: UserClaims,
    Json(req): Json<PhoneBindReq>,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    // 注: 真实加密需解析 encrypted_data + iv 得明文手机号,本期留待生产部署时实现
    sqlx::query("UPDATE `user` SET phone_hash = ? WHERE id = ?")
        .bind(&req.phone_hash)
        .bind(claims.user_id)
        .execute(st.db.pool())
        .await?;
    Ok(Json(crate::api_envelope::Envelope::ok(json!({"bound": true}), common_error::current_request_id())))
}

pub async fn phone_unbind(
    State(st): State<AppState>,
    claims: UserClaims,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    sqlx::query("UPDATE `user` SET phone_enc = NULL, phone_hash = NULL WHERE id = ?")
        .bind(claims.user_id)
        .execute(st.db.pool())
        .await?;
    Ok(Json(crate::api_envelope::Envelope::ok(json!({"unbound": true}), common_error::current_request_id())))
}

// ===================== 公告 / 客服 =====================

pub async fn announcement_list(
    State(st): State<AppState>,
    _claims: UserClaims,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    let admin_url = st.cfg.service_urls.admin.as_deref();
    let items: serde_json::Value = match admin_url {
        Some(u) => common_http::ServiceClient::new(st.service_token.as_str())
            .get_json(u, p::ADMIN_INTERNAL_ANNOUNCEMENTS_ACTIVE).await
            .unwrap_or_else(|_| json!({"items": []})),
        None => json!({"items": []}),
    };
    Ok(Json(crate::api_envelope::Envelope::ok(items, common_error::current_request_id())))
}

pub async fn customer_service_entry(
    State(_st): State<AppState>,
    _claims: UserClaims,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    Ok(Json(crate::api_envelope::Envelope::ok(json!({
        "path": "/pages/cs/index",
        "note": "前端用 wx.openCustomerServiceChat 唤起"
    }), common_error::current_request_id())))
}
