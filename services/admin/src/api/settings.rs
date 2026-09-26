//! 计费规则 / 分账模板 / OTA 配置

use crate::AppState;
use crate::api_types;
use axum::{extract::{Path, State}, Json};
use common_auth::AdminClaims;
use common_db::IdGen;
use common_error::AppResult;
use common_redis::StreamEnvelope;
use serde::Deserialize;
use serde_json::{json, Value};

// ===== 计费规则 =====
pub async fn charge_rules(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query("SELECT id, name, mode, service_fee_cents_per_kwh, service_fee_cents_per_min, min_charge_cents, version, status FROM pricing_rule WHERE deleted_at IS NULL")
        .fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "name": sqlx::Row::try_get::<String, _>(r, "name").unwrap_or_default(),
        "mode": sqlx::Row::try_get::<String, _>(r, "mode").unwrap_or_default(),
        "service_fee_cents_per_kwh": sqlx::Row::try_get::<i64, _>(r, "service_fee_cents_per_kwh").unwrap_or(0),
        "service_fee_cents_per_min": sqlx::Row::try_get::<i64, _>(r, "service_fee_cents_per_min").unwrap_or(0),
        "min_charge_cents": sqlx::Row::try_get::<i64, _>(r, "min_charge_cents").unwrap_or(0),
        "version": sqlx::Row::try_get::<u32, _>(r, "version").unwrap_or(1),
        "status": sqlx::Row::try_get::<String, _>(r, "status").unwrap_or_default(),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct ChargeRuleCreateReq {
    pub name: String,
    pub station_id: Option<u64>,
    pub mode: String,
    pub time_of_use_json: Option<serde_json::Value>,
    pub service_fee_cents_per_kwh: i64,
    pub service_fee_cents_per_min: i64,
    pub min_charge_cents: i64,
}

pub async fn charge_rule_create(State(st): State<AppState>, _c: AdminClaims, Json(req): Json<ChargeRuleCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let id: u64 = sqlx::query_scalar(
        "INSERT INTO pricing_rule (name, station_id, mode, time_of_use_json, service_fee_cents_per_kwh, service_fee_cents_per_min, min_charge_cents)
         VALUES (?, ?, ?, ?, ?, ?, ?)"
    )
    .bind(&req.name).bind(req.station_id).bind(&req.mode).bind(req.time_of_use_json)
    .bind(req.service_fee_cents_per_kwh).bind(req.service_fee_cents_per_min).bind(req.min_charge_cents)
    .fetch_one(st.db.pool()).await?;
    let env = StreamEnvelope::new("pricing_rule_changed", "admin", json!({"id": id, "key": req.name}));
    let _ = st.redis_stream.xadd_envelope(common_redis::streams::PRICING_RULE_CHANGED, &env).await;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": id}), common_error::current_request_id())))
}

// ===== 计费模板 =====
pub async fn pricing_templates(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query("SELECT id, code, name, default_pricing_rule_id FROM pricing_template WHERE deleted_at IS NULL")
        .fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "code": sqlx::Row::try_get::<String, _>(r, "code").unwrap_or_default(),
        "name": sqlx::Row::try_get::<String, _>(r, "name").unwrap_or_default(),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct PricingTemplateCreateReq {
    pub code: String,
    pub name: String,
    pub default_pricing_rule_id: Option<u64>,
}

pub async fn pricing_template_create(State(st): State<AppState>, _c: AdminClaims, Json(req): Json<PricingTemplateCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let id: u64 = sqlx::query_scalar("INSERT INTO pricing_template (code, name, default_pricing_rule_id) VALUES (?, ?, ?)")
        .bind(&req.code).bind(&req.name).bind(req.default_pricing_rule_id)
        .fetch_one(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": id}), common_error::current_request_id())))
}

// ===== 分账模板 =====
pub async fn split_templates(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query("SELECT id, code, name, mode, status FROM split_template WHERE deleted_at IS NULL")
        .fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "code": sqlx::Row::try_get::<String, _>(r, "code").unwrap_or_default(),
        "name": sqlx::Row::try_get::<String, _>(r, "name").unwrap_or_default(),
        "mode": sqlx::Row::try_get::<String, _>(r, "mode").unwrap_or_default(),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct SplitTemplateCreateReq {
    pub code: String,
    pub name: String,
    pub mode: String,
}

pub async fn split_template_create(State(st): State<AppState>, _c: AdminClaims, Json(req): Json<SplitTemplateCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let id: u64 = sqlx::query_scalar("INSERT INTO split_template (code, name, mode) VALUES (?, ?, ?)")
        .bind(&req.code).bind(&req.name).bind(&req.mode)
        .fetch_one(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": id}), common_error::current_request_id())))
}

pub async fn split_parties(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query("SELECT id, party_code, party_name, ratio_bp FROM split_party WHERE split_template_id = ?")
        .bind(id).fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "party_code": sqlx::Row::try_get::<String, _>(r, "party_code").unwrap_or_default(),
        "party_name": sqlx::Row::try_get::<String, _>(r, "party_name").unwrap_or_default(),
        "ratio_bp": sqlx::Row::try_get::<u32, _>(r, "ratio_bp").unwrap_or(0),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct SplitPartyCreateReq {
    pub party_code: String,
    pub party_name: String,
    pub ratio_bp: u32,
    pub bank_account: Option<String>,
    pub bank_name: Option<String>,
}

pub async fn split_party_create(State(st): State<AppState>, _c: AdminClaims, Path(id): Path<u64>, Json(req): Json<SplitPartyCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_types::CreatedIdResponse>>> {
    let new_id: u64 = sqlx::query_scalar(
        "INSERT INTO split_party (split_template_id, party_code, party_name, ratio_bp, bank_account, bank_name) VALUES (?, ?, ?, ?, ?, ?)"
    )
    .bind(id).bind(&req.party_code).bind(&req.party_name).bind(req.ratio_bp)
    .bind(req.bank_account.as_deref()).bind(req.bank_name.as_deref())
    .fetch_one(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_types::CreatedIdResponse { id: new_id }, common_error::current_request_id())))
}

// ===== OTA 配置 =====
pub async fn ota_get(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    // 实际从 ota_package 或 KV 存;本期返回空
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "storage_driver": "local",
        "max_concurrent_pushes": 50,
        "auto_rollback": true,
    }), common_error::current_request_id())))
}

pub async fn ota_put(State(_st): State<AppState>, _c: AdminClaims, Json(_req): Json<Value>) -> AppResult<Json<common_error::ApiEnvelope<api_types::OkFlagResponse>>> {
    Ok(Json(common_error::ApiEnvelope::ok(api_types::OkFlagResponse { ok: true }, common_error::current_request_id())))
}

#[allow(dead_code)]
fn _id_unused() { let _ = IdGen::new("ST"); }