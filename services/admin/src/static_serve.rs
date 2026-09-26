//! PC 后台 React SPA 静态资源服务
//!
//! 实际部署:把 admin-web/dist 挂到 /app/static,fallback 返回 index.html

use axum::{
    body::Body,
    extract::Request,
    http::{header, StatusCode},
    response::{IntoResponse, Response},
};
use std::path::PathBuf;
use tokio::fs;
use tracing::warn;

static SPA_DIR: &str = "/app/static";

pub async fn serve_spa(req: Request) -> Response {
    let path = req.uri().path();
    let target = path.trim_start_matches('/');
    // 默认 fallback 到 index.html
    let resolved_path = if target.is_empty() {
        "index.html".to_string()
    } else {
        target.to_string()
    };

    let file_path = PathBuf::from(SPA_DIR).join(&resolved_path);
    // 路径穿越防护
    if !file_path.starts_with(SPA_DIR) {
        return (StatusCode::BAD_REQUEST, "bad path").into_response();
    }

    match fs::read(&file_path).await {
        Ok(bytes) => {
            let mime = mime_guess(&resolved_path);
            let mut resp = Response::new(Body::from(bytes));
            resp.headers_mut().insert(header::CONTENT_TYPE, mime.parse().unwrap_or_else(|_| "application/octet-stream".parse().unwrap()));
            resp.headers_mut().insert(header::CACHE_CONTROL, if resolved_path == "index.html" {
                "no-cache".parse().unwrap()
            } else {
                "public, max-age=86400".parse().unwrap()
            });
            resp
        }
        Err(_) => {
            // SPA fallback:返回 index.html
            let index = PathBuf::from(SPA_DIR).join("index.html");
            match fs::read(&index).await {
                Ok(bytes) => {
                    let mut resp = Response::new(Body::from(bytes));
                    resp.headers_mut().insert(header::CONTENT_TYPE, "text/html; charset=utf-8".parse().unwrap());
                    resp.headers_mut().insert(header::CACHE_CONTROL, "no-cache".parse().unwrap());
                    resp
                }
                Err(_) => {
                    warn!(path = %file_path.display(), "SPA assets not found, returning 404");
                    (StatusCode::NOT_FOUND, "ChargePilot admin SPA not built yet. Run: cd admin-web && npm run build").into_response()
                }
            }
        }
    }
}

fn mime_guess(p: &str) -> &'static str {
    match p.rsplit('.').next().unwrap_or("") {
        "html" => "text/html; charset=utf-8",
        "js" | "mjs" => "application/javascript; charset=utf-8",
        "css" => "text/css; charset=utf-8",
        "json" => "application/json; charset=utf-8",
        "svg" => "image/svg+xml",
        "png" => "image/png",
        "jpg" | "jpeg" => "image/jpeg",
        "ico" => "image/x-icon",
        "woff2" => "font/woff2",
        "woff" => "font/woff",
        _ => "application/octet-stream",
    }
}