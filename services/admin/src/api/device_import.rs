use crate::AppState;
use api_contracts::devices::{DeviceProvisionBatch, DeviceProvisionResult};
use axum::{
    extract::{Path, State},
    Json,
};
use common_auth::AdminClaims;
use common_error::{ApiEnvelope, AppError, AppResult};
use common_http::internal::ApiClient;
use serde::{Deserialize, Serialize};
use sqlx::{Executor, Row};

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ImportRequest {
    import_id: uuid::Uuid,
    devices: Vec<api_contracts::devices::DeviceProvision>,
}
#[derive(Serialize)]
pub struct ImportJob {
    import_id: String,
    status: String,
    last_error: Option<String>,
}

async fn authorize(state: &AppState, actor_id: u64) -> AppResult<()> {
    let count: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM admin_user_role a JOIN role r ON r.id=a.role_id JOIN role_permission rp ON rp.role_id=r.id JOIN permission p ON p.id=rp.permission_id WHERE a.id=? AND a.status='active' AND a.deleted_at IS NULL AND r.deleted_at IS NULL AND p.code='device.import'")
        .bind(actor_id).fetch_one(state.db.pool()).await?;
    if count == 0 {
        return Err(AppError::Forbidden("缺少 device.import 权限".into()));
    }
    Ok(())
}

pub async fn list(
    State(state): State<AppState>,
    claims: AdminClaims,
) -> AppResult<Json<ApiEnvelope<Vec<ImportJob>>>> {
    authorize(&state, claims.admin_user_id).await?;
    let rows = sqlx::query("SELECT import_id,status,last_error FROM device_import WHERE actor_id=? ORDER BY created_at DESC,import_id LIMIT 50")
        .bind(claims.admin_user_id).fetch_all(state.db.pool()).await?;
    let mut jobs = vec![];
    for row in rows {
        jobs.push(ImportJob {
            import_id: row.try_get("import_id")?,
            status: row.try_get("status")?,
            last_error: row.try_get("last_error")?,
        });
    }
    Ok(Json(ApiEnvelope::ok(
        jobs,
        common_error::current_request_id(),
    )))
}

pub async fn create(
    State(state): State<AppState>,
    claims: AdminClaims,
    Json(req): Json<ImportRequest>,
) -> AppResult<Json<ApiEnvelope<ImportJob>>> {
    authorize(&state, claims.admin_user_id).await?;
    let mut batch = DeviceProvisionBatch {
        devices: req.devices,
    };
    batch.validate().map_err(AppError::BadRequest)?;
    batch
        .devices
        .sort_by_key(|d| d.device_id.to_ascii_lowercase());
    let request = serde_json::to_value(&batch)?;
    let id = req.import_id.to_string();
    let mut tx = state.db.pool().begin().await?;
    sqlx::query("INSERT INTO device_import (import_id,actor_id,request_json) VALUES (?,?,?) ON DUPLICATE KEY UPDATE import_id=device_import.import_id")
        .bind(&id).bind(claims.admin_user_id).bind(&request).execute(&mut *tx).await?;
    let row =
        sqlx::query("SELECT actor_id,request_json FROM device_import WHERE import_id=? FOR UPDATE")
            .bind(&id)
            .fetch_one(&mut *tx)
            .await?;
    if row.try_get::<u64, _>("actor_id")? != claims.admin_user_id
        || row.try_get::<serde_json::Value, _>("request_json")? != request
    {
        return Err(AppError::Conflict("导入编号已用于其他请求".into()));
    }
    tx.commit().await?;
    finish(state, claims.admin_user_id, id, false).await
}

pub async fn retry(
    State(state): State<AppState>,
    claims: AdminClaims,
    Path(id): Path<uuid::Uuid>,
) -> AppResult<Json<ApiEnvelope<ImportJob>>> {
    authorize(&state, claims.admin_user_id).await?;
    finish(state, claims.admin_user_id, id.to_string(), false).await
}

async fn finish(
    state: AppState,
    actor_id: u64,
    id: String,
    automatic: bool,
) -> AppResult<Json<ApiEnvelope<ImportJob>>> {
    // A job lock serializes retries. Identity locks serialize overlapping batches.
    let mut tx = state.db.pool().begin().await?;
    let row = sqlx::query(
        "SELECT request_json,status,last_error,attempts,retryable,(next_attempt_at<=UTC_TIMESTAMP(3)) AS due FROM device_import WHERE import_id=? AND actor_id=? FOR UPDATE",
    )
    .bind(&id)
    .bind(actor_id)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or_else(|| AppError::NotFound("导入记录不存在".into()))?;
    if row.try_get::<String, _>("status")? == "completed" {
        return Ok(Json(ApiEnvelope::ok(
            ImportJob {
                import_id: id,
                status: "completed".into(),
                last_error: None,
            },
            common_error::current_request_id(),
        )));
    }
    let attempts: u32 = row.try_get("attempts")?;
    if automatic
        && (!row.try_get::<bool, _>("retryable")?
            || row.try_get::<i64, _>("due")? == 0
            || attempts >= 8)
    {
        return Ok(Json(ApiEnvelope::ok(
            ImportJob {
                import_id: id,
                status: row.try_get("status")?,
                last_error: row.try_get("last_error")?,
            },
            common_error::current_request_id(),
        )));
    }
    let batch: DeviceProvisionBatch = serde_json::from_value(row.try_get("request_json")?)?;
    (&mut *tx).execute("SAVEPOINT import_attempt").await?;
    let result: AppResult<()> = async {
        // Recheck live permissions for every manual or automatic attempt.
        authorize(&state, actor_id).await?;
        batch.validate().map_err(AppError::BadRequest)?;
        for device in &batch.devices {
            let station: Option<u64> = sqlx::query_scalar("SELECT id FROM station WHERE id=? AND deleted_at IS NULL FOR SHARE")
                .bind(device.station_id).fetch_optional(&mut *tx).await?;
            if station.is_none() { return Err(AppError::BadRequest(format!("设备 {} 的站点不存在",device.device_id))); }
            let request = serde_json::to_value(device)?;
            sqlx::query("INSERT INTO device_import_identity (device_id,request_json) VALUES (?,?) ON DUPLICATE KEY UPDATE device_id=device_import_identity.device_id")
                .bind(&device.device_id).bind(&request).execute(&mut *tx).await?;
            let stored: serde_json::Value = sqlx::query_scalar("SELECT request_json FROM device_import_identity WHERE device_id=? FOR UPDATE")
                .bind(&device.device_id).fetch_one(&mut *tx).await?;
            if stored != request { return Err(AppError::Conflict(format!("设备 {} 已用其他参数导入",device.device_id))); }
            let existing = sqlx::query("SELECT station_id,vendor_id,model,deleted_at,status FROM device_meta WHERE device_id=? FOR UPDATE")
                .bind(&device.device_id).fetch_all(&mut *tx).await?;
            if existing.len() > 1 { return Err(AppError::Conflict("后台设备存在重复记录".into())); }
            if let Some(row) = existing.first() {
                if row.try_get::<Option<u64>,_>("station_id")? != Some(device.station_id) || row.try_get::<Option<u64>,_>("vendor_id")? != Some(device.vendor_id)
                    || row.try_get::<Option<String>,_>("model")? != device.model || row.try_get::<String,_>("status")? != "enabled"
                    || row.try_get::<Option<chrono::NaiveDateTime>,_>("deleted_at")?.is_some() {
                    return Err(AppError::Conflict(format!("后台设备 {} 已存在且配置不同",device.device_id)));
                }
            } else {
                sqlx::query("INSERT INTO device_meta (device_id,station_id,vendor_id,model) VALUES (?,?,?,?)")
                    .bind(&device.device_id).bind(device.station_id).bind(device.vendor_id).bind(&device.model).execute(&mut *tx).await?;
            }
        }
        let client = ApiClient::new(state.http.clone(),state.service_token.clone());
        let _: DeviceProvisionResult = client.post(state.cfg.service_urls.gateway.as_deref(),api_contracts::paths::GATEWAY_DEVICE_PROVISION,&batch).await?;
        sqlx::query("UPDATE device_import SET status='completed',last_error=NULL,retryable=FALSE,attempts=attempts+1 WHERE import_id=?").bind(&id).execute(&mut *tx).await?;
        sqlx::query("INSERT INTO audit_log (actor_id,module,action,target_type,target_id,after_json,created_month) VALUES (?,'device','import','device_import',?,?,DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
            .bind(actor_id).bind(&id).bind(serde_json::to_value(&batch)?).execute(&mut *tx).await?;
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
            (&mut *tx)
                .execute("ROLLBACK TO SAVEPOINT import_attempt")
                .await?;
            let message = error.message().chars().take(1000).collect::<String>();
            let retryable = retry_delay(&error, attempts + 1);
            sqlx::query("UPDATE device_import SET status='failed',last_error=?,attempts=attempts+1,retryable=?,next_attempt_at=TIMESTAMPADD(SECOND,?,UTC_TIMESTAMP(3)) WHERE import_id=?")
                .bind(&message).bind(retryable.is_some()).bind(retryable.unwrap_or(0)).bind(&id).execute(&mut *tx).await?;
            tx.commit().await?;
            ("failed", Some(message))
        }
    };
    Ok(Json(ApiEnvelope::ok(
        ImportJob {
            import_id: id,
            status: status.into(),
            last_error,
        },
        common_error::current_request_id(),
    )))
}

fn retry_delay(error: &AppError, attempts: u32) -> Option<u32> {
    if attempts >= 8 {
        return None;
    }
    match error {
        AppError::ServiceUnavailable(_) | AppError::HttpClient(_) => {
            Some((5 * 2u32.pow(attempts.min(6))).min(300))
        }
        _ => None,
    }
}

pub fn spawn_recovery(state: AppState) {
    tokio::spawn(async move {
        let mut interval = tokio::time::interval(std::time::Duration::from_secs(5));
        interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
        loop {
            interval.tick().await;
            let jobs: Result<Vec<(String,u64)>,_> = sqlx::query_as("SELECT import_id,actor_id FROM device_import WHERE status<>'completed' AND retryable=TRUE AND attempts<8 AND next_attempt_at<=UTC_TIMESTAMP(3) ORDER BY next_attempt_at,import_id LIMIT 10")
                .fetch_all(state.db.pool()).await;
            match jobs {
                Ok(jobs) => {
                    for (id, actor_id) in jobs {
                        if let Err(error) = finish(state.clone(), actor_id, id.clone(), true).await
                        {
                            tracing::error!(%id,%error,"device import recovery failed");
                        }
                    }
                }
                Err(error) => tracing::error!(%error,"cannot load pending device imports"),
            }
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn retries_only_transient_errors_with_a_bound() {
        let transient = AppError::ServiceUnavailable("network".into());
        assert_eq!(retry_delay(&transient, 1), Some(10));
        assert_eq!(retry_delay(&transient, 7), Some(300));
        assert_eq!(retry_delay(&transient, 8), None);
        assert_eq!(retry_delay(&AppError::Forbidden("revoked".into()), 1), None);
        assert_eq!(
            retry_delay(&AppError::Conflict("different device".into()), 1),
            None
        );
    }
}
