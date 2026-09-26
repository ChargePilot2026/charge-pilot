//! 白标配置

use crate::AppState;
use axum::{extract::State, Json};
use common_auth::AdminClaims;
use common_error::AppResult;
use serde_json::{json, Value};

pub async fn get(State(st): State<AppState>, _c: AdminClaims) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let r = sqlx::query("SELECT id, name, logo_url, mini_program_name, mini_program_appid, theme_color, contact_phone, about_text, config_json FROM whitelabel_config ORDER BY id DESC LIMIT 1")
        .fetch_optional(st.db.pool()).await?;
    let v: Value = match r {
        None => json!({}),
        Some(r) => json!({
            "id": sqlx::Row::try_get::<u64, _>(&r, "id")?,
            "name": sqlx::Row::try_get::<String, _>(&r, "name")?,
            "logo_url": sqlx::Row::try_get::<Option<String>, _>(&r, "logo_url")?,
            "mini_program_name": sqlx::Row::try_get::<Option<String>, _>(&r, "mini_program_name")?,
            "mini_program_appid": sqlx::Row::try_get::<Option<String>, _>(&r, "mini_program_appid")?,
            "theme_color": sqlx::Row::try_get::<Option<String>, _>(&r, "theme_color")?,
            "contact_phone": sqlx::Row::try_get::<Option<String>, _>(&r, "contact_phone")?,
            "about_text": sqlx::Row::try_get::<Option<String>, _>(&r, "about_text")?,
        }),
    };
    Ok(Json(common_error::ApiEnvelope::ok(v, common_error::current_request_id())))
}

pub async fn put(State(st): State<AppState>, _c: AdminClaims, Json(req): Json<Value>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    let id: u64 = sqlx::query_scalar(
        "INSERT INTO whitelabel_config (name, logo_url, mini_program_name, mini_program_appid, theme_color, contact_phone, about_text)
         VALUES (?, ?, ?, ?, ?, ?, ?)
         ON DUPLICATE KEY UPDATE name = VALUES(name), logo_url = VALUES(logo_url), theme_color = VALUES(theme_color)"
    )
    .bind(req.get("name").and_then(|v| v.as_str()).unwrap_or("Default"))
    .bind(req.get("logo_url").and_then(|v| v.as_str()))
    .bind(req.get("mini_program_name").and_then(|v| v.as_str()))
    .bind(req.get("mini_program_appid").and_then(|v| v.as_str()))
    .bind(req.get("theme_color").and_then(|v| v.as_str()))
    .bind(req.get("contact_phone").and_then(|v| v.as_str()))
    .bind(req.get("about_text").and_then(|v| v.as_str()))
    .fetch_one(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": id}), common_error::current_request_id())))
}