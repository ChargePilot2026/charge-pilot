//! user 服务 — 核心 API 路由处理(扫码 / 充电 / 个人中心 / 公告 / 客服)
//!
//! 所有跨服务 HTTP 走 [`crate::clients::ServiceClient`],路径与跨服务 DTO
//! 来自 `api_contracts::*`;禁止在 handler 里拼 URL 或 `json!{}` 构造响应。

use crate::api_types::{ChargeStopRequest, ScanStartResponse, ScanCancelRequest};
use crate::AppState;
use api_contracts::{
    paths as p, BillingQuoteRequest, ChargeStopCommand, ScanPortRequest, ScanResolveRequest,
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
    pub refresh_token: String,
    pub jwt_expires_in: u64,
}

pub async fn login(
    State(st): State<AppState>,
    Json(req): Json<LoginReq>,
) -> AppResult<Json<crate::api_envelope::Envelope<LoginResp>>> {
    let wechat = st.cfg.wechat.as_ref()
        .ok_or_else(|| AppError::Config("wechat config missing for user service".into()))?;
    let sess = common_wechat::code2session(&st.http, wechat, &req.code).await?;
    let openid = sess.openid.clone();

    let mut tx = st.db.pool().begin().await?;
    let (user_id, is_new) = crate::login::resolve_user(&mut tx, &openid, sess.unionid.as_deref()).await?;
    tx.commit().await?;
    let sid=uuid::Uuid::new_v4().to_string();
    let token = st.jwt.issue_user_with_session(&openid, user_id, Some(sid.clone()),900)?;
    let refresh_token = crate::session::issue(&st.redis_cache, &crate::session::Identity {user_id,openid:openid.clone(),sid}).await?;

    Ok(Json(crate::api_envelope::Envelope::ok(LoginResp {
        token, user_id, openid, is_new_user: is_new, refresh_token, jwt_expires_in: 900,
    }, common_error::current_request_id())))
}

// ===================== 扫码 3 端点 (P0-1:扫码 ≠ 启动) =====================

pub async fn scan_resolve(
    State(st): State<AppState>,
    _claims: UserClaims,
    Json(req): Json<ScanResolveRequest>,
) -> AppResult<Json<crate::api_envelope::Envelope<api_contracts::ScanResolveResponse>>> {
    let cli = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone());
    let resp = cli
        .post(st.cfg.service_urls.gateway.as_deref(), p::GW_SCAN_RESOLVE, &req)
        .await?;
    Ok(Json(crate::api_envelope::Envelope::ok(resp, common_error::current_request_id())))
}

pub async fn scan_port(
    State(st): State<AppState>,
    _claims: UserClaims,
    Json(req): Json<ScanPortRequest>,
) -> AppResult<Json<crate::api_envelope::Envelope<api_contracts::ScanPortDetail>>> {
    let cli = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone());
    let resp = cli
        .post(st.cfg.service_urls.gateway.as_deref(), p::GW_SCAN_PORT, &req)
        .await?;
    Ok(Json(crate::api_envelope::Envelope::ok(resp, common_error::current_request_id())))
}

// ScanStartRequest 在 api_types.rs 已定义,handler 直接引用

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ScanQuoteRequest {pub port_id:String,pub estimated_kwh:String,pub estimated_minutes:i64}
pub async fn scan_quote(State(st):State<AppState>,claims:UserClaims,Json(req):Json<ScanQuoteRequest>)
    ->AppResult<Json<crate::api_envelope::Envelope<crate::quote_confirmation::Preview>>> {
    let client=common_http::internal::ApiClient::new(st.http.clone(),st.service_token.clone());
    let quote=client.post(st.cfg.service_urls.billing.as_deref(),p::BILLING_QUOTE,
        &BillingQuoteRequest{port_id:req.port_id.clone(),user_id:claims.user_id,estimated_kwh:req.estimated_kwh,estimated_minutes:req.estimated_minutes}).await?;
    let preview=crate::quote_confirmation::save(&st.redis_cache,claims.user_id,req.port_id,quote).await?;
    Ok(Json(crate::api_envelope::Envelope::ok(preview,common_error::current_request_id())))
}

pub async fn scan_start(
    State(st): State<AppState>,
    claims: UserClaims,
    Json(req): Json<crate::api_types::ScanStartRequest>,
) -> AppResult<Json<crate::api_envelope::Envelope<ScanStartResponse>>> {
    let user_id = claims.user_id;
    let openid = claims.sub;

    let cli = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone());
    let port: api_contracts::ScanPortDetail = cli.post(
        st.cfg.service_urls.gateway.as_deref(), p::GW_SCAN_PORT,
        &ScanPortRequest { port_id: req.port_id.clone() },
    ).await?;
    if port.status != "idle" { return Err(AppError::PortOccupied); }
    if req.coupon_grant_id.is_some() {
        return Err(AppError::BadRequest("优惠券抵扣尚未接入，请暂时不选择优惠券".into()));
    }
    let quote_id=crate::quote_confirmation::canonical_id(req.quote_id.as_deref().ok_or_else(||AppError::BadRequest("请先预估并确认费用".into()))?)?;
    let saved=crate::quote_confirmation::load(&st.redis_cache,&quote_id,user_id,&port.port_id).await?;
    if saved.quote.estimated_kwh!=req.estimated_kwh || saved.quote.estimated_minutes!=req.estimated_minutes {
        return Err(AppError::Conflict("预计电量或时长已变化，请重新预估费用".into()));
    }
    let used:bool=sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM charge_order_pricing WHERE quote_id=?)").bind(&quote_id).fetch_one(st.db.pool()).await?;
    if used {return Err(AppError::Conflict("报价已用于订单，请在订单列表继续处理".into()));}
    let wechat = st.cfg.wechat.as_ref()
        .ok_or_else(|| AppError::Config("wechat missing".into()))?;
    let quote: api_contracts::pricing::PriceQuote = cli.post(
        st.cfg.service_urls.billing.as_deref(), p::BILLING_QUOTE,
        &BillingQuoteRequest { port_id: port.port_id.clone(), user_id, estimated_minutes: req.estimated_minutes, estimated_kwh:req.estimated_kwh.clone() },
    ).await?;
    crate::quote_confirmation::verify(&saved,&quote)?;
    common_wechat::validate_pay_config(wechat)?;
    let total_cents = crate::checkout::validate_quote(&quote.amount)?;
    let order_no = IdGen::new("ORD").next();
    let pay_order_no = IdGen::new("PAY").next();
    let expires_at = chrono::Utc::now() + chrono::Duration::seconds(300);
    let lock = PortLock::new(st.redis_cache.clone());
    let holder = format!("{user_id}:{order_no}");
    if !lock.try_hold(&port.port_id, &holder, 300).await? {
        return Err(AppError::PortOccupied);
    }
    // Before commit, a failed write can safely release this reservation. After
    // commit (including an uncertain commit result), retain it until recovery/expiry.
    let prepared = async {
        let mut tx = st.db.pool().begin().await?;
        let id = crate::checkout::persist_pending(&mut tx, user_id, &order_no,
            &pay_order_no, &port, &saved.quote.amount, expires_at).await?;
        crate::quote_confirmation::persist(&mut tx,id,&quote_id,&saved).await?;
        Ok::<_, AppError>((tx, id))
    }.await;
    let (tx, charge_order_id) = match prepared {
        Ok(value) => value,
        Err(error) => {
            if let Err(release_error) = lock.release_if_match(&port.port_id, &holder).await {
                tracing::error!(%release_error, "failed to release unsuccessful checkout reservation");
            }
            return Err(error);
        }
    };
    tx.commit().await?;

    let jsapi_req = common_wechat::JsapiOrderReq {
        appid: wechat.appid.clone(),
        mchid: wechat.mch_id.clone(),
        description: format!("充电订单 {order_no}"),
        out_trade_no: pay_order_no.clone(),
        time_expire: expires_at.to_rfc3339(),
        attach: Some(serde_json::to_string(&serde_json::json!({"order_id": charge_order_id})).unwrap_or_default()),
        notify_url: wechat.notify_url.clone(),
        amount: common_wechat::JsapiAmount { total: total_cents, currency: "CNY".into() },
        payer: common_wechat::JsapiPayer { openid: openid.clone() },
    };
    let jsapi_resp = common_wechat::jsapi_create_order(&st.http, wechat, &jsapi_req).await?;
    let pay_sign = common_wechat::sign_jsapi_pay(wechat, &jsapi_resp.prepay_id)?;

    Ok(Json(crate::api_envelope::Envelope::ok(ScanStartResponse {
        order_no,
        payment_order_no: pay_order_no,
        hold_expires_at: expires_at.to_rfc3339(),
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
    let port_code = crate::checkout::cancel_pending(&mut tx, claims.user_id, &req.order_no).await?;
    tx.commit().await?;
    let lock = PortLock::new(st.redis_cache.clone());
    lock.release_if_match(&port_code, &format!("{}:{}", claims.user_id, req.order_no)).await?;
    Ok(Json(crate::api_envelope::Envelope::ok(serde_json::json!({"order_no": req.order_no, "cancelled": true}), common_error::current_request_id())))
}

// ===================== 充电 =====================
// ChargeStopRequest 已在 api_types.rs 定义

pub async fn charge_stop(
    State(st): State<AppState>,
    claims: UserClaims,
    Json(req): Json<ChargeStopRequest>,
) -> AppResult<Json<crate::api_envelope::Envelope<crate::api_types::ChargeStopResponse>>> {
    // Authenticate the order before sending any command to the gateway.
    let status: Option<String> = sqlx::query_scalar(
        "SELECT status FROM charge_order WHERE order_no=? AND user_id=? AND deleted_at IS NULL"
    ).bind(&req.order_no).bind(claims.user_id).fetch_optional(st.db.pool()).await?;
    let status=status.ok_or_else(||AppError::NotFound("order".into()))?;
    if status != "charging" && status != "completed" {return Err(AppError::Conflict("订单不在充电中，不能停止".into()));}
    let body = ChargeStopCommand {
        order_no: req.order_no.clone(),
        user_id: claims.user_id,
        source: "user_app".into(),
    };
    let cli = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone());
    let response: api_contracts::ChargeStopResponse = cli
        .post(
            st.cfg.service_urls.gateway.as_deref(),
            p::GW_CHARGE_ORDERS_STOP,
            &body,
        )
        .await?;
    Ok(Json(crate::api_envelope::Envelope::ok(
        response,
        common_error::current_request_id(),
    )))
}

pub async fn charge_ongoing(
    State(st): State<AppState>,
    claims: UserClaims,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    let r = sqlx::query(
        "SELECT id, order_no, device_id, port_no, status, started_at, total_cents
         FROM charge_order WHERE user_id = ? AND deleted_at IS NULL AND status IN ('pending_payment','paid','charging')
         ORDER BY id DESC LIMIT 1"
    )
    .bind(claims.user_id)
    .fetch_optional(st.db.pool())
    .await?;
    let v = match r {
        None => serde_json::Value::Null,
        Some(r) => json!({
            "order_id": r.try_get::<u64, _>("id")?,
            "order_no": r.try_get::<String, _>("order_no")?,
            "device_id": r.try_get::<String, _>("device_id")?,
            "port_no": r.try_get::<u8, _>("port_no")?,
            "status": r.try_get::<String, _>("status")?,
            "started_at": r.try_get::<Option<chrono::DateTime<chrono::Utc>>, _>("started_at")?.map(|t| t.to_rfc3339()),
            "total_cents": r.try_get::<Option<i64>, _>("total_cents")?,
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
    if q.order_id.is_empty() || q.order_id.len()>64 {return Err(AppError::BadRequest("订单标识无效".into()));}
    // The database owns authorization and lifecycle, even when Redis has a snapshot.
    let (filter, numeric_id) = match q.order_id.parse::<u64>() {Ok(id)=>("id",Some(id)),Err(_)=>("order_no",None)};
    let sql=format!("SELECT id,order_no,status,CAST(charged_kwh AS CHAR) AS charged_kwh,charged_seconds,total_cents FROM charge_order WHERE {filter}=? AND user_id=? AND deleted_at IS NULL");
    let mut query=sqlx::query(&sql);
    query=if let Some(id)=numeric_id {query.bind(id)} else {query.bind(&q.order_id)};
    let r = query
        .bind(claims.user_id)
        .fetch_optional(st.db.pool())
        .await?.ok_or_else(||AppError::NotFound("order".into()))?;
    let status:String=r.try_get("status")?;
    let order_no:String=r.try_get("order_no")?;
    let poll_continue = matches!(status.as_str(), "pending_payment" | "paid" | "charging");
    let mut resp = json!({
        "order_id": r.try_get::<u64,_>("id")?, "order_no":order_no,
        "status":status, "charge_state":status,
        "current_power_w": null, "power_w":null, "current_a":null,
        "charged_kwh": r.try_get::<Option<String>,_>("charged_kwh")?,
        "current_fee_cents":r.try_get::<Option<i64>,_>("total_cents")?,
        "temperature_c": null, "voltage_v": null, "battery_soc":null,
        "elapsed_seconds": r.try_get::<Option<u32>,_>("charged_seconds")?,
        "telemetry_available":false,
        "poll_continue": poll_continue,
        "next_poll_after_ms": if poll_continue {5000} else {0},
        "server_ts":chrono::Utc::now().to_rfc3339(),
    });
    if status=="charging" {
        if let Ok(Some(cached))=st.redis_cache.get::<serde_json::Value>(&format!("snapshot:{order_no}")).await {
            // Whitelist measurements; cached state/identity/polling flags cannot override DB truth.
            for field in ["current_power_w","power_w","current_a","temperature_c","voltage_v","battery_soc"] {
                if let Some(value)=cached.get(field) {
                    let number=value.as_f64().or_else(||value.as_str().and_then(|s|s.parse::<f64>().ok()));
                    if number.is_some_and(f64::is_finite) {resp[field]=value.clone();resp["telemetry_available"]=json!(true);}
                }
            }
        }
    }
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
