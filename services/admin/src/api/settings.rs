//! 计费规则 / 分账模板 / OTA 配置

use crate::AppState;
use crate::api_types;
use axum::{extract::{Path, State}, Json};
use crate::auth::ActiveAdmin;
use common_db::IdGen;
use common_error::{AppError, AppResult};
use common_redis::StreamEnvelope;
use serde::Deserialize;
use serde_json::{json, Value};

// ===== 计费规则 =====
pub async fn charge_rules(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query("SELECT id, name, mode, service_fee_cents_per_kwh, service_fee_cents_per_min, min_charge_cents, version, status FROM pricing_rule WHERE deleted_at IS NULL")
        .fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| -> AppResult<Value> { Ok(json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id")?,
        "name": sqlx::Row::try_get::<String, _>(r, "name")?,
        "mode": sqlx::Row::try_get::<String, _>(r, "mode")?,
        "service_fee_cents_per_kwh": sqlx::Row::try_get::<i64, _>(r, "service_fee_cents_per_kwh")?,
        "service_fee_cents_per_min": sqlx::Row::try_get::<i64, _>(r, "service_fee_cents_per_min")?,
        "min_charge_cents": sqlx::Row::try_get::<i64, _>(r, "min_charge_cents")?,
        "version": sqlx::Row::try_get::<u32, _>(r, "version")?,
        "status": sqlx::Row::try_get::<String, _>(r, "status")?,
    })) }).collect::<AppResult<Vec<_>>>()?;
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

pub async fn charge_rule_create(State(st): State<AppState>, _c: ActiveAdmin, Json(req): Json<ChargeRuleCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    crate::auth::require_permission(&st,&_c,"pricing.rule.create").await?;
    let mut tx = st.db.pool().begin().await?;
    let result = sqlx::query(
        "INSERT INTO pricing_rule (name, station_id, mode, time_of_use_json, service_fee_cents_per_kwh, service_fee_cents_per_min, min_charge_cents)
         VALUES (?, ?, ?, ?, ?, ?, ?)"
    )
    .bind(&req.name).bind(req.station_id).bind(&req.mode).bind(req.time_of_use_json)
    .bind(req.service_fee_cents_per_kwh).bind(req.service_fee_cents_per_min).bind(req.min_charge_cents)
    .execute(&mut *tx).await?;
    let id = result.last_insert_id();

    // **D5 修复**:原先用 `let _ =` 吞掉发布失败 —— DB 已提交而事件永久丢失。
    // 现在事件与业务写**同事务**落 `event_outbox`(admin_db 见 0022 迁移),
    // 由发布器负责投递;投递失败也只是 outbox 状态变化,不会丢事件。
    let env = StreamEnvelope::new("pricing_rule_changed", "admin", json!({"id": id, "key": req.name}));
    sqlx::query("INSERT INTO event_outbox (event_id, stream, envelope_json) VALUES (?, ?, ?)")
        .bind(&env.event_id)
        .bind(common_redis::streams::PRICING_RULE_CHANGED)
        .bind(serde_json::to_value(&env)?)
        .execute(&mut *tx)
        .await?;
    tx.commit().await?;

    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": id}), common_error::current_request_id())))
}

// ===== 计费模板 =====
pub async fn pricing_templates(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query("SELECT id, code, name, default_pricing_rule_id FROM pricing_template WHERE deleted_at IS NULL")
        .fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| -> AppResult<Value> { Ok(json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id")?,
        "code": sqlx::Row::try_get::<String, _>(r, "code")?,
        "name": sqlx::Row::try_get::<String, _>(r, "name")?,
    })) }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct PricingTemplateCreateReq {
    pub code: String,
    pub name: String,
    pub default_pricing_rule_id: Option<u64>,
}

pub async fn pricing_template_create(State(st): State<AppState>, _c: ActiveAdmin, Json(req): Json<PricingTemplateCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    crate::auth::require_permission(&st,&_c,"pricing.template.create").await?;
    let result = sqlx::query("INSERT INTO pricing_template (code, name, default_pricing_rule_id) VALUES (?, ?, ?)")
        .bind(&req.code).bind(&req.name).bind(req.default_pricing_rule_id)
        .execute(st.db.pool()).await?;
    let id = result.last_insert_id();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": id}), common_error::current_request_id())))
}

// ===== 分账模板 =====
pub async fn split_templates(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query("SELECT id, code, name, mode, status FROM split_template WHERE deleted_at IS NULL")
        .fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| -> AppResult<Value> { Ok(json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id")?,
        "code": sqlx::Row::try_get::<String, _>(r, "code")?,
        "name": sqlx::Row::try_get::<String, _>(r, "name")?,
        "mode": sqlx::Row::try_get::<String, _>(r, "mode")?,
    })) }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize)]
pub struct SplitTemplateCreateReq {
    pub code: String,
    pub name: String,
    pub mode: String,
}

pub async fn split_template_create(State(st): State<AppState>, _c: ActiveAdmin, Json(req): Json<SplitTemplateCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    crate::auth::require_permission(&st,&_c,"finance.split_template.create").await?;
    let result = sqlx::query("INSERT INTO split_template (code, name, mode) VALUES (?, ?, ?)")
        .bind(&req.code).bind(&req.name).bind(&req.mode)
        .execute(st.db.pool()).await?;
    let id = result.last_insert_id();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": id}), common_error::current_request_id())))
}

pub async fn split_parties(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let rows = sqlx::query("SELECT id, party_code, party_name, ratio_bp FROM split_party WHERE split_template_id = ?")
        .bind(id).fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| -> AppResult<Value> { Ok(json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id")?,
        "party_code": sqlx::Row::try_get::<String, _>(r, "party_code")?,
        "party_name": sqlx::Row::try_get::<String, _>(r, "party_name")?,
        "ratio_bp": sqlx::Row::try_get::<u32, _>(r, "ratio_bp")?,
    })) }).collect::<AppResult<Vec<_>>>()?;
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

pub async fn split_party_create(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>, Json(req): Json<SplitPartyCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_types::CreatedIdResponse>>> {
    crate::auth::require_permission(&st,&_c,"finance.split_party.create").await?;
    let result = sqlx::query(
        "INSERT INTO split_party (split_template_id, party_code, party_name, ratio_bp, bank_account, bank_name) VALUES (?, ?, ?, ?, ?, ?)"
    )
    .bind(id).bind(&req.party_code).bind(&req.party_name).bind(req.ratio_bp)
    .bind(req.bank_account.as_deref()).bind(req.bank_name.as_deref())
    .execute(st.db.pool()).await?;
    let new_id = result.last_insert_id();
    Ok(Json(common_error::ApiEnvelope::ok(api_types::CreatedIdResponse { id: new_id }, common_error::current_request_id())))
}

// ===== OTA 配置 =====
pub async fn ota_get(State(_st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    Err(AppError::ServiceUnavailable("OTA 配置存储尚未接入".into()))
}

pub async fn ota_put(State(st): State<AppState>, c: ActiveAdmin, Json(_req): Json<Value>) -> AppResult<Json<common_error::ApiEnvelope<api_types::OkFlagResponse>>> {
    crate::auth::require_permission(&st,&c,"settings.ota.update").await?;
    Err(AppError::ServiceUnavailable("OTA 配置存储尚未接入，未保存设置".into()))
}

#[allow(dead_code)]
fn _id_unused() { let _ = IdGen::new("ST"); }
