//! 鉴权模块:JWT(openid / 角色)+ 服务间共享密钥 + 密码哈希
//!
//! 设计(对齐技术规格 § 6.x + § 7.3):
//!   - 用户侧 JWT: payload = { sub=openid, user_id, role? exp }
//!   - PC 后台 JWT: payload = { sub=admin_user_id, role, exp }
//!   - 服务间: Header `X-Service-Token: <SERVICE_TOKEN>`(Axum middleware 校验)
//!   - 密码: argon2id 哈希
//!   - 微信回调签名校验: 微信 V3 签名(技术规格 § 9.4)

use axum::{
    extract::{FromRequestParts, Request, State},
    http::{header, HeaderMap, StatusCode},
    middleware::Next,
    response::Response,
};
use common_config::AuthConfig;
use common_error::{AppError, AppResult};
use jsonwebtoken::{decode, encode, DecodingKey, EncodingKey, Header, Validation, Algorithm};
use serde::{Deserialize, Serialize};
use std::sync::Arc;

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UserClaims {
    pub sub: String,         // openid
    pub user_id: u64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub sid: Option<String>,
    #[serde(default)]
    pub role: Option<String>,
    pub exp: i64,
    pub iat: i64,
    pub iss: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AdminClaims {
    pub sub: String,         // admin user name
    pub admin_user_id: u64,
    pub role: String,        // customer_finance / customer_ops / customer_cs ...
    #[serde(default)]
    pub permissions: Vec<String>,
    pub exp: i64,
    pub iat: i64,
    pub iss: String,
}

/// JWT 编码 / 解码
pub struct JwtCodec {
    enc_key: EncodingKey,
    dec_key: DecodingKey,
    iss: String,
    ttl_secs: u64,
}

impl JwtCodec {
    pub fn new(cfg: &AuthConfig) -> Self {
        Self {
            enc_key: EncodingKey::from_secret(cfg.jwt_secret.as_bytes()),
            dec_key: DecodingKey::from_secret(cfg.jwt_secret.as_bytes()),
            iss: cfg.jwt_issuer.clone(),
            ttl_secs: cfg.jwt_ttl_secs,
        }
    }

    pub fn issue_user(&self, openid: &str, user_id: u64) -> AppResult<String> {
        self.issue_user_with_session(openid, user_id, None, self.ttl_secs)
    }

    pub fn issue_user_with_session(&self, openid: &str, user_id: u64, sid: Option<String>, ttl_secs: u64) -> AppResult<String> {
        let now = chrono::Utc::now().timestamp();
        let claims = UserClaims {
            sub: openid.to_string(),
            user_id,
            sid,
            role: None,
            iat: now,
            exp: now + ttl_secs as i64,
            iss: self.iss.clone(),
        };
        let token = encode(&Header::new(Algorithm::HS256), &claims, &self.enc_key)
            .map_err(|e| AppError::Internal(format!("jwt encode: {e}")))?;
        Ok(token)
    }

    pub fn issue_admin(
        &self,
        username: &str,
        admin_user_id: u64,
        role: &str,
        permissions: Vec<String>,
    ) -> AppResult<String> {
        let now = chrono::Utc::now().timestamp();
        let claims = AdminClaims {
            sub: username.to_string(),
            admin_user_id,
            role: role.to_string(),
            permissions,
            iat: now,
            exp: now + self.ttl_secs as i64,
            iss: self.iss.clone(),
        };
        let token = encode(&Header::new(Algorithm::HS256), &claims, &self.enc_key)
            .map_err(|e| AppError::Internal(format!("jwt encode: {e}")))?;
        Ok(token)
    }

    pub fn verify_user(&self, token: &str) -> AppResult<UserClaims> {
        let mut v = Validation::new(Algorithm::HS256);
        v.set_issuer(&[&self.iss]);
        let data = decode::<UserClaims>(token, &self.dec_key, &v)
            .map_err(|_| AppError::Unauthorized("invalid jwt".into()))?;
        Ok(data.claims)
    }

    pub fn verify_admin(&self, token: &str) -> AppResult<AdminClaims> {
        let mut v = Validation::new(Algorithm::HS256);
        v.set_issuer(&[&self.iss]);
        let data = decode::<AdminClaims>(token, &self.dec_key, &v)
            .map_err(|_| AppError::Unauthorized("invalid jwt".into()))?;
        Ok(data.claims)
    }
}

/// 密码哈希(argon2id)
pub fn hash_password(plain: &str) -> AppResult<String> {
    use argon2::{password_hash::{rand_core::OsRng, PasswordHasher, SaltString}, Argon2};
    let salt = SaltString::generate(&mut OsRng);
    let argon2 = Argon2::default();
    let hash = argon2
        .hash_password(plain.as_bytes(), &salt)
        .map_err(|e| AppError::Internal(format!("argon2 hash: {e}")))?
        .to_string();
    Ok(hash)
}

pub fn verify_password(plain: &str, hash: &str) -> bool {
    use argon2::{password_hash::{PasswordHash, PasswordVerifier}, Argon2};
    match PasswordHash::new(hash) {
        Ok(p) => Argon2::default()
            .verify_password(plain.as_bytes(), &p)
            .is_ok(),
        Err(_) => false,
    }
}

/// Axum middleware:校验 `X-Service-Token`
pub async fn service_token_middleware(
    State(token): State<Arc<String>>,
    req: Request,
    next: Next,
) -> Result<Response, StatusCode> {
    let provided = req
        .headers()
        .get("x-service-token")
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");
    if !constant_time_eq(provided.as_bytes(), token.as_bytes()) {
        return Err(StatusCode::UNAUTHORIZED);
    }
    Ok(next.run(req).await)
}

/// 从 header 提取 Bearer JWT 并校验(返回 UserClaims)
pub fn extract_user_bearer(codec: &JwtCodec, auth_header: Option<&HeaderMap>) -> AppResult<UserClaims> {
    let raw = auth_header
        .and_then(|h| h.get(header::AUTHORIZATION))
        .and_then(|v| v.to_str().ok())
        .ok_or_else(|| AppError::Unauthorized("missing Authorization".into()))?;
    let token = raw.strip_prefix("Bearer ").ok_or_else(|| AppError::Unauthorized("bad scheme".into()))?;
    codec.verify_user(token)
}

pub fn extract_admin_bearer(codec: &JwtCodec, auth_header: Option<&HeaderMap>) -> AppResult<AdminClaims> {
    let raw = auth_header
        .and_then(|h| h.get(header::AUTHORIZATION))
        .and_then(|v| v.to_str().ok())
        .ok_or_else(|| AppError::Unauthorized("missing Authorization".into()))?;
    let token = raw.strip_prefix("Bearer ").ok_or_else(|| AppError::Unauthorized("bad scheme".into()))?;
    codec.verify_admin(token)
}

/// 微信支付 V3 签名校验(技术规格 § 9.4)
pub fn verify_wechat_v3_signature(
    payload: &str,
    timestamp: &str,
    nonce: &str,
    signature_b64: &str,
    api_key: &str,
) -> bool {
    let s = format!("{timestamp}\n{nonce}\n{payload}\n");
    let expected = hmac_sha256_b64(api_key.as_bytes(), s.as_bytes());
    constant_time_eq(expected.as_bytes(), signature_b64.as_bytes())
}

/// HMAC-SHA256(用于 Webhook HMAC 签名,技术规格 § 9.5)
pub fn hmac_sha256_hex(secret: &[u8], msg: &[u8]) -> String {
    let bytes = hmac_sha256(secret, msg);
    hex::encode(bytes)
}

/// HMAC-SHA256 返回字节
pub fn hmac_sha256(secret: &[u8], msg: &[u8]) -> Vec<u8> {
    use hmac::{Hmac, Mac};
    let mut mac = <Hmac<sha2::Sha256> as Mac>::new_from_slice(secret)
        .expect("hmac key length");
    mac.update(msg);
    mac.finalize().into_bytes().to_vec()
}

/// HMAC-SHA256 base64 输出
pub fn hmac_sha256_b64(secret: &[u8], msg: &[u8]) -> String {
    use base64::Engine;
    let bytes = hmac_sha256(secret, msg);
    base64::engine::general_purpose::STANDARD.encode(bytes)
}

pub fn constant_time_eq(a: &[u8], b: &[u8]) -> bool {
    if a.len() != b.len() {
        return false;
    }
    let mut diff = 0u8;
    for (x, y) in a.iter().zip(b.iter()) {
        diff |= x ^ y;
    }
    diff == 0
}

/// Axum extractor:用户 JWT
pub struct AuthUser(pub UserClaims);

#[async_trait::async_trait]
impl<S> FromRequestParts<S> for AuthUser
where
    S: Send + Sync,
{
    type Rejection = common_error::AppError;
    async fn from_request_parts(parts: &mut axum::http::request::Parts, state: &S) -> Result<Self, Self::Rejection> {
        UserClaims::from_request_parts(parts, state).await.map(AuthUser)
    }
}

/// Axum extractor:管理员 JWT
pub struct AuthAdmin(pub AdminClaims);

#[async_trait::async_trait]
impl<S> FromRequestParts<S> for AuthAdmin
where
    S: Send + Sync,
{
    type Rejection = common_error::AppError;
    async fn from_request_parts(parts: &mut axum::http::request::Parts, state: &S) -> Result<Self, Self::Rejection> {
        AdminClaims::from_request_parts(parts, state).await.map(AuthAdmin)
    }
}

// 也让裸 Claims 自身作为 extractor(便于在路由层直接 `_claims: UserClaims`)
#[async_trait::async_trait]
impl<S> FromRequestParts<S> for UserClaims
where
    S: Send + Sync,
{
    type Rejection = common_error::AppError;
    async fn from_request_parts(parts: &mut axum::http::request::Parts, state: &S) -> Result<Self, Self::Rejection> {
        // JWT middleware has already verified these claims before inserting them.
        if let Some(claims) = parts.extensions.get::<UserClaims>() {
            return Ok(claims.clone());
        }
        let codec: axum::Extension<Arc<JwtCodec>> = axum::Extension::from_request_parts(parts, state)
            .await
            .map_err(|_| AppError::Unauthorized("no jwt codec".into()))?;
        extract_user_bearer(&codec, Some(&parts.headers))
    }
}

#[async_trait::async_trait]
impl<S> FromRequestParts<S> for AdminClaims
where
    S: Send + Sync,
{
    type Rejection = common_error::AppError;
    async fn from_request_parts(parts: &mut axum::http::request::Parts, state: &S) -> Result<Self, Self::Rejection> {
        // JWT middleware has already verified these claims before inserting them.
        if let Some(claims) = parts.extensions.get::<AdminClaims>() {
            return Ok(claims.clone());
        }
        let codec: axum::Extension<Arc<JwtCodec>> = axum::Extension::from_request_parts(parts, state)
            .await
            .map_err(|_| AppError::Unauthorized("no jwt codec".into()))?;
        extract_admin_bearer(&codec, Some(&parts.headers))
    }
}

/// 给 axum::Extension 提供 State 引用
pub mod refs {
    use std::sync::Arc;
    use super::JwtCodec;
    use axum::{
        extract::{Request, State},
        http::StatusCode,
        middleware::Next,
        response::Response,
    };
    use crate::constant_time_eq;

    /// 服务间 token 校验 middleware(State = Arc<String>)
    pub async fn internal_token_mw(
        State(token): State<Arc<String>>,
        req: Request,
        next: Next,
    ) -> Result<Response, StatusCode> {
        let provided = req
            .headers()
            .get("x-service-token")
            .and_then(|v| v.to_str().ok())
            .unwrap_or("");
        if !constant_time_eq(provided.as_bytes(), token.as_bytes()) {
            return Err(StatusCode::UNAUTHORIZED);
        }
        Ok(next.run(req).await)
    }

    /// 用户 JWT 校验 middleware(State = Arc<JwtCodec>)
    pub async fn require_user_jwt(
        State(codec): State<Arc<JwtCodec>>,
        mut req: Request,
        next: Next,
    ) -> Result<Response, StatusCode> {
        let raw = req
            .headers()
            .get(axum::http::header::AUTHORIZATION)
            .and_then(|v| v.to_str().ok())
            .unwrap_or("");
        let token = match raw.strip_prefix("Bearer ") {
            Some(t) => t,
            None => return Err(StatusCode::UNAUTHORIZED),
        };
        match codec.verify_user(token) {
            Ok(claims) => {
                req.extensions_mut().insert(claims);
                Ok(next.run(req).await)
            }
            Err(_) => Err(StatusCode::UNAUTHORIZED),
        }
    }

    /// 管理员 JWT 校验 middleware
    pub async fn require_admin_jwt(
        State(codec): State<Arc<JwtCodec>>,
        mut req: Request,
        next: Next,
    ) -> Result<Response, StatusCode> {
        let raw = req
            .headers()
            .get(axum::http::header::AUTHORIZATION)
            .and_then(|v| v.to_str().ok())
            .unwrap_or("");
        let token = match raw.strip_prefix("Bearer ") {
            Some(t) => t,
            None => return Err(StatusCode::UNAUTHORIZED),
        };
        match codec.verify_admin(token) {
            Ok(claims) => {
                req.extensions_mut().insert(claims);
                Ok(next.run(req).await)
            }
            Err(_) => Err(StatusCode::UNAUTHORIZED),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn password_roundtrip() {
        let h = hash_password("hello-world").unwrap();
        assert!(verify_password("hello-world", &h));
        assert!(!verify_password("hello-world-wrong", &h));
    }

    #[test]
    fn hmac_deterministic() {
        let a = hmac_sha256_hex(b"secret", b"msg");
        let b = hmac_sha256_hex(b"secret", b"msg");
        assert_eq!(a, b);
    }

    #[test]
    fn constant_time_eq_works() {
        assert!(constant_time_eq(b"abc", b"abc"));
        assert!(!constant_time_eq(b"abc", b"abd"));
        assert!(!constant_time_eq(b"abc", b"abcd"));
    }

    #[test]
    fn jwt_roundtrip() {
        let cfg = AuthConfig {
            jwt_secret: "test-secret".into(),
            jwt_issuer: "test".into(),
            jwt_ttl_secs: 3600,
            service_token: "svc".into(),
        };
        let codec = JwtCodec::new(&cfg);
        let token = codec.issue_user("openid-1", 42).unwrap();
        let claims = codec.verify_user(&token).unwrap();
        assert_eq!(claims.user_id, 42);
        assert_eq!(claims.sub, "openid-1");
        assert_eq!(claims.iss, "test");
    }
}
