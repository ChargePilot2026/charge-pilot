//! Exercise the same middleware and claims extractors used by protected routes.
use axum::{
    body::{to_bytes, Body},
    http::{Request, StatusCode},
    middleware,
    routing::get,
    Extension, Router,
};
use common_auth::{refs, AdminClaims, AuthAdmin, AuthUser, JwtCodec, UserClaims};
use common_config::AuthConfig;
use std::sync::Arc;
use tower::ServiceExt;

fn codec() -> Arc<JwtCodec> {
    Arc::new(JwtCodec::new(&AuthConfig {
        jwt_secret: "auth-extractor-test-secret".into(),
        jwt_issuer: "auth-extractor-test".into(),
        jwt_ttl_secs: 3600,
        service_token: "test-service".into(),
    }))
}

fn admin_routes() -> Router {
    Router::new()
        .route(
            "/claims",
            get(|c: AdminClaims| async move { c.admin_user_id.to_string() }),
        )
        .route(
            "/wrapper",
            get(|AuthAdmin(c): AuthAdmin| async move { c.admin_user_id.to_string() }),
        )
}

fn user_routes() -> Router {
    Router::new()
        .route(
            "/claims",
            get(|c: UserClaims| async move { c.user_id.to_string() }),
        )
        .route(
            "/wrapper",
            get(|AuthUser(c): AuthUser| async move { c.user_id.to_string() }),
        )
}

async fn check(app: Router, token: Option<&str>, expected: StatusCode) {
    for path in ["/claims", "/wrapper"] {
        let mut request = Request::builder().uri(path);
        if let Some(token) = token {
            request = request.header("Authorization", format!("Bearer {token}"));
        }
        let response = app
            .clone()
            .oneshot(request.body(Body::empty()).unwrap())
            .await
            .unwrap();
        assert_eq!(response.status(), expected, "{path}");
        if expected == StatusCode::OK {
            let body = to_bytes(response.into_body(), 1024).await.unwrap();
            assert_eq!(&body[..], b"42");
        }
    }
}

#[tokio::test]
async fn protected_admin_routes_accept_valid_login_and_reject_invalid_tokens() {
    let codec = codec();
    let admin_token = codec.issue_admin("admin", 42, "admin", vec![]).unwrap();
    let user_token = codec.issue_user("user", 42).unwrap();
    let app = admin_routes().layer(middleware::from_fn_with_state(
        codec,
        refs::require_admin_jwt,
    ));
    check(app.clone(), Some(&admin_token), StatusCode::OK).await;
    check(app.clone(), None, StatusCode::UNAUTHORIZED).await;
    check(app.clone(), Some("invalid"), StatusCode::UNAUTHORIZED).await;
    check(app, Some(&user_token), StatusCode::UNAUTHORIZED).await;
}

#[tokio::test]
async fn protected_user_routes_accept_valid_login_and_reject_admin_tokens() {
    let codec = codec();
    let admin_token = codec.issue_admin("admin", 42, "admin", vec![]).unwrap();
    let user_token = codec.issue_user("user", 42).unwrap();
    let app = user_routes().layer(middleware::from_fn_with_state(
        codec,
        refs::require_user_jwt,
    ));
    check(app.clone(), Some(&user_token), StatusCode::OK).await;
    check(app.clone(), None, StatusCode::UNAUTHORIZED).await;
    check(app.clone(), Some("invalid"), StatusCode::UNAUTHORIZED).await;
    check(app, Some(&admin_token), StatusCode::UNAUTHORIZED).await;
}

#[tokio::test]
async fn extractors_still_verify_tokens_with_explicit_codec_extension() {
    let codec = codec();
    let admin_token = codec.issue_admin("admin", 42, "admin", vec![]).unwrap();
    let user_token = codec.issue_user("user", 42).unwrap();
    let admin = admin_routes().layer(Extension(codec.clone()));
    let user = user_routes().layer(Extension(codec));
    check(admin.clone(), Some(&admin_token), StatusCode::OK).await;
    check(user.clone(), Some(&user_token), StatusCode::OK).await;
    check(admin, Some(&user_token), StatusCode::UNAUTHORIZED).await;
    check(user, Some(&admin_token), StatusCode::UNAUTHORIZED).await;
    check(admin_routes(), Some(&admin_token), StatusCode::UNAUTHORIZED).await;
    check(user_routes(), Some(&user_token), StatusCode::UNAUTHORIZED).await;
}
