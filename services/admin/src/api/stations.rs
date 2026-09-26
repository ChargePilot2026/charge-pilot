//! 站点 CRUD

use crate::AppState;
use axum::{extract::{Path, Query, State}, Json};
use common_auth::AdminClaims;
use common_error::{AppError, AppResult};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

async fn authorize(st:&AppState, actor:&AdminClaims, permission:&str)->AppResult<()> {
    let allowed: bool=sqlx::query_scalar(
        "SELECT EXISTS(SELECT 1 FROM admin_user_role a JOIN role r ON r.id=a.role_id          JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id          WHERE a.id=? AND a.username=? AND a.status='active' AND a.deleted_at IS NULL          AND r.deleted_at IS NULL AND p.code=?)"
    ).bind(actor.admin_user_id).bind(&actor.sub).bind(permission).fetch_one(st.db.pool()).await?;
    if !allowed {return Err(AppError::Forbidden(format!("缺少 {permission} 权限")));}
    Ok(())
}

#[derive(Debug,Deserialize)]
#[serde(deny_unknown_fields)]
pub struct StationQuery { page:Option<u32>,page_size:Option<u32>,keyword:Option<String>,status:Option<String> }

pub async fn list(State(st): State<AppState>, actor: AdminClaims, Query(q):Query<StationQuery>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    authorize(&st,&actor,"station.read").await?;
    let page=q.page.unwrap_or(1);let page_size=q.page_size.unwrap_or(20);
    if page==0 || !(1..=100).contains(&page_size){return Err(AppError::BadRequest("分页参数无效".into()));}
    let keyword=q.keyword.as_deref().map(str::trim).filter(|v|!v.is_empty());
    let status=q.status.as_deref().filter(|v|!v.is_empty());
    validate_text(keyword,128,false)?;
    validate_fields(None,None,status,None,None,None)?;
    let filter=" WHERE deleted_at IS NULL AND (? IS NULL OR LOCATE(?,code)>0 OR LOCATE(?,name)>0 OR LOCATE(?,address)>0) AND (? IS NULL OR status=?)";
    let mut tx=st.db.pool().begin().await?;
    let total:i64=sqlx::query_scalar(&format!("SELECT COUNT(*) FROM station{filter}"))
        .bind(keyword).bind(keyword).bind(keyword).bind(keyword).bind(status).bind(status).fetch_one(&mut *tx).await?;
    let rows = sqlx::query(&format!(
        "SELECT id,code,name,address,longitude+0e0 AS longitude,latitude+0e0 AS latitude,status,open_hours,contact_phone,pricing_template_id,split_template_id FROM station{filter} ORDER BY id DESC LIMIT ? OFFSET ?"
    )).bind(keyword).bind(keyword).bind(keyword).bind(keyword).bind(status).bind(status)
        .bind(page_size).bind(u64::from(page-1)*u64::from(page_size)).fetch_all(&mut *tx).await?;
    let items: Vec<Value> = rows.iter().map(|r| -> AppResult<Value> { Ok(json!({
        "id": sqlx::Row::try_get::<u64, _>(r,"id")?,
        "code": sqlx::Row::try_get::<String, _>(r,"code")?,
        "name": sqlx::Row::try_get::<String, _>(r,"name")?,
        "address": sqlx::Row::try_get::<Option<String>, _>(r,"address")?,
        "longitude": sqlx::Row::try_get::<f64, _>(r,"longitude")?,
        "latitude": sqlx::Row::try_get::<f64, _>(r,"latitude")?,
        "status": sqlx::Row::try_get::<String, _>(r,"status")?,
        "open_hours": sqlx::Row::try_get::<Option<String>, _>(r,"open_hours")?,
        "contact_phone": sqlx::Row::try_get::<Option<String>, _>(r,"contact_phone")?,
        "pricing_template_id": sqlx::Row::try_get::<Option<u64>, _>(r,"pricing_template_id")?,
        "split_template_id": sqlx::Row::try_get::<Option<u64>, _>(r,"split_template_id")?,
    })) }).collect::<AppResult<_>>()?;
    tx.commit().await?;
    let permissions:Vec<String>=sqlx::query_scalar(
        "SELECT p.code FROM admin_user_role a JOIN role r ON r.id=a.role_id          JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id          WHERE a.id=? AND a.status='active' AND a.deleted_at IS NULL AND r.deleted_at IS NULL          AND p.code IN ('station.read','station.create','station.update','station.delete') ORDER BY p.code"
    ).bind(actor.admin_user_id).fetch_all(st.db.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"items": items,"permissions":permissions,"total":total,"page":page,"page_size":page_size}), common_error::current_request_id())))
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct StationCreateReq {
    pub code: String,
    pub status: Option<String>,
    pub name: String,
    pub address: Option<String>,
    pub longitude: f64,
    pub latitude: f64,
    pub open_hours: Option<String>,
    pub contact_phone: Option<String>,
    pub pricing_template_id: Option<u64>,
    pub split_template_id: Option<u64>,
}

pub async fn create(State(st): State<AppState>, actor: AdminClaims, Json(req): Json<StationCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    authorize(&st,&actor,"station.create").await?;
    validate_text(Some(&req.code),64,true)?; validate_text(Some(&req.name),128,true)?;
    validate_fields(req.longitude.into(),req.latitude.into(),req.status.as_deref(),req.address.as_deref(),req.open_hours.as_deref(),req.contact_phone.as_deref())?;
    let mut tx=st.db.pool().begin().await?;
    sqlx::query("INSERT INTO station_code_identity (code) VALUES (?) ON DUPLICATE KEY UPDATE code=VALUES(code)").bind(&req.code).execute(&mut *tx).await?;
    let exists: bool=sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM station WHERE code=? AND deleted_at IS NULL)").bind(&req.code).fetch_one(&mut *tx).await?;
    if exists {return Err(AppError::Conflict("站点编码已存在".into()));}
    let id = sqlx::query(
        "INSERT INTO station (code, name, address, longitude, latitude, open_hours, contact_phone, pricing_template_id, split_template_id,status)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
    )
    .bind(&req.code).bind(&req.name).bind(req.address.as_deref())
    .bind(req.longitude).bind(req.latitude)
    .bind(req.open_hours.as_deref()).bind(req.contact_phone.as_deref())
    .bind(req.pricing_template_id).bind(req.split_template_id)
    .bind(req.status.as_deref().unwrap_or("active"))
    .execute(&mut *tx).await?.last_insert_id();
    audit(&mut tx,actor.admin_user_id,id,"create",serde_json::to_value(&req)?).await?;
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"id": id}), common_error::current_request_id())))
}

pub async fn get(State(st): State<AppState>, actor: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    authorize(&st,&actor,"station.read").await?;
    let r: Option<(u64, String, String, Option<String>, f64, f64, String)> = sqlx::query_as(
        "SELECT id, code, name, address, longitude+0e0, latitude+0e0, status FROM station WHERE id = ? AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.db.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("station".into()))?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({
        "id": r.0, "code": r.1, "name": r.2, "address": r.3,
        "longitude": r.4, "latitude": r.5, "status": r.6,
    }), common_error::current_request_id())))
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct StationUpdateReq {
    pub name: Option<String>,
    pub address: Option<String>,
    pub longitude: Option<f64>,
    pub latitude: Option<f64>,
    pub status: Option<String>,
    pub contact_phone: Option<String>,
    pub open_hours: Option<String>,
    pub pricing_template_id: Option<u64>,
    pub split_template_id: Option<u64>,
}

pub async fn update(State(st): State<AppState>, actor: AdminClaims, Path(id): Path<u64>, Json(req): Json<StationUpdateReq>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    authorize(&st,&actor,"station.update").await?;
    validate_text(req.name.as_deref(),128,true)?;
    validate_fields(req.longitude,req.latitude,req.status.as_deref(),req.address.as_deref(),req.open_hours.as_deref(),req.contact_phone.as_deref())?;
    let mut tx=st.db.pool().begin().await?;
    let exists: Option<u64>=sqlx::query_scalar("SELECT id FROM station WHERE id=? AND deleted_at IS NULL FOR UPDATE").bind(id).fetch_optional(&mut *tx).await?;
    if exists.is_none(){return Err(AppError::NotFound("station".into()));}
    sqlx::query(
        "UPDATE station SET
            name = COALESCE(?, name),
            address = NULLIF(COALESCE(?, address),''),
            longitude = COALESCE(?, longitude),
            latitude = COALESCE(?, latitude),
            status = COALESCE(?, status),
            open_hours = NULLIF(COALESCE(?, open_hours),''),
            contact_phone = NULLIF(COALESCE(?, contact_phone),''),
            pricing_template_id = COALESCE(?, pricing_template_id),
            split_template_id = COALESCE(?, split_template_id)
         WHERE id = ? AND deleted_at IS NULL"
    )
    .bind(req.name.as_deref()).bind(req.address.as_deref())
    .bind(req.longitude).bind(req.latitude)
    .bind(req.status.as_deref()).bind(req.open_hours.as_deref()).bind(req.contact_phone.as_deref())
    .bind(req.pricing_template_id).bind(req.split_template_id)
    .bind(id).execute(&mut *tx).await?;
    audit(&mut tx,actor.admin_user_id,id,"update",serde_json::to_value(&req)?).await?;
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(json!({"updated": true}), common_error::current_request_id())))
}

pub async fn delete(State(st): State<AppState>, actor: AdminClaims, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<Value>>> {
    authorize(&st,&actor,"station.delete").await?;
    let n = sqlx::query("UPDATE station SET deleted_at = NOW(3), deleted_by = ? WHERE id = ? AND deleted_at IS NULL")
        .bind(actor.admin_user_id).bind(id).execute(st.db.pool()).await?;
    if n.rows_affected() == 0 { return Err(AppError::NotFound("station".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(json!({"deleted": true}), common_error::current_request_id())))
}
fn validate_text(value:Option<&str>,max:usize,nonempty:bool)->AppResult<()> {
    if let Some(value)=value {if value.chars().count()>max || value.chars().any(char::is_control) || (nonempty && value.trim().is_empty()) {return Err(AppError::BadRequest("站点文本为空、过长或包含控制字符".into()));}}
    Ok(())
}
fn validate_fields(lng:Option<f64>,lat:Option<f64>,status:Option<&str>,address:Option<&str>,hours:Option<&str>,phone:Option<&str>)->AppResult<()> {
    if lng.is_some_and(|v| !v.is_finite() || !(-180.0..=180.0).contains(&v)) || lat.is_some_and(|v| !v.is_finite() || !(-90.0..=90.0).contains(&v)) {return Err(AppError::BadRequest("经纬度超出范围".into()));}
    if status.is_some_and(|v| !["active","disabled","construction"].contains(&v)) {return Err(AppError::BadRequest("站点状态无效".into()));}
    validate_text(address,255,false)?;validate_text(hours,64,false)?;validate_text(phone,32,false)
}
async fn audit(tx:&mut sqlx::Transaction<'_,sqlx::MySql>,actor:u64,id:u64,action:&str,data:Value)->AppResult<()> {
    sqlx::query("INSERT INTO audit_log (actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'station',?,'station',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
        .bind(actor).bind(action).bind(id.to_string()).bind(data).execute(&mut **tx).await?;Ok(())
}
