//! Persisted telemetry rollups used by historical curves.

use chrono::{DateTime, Datelike, Utc};
use common_error::{AppError, AppResult};
use common_db::Tx;

/// Insert one actual reading into both aggregate resolutions in the same transaction
/// as its raw telemetry row. Replayed/backfilled readings are counted as observations.
pub async fn aggregate_measurement(
    tx: &mut Tx<'_>,
    device_id: &str,
    port_no: u8,
    metric: &str,
    value: f64,
    at: DateTime<Utc>,
) -> AppResult<()> {
    if device_id.is_empty() || port_no == 0 || !value.is_finite() {
        return Err(AppError::BadRequest("遥测聚合数据无效".into()));
    }
    let seconds = at.timestamp();
    for (table, width) in [
        ("telemetry_aggregate_15min", 15 * 60),
        ("telemetry_aggregate_hourly", 60 * 60),
    ] {
        let bucket_seconds = seconds.div_euclid(width) * width;
        let bucket = DateTime::<Utc>::from_timestamp(bucket_seconds, 0)
            .ok_or_else(|| AppError::BadRequest("遥测时间超出支持范围".into()))?;
        let bucket_month = chrono::NaiveDate::from_ymd_opt(bucket.year(), bucket.month(), 1)
            .ok_or_else(|| AppError::Internal("invalid telemetry aggregate month".into()))?;
        let sql = format!(
            "INSERT INTO {table} (device_id, port_no, metric, bucket_start, bucket_month, avg_value, min_value, max_value, `count`)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)
             ON DUPLICATE KEY UPDATE
               avg_value = ((avg_value * `count`) + VALUES(avg_value)) / (`count` + 1),
               min_value = LEAST(min_value, VALUES(min_value)),
               max_value = GREATEST(max_value, VALUES(max_value)),
               `count` = `count` + 1"
        );
        sqlx::query(&sql)
            .bind(device_id)
            .bind(port_no)
            .bind(metric)
            .bind(bucket)
            .bind(bucket_month)
            .bind(value)
            .bind(value)
            .bind(value)
            .execute(tx.executor())
            .await?;
    }
    Ok(())
}
