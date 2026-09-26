use crate::AppState;
use axum::{extract::State, Json};
use common_auth::UserClaims;
use common_error::{ApiEnvelope, AppError, AppResult};
use serde::Serialize;
use sqlx::Row;

#[derive(Serialize)]
pub struct Wallet { available_cents: i64, frozen_cents: i64, status: String }
#[derive(Serialize)]
pub struct Membership { card_type: String, start_at: String, end_at: String }
#[derive(Serialize)]
pub struct Profile {
    user_id: u64, nickname: Option<String>, avatar_url: Option<String>,
    gender: String, phone_bound: bool, registered_at: String,
    balance_cents: i64, wallet: Wallet, coupon_unused_count: i64,
    membership_card: Option<Membership>,
}

pub async fn get(State(st): State<AppState>, claims: UserClaims) -> AppResult<Json<ApiEnvelope<Profile>>> {
    let mut tx=st.db.pool().begin().await?;
    let user=sqlx::query("SELECT nickname,avatar_url,gender,(phone_enc IS NOT NULL) AS phone_bound,first_seen_at FROM `user` WHERE id=? AND deleted_at IS NULL AND status='active'")
        .bind(claims.user_id).fetch_optional(&mut *tx).await?.ok_or_else(|| AppError::NotFound("用户不存在".into()))?;
    let wallets: Vec<(i64,i64,String)>=sqlx::query_as("SELECT balance_cents,frozen_cents,status FROM wallet_account WHERE user_id=? AND deleted_at IS NULL")
        .bind(claims.user_id).fetch_all(&mut *tx).await?;
    if wallets.len()>1 { return Err(AppError::Conflict("钱包账户重复，请联系客服".into())); }
    let (available_cents,frozen_cents,status)=wallets.into_iter().next().unwrap_or((0,0,"active".into()));
    let coupon_unused_count=sqlx::query_scalar(
        "SELECT COUNT(*) FROM coupon_grant g JOIN coupon c ON c.id=g.coupon_id \
         WHERE g.user_id=? AND g.status='unused' AND g.deleted_at IS NULL AND g.expired_at>UTC_TIMESTAMP(3) \
         AND c.deleted_at IS NULL AND c.status='active' AND (c.start_at IS NULL OR c.start_at<=UTC_TIMESTAMP(3)) \
         AND (c.end_at IS NULL OR c.end_at>UTC_TIMESTAMP(3))"
    ).bind(claims.user_id).fetch_one(&mut *tx).await?;
    let membership: Option<(String,chrono::DateTime<chrono::Utc>,chrono::DateTime<chrono::Utc>)>=sqlx::query_as(
        "SELECT card_type,start_at,end_at FROM membership_card WHERE user_id=? AND status='active' AND deleted_at IS NULL \
         AND start_at<=UTC_TIMESTAMP(3) AND end_at>UTC_TIMESTAMP(3) ORDER BY end_at DESC,id DESC LIMIT 1"
    ).bind(claims.user_id).fetch_optional(&mut *tx).await?;
    let registered: chrono::DateTime<chrono::Utc>=user.try_get("first_seen_at")?;
    let result=Profile {user_id:claims.user_id,nickname:user.try_get("nickname")?,avatar_url:user.try_get("avatar_url")?,
        gender:user.try_get("gender")?,phone_bound:user.try_get("phone_bound")?,registered_at:registered.to_rfc3339(),
        balance_cents:available_cents,wallet:Wallet {available_cents,frozen_cents,status},coupon_unused_count,
        membership_card:membership.map(|(card_type,start,end)| Membership {card_type,start_at:start.to_rfc3339(),end_at:end.to_rfc3339()})};
    tx.commit().await?;
    Ok(Json(ApiEnvelope::ok(result,common_error::current_request_id())))
}
