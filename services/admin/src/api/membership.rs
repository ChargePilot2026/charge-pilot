//! 会员卡配置

use crate::AppState;
use axum::{extract::State, Json};
use crate::auth::ActiveAdmin;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use serde_json::{json, Value};

#[derive(Debug, Deserialize)]
pub struct MembershipCreateReq {
    pub name: String,
    pub card_type: String,
    pub price_cents: i64,
    pub valid_days: i32,
}

pub async fn list(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::MembershipCard>>>> {
    // 会员卡模板仍属于预留功能；这里只展示已存在的用户会员卡记录。
    let rows = sqlx::query("SELECT id, user_id, card_type, status, start_at, end_at, price_cents FROM membership_card WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 200")
        .fetch_all(st.config.pool()).await?;
    let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::MembershipCard> { Ok(api_contracts::admin::MembershipCard {
        id: sqlx::Row::try_get::<u64, _>(r, "id")?,
        user_id: sqlx::Row::try_get::<u64, _>(r, "user_id")?,
        card_type: sqlx::Row::try_get::<String, _>(r, "card_type")?,
        status: sqlx::Row::try_get::<String, _>(r, "status")?,
        price_cents: sqlx::Row::try_get::<i64, _>(r, "price_cents")?,
        start_at: sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(r, "start_at")?.map(|time| time.to_rfc3339()),
        end_at: sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(r, "end_at")?.map(|time| time.to_rfc3339()),
    }) }).collect::<AppResult<Vec<_>>>()?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::ListResponse::new(items), common_error::current_request_id())))
}

/// 未开放功能的桩:永远返回 `Err`,成功响应体不存在,故类型写 `()`。
pub async fn create(State(st): State<AppState>, c: ActiveAdmin, Json(_req): Json<MembershipCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<()>>> {
    crate::auth::require_permission(&st,&c,"membership.create").await?;
    // membership_card is explicitly reserved for a later phase; do not report a fake creation success.
    Err(AppError::ServiceUnavailable("会员卡模板功能尚未开放".into()))
}
