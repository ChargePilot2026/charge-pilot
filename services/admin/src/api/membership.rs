//! 会员卡配置

use crate::AppState;
use axum::{extract::State, Json};
use common_auth::AdminClaims;
use common_error::AppResult;
use serde::Deserialize;
use serde_json::{json, Value};

#[derive(Debug, Deserialize)]
pub struct MembershipCreateReq {
    pub name: String,
    pub card_type: String,
    pub price_cents: i64,
    pub valid_days: i32,
}

pub async fn list(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    // 后台视角:返回所有 membership_card 模板(本期预留)
    let rows = sqlx::query("SELECT id, user_id, card_type, status, start_at, end_at, price_cents FROM membership_card WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 200")
        .fetch_all(st.db.pool()).await?;
    let items: Vec<Value> = rows.iter().map(|r| json!({
        "id": sqlx::Row::try_get::<u64, _>(r, "id").unwrap_or(0),
        "user_id": sqlx::Row::try_get::<u64, _>(r, "user_id").unwrap_or(0),
        "card_type": sqlx::Row::try_get::<String, _>(r, "card_type").unwrap_or_default(),
        "status": sqlx::Row::try_get::<String, _>(r, "status").unwrap_or_default(),
        "price_cents": sqlx::Row::try_get::<i64, _>(r, "price_cents").unwrap_or(0),
    })).collect();
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items}), common_error::current_request_id())))
}

pub async fn create(State(_st): State<AppState>, _c: AdminClaims, Json(_req): Json<MembershipCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    // 实际创建会员卡模板走 admin_coupon 或新建 membership_template 表;本期占位
    Ok(Json(common_error::ApiEnvelope::ok(json!({"created": true}), common_error::current_request_id())))
}