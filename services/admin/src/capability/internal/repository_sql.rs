//! internal 域的 repository 层 —— SQL 只允许出现在这里
//!
//! (方案 §三:handler / usecase / domain 层禁 SQL,由 clippy disallowed-methods 保证)
//! 本域只留**与其它端点 SQL 口径不同**的两个站点读:`stations_nearby` 用
//! "纬度带粗筛 + Rust 侧算距离"的近似法,与 `api::station_reads::nearby`
//! 的球面公式是两套实现,不可合并。

#![allow(clippy::disallowed_methods)]

use crate::AppState;
use common_error::{AppError, AppResult};

#[derive(Debug, serde::Deserialize)]
pub struct NearbyQuery {
    pub lat: f64,
    pub lng: f64,
    pub radius_km: Option<f64>,
}

/// 简单边界:经纬度 ± 0.1 度近似筛选(注释原文如此 —— 实际是 ± 1.0 度)。
pub async fn stations_nearby(
    st: &AppState,
    radius: f64,
    lat: f64,
    lng: f64,
) -> AppResult<Vec<api_contracts::admin::NearbyStation>> {
    let rows = sqlx::query(
        "SELECT id, code, name, address, longitude, latitude
         FROM station
         WHERE status = 'active' AND deleted_at IS NULL
           AND latitude BETWEEN ? AND ?
           AND longitude BETWEEN ? AND ?
         LIMIT 100",
    )
    .bind(lat - 1.0).bind(lat + 1.0)
    .bind(lng - 1.0).bind(lng + 1.0)
    .fetch_all(st.device.pool()).await?;
    let items: Vec<Option<api_contracts::admin::NearbyStation>> = rows.iter().map(|r| -> AppResult<Option<api_contracts::admin::NearbyStation>> {
        let slat = sqlx::Row::try_get::<f64, _>(r, "latitude")?;
        let slng = sqlx::Row::try_get::<f64, _>(r, "longitude")?;
        let dist = ((slat - lat).powi(2) + (slng - lng).powi(2)).sqrt() * 111.0; // 近似 km
        if dist > radius { return Ok(None); }
        Ok(Some(api_contracts::admin::NearbyStation {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            code: sqlx::Row::try_get::<String, _>(r, "code")?,
            name: sqlx::Row::try_get::<String, _>(r, "name")?,
            address: sqlx::Row::try_get::<Option<String>, _>(r, "address")?,
            longitude: slng,
            latitude: slat,
            distance_km: dist,
        }))
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(items.into_iter().flatten().collect())
}

/// D18 修复:SQL 列序为 (…, longitude, latitude, …) 即 r.4=longitude、r.5=latitude,
/// 原实现写成 longitude: r.5 / latitude: r.4,经纬度颠倒。这里显式按名赋值。
pub async fn station_for_user(
    st: &AppState,
    id: u64,
) -> AppResult<api_contracts::admin::StationForUser> {
    let r: Option<(u64, String, String, Option<String>, f64, f64, String)> = sqlx::query_as(
        "SELECT id, code, name, address, longitude, latitude, status FROM station WHERE id = ? AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.device.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("station".into()))?;
    Ok(api_contracts::admin::StationForUser {
        id: r.0,
        code: r.1,
        name: r.2,
        address: r.3,
        longitude: r.4,
        latitude: r.5,
        status: r.6,
    })
}
