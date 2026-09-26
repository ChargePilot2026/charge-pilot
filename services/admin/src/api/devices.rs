//! Admin-owned device metadata; runtime telemetry belongs to gateway.
use crate::AppState;
use axum::{extract::{Path, Query, State}, Json};
use common_auth::AdminClaims;
use common_error::{ApiEnvelope, AppError, AppResult};
use serde::Deserialize;
use serde_json::{json, Value};
use sqlx::{MySql, QueryBuilder, Row};

async fn authorize(st: &AppState, actor: &AdminClaims) -> AppResult<Vec<String>> {
    let permissions: Vec<String> = sqlx::query_scalar(
        "SELECT p.code FROM admin_user_role a JOIN role r ON r.id=a.role_id
         JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id
         WHERE a.id=? AND a.username=? AND a.status='active' AND a.deleted_at IS NULL
         AND r.deleted_at IS NULL AND p.code IN ('device.read','device.import')"
    ).bind(actor.admin_user_id).bind(&actor.sub).fetch_all(st.db.pool()).await?;
    if !permissions.iter().any(|p| p == "device.read") {
        return Err(AppError::Forbidden("缺少 device.read 权限".into()));
    }
    Ok(permissions)
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct DeviceQuery {
    page: Option<u32>, page_size: Option<u32>, keyword: Option<String>,
    status: Option<String>, station_id: Option<u64>, vendor_id: Option<u64>,
}
impl DeviceQuery {
    fn validate(&self) -> AppResult<(u32,u32)> {
        let page=self.page.unwrap_or(1); let size=self.page_size.unwrap_or(20);
        if page==0 || !(1..=100).contains(&size)
            || self.station_id==Some(0) || self.vendor_id==Some(0)
            || self.keyword.as_ref().is_some_and(|s| s.chars().count()>128 || s.chars().any(char::is_control))
            || self.status.as_deref().is_some_and(|s| !["","enabled","disabled","retired","fault"].contains(&s)) {
            return Err(AppError::BadRequest("设备查询参数无效".into()));
        }
        Ok((page,size))
    }
    fn filter<'a>(&'a self, sql: &mut QueryBuilder<'a,MySql>) {
        sql.push(" WHERE d.deleted_at IS NULL");
        if let Some(keyword)=self.keyword.as_deref().map(str::trim).filter(|s| !s.is_empty()) {
            sql.push(" AND (LOCATE(").push_bind(keyword).push(",d.device_id)>0 OR LOCATE(")
                .push_bind(keyword).push(",d.model)>0 OR LOCATE(").push_bind(keyword)
                .push(",s.name)>0 OR LOCATE(").push_bind(keyword).push(",s.code)>0)");
        }
        if let Some(status)=self.status.as_deref().filter(|s| !s.is_empty()) { sql.push(" AND d.status=").push_bind(status); }
        if let Some(id)=self.station_id { sql.push(" AND d.station_id=").push_bind(id); }
        if let Some(id)=self.vendor_id { sql.push(" AND d.vendor_id=").push_bind(id); }
    }
}
const FROM: &str=" FROM device_meta d LEFT JOIN station s ON s.id=d.station_id AND s.deleted_at IS NULL";
const COLUMNS: &str="SELECT d.id,d.device_id,d.station_id,s.name AS station_name,s.code AS station_code,d.vendor_id,d.model,d.status,d.install_at";
fn device(r: &sqlx::mysql::MySqlRow) -> AppResult<Value> {
    Ok(json!({
        "id":r.try_get::<u64,_>("id")?, "device_id":r.try_get::<String,_>("device_id")?,
        "station_id":r.try_get::<Option<u64>,_>("station_id")?,
        "station_name":r.try_get::<Option<String>,_>("station_name")?,
        "station_code":r.try_get::<Option<String>,_>("station_code")?,
        "vendor_id":r.try_get::<Option<u64>,_>("vendor_id")?, "model":r.try_get::<Option<String>,_>("model")?,
        "status":r.try_get::<String,_>("status")?,
        "install_at":r.try_get::<Option<chrono::DateTime<chrono::Utc>>,_>("install_at")?.map(|t|t.to_rfc3339()),
    }))
}
pub async fn list(State(st): State<AppState>, actor: AdminClaims, Query(q): Query<DeviceQuery>) -> AppResult<Json<ApiEnvelope<Value>>> {
    let permissions=authorize(&st,&actor).await?;
    let (page,size)=q.validate()?;
    let mut tx=st.db.pool().begin().await?;
    let mut count=QueryBuilder::<MySql>::new(format!("SELECT COUNT(*){FROM}")); q.filter(&mut count);
    let total:i64=count.build_query_scalar().fetch_one(&mut *tx).await?;
    let mut sql=QueryBuilder::<MySql>::new(format!("{COLUMNS}{FROM}")); q.filter(&mut sql);
    sql.push(" ORDER BY d.id DESC LIMIT ").push_bind(size).push(" OFFSET ").push_bind(u64::from(page-1)*u64::from(size));
    let rows=sql.build().fetch_all(&mut *tx).await?;
    let items=rows.iter().map(device).collect::<AppResult<Vec<_>>>()?;
    tx.commit().await?;
    Ok(Json(ApiEnvelope::ok(json!({"items":items,"total":total,"page":page,"page_size":size,"permissions":permissions}),common_error::current_request_id())))
}
pub async fn get(State(st): State<AppState>, actor: AdminClaims, Path(id): Path<String>) -> AppResult<Json<ApiEnvelope<Value>>> {
    authorize(&st,&actor).await?;
    let row=sqlx::query(&format!("{COLUMNS}{FROM} WHERE d.device_id=? AND d.deleted_at IS NULL"))
        .bind(id).fetch_optional(st.db.pool()).await?.ok_or_else(||AppError::NotFound("device".into()))?;
    Ok(Json(ApiEnvelope::ok(device(&row)?,common_error::current_request_id())))
}
pub async fn orders(State(st): State<AppState>, actor: AdminClaims, Path(id): Path<String>, Query(mut q): Query<api_contracts::orders::OrderQuery>) -> AppResult<Json<ApiEnvelope<api_contracts::orders::OrderPage>>> {
    authorize(&st,&actor).await?;
    let exists:bool=sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM device_meta WHERE device_id=? AND deleted_at IS NULL)")
        .bind(&id).fetch_one(st.db.pool()).await?;
    if !exists {return Err(AppError::NotFound("device".into()));}
    q.device_id=Some(id);
    super::orders::list(State(st),actor,Query(q)).await
}
