//! Final meter readings are supplied by a confirmed device STOP, never synthesized.
use crate::AppState;
use api_contracts::ChargeEndRequest;
use axum::{
    extract::{Path, State},
    Json,
};
use common_error::{ApiEnvelope, AppError, AppResult};
use sqlx::Row;

fn conflict() -> AppError {
    AppError::Conflict("充电结束确认与订单不一致".into())
}
pub async fn metered_order(
    State(st): State<AppState>, Path(cid): Path<u64>,
) -> AppResult<Json<ApiEnvelope<api_contracts::pricing::MeteredOrder>>> {
    let rows=sqlx::query("SELECT c.order_no,c.user_id,c.started_at,r.meter_json,p.quote_snapshot FROM charge_order c JOIN charge_end_receipt r ON r.charge_order_id=c.id JOIN charge_order_pricing p ON p.charge_order_id=c.id AND p.user_id=c.user_id WHERE c.id=? AND c.status='completed' AND c.deleted_at IS NULL")
        .bind(cid).fetch_all(st.db.pool()).await?;
    if rows.len()!=1 {return Err(AppError::Conflict("订单尚未完成或缺少计量/计价快照".into()));}
    let row=&rows[0];
    Ok(Json(ApiEnvelope::ok(api_contracts::pricing::MeteredOrder {
        charge_order_id:cid,order_no:row.try_get("order_no")?,user_id:row.try_get("user_id")?,
        started_at:row.try_get::<chrono::NaiveDateTime,_>("started_at")?.and_utc(),
        meter:serde_json::from_value(row.try_get("meter_json")?)?,
        quote:serde_json::from_value(row.try_get("quote_snapshot")?)?,
    },common_error::current_request_id())))
}
pub async fn receive(
    State(st): State<AppState>,
    Path(order): Path<String>,
    Json(req): Json<ChargeEndRequest>,
) -> AppResult<Json<ApiEnvelope<serde_json::Value>>> {
    let mut tx = st.db.pool().begin().await?;
    apply(&mut tx, &order, &req).await?;
    tx.commit().await?;
    // Canonical database state is authoritative even if an old telemetry cache remains.
    Ok(Json(ApiEnvelope::ok(
        serde_json::json!({"ok":true}),
        common_error::current_request_id(),
    )))
}
pub async fn apply(
    tx: &mut sqlx::Transaction<'_, sqlx::MySql>,
    order: &str,
    req: &ChargeEndRequest,
) -> AppResult<()> {
    let start = uuid::Uuid::parse_str(&req.start_command_id)
        .map_err(|_| conflict())?
        .to_string();
    let stop = uuid::Uuid::parse_str(&req.stop_command_id)
        .map_err(|_| conflict())?
        .to_string();
    if order != req.order_no
        || req.port_id == 0
        || req.port_no == 0
        || req.meter.charged_wh > 100_000_000
        || req.meter.charged_seconds > 604800
        || req.meter.ended_at > chrono::Utc::now() + chrono::Duration::minutes(5)
    {
        return Err(conflict());
    }
    let orders=sqlx::query("SELECT id,device_id,port_no,status,started_at FROM charge_order WHERE order_no=? AND deleted_at IS NULL FOR UPDATE")
        .bind(order).fetch_all(&mut **tx).await?;
    if orders.len() != 1 {
        return Err(conflict());
    }
    let row = &orders[0];
    let cid: u64 = row.try_get("id")?;
    if row.try_get::<String, _>("device_id")? != req.device_id
        || row.try_get::<u8, _>("port_no")? != req.port_no
    {
        return Err(conflict());
    }
    let started:Option<(String,Option<u64>,bool)>=sqlx::query_as("SELECT CAST(command_id AS CHAR CHARACTER SET utf8mb4),port_id,success FROM charge_start_receipt WHERE charge_order_id=?")
        .bind(cid).fetch_optional(&mut **tx).await?;
    if started != Some((start, Some(req.port_id), true)) {
        return Err(conflict());
    }
    let existing:Option<(String,serde_json::Value)>=sqlx::query_as("SELECT CAST(stop_command_id AS CHAR CHARACTER SET utf8mb4),meter_json FROM charge_end_receipt WHERE charge_order_id=? FOR UPDATE")
        .bind(cid).fetch_optional(&mut **tx).await?;
    let meter = serde_json::to_value(&req.meter)?;
    if let Some((saved, data)) = existing {
        if saved == stop && data == meter {
            return Ok(());
        }
        return Err(conflict());
    }
    if row.try_get::<String, _>("status")? != "charging" {
        return Err(conflict());
    }
    let begin: chrono::NaiveDateTime = row.try_get("started_at")?;
    let seconds = (req.meter.ended_at.naive_utc() - begin).num_seconds();
    if seconds < 0 || i64::from(req.meter.charged_seconds) > seconds + 300 {
        return Err(conflict());
    }
    let changed=sqlx::query("UPDATE active_port_charge SET ended_at=? WHERE port_id=? AND charge_order_id=? AND device_id=? AND port_no=? AND ended_at IS NULL")
        .bind(req.meter.ended_at.naive_utc()).bind(req.port_id).bind(cid).bind(&req.device_id).bind(req.port_no).execute(&mut **tx).await?.rows_affected();
    if changed != 1 {
        return Err(conflict());
    }
    let kwh = format!(
        "{}.{:03}",
        req.meter.charged_wh / 1000,
        req.meter.charged_wh % 1000
    );
    sqlx::query("UPDATE charge_order SET status='completed',ended_at=?,charged_kwh=?,charged_seconds=? WHERE id=?")
        .bind(req.meter.ended_at.naive_utc()).bind(kwh).bind(req.meter.charged_seconds).bind(cid).execute(&mut **tx).await?;
    sqlx::query("INSERT INTO charge_end_receipt (charge_order_id,stop_command_id,meter_json) VALUES (?,?,?)")
        .bind(cid).bind(stop).bind(meter).execute(&mut **tx).await?;
    crate::order_events::record(
        tx,
        cid,
        "device_stopped",
        "gateway",
        "设备确认停止，已记录最终电量，等待计费",
    )
    .await?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[tokio::test]
    #[ignore = "requires development MySQL"]
    async fn measured_end_is_immutable_and_replay_cannot_release_a_new_port_owner() {
        let pool = sqlx::MySqlPool::connect(&std::env::var("DATABASE_URL").unwrap())
            .await
            .unwrap();
        let mut tx = pool.begin().await.unwrap();
        let tag = uuid::Uuid::new_v4().simple().to_string();
        let order = format!("END_{tag}");
        let port = api_contracts::ScanPortDetail {
            port_id: tag.clone(),
            port_code: tag.clone(),
            device_id: format!("END_{tag}"),
            port_no: 1,
            status: "idle".into(),
        };
        let cid = crate::checkout::persist_pending(
            &mut tx,
            123,
            &order,
            &format!("PAY_{tag}"),
            &port,
            &api_contracts::QuoteResponse {
                electric_cents: 80,
                service_cents: 20,
                total_cents: 100,
            },
            chrono::Utc::now() + chrono::Duration::minutes(5),
        )
        .await
        .unwrap();
        sqlx::query("UPDATE charge_order SET status='paid' WHERE id=?")
            .bind(cid)
            .execute(&mut *tx)
            .await
            .unwrap();
        sqlx::query("UPDATE payment_order SET status='paid',paid_cents=100 WHERE biz_id=? AND biz_type='charge'").bind(cid).execute(&mut *tx).await.unwrap();
        let start = api_contracts::StartResultRequest {
            order_no: order.clone(),
            command_id: uuid::Uuid::new_v4().to_string(),
            device_id: port.device_id.clone(),
            port_no: 1,
            port_id: Some(9_000_000_000 + cid),
            success: true,
            error: None,
        };
        crate::charge_start::apply(&mut tx, &order, &start)
            .await
            .unwrap();
        let req = ChargeEndRequest {
            order_no: order.clone(),
            start_command_id: start.command_id.clone(),
            stop_command_id: uuid::Uuid::new_v4().to_string(),
            device_id: port.device_id,
            port_no: 1,
            port_id: start.port_id.unwrap(),
            meter: api_contracts::ChargeEndMeter {
                charged_wh: 125,
                charged_seconds: 1,
                ended_at: chrono::Utc::now(),
            },
        };
        let mut wrong = req.clone();
        wrong.port_id += 1;
        assert!(apply(&mut tx, &order, &wrong).await.is_err());
        wrong = req.clone();
        wrong.start_command_id = uuid::Uuid::new_v4().to_string();
        assert!(apply(&mut tx, &order, &wrong).await.is_err());
        apply(&mut tx, &order, &req).await.unwrap();
        let data:(String,String,u32,Option<i64>)=sqlx::query_as("SELECT status,CAST(charged_kwh AS CHAR),charged_seconds,total_cents FROM charge_order WHERE id=?").bind(cid).fetch_one(&mut *tx).await.unwrap();
        assert_eq!(data, ("completed".into(), "0.1250".into(), 1, None));
        wrong = req.clone();
        wrong.meter.charged_wh = 126;
        assert!(apply(&mut tx, &order, &wrong).await.is_err());
        sqlx::query(
            "UPDATE active_port_charge SET charge_order_id=?,ended_at=NULL WHERE port_id=?",
        )
        .bind(cid + 1)
        .bind(req.port_id)
        .execute(&mut *tx)
        .await
        .unwrap();
        apply(&mut tx, &order, &req).await.unwrap();
        let owner: (u64, bool) = sqlx::query_as(
            "SELECT charge_order_id,ended_at IS NULL FROM active_port_charge WHERE port_id=?",
        )
        .bind(req.port_id)
        .fetch_one(&mut *tx)
        .await
        .unwrap();
        assert_eq!(owner, (cid + 1, true));
        let events:i64=sqlx::query_scalar("SELECT COUNT(*) FROM charge_event_log WHERE charge_order_id=? AND event='device_stopped'").bind(cid).fetch_one(&mut *tx).await.unwrap();
        assert_eq!(events, 1);
        tx.rollback().await.unwrap();
    }
}
