//! 单例白标配置：字段名与 API 文档一致，写入总是更新 id=1。

use crate::AppState;
use axum::{extract::State, Json};
use crate::auth::ActiveAdmin;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use serde_json::{json, Value};
use sqlx::Row;

async fn require_permission(st: &AppState, c: &ActiveAdmin, permission: &str) -> AppResult<()> {
    let allowed: bool = sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM admin_user_role a JOIN role r ON r.id=a.role_id AND r.deleted_at IS NULL
         JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id
         WHERE a.id=? AND a.username=? AND a.status='active' AND a.deleted_at IS NULL AND p.code=?)",
    ).bind(c.admin_user_id).bind(&c.sub).bind(permission).fetch_one(st.db.pool()).await?;
    if !allowed { return Err(AppError::Forbidden(format!("缺少 {permission} 权限"))); }
    Ok(())
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct WhitelabelUpdate {
    pub miniprogram_name: String,
    pub miniprogram_logo_url: Option<String>,
    pub admin_logo_url: Option<String>,
    pub theme_color: String,
    pub service_phone: Option<String>,
    pub service_wechat_id: Option<String>,
    pub icp_record_no: Option<String>,
    pub custom_domain: Option<String>,
    pub agreement_url: Option<String>,
    pub privacy_url: Option<String>,
    pub about_us: Option<String>,
}

fn valid_https(value: &Option<String>) -> bool {
    value.as_deref().is_none_or(|s| {
        s.trim().is_empty() || (s.len() <= 512 && !s.chars().any(char::is_control)
            && reqwest::Url::parse(s).is_ok_and(|url| url.scheme() == "https" && url.host_str().is_some()))
    })
}

fn clean_optional(value: &mut Option<String>) {
    *value = value.take().map(|s| s.trim().to_owned()).filter(|s| !s.is_empty());
}

fn validate(req: &WhitelabelUpdate) -> AppResult<()> {
    let name = req.miniprogram_name.trim();
    if name.is_empty() || name.chars().count() > 64 || name.chars().any(char::is_control) {
        return Err(AppError::BadRequest("小程序名称不能为空且最多 64 字".into()));
    }
    if !(req.theme_color.len() == 7 || req.theme_color.len() == 9)
        || !req.theme_color.starts_with('#')
        || !req.theme_color[1..].bytes().all(|b| b.is_ascii_hexdigit())
    {
        return Err(AppError::BadRequest("主题色必须为 #RRGGBB 或 #RRGGBBAA".into()));
    }
    if !valid_https(&req.miniprogram_logo_url) || !valid_https(&req.admin_logo_url)
        || !valid_https(&req.agreement_url) || !valid_https(&req.privacy_url)
    {
        return Err(AppError::BadRequest("Logo、协议和隐私链接必须使用 HTTPS".into()));
    }
    if req.service_phone.as_deref().is_some_and(|s| s.len() > 32 || s.chars().any(char::is_control))
        || req.service_wechat_id.as_deref().is_some_and(|s| s.trim().is_empty() || s.len() > 64 || s.chars().any(char::is_control))
        || req.icp_record_no.as_deref().is_some_and(|s| s.len() > 128 || s.chars().any(char::is_control))
        || req.about_us.as_deref().is_some_and(|s| s.chars().count() > 20_000 || s.chars().any(|c| c.is_control() && c != '\n' && c != '\r' && c != '\t'))
    {
        return Err(AppError::BadRequest("白标联系方式、备案信息或介绍内容无效".into()));
    }
    if req.custom_domain.as_deref().is_some_and(|domain| {
        domain.is_empty() || domain.len() > 253 || !domain.contains('.')
            || !domain.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'.' || b == b'-')
            || domain.split('.').any(|part| part.is_empty() || part.starts_with('-') || part.ends_with('-'))
    }) {
        return Err(AppError::BadRequest("自定义域名格式无效".into()));
    }
    Ok(())
}

fn public_config(row: &sqlx::mysql::MySqlRow) -> AppResult<api_contracts::admin::WhitelabelPublicConfig> {
    let extra: Option<Value> = row.try_get("config_json")?;
    let extra = extra.unwrap_or_else(|| json!({}));
    let pick = |key: &str| extra.get(key).cloned().unwrap_or(Value::Null);
    Ok(api_contracts::admin::WhitelabelPublicConfig {
        id: row.try_get::<u64, _>("id")?,
        miniprogram_name: row.try_get::<String, _>("mini_program_name")?,
        miniprogram_logo_url: row.try_get::<Option<String>, _>("logo_url")?,
        admin_logo_url: pick("admin_logo_url"),
        theme_color: row.try_get::<Option<String>, _>("theme_color")?,
        service_phone: row.try_get::<Option<String>, _>("contact_phone")?,
        service_wechat_id: pick("service_wechat_id"),
        icp_record_no: pick("icp_record_no"),
        custom_domain: pick("custom_domain"),
        agreement_url: pick("agreement_url"),
        privacy_url: pick("privacy_url"),
        about_us: row.try_get::<Option<String>, _>("about_text")?,
    })
}

pub async fn get(State(st): State<AppState>, c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::WhitelabelPublicConfig>>> {
    require_permission(&st, &c, "whitelabel.read").await?;
    let row = sqlx::query("SELECT id, mini_program_name, logo_url, theme_color, contact_phone, about_text, config_json FROM whitelabel_config WHERE id=1")
        .fetch_optional(st.db.pool()).await?;
    let config = match row {
        Some(row) => public_config(&row)?,
        None => {
            // 回退到最近一行;一行都没有时回默认视图(历史实现回空对象 `{}`)。
            let latest = sqlx::query("SELECT id, mini_program_name, logo_url, theme_color, contact_phone, about_text, config_json FROM whitelabel_config ORDER BY id DESC LIMIT 1")
                .fetch_optional(st.db.pool()).await?;
            match latest { Some(row) => public_config(&row)?, None => api_contracts::admin::WhitelabelPublicConfig::unset() }
        },
    };
    Ok(Json(common_error::ApiEnvelope::ok(config, common_error::current_request_id())))
}

pub async fn put(
    State(st): State<AppState>, c: ActiveAdmin, Json(mut req): Json<WhitelabelUpdate>,
) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::WhitelabelSaved>>> {
    crate::auth::require_permission(&st, &c, "whitelabel.update").await?;
    clean_optional(&mut req.miniprogram_logo_url);
    clean_optional(&mut req.admin_logo_url);
    clean_optional(&mut req.service_phone);
    clean_optional(&mut req.service_wechat_id);
    clean_optional(&mut req.icp_record_no);
    clean_optional(&mut req.custom_domain);
    clean_optional(&mut req.agreement_url);
    clean_optional(&mut req.privacy_url);
    require_permission(&st, &c, "whitelabel.update").await?;
    validate(&req)?;
    let mut tx = st.db.pool().begin().await?;
    let old = sqlx::query("SELECT id, mini_program_name, logo_url, theme_color, contact_phone, about_text, config_json FROM whitelabel_config WHERE id=1 FOR UPDATE")
        .fetch_optional(&mut *tx).await?;
    let before = match old { Some(row) => Some(serde_json::to_value(public_config(&row)?)?), None => Some(Value::Null) };
    let config_json = json!({
        "admin_logo_url": req.admin_logo_url.clone(),
        "service_wechat_id": req.service_wechat_id.clone(),
        "icp_record_no": req.icp_record_no.clone(),
        "custom_domain": req.custom_domain.clone(),
        "agreement_url": req.agreement_url.clone(),
        "privacy_url": req.privacy_url.clone(),
    });
    sqlx::query(
        "INSERT INTO whitelabel_config (id,name,logo_url,mini_program_name,theme_color,contact_phone,about_text,config_json)
         VALUES (1,'Default',?,?,?,?,?,?)
         ON DUPLICATE KEY UPDATE id=LAST_INSERT_ID(id), logo_url=VALUES(logo_url),
         mini_program_name=VALUES(mini_program_name), theme_color=VALUES(theme_color),
         contact_phone=VALUES(contact_phone), about_text=VALUES(about_text), config_json=VALUES(config_json)",
    )
    .bind(req.miniprogram_logo_url.as_deref())
    .bind(req.miniprogram_name.trim())
    .bind(&req.theme_color)
    .bind(req.service_phone.as_deref())
    .bind(req.about_us.as_deref())
    .bind(config_json.clone())
    .execute(&mut *tx).await?;
    let after = api_contracts::admin::WhitelabelPublicConfig {
        id: 1,
        miniprogram_name: req.miniprogram_name.trim().to_string(),
        miniprogram_logo_url: req.miniprogram_logo_url.clone(),
        // config_json 里的六项经 json! 组装,取值是 null 或字符串,
        // 故这里取出的 Value 原样塞进 Option<Value>,序列化仍是 null/字符串。
        admin_logo_url: config_json["admin_logo_url"].clone(),
        theme_color: Some(req.theme_color.clone()),
        service_phone: req.service_phone.clone(),
        service_wechat_id: config_json["service_wechat_id"].clone(),
        icp_record_no: config_json["icp_record_no"].clone(),
        custom_domain: config_json["custom_domain"].clone(),
        agreement_url: config_json["agreement_url"].clone(),
        privacy_url: config_json["privacy_url"].clone(),
        about_us: req.about_us.clone(),
    };
    sqlx::query("INSERT INTO audit_log(actor_id,module,action,target_type,target_id,before_json,after_json,created_month) VALUES (?,'settings','whitelabel.update','whitelabel_config','1',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
        .bind(c.admin_user_id).bind(before).bind(serde_json::to_value(&after)?).execute(&mut *tx).await?;
    tx.commit().await?;
    st.redis_cache.del("whitelabel:config").await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::admin::WhitelabelSaved { id: 1, config: after }, common_error::current_request_id())))
}
