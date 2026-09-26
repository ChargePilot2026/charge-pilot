use crate::AppState;
use api_contracts::pricing::DevicePricing;
use axum::{extract::{Path,State},Json};
use common_error::{ApiEnvelope,AppError,AppResult};
use sqlx::Row;

pub async fn device(State(st):State<AppState>,Path(id):Path<String>)->AppResult<Json<ApiEnvelope<DevicePricing>>> {
    let mut tx=st.db.pool().begin().await?;
    let stations:Vec<(u64,String,Option<u64>)>=sqlx::query_as(
        "SELECT s.id,s.name,s.pricing_template_id FROM device_meta d JOIN station s ON s.id=d.station_id
         WHERE d.device_id=? AND d.deleted_at IS NULL AND d.status='enabled' AND s.deleted_at IS NULL AND s.status='active'"
    ).bind(id).fetch_all(&mut *tx).await?;
    if stations.len()>1 {return Err(AppError::Conflict("设备站点配置重复".into()));}
    let (station_id,station_name,template_id)=stations.into_iter().next().ok_or_else(||AppError::NotFound("设备未绑定可运营站点".into()))?;
    let columns="SELECT id,name,version,mode,time_of_use_json,service_fee_cents_per_kwh,service_fee_cents_per_min,min_charge_cents FROM pricing_rule";
    let active="deleted_at IS NULL AND status='active' AND (effective_from IS NULL OR effective_from<=UTC_TIMESTAMP(3)) AND (effective_to IS NULL OR effective_to>UTC_TIMESTAMP(3))";
    let mut rows=sqlx::query(&format!("{columns} WHERE station_id=? AND {active}"))
        .bind(station_id).fetch_all(&mut *tx).await?;
    if rows.len()>1 {return Err(AppError::Conflict("站点同时存在多个生效计费规则".into()));}
    if rows.is_empty() {
        if let Some(template_id)=template_id {
            rows=sqlx::query(&format!("{columns} WHERE id=(SELECT default_pricing_rule_id FROM pricing_template WHERE id=? AND deleted_at IS NULL) AND (station_id IS NULL OR station_id=?) AND {active}"))
                .bind(template_id).bind(station_id).fetch_all(&mut *tx).await?;
        }
    }
    let row=rows.pop().ok_or_else(||AppError::NotFound("站点未配置生效计费规则".into()))?;
    let result=DevicePricing {station_id,station_name,rule_id:row.try_get("id")?,name:row.try_get("name")?,version:row.try_get("version")?,mode:row.try_get("mode")?,
        time_of_use:row.try_get::<Option<serde_json::Value>,_>("time_of_use_json")?.unwrap_or(serde_json::Value::Null),
        service_fee_cents_per_kwh:row.try_get("service_fee_cents_per_kwh")?,service_fee_cents_per_min:row.try_get("service_fee_cents_per_min")?,min_charge_cents:row.try_get("min_charge_cents")?};
    tx.commit().await?;
    Ok(Json(ApiEnvelope::ok(result,common_error::current_request_id())))
}
