//! device 域的 repository 层 —— SQL 只允许出现在这里
//!
//! (方案 §三:handler / usecase / domain 层禁 SQL,由 clippy disallowed-methods 保证)
//! 覆盖 `station` / `station_code_identity` / `device_meta` / `device_import` /
//! `device_import_identity` / `ota_package` / `ota_schedule`。

#![allow(clippy::disallowed_methods, clippy::disallowed_types)]

use crate::AppState;
use crate::capability::device::domain::{DEVICE_COLUMNS, DEVICE_FROM};
use common_error::{AppError, AppResult};
use serde::{Deserialize, Serialize};
use sqlx::Row;

// 上面多出的 `disallowed_types` 豁免只服务本文件里的两处 JSON 列:
// `device_import.request_json` 与 `device_import_identity.request_json`。
// 两者都是**原样透传**的数据库 JSON 列(方案 §三例外清单第 2 条):
// 写入前由 `serde_json::to_value(&DeviceProvision)` 从具名 DTO 生成,
// 读出后原样反序列回同一个 DTO,类型化反而会丢掉未知字段。

// ===== 站点 =====

/// 站点列表 / 详情的共用 WHERE 片段。
pub const STATION_FILTER: &str = " WHERE deleted_at IS NULL AND (? IS NULL OR LOCATE(?,code)>0 OR LOCATE(?,name)>0 OR LOCATE(?,address)>0) AND (? IS NULL OR status=?)";

pub async fn station_count(
    tx: &mut common_db::Tx<'_>,
    keyword: Option<&str>,
    status: Option<&str>,
) -> AppResult<i64> {
    let total: i64 = sqlx::query_scalar(&format!("SELECT COUNT(*) FROM station{STATION_FILTER}"))
        .bind(keyword).bind(keyword).bind(keyword).bind(keyword).bind(status).bind(status)
        .fetch_one(tx.executor()).await?;
    Ok(total)
}

pub async fn station_page(
    tx: &mut common_db::Tx<'_>,
    keyword: Option<&str>,
    status: Option<&str>,
    page: u32,
    page_size: u32,
) -> AppResult<Vec<sqlx::mysql::MySqlRow>> {
    Ok(sqlx::query(&format!(
        "SELECT id,code,name,address,longitude+0e0 AS longitude,latitude+0e0 AS latitude,status,open_hours,contact_phone,pricing_template_id,split_template_id FROM station{STATION_FILTER} ORDER BY id DESC LIMIT ? OFFSET ?"
    )).bind(keyword).bind(keyword).bind(keyword).bind(keyword).bind(status).bind(status)
        .bind(page_size).bind(u64::from(page-1)*u64::from(page_size))
        .fetch_all(tx.executor()).await?)
}

pub fn station_row(r: &sqlx::mysql::MySqlRow) -> AppResult<api_contracts::admin::Station> {
    Ok(api_contracts::admin::Station {
        id: sqlx::Row::try_get::<u64, _>(r,"id")?,
        code: sqlx::Row::try_get::<String, _>(r,"code")?,
        name: sqlx::Row::try_get::<String, _>(r,"name")?,
        address: sqlx::Row::try_get::<Option<String>, _>(r,"address")?,
        longitude: sqlx::Row::try_get::<f64, _>(r,"longitude")?,
        latitude: sqlx::Row::try_get::<f64, _>(r,"latitude")?,
        status: sqlx::Row::try_get::<String, _>(r,"status")?,
        open_hours: sqlx::Row::try_get::<Option<String>, _>(r,"open_hours")?,
        contact_phone: sqlx::Row::try_get::<Option<String>, _>(r,"contact_phone")?,
        pricing_template_id: sqlx::Row::try_get::<Option<u64>, _>(r,"pricing_template_id")?,
        split_template_id: sqlx::Row::try_get::<Option<u64>, _>(r,"split_template_id")?,
    })
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct StationQuery {
    pub page: Option<u32>,
    pub page_size: Option<u32>,
    pub keyword: Option<String>,
    pub status: Option<String>,
}

pub async fn station_codes(
    actor_id: u64,
    pool: &sqlx::MySqlPool,
) -> AppResult<Vec<String>> {
    Ok(sqlx::query_scalar(
        "SELECT p.code FROM admin_user_role a JOIN role r ON r.id=a.role_id          JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id          WHERE a.id=? AND a.status='active' AND a.deleted_at IS NULL AND r.deleted_at IS NULL          AND p.code IN ('station.read','station.create','station.update','station.delete') ORDER BY p.code"
    ).bind(actor_id).fetch_all(pool).await?)
}

pub async fn lock_station_code(tx: &mut common_db::Tx<'_>, code: &str) -> AppResult<()> {
    sqlx::query("INSERT INTO station_code_identity (code) VALUES (?) ON DUPLICATE KEY UPDATE code=VALUES(code)")
        .bind(code).execute(tx.executor()).await?;
    Ok(())
}

pub async fn station_code_taken(
    tx: &mut common_db::Tx<'_>,
    code: &str,
) -> AppResult<bool> {
    let exists: bool = sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM station WHERE code=? AND deleted_at IS NULL)")
        .bind(code).fetch_one(tx.executor()).await?;
    Ok(exists)
}

pub async fn insert_station(
    tx: &mut common_db::Tx<'_>,
    req: &crate::capability::device::StationCreateReq,
) -> AppResult<u64> {
    let id = sqlx::query(
        "INSERT INTO station (code, name, address, longitude, latitude, open_hours, contact_phone, pricing_template_id, split_template_id,status)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
    )
    .bind(&req.code).bind(&req.name).bind(req.address.as_deref())
    .bind(req.longitude).bind(req.latitude)
    .bind(req.open_hours.as_deref()).bind(req.contact_phone.as_deref())
    .bind(req.pricing_template_id).bind(req.split_template_id)
    .bind(req.status.as_deref().unwrap_or("active"))
    .execute(tx.executor()).await?.last_insert_id();
    Ok(id)
}

pub async fn station_detail(st: &AppState, id: u64) -> AppResult<api_contracts::admin::StationDetail> {
    let r: Option<(u64, String, String, Option<String>, f64, f64, String)> = sqlx::query_as(
        "SELECT id, code, name, address, longitude+0e0, latitude+0e0, status FROM station WHERE id = ? AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.device.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("station".into()))?;
    Ok(api_contracts::admin::StationDetail {
        id: r.0, code: r.1, name: r.2, address: r.3,
        longitude: r.4, latitude: r.5, status: r.6,
    })
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

pub async fn lock_station(tx: &mut common_db::Tx<'_>, id: u64) -> AppResult<bool> {
    let exists: Option<u64> = sqlx::query_scalar("SELECT id FROM station WHERE id=? AND deleted_at IS NULL FOR UPDATE")
        .bind(id).fetch_optional(tx.executor()).await?;
    Ok(exists.is_some())
}

pub async fn update_station(
    tx: &mut common_db::Tx<'_>,
    id: u64,
    req: &StationUpdateReq,
) -> AppResult<()> {
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
         WHERE id = ? AND deleted_at IS NULL",
    )
    .bind(req.name.as_deref()).bind(req.address.as_deref())
    .bind(req.longitude).bind(req.latitude)
    .bind(req.status.as_deref()).bind(req.open_hours.as_deref()).bind(req.contact_phone.as_deref())
    .bind(req.pricing_template_id).bind(req.split_template_id)
    .bind(id).execute(tx.executor()).await?;
    Ok(())
}

pub async fn soft_delete_station(st: &AppState, id: u64, actor_id: u64) -> AppResult<bool> {
    let n = sqlx::query("UPDATE station SET deleted_at = NOW(3), deleted_by = ? WHERE id = ? AND deleted_at IS NULL")
        .bind(actor_id).bind(id).execute(st.device.pool()).await?;
    Ok(n.rows_affected() > 0)
}

/// `audit_log.after_json` 的写入。这是数据库 JSON 列,按方案 §三例外清单
/// 第 2 条豁免 `disallowed_types` —— 载荷由具名 DTO 序列化而来。
pub async fn audit_station(
    tx: &mut common_db::Tx<'_>,
    actor: u64,
    id: u64,
    action: &str,
    data: &serde_json::Value,
) -> AppResult<()> {
    sqlx::query("INSERT INTO audit_log (actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'station',?,'station',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
        .bind(actor).bind(action).bind(id.to_string()).bind(data).execute(tx.executor()).await?;
    Ok(())
}

// ===== 站点公开读(供小程序) =====

pub async fn nearby_stations(
    st: &AppState,
    lat: f64,
    lng: f64,
    radius: f64,
) -> AppResult<Vec<api_contracts::NearbyStationItem>> {
    let rows = sqlx::query("SELECT id,code,name,address,longitude+0e0 AS longitude,latitude+0e0 AS latitude,
        6371.0088 * ACOS(LEAST(1e0,GREATEST(-1e0,SIN(RADIANS(?))*SIN(RADIANS(latitude)) + COS(RADIANS(?))*COS(RADIANS(latitude))*COS(RADIANS(longitude-?))))) AS distance_km
        FROM station WHERE status='active' AND deleted_at IS NULL AND latitude BETWEEN ? AND ?
        HAVING distance_km <= ? ORDER BY distance_km,id LIMIT 100")
        .bind(lat).bind(lat).bind(lng).bind(lat-radius/110.0).bind(lat+radius/110.0).bind(radius)
        .fetch_all(st.device.pool()).await?;
    let mut items = Vec::with_capacity(rows.len());
    for row in rows {
        items.push(api_contracts::NearbyStationItem {
            id: row.try_get("id")?,
            code: row.try_get("code")?,
            name: row.try_get("name")?,
            address: row.try_get("address")?,
            longitude: row.try_get("longitude")?,
            latitude: row.try_get("latitude")?,
            distance_km: row.try_get("distance_km")?,
        });
    }
    Ok(items)
}

pub async fn station_public_detail(
    st: &AppState,
    id: u64,
) -> AppResult<api_contracts::StationPublicDetail> {
    let row = sqlx::query("SELECT id,code,name,address,longitude+0e0 AS longitude,latitude+0e0 AS latitude,open_hours,contact_phone FROM station WHERE id=? AND status='active' AND deleted_at IS NULL")
        .bind(id).fetch_optional(st.device.pool()).await?.ok_or_else(||AppError::NotFound("站点不存在或未开放".into()))?;
    Ok(api_contracts::StationPublicDetail {
        id: row.try_get("id")?,
        code: row.try_get("code")?,
        name: row.try_get("name")?,
        address: row.try_get("address")?,
        longitude: row.try_get("longitude")?,
        latitude: row.try_get("latitude")?,
        open_hours: row.try_get("open_hours")?,
        contact_phone: row.try_get("contact_phone")?,
    })
}

// ===== 设备 =====

/// 调用者在设备域的实际权限(供列表过滤),不是布尔校验。
pub async fn device_permissions(
    st: &AppState,
    admin_user_id: u64,
    username: &str,
) -> AppResult<Vec<String>> {
    Ok(sqlx::query_scalar(
        "SELECT p.code FROM admin_user_role a JOIN role r ON r.id=a.role_id
         JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id
         WHERE a.id=? AND a.username=? AND a.status='active' AND a.deleted_at IS NULL
         AND r.deleted_at IS NULL AND p.code IN ('device.read','device.import')"
    ).bind(admin_user_id).bind(username).fetch_all(st.device.pool()).await?)
}

/// 行 → DTO。列名与 `DEVICE_COLUMNS` 对齐。
pub fn device_row(r: &sqlx::mysql::MySqlRow) -> AppResult<api_contracts::admin::DeviceRow> {
    Ok(api_contracts::admin::DeviceRow {
        id: r.try_get("id")?,
        device_id: r.try_get("device_id")?,
        station_id: r.try_get("station_id")?,
        station_name: r.try_get("station_name")?,
        station_code: r.try_get("station_code")?,
        vendor_id: r.try_get("vendor_id")?,
        model: r.try_get("model")?,
        status: r.try_get("status")?,
        install_at: r.try_get::<Option<chrono::DateTime<chrono::Utc>>,_>("install_at")?
            .map(|t| t.to_rfc3339()),
    })
}

pub async fn device_count(
    tx: &mut common_db::Tx<'_>,
    q: &crate::capability::device::domain::DeviceQuery,
) -> AppResult<i64> {
    let mut count = sqlx::QueryBuilder::<sqlx::MySql>::new(format!(
        "SELECT COUNT(*){DEVICE_FROM}"
    ));
    q.filter(&mut count);
    Ok(count.build_query_scalar().fetch_one(tx.executor()).await?)
}

pub async fn device_page(
    tx: &mut common_db::Tx<'_>,
    q: &crate::capability::device::domain::DeviceQuery,
    page: u32,
    size: u32,
) -> AppResult<Vec<sqlx::mysql::MySqlRow>> {
    let mut sql = sqlx::QueryBuilder::<sqlx::MySql>::new(format!("{DEVICE_COLUMNS}{DEVICE_FROM}"));
    q.filter(&mut sql);
    sql.push(" ORDER BY d.id DESC LIMIT ").push_bind(size)
        .push(" OFFSET ").push_bind(u64::from(page - 1) * u64::from(size));
    Ok(sql.build().fetch_all(tx.executor()).await?)
}

pub async fn device_by_id(st: &AppState, id: &str) -> AppResult<api_contracts::admin::DeviceRow> {
    let row = sqlx::query(&format!("{DEVICE_COLUMNS}{DEVICE_FROM} WHERE d.device_id=? AND d.deleted_at IS NULL"))
        .bind(id).fetch_optional(st.device.pool()).await?.ok_or_else(||AppError::NotFound("device".into()))?;
    device_row(&row)
}

pub async fn device_exists(st: &AppState, id: &str) -> AppResult<bool> {
    let exists: bool = sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM device_meta WHERE device_id=? AND deleted_at IS NULL)")
        .bind(id).fetch_one(st.device.pool()).await?;
    Ok(exists)
}

// ===== 设备导入 =====

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ImportRequest {
    pub import_id: uuid::Uuid,
    pub devices: Vec<api_contracts::devices::DeviceProvision>,
}

#[derive(Serialize)]
pub struct ImportJob {
    pub import_id: String,
    pub status: String,
    pub last_error: Option<String>,
}

pub async fn import_jobs(st: &AppState, actor_id: u64) -> AppResult<Vec<ImportJob>> {
    let rows = sqlx::query("SELECT import_id,status,last_error FROM device_import WHERE actor_id=? ORDER BY created_at DESC,import_id LIMIT 50")
        .bind(actor_id).fetch_all(st.device.pool()).await?;
    let mut jobs = vec![];
    for row in rows {
        jobs.push(ImportJob {
            import_id: row.try_get("import_id")?,
            status: row.try_get("status")?,
            last_error: row.try_get("last_error")?,
        });
    }
    Ok(jobs)
}

pub async fn register_import(
    tx: &mut common_db::Tx<'_>,
    id: &str,
    actor_id: u64,
    request: &serde_json::Value,
) -> AppResult<()> {
    sqlx::query("INSERT INTO device_import (import_id,actor_id,request_json) VALUES (?,?,?) ON DUPLICATE KEY UPDATE import_id=device_import.import_id")
        .bind(id).bind(actor_id).bind(request).execute(tx.executor()).await?;
    Ok(())
}

/// 锁住导入记录并核对它确实是**同一个请求**(同 actor + 同请求体)。
pub async fn lock_import(
    tx: &mut common_db::Tx<'_>,
    id: &str,
) -> AppResult<(u64, serde_json::Value)> {
    let row = sqlx::query("SELECT actor_id,request_json FROM device_import WHERE import_id=? FOR UPDATE")
        .bind(id).fetch_one(tx.executor()).await?;
    Ok((row.try_get("actor_id")?, row.try_get("request_json")?))
}

pub async fn lock_import_for_attempt(
    tx: &mut common_db::Tx<'_>,
    id: &str,
    actor_id: u64,
) -> AppResult<sqlx::mysql::MySqlRow> {
    sqlx::query(
        "SELECT request_json,status,last_error,attempts,retryable,(next_attempt_at<=UTC_TIMESTAMP(3)) AS due FROM device_import WHERE import_id=? AND actor_id=? FOR UPDATE",
    )
    .bind(id).bind(actor_id).fetch_optional(tx.executor()).await?
        .ok_or_else(|| AppError::NotFound("导入记录不存在".into()))
}

pub async fn station_exists_for_device(
    tx: &mut common_db::Tx<'_>,
    station_id: u64,
) -> AppResult<bool> {
    let station: Option<u64> = sqlx::query_scalar("SELECT id FROM station WHERE id=? AND deleted_at IS NULL FOR SHARE")
        .bind(station_id).fetch_optional(tx.executor()).await?;
    Ok(station.is_some())
}

pub async fn lock_device_identity(
    tx: &mut common_db::Tx<'_>,
    device_id: &str,
    request: &serde_json::Value,
) -> AppResult<()> {
    sqlx::query("INSERT INTO device_import_identity (device_id,request_json) VALUES (?,?) ON DUPLICATE KEY UPDATE device_id=device_import_identity.device_id")
        .bind(device_id).bind(request).execute(tx.executor()).await?;
    Ok(())
}

pub async fn stored_device_identity(
    tx: &mut common_db::Tx<'_>,
    device_id: &str,
) -> AppResult<serde_json::Value> {
    let stored: serde_json::Value = sqlx::query_scalar("SELECT request_json FROM device_import_identity WHERE device_id=? FOR UPDATE")
        .bind(device_id).fetch_one(tx.executor()).await?;
    Ok(stored)
}

pub async fn lock_device_meta(
    tx: &mut common_db::Tx<'_>,
    device_id: &str,
) -> AppResult<Vec<sqlx::mysql::MySqlRow>> {
    Ok(sqlx::query("SELECT station_id,vendor_id,model,deleted_at,status FROM device_meta WHERE device_id=? FOR UPDATE")
        .bind(device_id).fetch_all(tx.executor()).await?)
}

pub async fn insert_device_meta(
    tx: &mut common_db::Tx<'_>,
    device: &api_contracts::devices::DeviceProvision,
) -> AppResult<()> {
    sqlx::query("INSERT INTO device_meta (device_id,station_id,vendor_id,model) VALUES (?,?,?,?)")
        .bind(&device.device_id).bind(device.station_id).bind(device.vendor_id).bind(&device.model)
        .execute(tx.executor()).await?;
    Ok(())
}

pub async fn complete_import(tx: &mut common_db::Tx<'_>, id: &str) -> AppResult<()> {
    sqlx::query("UPDATE device_import SET status='completed',last_error=NULL,retryable=FALSE,attempts=attempts+1 WHERE import_id=?")
        .bind(id).execute(tx.executor()).await?;
    Ok(())
}

pub async fn audit_device_import(
    tx: &mut common_db::Tx<'_>,
    actor_id: u64,
    id: &str,
    payload: &serde_json::Value,
) -> AppResult<()> {
    sqlx::query("INSERT INTO audit_log (actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'device','import','device_import',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
        .bind(actor_id).bind(id).bind(payload).execute(tx.executor()).await?;
    Ok(())
}

pub async fn fail_import(
    tx: &mut common_db::Tx<'_>,
    id: &str,
    message: &str,
    retryable: bool,
    delay_secs: u32,
) -> AppResult<()> {
    sqlx::query("UPDATE device_import SET status='failed',last_error=?,attempts=attempts+1,retryable=?,next_attempt_at=TIMESTAMPADD(SECOND,?,UTC_TIMESTAMP(3)) WHERE import_id=?")
        .bind(message).bind(retryable).bind(delay_secs).bind(id).execute(tx.executor()).await?;
    Ok(())
}

pub async fn pending_imports(st: &AppState) -> AppResult<Vec<(String, u64)>> {
    let rows: Vec<(String, u64)> = sqlx::query_as("SELECT import_id,actor_id FROM device_import WHERE status<>'completed' AND retryable=TRUE AND attempts<8 AND next_attempt_at<=UTC_TIMESTAMP(3) ORDER BY next_attempt_at,import_id LIMIT 10")
        .fetch_all(st.device.pool()).await?;
    Ok(rows)
}

// ===== Stream 消费侧 =====

/// 更新设备最近一次遥测时间(便于 PC 后台看板显示)。
pub async fn touch_device_seen(st: &AppState, device_id: &str) -> AppResult<()> {
    sqlx::query("UPDATE device_meta SET last_seen_at = NOW(3) WHERE device_id = ?")
        .bind(device_id)
        .execute(st.device.pool())
        .await?;
    Ok(())
}

// ===== OTA =====

pub async fn ota_packages(st: &AppState) -> AppResult<api_contracts::common::ListResponse<api_contracts::admin::OtaPackage>> {
    let rows = sqlx::query(
        "SELECT id, code, vendor_id, version, size_bytes, checksum_sha256, status, created_at
         FROM ota_package WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.device.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::OtaPackage> {
        Ok(api_contracts::admin::OtaPackage {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            code: sqlx::Row::try_get::<String, _>(r, "code")?,
            vendor_id: sqlx::Row::try_get::<Option<u64>, _>(r, "vendor_id")?,
            version: sqlx::Row::try_get::<String, _>(r, "version")?,
            size_bytes: sqlx::Row::try_get::<u64, _>(r, "size_bytes")?,
            checksum_sha256: sqlx::Row::try_get::<String, _>(r, "checksum_sha256")?,
            status: sqlx::Row::try_get::<String, _>(r, "status")?,
            created_at: sqlx::Row::try_get::<chrono::DateTime<chrono::Utc>, _>(r, "created_at")?.to_rfc3339(),
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::common::ListResponse::new(items))
}

#[derive(Debug, Deserialize)]
pub struct PackageCreateReq {
    pub code: String,
    pub vendor_id: Option<u64>,
    pub version: String,
    pub storage_url: String,
    pub size_bytes: u64,
    pub checksum_sha256: String,
    pub sign: Option<String>,
    pub release_notes: Option<String>,
}

pub async fn insert_ota_package(st: &AppState, req: &PackageCreateReq) -> AppResult<u64> {
    let result = sqlx::query(
        "INSERT INTO ota_package (code, vendor_id, version, storage_url, size_bytes, checksum_sha256, sign, release_notes, status)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'draft')",
    )
    .bind(&req.code).bind(req.vendor_id).bind(&req.version).bind(&req.storage_url)
    .bind(req.size_bytes).bind(&req.checksum_sha256).bind(req.sign.as_deref()).bind(req.release_notes.as_deref())
    .execute(st.device.pool()).await?;
    Ok(result.last_insert_id())
}

pub async fn ota_package_detail(st: &AppState, id: u64) -> AppResult<api_contracts::admin::OtaPackageDetail> {
    let r: Option<(u64, String, String, String, String, u64)> = sqlx::query_as(
        "SELECT id, code, version, storage_url, checksum_sha256, size_bytes FROM ota_package WHERE id = ? AND deleted_at IS NULL"
    ).bind(id).fetch_optional(st.device.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("ota package".into()))?;
    Ok(api_contracts::admin::OtaPackageDetail {
        id: r.0, code: r.1, version: r.2, storage_url: r.3, checksum_sha256: r.4, size_bytes: r.5,
    })
}

pub async fn delete_ota_package(st: &AppState, id: u64) -> AppResult<bool> {
    let n = sqlx::query("UPDATE ota_package SET deleted_at = NOW(3) WHERE id = ? AND deleted_at IS NULL")
        .bind(id).execute(st.device.pool()).await?;
    Ok(n.rows_affected() > 0)
}

pub async fn ota_schedules(st: &AppState) -> AppResult<api_contracts::common::ListResponse<api_contracts::admin::OtaSchedule>> {
    let rows = sqlx::query(
        "SELECT id, package_id, rollout_strategy, batch_size, status, scheduled_at, started_at, completed_at, created_at
         FROM ota_schedule ORDER BY id DESC LIMIT 200"
    ).fetch_all(st.device.pool()).await?;
        let items = rows.iter().map(|r| -> AppResult<api_contracts::admin::OtaSchedule> {
        Ok(api_contracts::admin::OtaSchedule {
            id: sqlx::Row::try_get::<u64, _>(r, "id")?,
            package_id: sqlx::Row::try_get::<u64, _>(r, "package_id")?,
            rollout_strategy: sqlx::Row::try_get::<String, _>(r, "rollout_strategy")?,
            batch_size: sqlx::Row::try_get::<Option<u32>, _>(r, "batch_size")?,
            status: sqlx::Row::try_get::<String, _>(r, "status")?,
            scheduled_at: sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(r, "scheduled_at")?.map(|t| t.to_rfc3339()),
            started_at: sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(r, "started_at")?.map(|t| t.to_rfc3339()),
            completed_at: sqlx::Row::try_get::<Option<chrono::DateTime<chrono::Utc>>, _>(r, "completed_at")?.map(|t| t.to_rfc3339()),
        })
    }).collect::<AppResult<Vec<_>>>()?;
    Ok(api_contracts::common::ListResponse::new(items))
}

#[derive(Debug, Deserialize)]
pub struct ScheduleCreateReq {
    pub package_id: u64,
    pub rollout_strategy: String,
    pub batch_size: Option<u32>,
    pub target_filter_json: Option<serde_json::Value>,
    pub scheduled_at: Option<chrono::DateTime<chrono::Utc>>,
}

pub async fn ota_schedule_detail(st: &AppState, id: u64) -> AppResult<api_contracts::admin::OtaScheduleDetail> {
    let r: Option<(u64, u64, String, String)> = sqlx::query_as(
        "SELECT id, package_id, rollout_strategy, status FROM ota_schedule WHERE id = ?"
    ).bind(id).fetch_optional(st.device.pool()).await?;
    let r = r.ok_or_else(|| AppError::NotFound("ota schedule".into()))?;
    Ok(api_contracts::admin::OtaScheduleDetail {
        id: r.0, package_id: r.1, rollout_strategy: r.2, status: r.3,
    })
}
