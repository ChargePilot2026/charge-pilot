//! Single-use refresh tokens. Access JWTs are never accepted as refresh tokens.
use crate::AppState;
use axum::{extract::State, http::HeaderMap, Json};
use common_error::{ApiEnvelope, AppError, AppResult};
use common_redis::RedisCache;
use serde::{Deserialize, Serialize};

const TTL: u64 = 7 * 24 * 60 * 60;
#[derive(Serialize, Deserialize)]
pub struct Identity { pub user_id: u64, pub openid: String, pub sid: String }
#[derive(Serialize)]
pub struct Tokens { pub token: String, pub refresh_token: String, pub jwt_expires_in: u64 }

fn fresh() -> String { format!("RT_{}{}",uuid::Uuid::new_v4().simple(),uuid::Uuid::new_v4().simple()) }
fn key(token: &str) -> AppResult<String> {
    if token.len()!=67 || !token.starts_with("RT_") || !token[3..].bytes().all(|b| b.is_ascii_hexdigit()) {
        return Err(AppError::Unauthorized("刷新令牌无效，请重新登录".into()));
    }
    Ok(format!("auth:user:refresh:{token}"))
}
fn bearer(headers: &HeaderMap) -> AppResult<&str> {
    headers.get("authorization").and_then(|v| v.to_str().ok()).and_then(|v| v.strip_prefix("Bearer "))
        .ok_or_else(|| AppError::Unauthorized("缺少刷新令牌".into()))
}
pub async fn issue(cache: &RedisCache, identity: &Identity) -> AppResult<String> {
    let token=fresh(); let mut conn=cache.conn();
    let value=serde_json::to_string(identity)?;
    let _: i64=redis::Script::new("redis.call('SET',KEYS[1],ARGV[1],'EX',ARGV[3]); redis.call('SET',KEYS[2],ARGV[2],'EX',ARGV[3]); return 1")
        .key(key(&token)?).key(session_key(&identity.sid)).arg(value).arg(&token).arg(TTL).invoke_async(&mut conn).await?;
    Ok(token)
}
async fn rotate(cache: &RedisCache, old: &str, expected: &str) -> AppResult<String> {
    let new=fresh(); let mut conn=cache.conn();
    let identity: Identity=serde_json::from_str(expected)?;
    let changed: i64=redis::Script::new(
        "if redis.call('GET',KEYS[1]) ~= ARGV[1] or redis.call('GET',KEYS[3]) ~= ARGV[3] then return 0 end \
         redis.call('SET',KEYS[2],ARGV[1],'EX',ARGV[2]); redis.call('SET',KEYS[3],ARGV[4],'EX',ARGV[2]); return 1"
    ).key(key(old)?).key(key(&new)?).key(session_key(&identity.sid)).arg(expected).arg(TTL).arg(old).arg(&new).invoke_async(&mut conn).await?;
    if changed!=1 { return Err(AppError::Unauthorized("刷新令牌已使用或过期，请重新登录".into())); }
    Ok(new)
}
pub async fn refresh(State(st): State<AppState>, headers: HeaderMap) -> AppResult<Json<ApiEnvelope<Tokens>>> {
    let old=bearer(&headers)?; let mut conn=st.redis_cache.conn();
    let stored: Option<String>=redis::cmd("GET").arg(key(old)?).query_async(&mut conn).await?;
    let stored=stored.ok_or_else(|| AppError::Unauthorized("登录会话已过期".into()))?;
    let identity: Identity=serde_json::from_str(&stored).map_err(|_| AppError::Unauthorized("请重新登录".into()))?;
    let current: Option<String>=redis::cmd("GET").arg(session_key(&identity.sid)).query_async(&mut conn).await?;
    if current.as_deref()!=Some(old) { return Err(AppError::Unauthorized("刷新令牌已使用或会话已退出".into())); }
    let active: bool=sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM `user` WHERE id=? AND BINARY openid=? AND status='active' AND deleted_at IS NULL)")
        .bind(identity.user_id).bind(identity.openid.as_bytes()).fetch_one(st.db.pool()).await?;
    if !active {
        st.redis_cache.del(&session_key(&identity.sid)).await?;
        return Err(AppError::Forbidden("账户已停用".into()));
    }
    let token=st.jwt.issue_user_with_session(&identity.openid,identity.user_id,Some(identity.sid.clone()),900)?;
    let refresh_token=rotate(&st.redis_cache,old,&stored).await?;
    Ok(Json(ApiEnvelope::ok(Tokens {token,refresh_token,jwt_expires_in:900},common_error::current_request_id())))
}
pub async fn logout(State(st): State<AppState>, headers: HeaderMap) -> AppResult<Json<ApiEnvelope<serde_json::Value>>> {
    revoke(&st.redis_cache,bearer(&headers)?).await?;
    Ok(Json(ApiEnvelope::ok(serde_json::json!({"logged_out":true}),common_error::current_request_id())))
}

fn session_key(sid: &str) -> String { format!("auth:user:session:{sid}") }
async fn revoke(cache: &RedisCache, token: &str) -> AppResult<()> {
    let mut conn=cache.conn();
    let stored: Option<String>=redis::cmd("GET").arg(key(token)?).query_async(&mut conn).await?;
    if let Some(stored)=stored {
        let identity: Identity=serde_json::from_str(&stored)?;
        cache.del(&session_key(&identity.sid)).await?;
    }
    Ok(())
}

pub async fn require_session(State(st): State<AppState>, mut req: axum::extract::Request, next: axum::middleware::Next) -> AppResult<axum::response::Response> {
    let jwt=bearer(req.headers())?;
    let claims=st.jwt.verify_user(jwt)?;
    let sid=claims.sid.as_deref().ok_or_else(|| AppError::Unauthorized("请重新登录".into()))?;
    let mut conn=st.redis_cache.conn();
    let exists: bool=redis::cmd("EXISTS").arg(session_key(sid)).query_async(&mut conn).await?;
    if !exists { return Err(AppError::Unauthorized("登录会话已退出或过期".into())); }
    let active: bool=sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM `user` WHERE id=? AND BINARY openid=? AND status='active' AND deleted_at IS NULL)")
        .bind(claims.user_id).bind(claims.sub.as_bytes()).fetch_one(st.db.pool()).await?;
    if !active { return Err(AppError::Forbidden("账户已停用".into())); }
    req.extensions_mut().insert(claims);
    Ok(next.run(req).await)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[tokio::test]
    #[ignore = "requires development Redis cache"]
    async fn only_one_concurrent_refresh_wins_and_logout_revokes() {
        let cache=RedisCache::connect(&common_config::RedisConfig {url:std::env::var("REDIS_CACHE_URL").unwrap(),is_stream:false,pool_size:2}).await.unwrap();
        let identity=Identity {user_id:123,openid:"session-test".into(),sid:uuid::Uuid::new_v4().to_string()};
        let old=issue(&cache,&identity).await.unwrap();
        let value=serde_json::to_string(&identity).unwrap();
        let (a,b)=tokio::join!(rotate(&cache,&old,&value),rotate(&cache,&old,&value));
        assert_ne!(a.is_ok(),b.is_ok());
        let winner=a.or(b).unwrap();
        assert!(rotate(&cache,&old,&value).await.is_err());
        revoke(&cache,&old).await.unwrap();
        assert!(rotate(&cache,&winner,&value).await.is_err());
        assert!(key("eyJ.access.jwt").is_err());
        cache.del(&key(&old).unwrap()).await.unwrap();
        cache.del(&key(&winner).unwrap()).await.unwrap();
    }
}
