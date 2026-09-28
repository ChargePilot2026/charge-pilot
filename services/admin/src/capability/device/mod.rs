//! device 域 —— 站点 / 设备 / 设备导入 / OTA
//!
//! 设备导入是**唯一带后台重试**的写路径:`finish` 用 SAVEPOINT 包住整批
//! 写入,失败时回滚到 SAVEPOINT 而不是回滚整个事务 —— 导入锁要保留,
//! 否则并发的重试请求会同时进入同一批设备。

pub mod domain;
pub mod repository_sql;

use crate::AppState;
use crate::capability::identity::{require_permission, require_permission_by_id, ActiveAdmin};
use axum::{extract::Path, extract::State, Json};
use common_error::{AppError, AppResult};
use serde::{Deserialize, Serialize};
// `finish` 直接操作事务(SAVEPOINT)并读回锁定行,故在本层需要这两个 trait。
// SQL 本身仍全部在 `repository_sql.rs`,这里只借用 trait 与行类型。
use sqlx::{Executor, Row};

// ===== 站点 CRUD =====

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

pub async fn stations(State(st): State<AppState>, actor: ActiveAdmin, axum::extract::Query(q): axum::extract::Query<repository_sql::StationQuery>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::PagedResponse<api_contracts::admin::Station>>>> {
    require_permission(&st,&actor, "station.read").await?;
    let (page,page_size)=domain::station_page(q.page,q.page_size)?;
    let keyword=q.keyword.as_deref().map(str::trim).filter(|v|!v.is_empty());
    let status=q.status.as_deref().filter(|v|!v.is_empty());
    domain::validate_text(keyword,128,false)?;
    domain::validate_fields(None,None,status,None,None,None)?;
    let mut tx=st.device.begin().await?;
    let total=repository_sql::station_count(&mut tx,keyword,status).await?;
    let rows=repository_sql::station_page(&mut tx,keyword,status,page,page_size).await?;
    let items = rows.iter().map(|r| repository_sql::station_row(r)).collect::<AppResult<Vec<_>>>()?;
    tx.commit().await?;
    let permissions=repository_sql::station_codes(actor.admin_user_id, st.device.pool()).await?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::common::PagedResponse { items, permissions, total, page, page_size },
        common_error::current_request_id(),
    )))
}

pub async fn station_create(State(st): State<AppState>, actor: ActiveAdmin, Json(req): Json<StationCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::CreatedResponse>>> {
    require_permission(&st,&actor, "station.create").await?;
    domain::validate_text(Some(&req.code),64,true)?; domain::validate_text(Some(&req.name),128,true)?;
    domain::validate_fields(req.longitude.into(),req.latitude.into(),req.status.as_deref(),req.address.as_deref(),req.open_hours.as_deref(),req.contact_phone.as_deref())?;
    let mut tx=st.device.begin().await?;
    repository_sql::lock_station_code(&mut tx,&req.code).await?;
    if repository_sql::station_code_taken(&mut tx,&req.code).await? {return Err(AppError::Conflict("站点编码已存在".into()));}
    let id = repository_sql::insert_station(&mut tx,&req).await?;
    repository_sql::audit_station(&mut tx,actor.admin_user_id,id,"create",&serde_json::to_value(&req)?).await?;
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::CreatedResponse { id }, common_error::current_request_id())))
}

pub async fn station_detail(State(st): State<AppState>, actor: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::StationDetail>>> {
    require_permission(&st,&actor, "station.read").await?;
    let detail = repository_sql::station_detail(&st,id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(detail, common_error::current_request_id())))
}

pub async fn station_update(State(st): State<AppState>, actor: ActiveAdmin, Path(id): Path<u64>, Json(req): Json<repository_sql::StationUpdateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::UpdatedResponse>>> {
    require_permission(&st,&actor, "station.update").await?;
    domain::validate_text(req.name.as_deref(),128,true)?;
    domain::validate_fields(req.longitude,req.latitude,req.status.as_deref(),req.address.as_deref(),req.open_hours.as_deref(),req.contact_phone.as_deref())?;
    let mut tx=st.device.begin().await?;
    if !repository_sql::lock_station(&mut tx,id).await?{return Err(AppError::NotFound("station".into()));}
    repository_sql::update_station(&mut tx,id,&req).await?;
    repository_sql::audit_station(&mut tx,actor.admin_user_id,id,"update",&serde_json::to_value(&req)?).await?;
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::UpdatedResponse::new(), common_error::current_request_id())))
}

pub async fn station_delete(State(st): State<AppState>, actor: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::DeletedResponse>>> {
    require_permission(&st,&actor, "station.delete").await?;
    if !repository_sql::soft_delete_station(&st,id,actor.admin_user_id).await? { return Err(AppError::NotFound("station".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::DeletedResponse::new(), common_error::current_request_id())))
}

// ===== 站点公开读(供小程序;无 JWT,走 internal 路由) =====

pub async fn nearby(State(st): State<AppState>, axum::extract::Query(q): axum::extract::Query<api_contracts::NearbyStationsQuery>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::NearbyStationsResponse>>> {
    let radius = domain::nearby_radius(&q)?;
    // Latitude bound reduces candidates; spherical longitude math also handles the date line.
    // Filter and sort BEFORE LIMIT so nearer stations are never discarded arbitrarily.
    let items = repository_sql::nearby_stations(&st,q.lat,q.lng,radius).await?;
    Ok(Json(common_error::ApiEnvelope::ok(
        api_contracts::NearbyStationsResponse { items },
        common_error::current_request_id(),
    )))
}

pub async fn public_detail(State(st): State<AppState>, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::StationPublicDetail>>> {
    let detail = repository_sql::station_public_detail(&st,id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(detail, common_error::current_request_id())))
}

// ===== 设备 =====

pub async fn device_list(State(st): State<AppState>, actor: ActiveAdmin, axum::extract::Query(q): axum::extract::Query<domain::DeviceQuery>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::PagedResponse<api_contracts::admin::DeviceRow>>>> {
    let permissions=repository_sql::device_permissions(&st,actor.admin_user_id,&actor.sub).await?;
    let (page,size)=q.validate()?;
    let mut tx=st.device.begin().await?;
    let total=repository_sql::device_count(&mut tx,&q).await?;
    let rows=repository_sql::device_page(&mut tx,&q,page,size).await?;
    let items=rows.iter().map(repository_sql::device_row).collect::<AppResult<Vec<_>>>()?;
    tx.commit().await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::PagedResponse{items,total,page,page_size:size,permissions},common_error::current_request_id())))
}

pub async fn device_get(State(st): State<AppState>, actor: ActiveAdmin, Path(id): Path<String>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::DeviceRow>>> {
    let _ = repository_sql::device_permissions(&st,actor.admin_user_id,&actor.sub).await?;
    let row=repository_sql::device_by_id(&st,&id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(row,common_error::current_request_id())))
}

pub async fn device_orders(State(st): State<AppState>, actor: ActiveAdmin, Path(id): Path<String>, axum::extract::Query(mut q): axum::extract::Query<api_contracts::orders::OrderQuery>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::orders::OrderPage>>> {
    let _ = repository_sql::device_permissions(&st,actor.admin_user_id,&actor.sub).await?;
    if !repository_sql::device_exists(&st,&id).await? {return Err(AppError::NotFound("device".into()));}
    q.device_id=Some(id);
    crate::capability::order::list(State(st),actor,axum::extract::Query(q)).await
}

// ===== 设备导入 =====

/// 设备导入权限:统一走 `require_permission`(旧实现只按 actor_id 查,
/// 未带 username 复核,此处补齐与其它端点一致的口径)。
async fn authorize(state: &AppState, actor: &ActiveAdmin) -> AppResult<()> {
    require_permission(state, actor, "device.import").await
}

/// 后台恢复路径只有 admin_user_id,按 id 解析后走同一段鉴权 SQL。
async fn authorize_by_id(state: &AppState, actor_id: u64) -> AppResult<()> {
    require_permission_by_id(state, actor_id, "device.import").await
}

pub async fn import_list(State(state): State<AppState>, claims: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<Vec<repository_sql::ImportJob>>>> {
    authorize(&state, &claims).await?;
    let jobs = repository_sql::import_jobs(&state, claims.admin_user_id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(jobs, common_error::current_request_id())))
}

pub async fn import_create(
    State(state): State<AppState>,
    claims: ActiveAdmin,
    Json(req): Json<repository_sql::ImportRequest>,
) -> AppResult<Json<common_error::ApiEnvelope<repository_sql::ImportJob>>> {
    authorize(&state, &claims).await?;
    let mut batch = api_contracts::devices::DeviceProvisionBatch { devices: req.devices };
    batch.validate().map_err(AppError::BadRequest)?;
    batch.devices.sort_by_key(|d| d.device_id.to_ascii_lowercase());
    let request = serde_json::to_value(&batch)?;
    let id = req.import_id.to_string();
    let mut tx = state.device.begin().await?;
    repository_sql::register_import(&mut tx, &id, claims.admin_user_id, &request).await?;
    let (actor_id, stored) = repository_sql::lock_import(&mut tx, &id).await?;
    if actor_id != claims.admin_user_id || stored != request {
        return Err(AppError::Conflict("导入编号已用于其他请求".into()));
    }
    tx.commit().await?;
    finish(state, claims.admin_user_id, id, false).await
}

pub async fn import_retry(
    State(state): State<AppState>,
    claims: ActiveAdmin,
    Path(id): Path<uuid::Uuid>,
) -> AppResult<Json<common_error::ApiEnvelope<repository_sql::ImportJob>>> {
    authorize(&state, &claims).await?;
    finish(state, claims.admin_user_id, id.to_string(), false).await
}

async fn finish(
    state: AppState,
    actor_id: u64,
    id: String,
    automatic: bool,
) -> AppResult<Json<common_error::ApiEnvelope<repository_sql::ImportJob>>> {
    // A job lock serializes retries. Identity locks serialize overlapping batches.
    let mut tx = state.device.begin().await?;
    let row = repository_sql::lock_import_for_attempt(&mut tx, &id, actor_id).await?;
    if row.try_get::<String, _>("status")? == "completed" {
        return Ok(Json(common_error::ApiEnvelope::ok(
            repository_sql::ImportJob { import_id: id, status: "completed".into(), last_error: None },
            common_error::current_request_id(),
        )));
    }
    let attempts: u32 = row.try_get("attempts")?;
    if automatic
        && (!row.try_get::<bool, _>("retryable")?
            || row.try_get::<i64, _>("due")? == 0
            || attempts >= 8)
    {
        return Ok(Json(common_error::ApiEnvelope::ok(
            repository_sql::ImportJob {
                import_id: id,
                status: row.try_get("status")?,
                last_error: row.try_get("last_error")?,
            },
            common_error::current_request_id(),
        )));
    }
    let batch: api_contracts::devices::DeviceProvisionBatch = serde_json::from_value(row.try_get("request_json")?)?;
    (tx.executor()).execute("SAVEPOINT import_attempt").await?;
    let result: AppResult<()> = async {
        // Recheck live permissions for every manual or automatic attempt.
        authorize_by_id(&state, actor_id).await?;
        batch.validate().map_err(AppError::BadRequest)?;
        for device in &batch.devices {
            if !repository_sql::station_exists_for_device(&mut tx, device.station_id).await? {
                return Err(AppError::BadRequest(format!("设备 {} 的站点不存在",device.device_id)));
            }
            let request = serde_json::to_value(device)?;
            repository_sql::lock_device_identity(&mut tx, &device.device_id, &request).await?;
            let stored = repository_sql::stored_device_identity(&mut tx, &device.device_id).await?;
            if stored != request { return Err(AppError::Conflict(format!("设备 {} 已用其他参数导入",device.device_id))); }
            let existing = repository_sql::lock_device_meta(&mut tx, &device.device_id).await?;
            if existing.len() > 1 { return Err(AppError::Conflict("后台设备存在重复记录".into())); }
            if let Some(row) = existing.first() {
                if row.try_get::<Option<u64>,_>("station_id")? != Some(device.station_id) || row.try_get::<Option<u64>,_>("vendor_id")? != Some(device.vendor_id)
                    || row.try_get::<Option<String>,_>("model")? != device.model || row.try_get::<String,_>("status")? != "enabled"
                    || row.try_get::<Option<chrono::NaiveDateTime>,_>("deleted_at")?.is_some() {
                    return Err(AppError::Conflict(format!("后台设备 {} 已存在且配置不同",device.device_id)));
                }
            } else {
                repository_sql::insert_device_meta(&mut tx, device).await?;
            }
        }
        let client = common_http::internal::ApiClient::new(state.http.clone(),state.service_token.clone());
        let _: api_contracts::devices::DeviceProvisionResult = client.post(state.cfg.service_urls.gateway.as_deref(),api_contracts::paths::GATEWAY_DEVICE_PROVISION,&batch).await?;
        repository_sql::complete_import(&mut tx, &id).await?;
        repository_sql::audit_device_import(&mut tx, actor_id, &id, &serde_json::to_value(&batch)?).await?;
        Ok(())
    }.await;
    let (status, last_error) = match result {
        Ok(()) => {
            tx.commit().await?;
            ("completed", None)
        }
        Err(error) => {
            // Retain the job lock while discarding partial metadata writes.
            // A late failed attempt must never overwrite another completion.
            (tx.executor()).execute("ROLLBACK TO SAVEPOINT import_attempt").await?;
            let message = error.message().chars().take(1000).collect::<String>();
            let retryable = domain::retry_delay(&error, attempts + 1);
            repository_sql::fail_import(&mut tx, &id, &message, retryable.is_some(), retryable.unwrap_or(0)).await?;
            tx.commit().await?;
            ("failed", Some(message))
        }
    };
    Ok(Json(common_error::ApiEnvelope::ok(
        repository_sql::ImportJob { import_id: id, status: status.into(), last_error },
        common_error::current_request_id(),
    )))
}

pub fn spawn_recovery(state: AppState) {
    tokio::spawn(async move {
        let mut interval = tokio::time::interval(std::time::Duration::from_secs(5));
        interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
        loop {
            interval.tick().await;
            match repository_sql::pending_imports(&state).await {
                Ok(jobs) => {
                    for (id, actor_id) in jobs {
                        if let Err(error) = finish(state.clone(), actor_id, id.clone(), true).await
                        {
                            tracing::error!(%id, %error, backtrace = %common_error::backtrace(), "device import recovery failed");
                        }
                    }
                }
                Err(error) => tracing::error!(%error, backtrace = %common_error::backtrace(), "cannot load pending device imports"),
            }
        }
    });
}

// ===== OTA =====

pub async fn packages_list(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::OtaPackage>>>> {
    let items = repository_sql::ota_packages(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

pub async fn packages_create(State(st): State<AppState>, _c: ActiveAdmin, Json(req): Json<repository_sql::PackageCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::CreatedResponse>>> {
    require_permission(&st,&_c,"ota.package.create").await?;
    let id = repository_sql::insert_ota_package(&st,&req).await?;
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::CreatedResponse { id }, common_error::current_request_id())))
}

pub async fn packages_get(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::OtaPackageDetail>>> {
    let detail = repository_sql::ota_package_detail(&st,id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(detail, common_error::current_request_id())))
}

pub async fn packages_delete(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::DeletedResponse>>> {
    require_permission(&st,&_c,"ota.package.delete").await?;
    if !repository_sql::delete_ota_package(&st,id).await? { return Err(AppError::NotFound("ota package".into())); }
    Ok(Json(common_error::ApiEnvelope::ok(api_contracts::common::DeletedResponse::new(), common_error::current_request_id())))
}

pub async fn schedules_list(State(st): State<AppState>, _c: ActiveAdmin) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::common::ListResponse<api_contracts::admin::OtaSchedule>>>> {
    let items = repository_sql::ota_schedules(&st).await?;
    Ok(Json(common_error::ApiEnvelope::ok(items, common_error::current_request_id())))
}

pub async fn schedules_create(State(st): State<AppState>, c: ActiveAdmin, Json(req): Json<repository_sql::ScheduleCreateReq>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::gateway_devices::NotImplementedResponse>>> {
    require_permission(&st,&c,"ota.schedule.create").await?;
    let _ = (st, c, req);
    Err(AppError::ServiceUnavailable(
        "OTA 调度器尚未接入设备筛选、传输与 ACK 确认，未创建调度".into(),
    ))
}

pub async fn schedules_get(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::admin::OtaScheduleDetail>>> {
    let detail = repository_sql::ota_schedule_detail(&st,id).await?;
    Ok(Json(common_error::ApiEnvelope::ok(detail, common_error::current_request_id())))
}

pub async fn schedules_trigger(State(st): State<AppState>, _c: ActiveAdmin, Path(id): Path<u64>) -> AppResult<Json<common_error::ApiEnvelope<api_contracts::gateway_devices::NotImplementedResponse>>> {
    require_permission(&st,&_c,"ota.schedule.trigger").await?;
    let _ = (st, id);
    Err(AppError::ServiceUnavailable(
        "OTA 调度器尚未接入设备筛选、传输与 ACK 确认，未触发升级".into(),
    ))
}

#[cfg(test)]
mod tests {
    use super::domain::DeviceQuery;
    use crate::capability::device::domain::nearby_radius;
    use api_contracts::NearbyStationsQuery;

    #[test]
    fn rejects_invalid_geography() {
        for (lat, lng, r) in [
            (91.0, 0.0, 5.0),
            (0.0, 181.0, 5.0),
            (f64::NAN, 0.0, 5.0),
            (0.0, 0.0, 0.0),
            (0.0, 0.0, 51.0),
        ] {
            assert!(nearby_radius(&NearbyStationsQuery {
                lat,
                lng,
                radius_km: Some(r)
            })
            .is_err());
        }
        assert_eq!(
            nearby_radius(&NearbyStationsQuery {
                lat: 90.0,
                lng: 180.0,
                radius_km: None
            })
            .unwrap(),
            5.0
        );
    }

    /// 设备查询 DTO 来自 HTTP 层,这里锁住"未知字段直接拒绝"的口径。
    #[test]
    fn device_query_rejects_unknown_fields_from_http() {
        let ok: DeviceQuery =
            serde_json::from_str(r#"{"page":1,"page_size":20,"status":"enabled"}"#).unwrap();
        assert_eq!(ok.validate().unwrap(), (1, 20));
        assert!(serde_json::from_str::<DeviceQuery>(r#"{"unexpected":1}"#).is_err());
    }
}
