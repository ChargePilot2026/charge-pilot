use crate::AppState;
use api_contracts::orders::{OrderEvent, OrderTimeline};
use axum::{
    extract::{Path, State},
    Json,
};
use common_error::{ApiEnvelope, AppError, AppResult};
use sqlx::{MySql, Row, Transaction};

/// Must be called in the same transaction as the state transition.
pub async fn record(
    tx: &mut Transaction<'_, MySql>,
    order_id: u64,
    event: &str,
    actor: &str,
    detail: &str,
) -> AppResult<()> {
    sqlx::query("INSERT INTO charge_event_log (charge_order_id,event_id,event,actor,detail) VALUES (?,?,?,?,?)")
        .bind(order_id).bind(uuid::Uuid::new_v4().to_string()).bind(event).bind(actor).bind(detail)
        .execute(&mut **tx).await?;
    Ok(())
}

pub async fn timeline(
    State(state): State<AppState>,
    Path(id): Path<u64>,
) -> AppResult<Json<ApiEnvelope<OrderTimeline>>> {
    let mut tx = state.db.pool().begin().await?;
    let exists: Option<u64> =
        sqlx::query_scalar("SELECT id FROM charge_order WHERE id=? AND deleted_at IS NULL")
            .bind(id)
            .fetch_optional(&mut *tx)
            .await?;
    exists.ok_or_else(|| AppError::NotFound("订单不存在".into()))?;
    let rows = sqlx::query("SELECT event_id,occurred_at,event,actor,detail FROM charge_event_log WHERE charge_order_id=? ORDER BY occurred_at,id")
        .bind(id).fetch_all(&mut *tx).await?;
    let mut timeline = Vec::with_capacity(rows.len());
    for row in rows {
        let at: chrono::NaiveDateTime = row.try_get("occurred_at")?;
        timeline.push(OrderEvent {
            event_id: row.try_get("event_id")?,
            at: at
                .and_utc()
                .to_rfc3339_opts(chrono::SecondsFormat::Millis, true),
            event: row.try_get("event")?,
            actor: row.try_get("actor")?,
            detail: row.try_get("detail")?,
        });
    }
    tx.commit().await?;
    Ok(Json(ApiEnvelope::ok(
        OrderTimeline {
            order_id: id,
            timeline,
        },
        common_error::current_request_id(),
    )))
}
