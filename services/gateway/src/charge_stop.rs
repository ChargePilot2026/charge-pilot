//! User-requested STOP is durable and keeps the port occupied until a metered device ACK.
use crate::{protocol::Frame, AppState};
use api_contracts::{ChargeEndMeter, ChargeEndRequest, ChargeStopCommand, ChargeStopResponse};
use axum::{extract::State, Json};
use common_error::{ApiEnvelope, AppError, AppResult};
use common_http::internal::ApiClient;
use common_redis::StreamEnvelope;
use sqlx::Row;
use std::time::Duration;

#[derive(sqlx::FromRow)]
struct Stop {
    command_id: String,
    start_command_id: String,
    charge_order_id: u64,
    order_no: String,
    user_id: u64,
    device_id: String,
    port_no: u8,
    port_id: u64,
    status: String,
    session_id: Option<String>,
    sent_at: Option<chrono::NaiveDateTime>,
    meter_json: Option<serde_json::Value>,
    result_reported: bool,
}
fn conflict() -> AppError {
    AppError::Conflict("停止请求与充电订单不一致".into())
}
async fn load(st: &AppState, order: &str) -> AppResult<Option<Stop>> {
    Ok(
        sqlx::query_as("SELECT * FROM charge_stop_command WHERE order_no=?")
            .bind(order)
            .fetch_optional(st.db.pool())
            .await?,
    )
}
pub async fn request(
    State(st): State<AppState>,
    Json(req): Json<ChargeStopCommand>,
) -> AppResult<Json<ApiEnvelope<ChargeStopResponse>>> {
    if req.order_no.is_empty()
        || req.order_no.len() > 64
        || req.user_id == 0
        || req.source != "user_app"
    {
        return Err(AppError::BadRequest("停止请求无效".into()));
    }
    let mut saved = load(&st, &req.order_no).await?;
    if saved.is_none() {
        let start=sqlx::query("SELECT charge_order_id,user_id FROM charge_command WHERE order_no=? AND result_reported=TRUE AND status='acked' AND owns_port=TRUE")
            .bind(&req.order_no).fetch_optional(st.db.pool()).await?.ok_or_else(conflict)?;
        let cid: u64 = start.try_get("charge_order_id")?;
        if start.try_get::<u64, _>("user_id")? != req.user_id {
            return Err(conflict());
        }
        let order: api_contracts::orders::OrderDetail =
            ApiClient::new(st.http.clone(), st.service_token.clone())
                .get(
                    st.cfg.service_urls.user.as_deref(),
                    &api_contracts::paths::USER_INTERNAL_ORDER_DETAIL
                        .replace(":order_id", &cid.to_string()),
                    &(),
                )
                .await?;
        if order.order.order_no != req.order_no
            || order.order.user_id != req.user_id
            || order.order.status != "charging"
        {
            return Err(conflict());
        }
        let mut tx = st.db.pool().begin().await?;
        let count=sqlx::query("INSERT IGNORE INTO charge_stop_command (command_id,start_command_id,charge_order_id,order_no,user_id,device_id,port_no,port_id) SELECT ?,c.command_id,c.charge_order_id,c.order_no,c.user_id,c.device_id,c.port_no,c.port_id FROM charge_command c JOIN device_port p ON p.id=c.port_id WHERE c.charge_order_id=? AND c.status='acked' AND c.result_reported=TRUE AND p.current_order_id=c.order_no AND p.status='charging'")
            .bind(uuid::Uuid::new_v4().to_string()).bind(cid).execute(&mut *tx).await?.rows_affected();
        tx.commit().await?;
        saved = load(&st, &req.order_no).await?;
        if count == 0 && saved.is_none() {
            return Err(conflict());
        }
    }
    let saved = saved.ok_or_else(conflict)?;
    if saved.user_id != req.user_id || saved.order_no != req.order_no {
        return Err(conflict());
    }
    Ok(Json(ApiEnvelope::ok(
        ChargeStopResponse {
            accepted: true,
            stopped: saved.result_reported,
            command_id: saved.command_id,
        },
        common_error::current_request_id(),
    )))
}
async fn drive(st: &AppState, order: &str) -> AppResult<()> {
    let c = load(st, order).await?.ok_or_else(conflict)?;
    if c.result_reported {
        return Ok(());
    }
    if c.status == "pending" || c.status == "sent" {
        if c.sent_at
            .is_some_and(|t| chrono::Utc::now().naive_utc() - t < chrono::Duration::seconds(2))
        {
            return Ok(());
        }
        if let Some(session) = st.connections.get(&c.device_id).await {
            let changed=sqlx::query("UPDATE charge_stop_command SET status='sent',session_id=?,sent_at=UTC_TIMESTAMP(3) WHERE command_id=? AND status IN ('pending','sent') AND (sent_at IS NULL OR sent_at<UTC_TIMESTAMP(3)-INTERVAL 2 SECOND)")
                .bind(&session.id).bind(&c.command_id).execute(st.db.pool()).await?.rows_affected();
            if changed == 1 {
                let frame = Frame {
                    device_id: c.device_id,
                    port_no: Some(c.port_no),
                    msg_type: "cmd".into(),
                    ts: chrono::Utc::now(),
                    payload: serde_json::json!({"command":"STOP","command_id":c.command_id,"order_no":c.order_no,"meter_required":true}),
                };
                // Keep retrying the same STOP after disconnect/partial write, never pretend it stopped.
                let _ = session.send(&frame).await;
            }
        }
        return Ok(());
    }
    let meter: ChargeEndMeter = serde_json::from_value(c.meter_json.clone().ok_or_else(conflict)?)?;
    let req = ChargeEndRequest {
        order_no: c.order_no.clone(),
        start_command_id: c.start_command_id.clone(),
        stop_command_id: c.command_id.clone(),
        device_id: c.device_id.clone(),
        port_no: c.port_no,
        port_id: c.port_id,
        meter: meter.clone(),
    };
    let result: serde_json::Value = ApiClient::new(st.http.clone(), st.service_token.clone())
        .post(
            st.cfg.service_urls.user.as_deref(),
            &api_contracts::paths::USER_INTERNAL_END_RESULT.replace(":order_id",&c.order_no),
            &req,
        )
        .await?;
    if result.get("ok").and_then(|v| v.as_bool()) != Some(true) {
        return Err(conflict());
    }
    let mut tx = st.db.pool().begin().await?;
    let reported: bool = sqlx::query_scalar(
        "SELECT result_reported FROM charge_stop_command WHERE command_id=? FOR UPDATE",
    )
    .bind(&c.command_id)
    .fetch_one(&mut *tx)
    .await?;
    if !reported {
        sqlx::query("UPDATE device_port SET status=IF(status='charging','idle',status),current_order_id=NULL WHERE id=? AND current_order_id=?")
            .bind(c.port_id).bind(&c.order_no).execute(&mut *tx).await?;
        let mut event = StreamEnvelope::new(
            "charge_ended",
            "gateway",
            serde_json::json!({"charge_order_id":c.charge_order_id,"order_no":c.order_no,"user_id":c.user_id,"device_id":c.device_id,"port_no":c.port_no,"stop_command_id":c.command_id,"charged_wh":meter.charged_wh,"charged_kwh":format!("{}.{:03}",meter.charged_wh/1000,meter.charged_wh%1000),"charged_seconds":meter.charged_seconds,"ended_at":meter.ended_at}),
        );
        event.event_id = c.command_id.clone();
        sqlx::query("INSERT INTO event_outbox (event_id,stream,envelope_json) VALUES (?,?,?)")
            .bind(&event.event_id)
            .bind(common_redis::streams::CHARGE_ENDED)
            .bind(serde_json::to_value(&event)?)
            .execute(&mut *tx)
            .await?;
        sqlx::query("UPDATE charge_stop_command SET result_reported=TRUE WHERE command_id=?")
            .bind(&c.command_id)
            .execute(&mut *tx)
            .await?;
    }
    tx.commit().await?;
    Ok(())
}
pub async fn acknowledge(st: &AppState, session: &str, frame: &Frame) -> AppResult<bool> {
    let Some(id) = frame.payload.get("command_id").and_then(|v| v.as_str()) else {
        return Ok(false);
    };
    let mut tx = st.db.pool().begin().await?;
    let c: Option<Stop> =
        sqlx::query_as("SELECT * FROM charge_stop_command WHERE command_id=? FOR UPDATE")
            .bind(id)
            .fetch_optional(&mut *tx)
            .await?;
    let Some(c) = c else {
        return Ok(false);
    };
    if c.device_id != frame.device_id
        || Some(c.port_no) != frame.port_no
        || c.session_id.as_deref() != Some(session)
        || frame.payload.get("command").and_then(|v| v.as_str()) != Some("STOP")
    {
        return Err(conflict());
    }
    let success = frame
        .payload
        .get("success")
        .and_then(|v| v.as_bool())
        .ok_or_else(conflict)?;
    if !success {
        return Ok(true);
    }
    let meter: ChargeEndMeter =
        serde_json::from_value(frame.payload.get("meter").cloned().ok_or_else(conflict)?)
            .map_err(|_| conflict())?;
    if meter.charged_wh > 100_000_000
        || meter.charged_seconds > 604800
        || meter.ended_at > chrono::Utc::now() + chrono::Duration::minutes(5)
    {
        return Err(conflict());
    }
    let value = serde_json::to_value(&meter)?;
    if c.meter_json.as_ref().is_some_and(|v| v != &value) {
        return Err(conflict());
    }
    sqlx::query("UPDATE charge_stop_command SET status='acked',meter_json=? WHERE command_id=?")
        .bind(value)
        .bind(id)
        .execute(&mut *tx)
        .await?;
    tx.commit().await?;
    Ok(true)
}
pub fn spawn(st: AppState) {
    tokio::spawn(async move {
        let mut tick = tokio::time::interval(Duration::from_secs(2));
        let mut cursor = 0_u64;
        loop {
            tick.tick().await;
            let rows:Result<Vec<(u64,String)>,_>=sqlx::query_as("SELECT charge_order_id,order_no FROM charge_stop_command WHERE result_reported=FALSE AND charge_order_id>? ORDER BY charge_order_id LIMIT 50").bind(cursor).fetch_all(st.db.pool()).await;
            if let Ok(rows) = rows {
                cursor = rows.last().map(|v| v.0).unwrap_or(0);
                for (_, order) in rows {
                    if drive(&st, &order).await.is_err() {
                        tracing::warn!(order_no=%order,"stop confirmation deferred");
                    }
                }
            } else {
                tracing::warn!("stop recovery query failed");
            }
        }
    });
}
