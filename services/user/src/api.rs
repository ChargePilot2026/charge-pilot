//! user 服务 — 核心 API 路由处理(扫码 / 充电 / 个人中心 / 公告 / 客服)
//!
//! 所有跨服务 HTTP 走 [`crate::clients::ServiceClient`],路径与跨服务 DTO
//! 来自 `api_contracts::*`;禁止在 handler 里拼 URL 或 `json!{}` 构造响应。

use crate::api_types::{ChargeStopRequest, ScanStartResponse, ScanCancelRequest};
use crate::AppState;
use api_contracts::{
    paths as p, BillingQuoteRequest, ChargeStopCommand, ScanPortRequest, ScanResolveRequest,
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
use sha2::Digest;
use sqlx::Row;

pub async fn health(State(st): State<AppState>) -> AppResult<&'static str> {
    st.db.ping().await?;
    st.redis_cache.ping().await?;
    st.redis_stream.ping().await?;
    Ok("ok")
}

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
    let expires_at = chrono::DateTime::from_timestamp_millis(chrono::Utc::now().timestamp_millis()+300_000).expect("current timestamp");
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
    let jsapi_req = common_wechat::JsapiOrderReq {
        appid: wechat.appid.clone(),
        mchid: wechat.mch_id.clone(),
        description: format!("充电订单 {order_no}"),
        out_trade_no: pay_order_no.clone(),
        time_expire: expires_at.to_rfc3339(),
        attach: Some(serde_json::to_string(&serde_json::json!({"order_id": id})).unwrap_or_default()),
        notify_url: wechat.notify_url.clone(),
        amount: common_wechat::JsapiAmount { total: total_cents, currency: "CNY".into() },
        payer: common_wechat::JsapiPayer { openid: openid.clone() },
    };
        sqlx::query("INSERT INTO charge_prepay (charge_order_id,request_json) VALUES (?,?)").bind(id).bind(serde_json::to_value(&jsapi_req)?).execute(&mut *tx).await?;
        Ok::<_, AppError>((tx, id))
    }.await;
    let (tx, _charge_order_id) = match prepared {
        Ok(value) => value,
        Err(error) => {
            if let Err(release_error) = lock.release_if_match(&port.port_id, &holder).await {
                tracing::error!(%release_error, backtrace = %common_error::backtrace(), "failed to release unsuccessful checkout reservation");
            }
            return Err(error);
        }
    };
    tx.commit().await?;

    let response=crate::prepay::prepare(&st,user_id,&openid,&order_no).await?;
    Ok(Json(crate::api_envelope::Envelope::ok(response,common_error::current_request_id())))
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

#[derive(Debug, Serialize, Deserialize)]
pub struct CurveQuery { pub order_id: String, pub window: Option<String> }

#[derive(Debug, Serialize)]
struct DeviceSnapshotQuery { order_id: String, port_no: u8 }

#[derive(Debug, Serialize)]
struct DeviceCurveQuery { order_id: String, port_no: u8, window: String, started_at: Option<String> }

#[derive(Debug, Serialize, Deserialize)]
pub struct HistoricalCurveQuery { pub granularity: Option<String> }

#[derive(Debug, Serialize)]
struct DeviceHistoricalCurveQuery {
    order_id: String,
    port_no: u8,
    granularity: String,
    started_at: String,
    ended_at: String,
}

pub async fn charge_snapshot(
    State(st): State<AppState>,
    claims: UserClaims,
    Query(q): Query<SnapshotQuery>,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    if q.order_id.is_empty() || q.order_id.len()>64 {return Err(AppError::BadRequest("订单标识无效".into()));}
    // The database owns authorization and lifecycle, even when Redis has a snapshot.
    let (filter, numeric_id) = match q.order_id.parse::<u64>() {Ok(id)=>("id",Some(id)),Err(_)=>("order_no",None)};
    let sql=format!("SELECT id,order_no,device_id,port_no,status,CAST(charged_kwh AS CHAR) AS charged_kwh,charged_seconds,total_cents FROM charge_order WHERE {filter}=? AND user_id=? AND deleted_at IS NULL");
    let mut query=sqlx::query(&sql);
    query=if let Some(id)=numeric_id {query.bind(id)} else {query.bind(&q.order_id)};
    let r = query
        .bind(claims.user_id)
        .fetch_optional(st.db.pool())
        .await?.ok_or_else(||AppError::NotFound("order".into()))?;
    let status:String=r.try_get("status")?;
    let order_no:String=r.try_get("order_no")?;
    let device_id: String = r.try_get("device_id")?;
    let port_no: u8 = r.try_get("port_no")?;
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
        let path = p::GW_DEVICE_SNAPSHOT.replace(":id", &device_id);
        let query = DeviceSnapshotQuery { order_id: order_no.clone(), port_no };
        let snapshot: serde_json::Value = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone())
            .get(st.cfg.service_urls.gateway.as_deref(), &path, &query).await?;
        if let Some(data) = snapshot.get("snapshot") {
            for (source, target) in [("power_w", "power_w"), ("power_w", "current_power_w"),
                ("current_a", "current_a"), ("voltage_v", "voltage_v"),
                ("temperature_c", "temperature_c"), ("battery_soc", "battery_soc")] {
                if let Some(value) = data.get(source) {
                    let number = value.as_f64().or_else(|| value.as_str().and_then(|text| text.parse::<f64>().ok()));
                    if number.is_some_and(f64::is_finite) {
                        resp[target] = value.clone();
                        resp["telemetry_available"] = json!(true);
                    }
                }
            }
            if let Some(value) = data.get("ts") { resp["telemetry_ts"] = value.clone(); }
        }
        if let Err(error) = common_redis::write_snapshot(&st.redis_cache, &order_no, &resp).await {
            tracing::warn!(order_no, error = %error, "charge snapshot cache write failed");
        }
    }
    Ok(Json(crate::api_envelope::Envelope::ok(resp, common_error::current_request_id())))
}

pub async fn charge_curve(
    State(st): State<AppState>,
    claims: UserClaims,
    Query(q): Query<CurveQuery>,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    if q.order_id.is_empty() || q.order_id.len() > 64 { return Err(AppError::BadRequest("订单标识无效".into())); }
    let window = q.window.as_deref().unwrap_or("last_30min");
    if !["last_5min", "last_30min", "last_2h", "since_start"].contains(&window) {
        return Err(AppError::BadRequest("window 无效".into()));
    }
    let (filter, numeric_id) = match q.order_id.parse::<u64>() { Ok(id) => ("id", Some(id)), Err(_) => ("order_no", None) };
    let sql = format!("SELECT id,order_no,device_id,port_no,status,started_at,CAST(charged_kwh AS CHAR) AS charged_kwh FROM charge_order WHERE {filter}=? AND user_id=? AND deleted_at IS NULL");
    let mut query = sqlx::query(&sql);
    query = if let Some(id) = numeric_id { query.bind(id) } else { query.bind(&q.order_id) };
    let row = query.bind(claims.user_id).fetch_optional(st.db.pool()).await?
        .ok_or_else(|| AppError::NotFound("order".into()))?;
    let status: String = row.try_get("status")?;
    if window == "since_start" && status != "completed" {
        return Err(AppError::Conflict("since_start 仅支持已完成订单".into()));
    }
    if window != "since_start" && status != "charging" {
        return Err(AppError::Conflict("当前订单没有实时充电曲线".into()));
    }
    let order_no: String = row.try_get("order_no")?;
    let device_id: String = row.try_get("device_id")?;
    let port_no: u8 = row.try_get("port_no")?;
    let started_at: Option<chrono::DateTime<chrono::Utc>> = row.try_get("started_at")?;
    let path = p::GW_DEVICE_CURVE.replace(":id", &device_id);
    let device_query = DeviceCurveQuery {
        order_id: order_no,
        port_no,
        window: window.to_string(),
        started_at: started_at.map(|time| time.to_rfc3339()),
    };
    let mut data: serde_json::Value = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone())
        .get(st.cfg.service_urls.gateway.as_deref(), &path, &device_query).await?;
    if let Some(summary) = data.get_mut("summary") {
        if let Some(total_kwh) = row.try_get::<Option<String>, _>("charged_kwh")? {
            summary["total_kwh"] = json!(total_kwh);
        }
    }
    Ok(Json(crate::api_envelope::Envelope::ok(data, common_error::current_request_id())))
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
    let arr: Vec<serde_json::Value> = rows.iter().map(|r| -> AppResult<serde_json::Value> { Ok(json!({
        "order_no": r.try_get::<String, _>("order_no")?,
        "status": r.try_get::<String, _>("status")?,
        "total_cents": r.try_get::<Option<i64>, _>("total_cents")?,
        "started_at": r.try_get::<Option<chrono::DateTime<chrono::Utc>>, _>("started_at")?.map(|t| t.to_rfc3339()),
        "ended_at": r.try_get::<Option<chrono::DateTime<chrono::Utc>>, _>("ended_at")?.map(|t| t.to_rfc3339()),
    })) }).collect::<AppResult<Vec<_>>>()?;
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
    State(st): State<AppState>,
    claims: UserClaims,
    Path(order_id): Path<String>,
    Query(q): Query<HistoricalCurveQuery>,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    if order_id.is_empty() || order_id.len() > 64 { return Err(AppError::BadRequest("订单标识无效".into())); }
    let granularity = q.granularity.as_deref().unwrap_or("15min");
    if !["15min", "hourly"].contains(&granularity) { return Err(AppError::BadRequest("granularity 无效".into())); }
    let (filter, numeric_id) = match order_id.parse::<u64>() { Ok(id) => ("id", Some(id)), Err(_) => ("order_no", None) };
    let sql = format!("SELECT id,order_no,device_id,port_no,status,started_at,ended_at,CAST(charged_kwh AS CHAR) AS charged_kwh FROM charge_order WHERE {filter}=? AND user_id=? AND deleted_at IS NULL");
    let mut query = sqlx::query(&sql);
    query = if let Some(id) = numeric_id { query.bind(id) } else { query.bind(&order_id) };
    let row = query.bind(claims.user_id).fetch_optional(st.db.pool()).await?
        .ok_or_else(|| AppError::NotFound("order".into()))?;
    let status: String = row.try_get("status")?;
    if status != "completed" { return Err(AppError::Conflict("只有已完成订单提供历史曲线".into())); }
    let device_id: String = row.try_get("device_id")?;
    let port_no: u8 = row.try_get("port_no")?;
    let started_at: chrono::DateTime<chrono::Utc> = row.try_get::<Option<chrono::DateTime<chrono::Utc>>, _>("started_at")?
        .ok_or_else(|| AppError::Conflict("订单缺少开始时间".into()))?;
    let ended_at: chrono::DateTime<chrono::Utc> = row.try_get::<Option<chrono::DateTime<chrono::Utc>>, _>("ended_at")?
        .ok_or_else(|| AppError::Conflict("订单缺少结束时间".into()))?;
    if started_at < chrono::Utc::now() - chrono::Duration::days(365 * 3) {
        return Err(AppError::business(2018, "订单曲线已超过聚合数据保留期限"));
    }
    let path = p::GW_DEVICE_HIST_CURVE.replace(":id", &device_id);
    let device_query = DeviceHistoricalCurveQuery {
        order_id: row.try_get("order_no")?,
        port_no,
        granularity: granularity.to_string(),
        started_at: started_at.to_rfc3339(),
        ended_at: ended_at.to_rfc3339(),
    };
    let data: serde_json::Value = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone())
        .get(st.cfg.service_urls.gateway.as_deref(), &path, &device_query).await?;
    let series = data.get("series").cloned().unwrap_or_else(|| json!([]));
    let mut summary = data.get("summary").cloned().unwrap_or_else(|| json!({}));
    summary["total_kwh"] = row.try_get::<Option<String>, _>("charged_kwh")?
        .map_or(serde_json::Value::Null, |amount| json!(amount));
    Ok(Json(crate::api_envelope::Envelope::ok(json!({
        "order_id": row.try_get::<u64, _>("id")?,
        "granularity": granularity,
        "series": series,
        "summary": summary
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
    if order_id.is_empty() || order_id.len() > 64 {
        return Err(AppError::BadRequest("订单编号无效".into()));
    }
    if req.rating.is_some_and(|rating| !(1..=5).contains(&rating)) {
        return Err(AppError::BadRequest("评分必须在 1–5 星之间".into()));
    }
    if !["rating", "complaint", "suggestion"].contains(&req.category.as_str()) {
        return Err(AppError::BadRequest("反馈类型无效".into()));
    }
    if req.category == "rating" && req.rating.is_none() {
        return Err(AppError::BadRequest("评价必须选择 1–5 星".into()));
    }
    if req.category != "rating"
        && req.content.as_deref().is_none_or(|content| content.trim().is_empty())
        && req.images.as_ref().is_none_or(|images| images.is_empty())
    {
        return Err(AppError::BadRequest("投诉或建议请填写说明或上传图片".into()));
    }
    if req.content.as_deref().is_some_and(|v| v.chars().count() > 2000 || v.chars().any(|c| c.is_control() && c != '\n' && c != '\r' && c != '\t')) {
        return Err(AppError::BadRequest("反馈内容不得超过 2000 字或包含控制字符".into()));
    }
    if req.images.as_ref().is_some_and(|images| images.len() > 5 || images.iter().any(|url| url.len() > 512 || !url.starts_with("https://") || url.chars().any(char::is_control))) {
        return Err(AppError::BadRequest("反馈图片链接无效".into()));
    }
    let mut tx = st.db.pool().begin().await?;
    let order_id_db: Option<u64> = sqlx::query_scalar(
        "SELECT id FROM charge_order WHERE order_no = ? AND user_id = ? AND status = 'completed' AND deleted_at IS NULL FOR UPDATE",
    )
    .bind(&order_id)
    .bind(claims.user_id)
    .fetch_optional(&mut *tx)
    .await?;
    let order_id_db = order_id_db.ok_or_else(|| AppError::Conflict("只有本人已完成的订单可以评价".into()))?;
    let exists: bool = sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM feedback WHERE user_id = ? AND order_id = ? AND deleted_at IS NULL)")
        .bind(claims.user_id).bind(order_id_db).fetch_one(&mut *tx).await?;
    if exists {
        return Err(AppError::Conflict("该订单已提交过反馈".into()));
    }
    let feedback_id = sqlx::query(
        "INSERT INTO feedback (user_id, order_id, device_id, rating, category, content, images_json)
         VALUES (?, ?, NULL, ?, ?, ?, ?)",
    )
    .bind(claims.user_id)
    .bind(order_id_db)
    .bind(req.rating)
    .bind(&req.category)
    .bind(req.content.as_deref())
    .bind(req.images.as_ref().map(serde_json::to_value).transpose()?)
    .execute(&mut *tx)
    .await?
    .last_insert_id();
    tx.commit().await?;
    Ok(Json(crate::api_envelope::Envelope::ok(json!({"submitted": true,"feedback_id":feedback_id.to_string()}), common_error::current_request_id())))
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
        "order_id": r.try_get::<u64, _>("id")?,
        "order_no": r.try_get::<String, _>("order_no")?,
        "user_id": r.try_get::<u64, _>("user_id")?,
        "device_id": r.try_get::<String, _>("device_id")?,
        "port_no": r.try_get::<u8, _>("port_no")?,
        "status": r.try_get::<String, _>("status")?,
        "electric_cents": r.try_get::<Option<i64>, _>("electric_cents")?,
        "service_cents": r.try_get::<Option<i64>, _>("service_cents")?,
        "total_cents": r.try_get::<Option<i64>, _>("total_cents")?,
        "started_at": r.try_get::<Option<chrono::DateTime<chrono::Utc>>, _>("started_at")?.map(|t| t.to_rfc3339()),
        "ended_at": r.try_get::<Option<chrono::DateTime<chrono::Utc>>, _>("ended_at")?.map(|t| t.to_rfc3339()),
    }), common_error::current_request_id())))
}

// ===================== 个人中心 =====================

#[derive(Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct PhoneBindReq {
    pub code: String,
}

async fn wechat_access_token(st: &AppState) -> AppResult<String> {
    const CACHE_KEY: &str = "wechat:miniapp:access-token";
    if let Some(token) = st.redis_cache.get::<String>(CACHE_KEY).await? {
        if !token.trim().is_empty() {
            return Ok(token);
        }
    }

    let config = st.cfg.wechat.as_ref().ok_or_else(|| AppError::ServiceUnavailable("微信服务未配置".into()))?;
    let response = st.http.get("https://api.weixin.qq.com/cgi-bin/token")
        .query(&[("grant_type", "client_credential"), ("appid", config.appid.as_str()), ("secret", config.secret.as_str())])
        .timeout(std::time::Duration::from_secs(8)).send().await
        .map_err(|_| AppError::ServiceUnavailable("微信手机号验证暂不可用".into()))?;
    if !response.status().is_success() {
        return Err(AppError::ServiceUnavailable("微信手机号验证暂不可用".into()));
    }
    let body: serde_json::Value = response.json().await
        .map_err(|_| AppError::ServiceUnavailable("微信手机号验证响应无效".into()))?;
    if body.get("errcode").and_then(serde_json::Value::as_i64).is_some_and(|code| code != 0) {
        return Err(AppError::ServiceUnavailable("微信手机号验证暂不可用".into()));
    }
    let token = body.get("access_token").and_then(serde_json::Value::as_str)
        .filter(|value| !value.trim().is_empty())
        .ok_or_else(|| AppError::ServiceUnavailable("微信手机号验证响应无效".into()))?;
    let ttl = body.get("expires_in").and_then(serde_json::Value::as_u64).unwrap_or(7200).saturating_sub(300).max(60);
    st.redis_cache.set_ex(CACHE_KEY, &token, ttl).await?;
    Ok(token.to_owned())
}

pub async fn phone_bind(
    State(st): State<AppState>,
    claims: UserClaims,
    Json(req): Json<PhoneBindReq>,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    if req.code.trim().is_empty() || req.code.len() > 512 || req.code.chars().any(char::is_control) {
        return Err(AppError::BadRequest("手机号授权凭证无效".into()));
    }
    let token = wechat_access_token(&st).await?;
    let response = st.http.post("https://api.weixin.qq.com/wxa/business/getuserphonenumber")
        .query(&[("access_token", token.as_str())])
        .json(&json!({"code": req.code.trim()}))
        .timeout(std::time::Duration::from_secs(8)).send().await
        .map_err(|_| AppError::ServiceUnavailable("微信手机号验证暂不可用".into()))?;
    if !response.status().is_success() {
        return Err(AppError::ServiceUnavailable("微信手机号验证暂不可用".into()));
    }
    let body: serde_json::Value = response.json().await
        .map_err(|_| AppError::ServiceUnavailable("微信手机号验证响应无效".into()))?;
    if let Some(code) = body.get("errcode").and_then(serde_json::Value::as_i64).filter(|code| *code != 0) {
        if [40001, 40014, 42001].contains(&code) {
            st.redis_cache.del("wechat:miniapp:access-token").await?;
            return Err(AppError::ServiceUnavailable("微信凭证已过期，请重新授权手机号".into()));
        }
        if [40029, 40163].contains(&code) {
            return Err(AppError::BadRequest("手机号授权已失效，请重新授权".into()));
        }
        return Err(AppError::ServiceUnavailable("微信手机号验证暂不可用".into()));
    }
    let phone = body.get("phone_info").and_then(|v| v.get("purePhoneNumber"))
        .and_then(serde_json::Value::as_str)
        .filter(|value| value.len() == 11 && value.starts_with('1') && value.bytes().all(|b| b.is_ascii_digit()))
        .ok_or_else(|| AppError::BadRequest("微信返回的手机号格式无效".into()))?;
    let phone_hash = format!("{:x}", sha2::Sha256::digest(phone.as_bytes()));

    let mut tx = st.db.pool().begin().await?;
    let current: Option<String> = sqlx::query_scalar(
        "SELECT phone_hash FROM `user` WHERE id = ? AND status = 'active' AND deleted_at IS NULL FOR UPDATE",
    )
    .bind(claims.user_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or_else(|| AppError::Forbidden("账号不可绑定手机号".into()))?;
    if current.as_deref() == Some(phone_hash.as_str()) {
        tx.commit().await?;
        return Ok(Json(crate::api_envelope::Envelope::ok(json!({"bound": true}), common_error::current_request_id())));
    }
    let owner: Option<u64> = sqlx::query_scalar("SELECT id FROM `user` WHERE phone_hash = ? AND deleted_at IS NULL FOR UPDATE")
        .bind(&phone_hash).fetch_optional(&mut *tx).await?;
    if owner.is_some_and(|owner| owner != claims.user_id) {
        return Err(AppError::Conflict("该手机号已绑定其他账号".into()));
    }
    let updated = match sqlx::query("UPDATE `user` SET phone_hash = ? WHERE id = ? AND status = 'active' AND deleted_at IS NULL")
        .bind(&phone_hash).bind(claims.user_id).execute(&mut *tx).await {
        Ok(result) => result,
        Err(sqlx::Error::Database(error)) if error.code().as_deref() == Some("1062") => {
            return Err(AppError::Conflict("该手机号已绑定其他账号".into()));
        }
        Err(error) => return Err(error.into()),
    };
    if updated.rows_affected() != 1 {
        return Err(AppError::Forbidden("账号不可绑定手机号".into()));
    }
    tx.commit().await?;
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
    let items: serde_json::Value = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone())
        .get(st.cfg.service_urls.admin.as_deref(), p::ADMIN_INTERNAL_ANNOUNCEMENTS_ACTIVE, &())
        .await?;
    Ok(Json(crate::api_envelope::Envelope::ok(items, common_error::current_request_id())))
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct CustomerServiceRequest {
    pub scene: String,
}

pub async fn customer_service_entry(
    State(st): State<AppState>,
    _claims: UserClaims,
    Json(req): Json<CustomerServiceRequest>,
) -> AppResult<Json<crate::api_envelope::Envelope<serde_json::Value>>> {
    if !["general", "refund", "complaint"].contains(&req.scene.as_str()) {
        return Err(AppError::BadRequest("客服场景无效".into()));
    }
    let mut entry: serde_json::Value = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone())
        .get(st.cfg.service_urls.admin.as_deref(), p::ADMIN_INTERNAL_CUSTOMER_SERVICE_ENTRY, &req)
        .await?;
    let corp_id = st.cfg.wechat.as_ref().and_then(|config| config.customer_service_corp_id.as_deref());
    let entry_url = entry.get("entry_url").and_then(serde_json::Value::as_str).filter(|url| url.starts_with("https://"));
    let available = corp_id.is_some() && entry_url.is_some();
    entry["corp_id"] = corp_id.map_or(serde_json::Value::Null, |value| json!(value));
    entry["available"] = json!(available);
    Ok(Json(crate::api_envelope::Envelope::ok(entry, common_error::current_request_id())))
}
