//! Durable START / compensating STOP state machine. Socket writes are never device ACKs.
use crate::{protocol::Frame, AppState};
use common_error::{AppError, AppResult};
use common_http::internal::ApiClient;
use common_redis::{PortLock, StreamEntry};
use serde::Deserialize;
use sqlx::Row;
use std::time::Duration;

#[derive(Debug, Deserialize)]
struct Started {
    charge_order_id: u64,
    payment_order_id: u64,
    user_id: u64,
    order_no: String,
    device_id: String,
    port_no: u8,
    port_code: String,
}
#[derive(Debug, sqlx::FromRow)]
struct Command {
    command_id: String,
    stop_command_id: String,
    charge_order_id: u64,
    payment_order_id: u64,
    order_no: String,
    user_id: u64,
    device_id: String,
    port_no: u8,
    port_code: String,
    port_id: Option<u64>,
    owns_port: bool,
    status: String,
    session_id: Option<String>,
    stop_session_id: Option<String>,
    sent_at: Option<chrono::NaiveDateTime>,
    stop_sent_at: Option<chrono::NaiveDateTime>,
    error: Option<String>,
    result_reported: bool,
}
const COMMAND_SELECT:&str="SELECT CAST(command_id AS CHAR CHARACTER SET utf8mb4) AS command_id,CAST(stop_command_id AS CHAR CHARACTER SET utf8mb4) AS stop_command_id,charge_order_id,payment_order_id,order_no,user_id,device_id,port_no,port_code,port_id,owns_port,status,session_id,stop_session_id,sent_at,stop_sent_at,error,result_reported FROM charge_command";

fn mismatch() -> AppError {
    AppError::Conflict("启动事件与设备或订单不一致".into())
}
async fn load(st: &AppState, id: u64) -> AppResult<Option<Command>> {
    Ok(
        sqlx::query_as(&format!("{COMMAND_SELECT} WHERE charge_order_id=?"))
            .bind(id)
            .fetch_optional(st.db.pool())
            .await?,
    )
}
fn same(c: &Command, p: &Started) -> bool {
    c.charge_order_id == p.charge_order_id
        && c.payment_order_id == p.payment_order_id
        && c.user_id == p.user_id
        && c.order_no == p.order_no
        && c.device_id == p.device_id
        && c.port_no == p.port_no
        && c.port_code == p.port_code
}
pub async fn handle(st: &AppState, entry: &StreamEntry) -> AppResult<()> {
    let p: Started = common_stream::parse_payload(&entry.envelope)?;
    if p.charge_order_id == 0
        || p.payment_order_id == 0
        || p.user_id == 0
        || p.port_no == 0
        || [&p.order_no, &p.device_id, &p.port_code].iter().any(|v| {
            v.is_empty()
                || v.len() > 64
                || !v
                    .bytes()
                    .all(|c| c.is_ascii_alphanumeric() || b"-_:.".contains(&c))
        })
    {
        return Err(mismatch());
    }
    if let Some(c) = load(st, p.charge_order_id).await? {
        if !same(&c, &p) {
            return Err(mismatch());
        }
    } else {
        create(st, &p).await?;
    }
    // Keep the Stream unacknowledged until the user service durably accepts a real outcome.
    for _ in 0..20 {
        if drive(st, p.charge_order_id).await? {
            return Ok(());
        }
        tokio::time::sleep(Duration::from_millis(250)).await;
    }
    Err(AppError::ServiceUnavailable("设备启动结果尚未确认".into()))
}

async fn create(st: &AppState, p: &Started) -> AppResult<()> {
    let client = ApiClient::new(st.http.clone(), st.service_token.clone());
    let order: api_contracts::orders::OrderDetail = client
        .get(
            st.cfg.service_urls.user.as_deref(),
            &api_contracts::paths::USER_INTERNAL_ORDER_DETAIL
                .replace(":order_id", &p.charge_order_id.to_string()),
            &(),
        )
        .await?;
    if order.order.order_id != p.charge_order_id
        || order.order.order_no != p.order_no
        || order.order.user_id != p.user_id
        || order.order.device_id != p.device_id
        || order.order.port_no != p.port_no
        || order.payment_order_id != Some(p.payment_order_id)
        || order.order.status != "paid"
        || order.payment_status.as_deref() != Some("paid")
    {
        return Err(mismatch());
    }
    let holder = format!("{}:{}", p.user_id, p.order_no);
    let held = PortLock::new(st.redis_cache.clone())
        .check_holder(&p.port_code, &holder)
        .await?;
    let mut tx = st.db.pool().begin().await?;
    let ports=sqlx::query("SELECT p.id,p.status,p.current_order_id,d.status AS device_status,v.status AS vendor_status FROM device_port p JOIN device d ON d.device_id=p.device_id AND d.deleted_at IS NULL JOIN vendor v ON v.id=d.vendor_id AND v.deleted_at IS NULL WHERE p.port_code=? AND p.device_id=? AND p.port_no=? AND p.deleted_at IS NULL FOR UPDATE")
        .bind(&p.port_code).bind(&p.device_id).bind(p.port_no).fetch_all(&mut *tx).await?;
    if ports.len() > 1 {
        return Err(mismatch());
    }
    let mut error = None;
    let port_id = if let Some(port) = ports.first() {
        let id: u64 = port.try_get("id")?;
        let current: Option<String> = port.try_get("current_order_id")?;
        if !held {
            error = Some("reservation_lost");
        } else if port.try_get::<String, _>("device_status")? != "enabled"
            || port.try_get::<String, _>("vendor_status")? != "enabled"
        {
            error = Some("device_disabled");
        } else if port.try_get::<String, _>("status")? != "idle" || current.is_some() {
            error = Some("port_occupied");
        }
        if error.is_none() {
            sqlx::query("UPDATE device_port SET current_order_id=? WHERE id=?")
                .bind(&p.order_no)
                .bind(id)
                .execute(&mut *tx)
                .await?;
        }
        Some(id)
    } else {
        error = Some("port_missing");
        None
    };
    sqlx::query("INSERT INTO charge_command (command_id,stop_command_id,charge_order_id,payment_order_id,order_no,user_id,device_id,port_no,port_code,port_id,owns_port,status,error) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)")
        .bind(uuid::Uuid::new_v4().to_string()).bind(uuid::Uuid::new_v4().to_string()).bind(p.charge_order_id).bind(p.payment_order_id)
        .bind(&p.order_no).bind(p.user_id).bind(&p.device_id).bind(p.port_no).bind(&p.port_code).bind(port_id).bind(error.is_none())
        .bind(if error.is_some(){"rejected"}else{"pending"}).bind(error).execute(&mut *tx).await?;
    tx.commit().await?;
    Ok(())
}

fn frame(c: &Command, stop: bool) -> Frame {
    Frame {
        device_id: c.device_id.clone(),
        port_no: Some(c.port_no),
        msg_type: "cmd".into(),
        ts: chrono::Utc::now(),
        payload: serde_json::json!({"command":if stop{"STOP"}else{"START"},"command_id":if stop{&c.stop_command_id}else{&c.command_id},"order_no":c.order_no}),
    }
}

async fn drive(st: &AppState, id: u64) -> AppResult<bool> {
    let c = load(st, id).await?.ok_or_else(mismatch)?;
    if c.result_reported {
        return Ok(true);
    }
    match c.status.as_str() {
        "pending" => {
            let holder = format!("{}:{}", c.user_id, c.order_no);
            let lock = PortLock::new(st.redis_cache.clone());
            if !lock.check_holder(&c.port_code, &holder).await? {
                sqlx::query("UPDATE charge_command SET status='rejected',error='reservation_lost' WHERE command_id=? AND status='pending'").bind(&c.command_id).execute(st.db.pool()).await?;
                return Ok(false);
            }
            let Some(session) = st.connections.get(&c.device_id).await else {
                sqlx::query("UPDATE charge_command SET status='rejected',error='device_offline' WHERE command_id=? AND status='pending'").bind(&c.command_id).execute(st.db.pool()).await?;
                return Ok(false);
            };
            if !lock.try_lock(&c.port_code, &holder).await? {
                return Ok(false);
            }
            let changed=sqlx::query("UPDATE charge_command SET status='sent',sent_at=UTC_TIMESTAMP(3),session_id=? WHERE command_id=? AND status='pending'")
                .bind(&session.id).bind(&c.command_id).execute(st.db.pool()).await?.rows_affected();
            if changed == 1 && session.send(&frame(&c, false)).await.is_err() {
                // A partial socket write has unknown physical outcome; STOP must be confirmed.
                sqlx::query("UPDATE charge_command SET status='stopping',error='start_write_uncertain' WHERE command_id=? AND status='sent'").bind(&c.command_id).execute(st.db.pool()).await?;
            }
        }
        "sent" => {
            if c.sent_at
                .is_some_and(|t| chrono::Utc::now().naive_utc() - t > chrono::Duration::seconds(20))
            {
                sqlx::query("UPDATE charge_command SET status='stopping',error='start_ack_timeout' WHERE command_id=? AND status='sent'").bind(&c.command_id).execute(st.db.pool()).await?;
            }
        }
        "stopping" => {
            if c.stop_sent_at
                .is_some_and(|t| chrono::Utc::now().naive_utc() - t < chrono::Duration::seconds(2))
            {
                return Ok(false);
            }
            if let Some(session) = st.connections.get(&c.device_id).await {
                let changed=sqlx::query("UPDATE charge_command SET stop_session_id=?,stop_sent_at=UTC_TIMESTAMP(3) WHERE command_id=? AND status='stopping' AND (stop_sent_at IS NULL OR stop_sent_at<UTC_TIMESTAMP(3)-INTERVAL 2 SECOND)")
                    .bind(&session.id).bind(&c.command_id).execute(st.db.pool()).await?.rows_affected();
                if changed == 1 {
                    let _ = session.send(&frame(&c, true)).await;
                }
            }
        }
        "acked" | "rejected" => {
            let req = api_contracts::StartResultRequest {
                order_no: c.order_no.clone(),
                command_id: c.command_id.clone(),
                device_id: c.device_id.clone(),
                port_no: c.port_no,
                port_id: c.port_id,
                success: c.status == "acked",
                error: c.error.clone(),
            };
            let client = ApiClient::new(st.http.clone(), st.service_token.clone());
            let response: AppResult<serde_json::Value> = client
                .post(
                    st.cfg.service_urls.user.as_deref(),
                    &api_contracts::paths::USER_INTERNAL_START_RESULT
                        .replace(":order_id", &c.order_no),
                    &req,
                )
                .await;
            match response {
                Ok(value) if value.get("ok").and_then(|v| v.as_bool()) == Some(true) => {
                    let mut tx = st.db.pool().begin().await?;
                    if c.status == "rejected" && c.owns_port {
                        sqlx::query("UPDATE device_port SET current_order_id=NULL,status=IF(status='charging','idle',status) WHERE id=? AND current_order_id=?")
                            .bind(c.port_id).bind(&c.order_no).execute(&mut *tx).await?;
                    }
                    sqlx::query("UPDATE charge_command SET result_reported=TRUE WHERE command_id=? AND status=?").bind(&c.command_id).bind(&c.status).execute(&mut *tx).await?;
                    tx.commit().await?;
                    let lock = PortLock::new(st.redis_cache.clone());
                    let holder = format!("{}:{}", c.user_id, c.order_no);
                    let _ = lock.release_lock_if_match(&c.port_code, &holder).await;
                    return Ok(true);
                }
                Err(AppError::Conflict(_)) if c.status == "acked" => {
                    sqlx::query("UPDATE charge_command SET status='stopping',error='order_confirmation_conflict' WHERE command_id=? AND status='acked' AND result_reported=FALSE").bind(&c.command_id).execute(st.db.pool()).await?;
                }
                Err(error) => return Err(error),
                _ => return Err(AppError::ServiceUnavailable("启动结果未持久化确认".into())),
            }
        }
        _ => return Err(mismatch()),
    }
    Ok(false)
}

/// Called only on the same validated device connection that received the command.
pub async fn acknowledge(st: &AppState, session: &str, frame: &Frame) -> AppResult<()> {
    if crate::charge_stop::acknowledge(st,session,frame).await? {return Ok(()); }
    let command = frame
        .payload
        .get("command_id")
        .and_then(|v| v.as_str())
        .ok_or_else(mismatch)?;
    let success = frame
        .payload
        .get("success")
        .and_then(|v| v.as_bool())
        .ok_or_else(mismatch)?;
    let action = frame
        .payload
        .get("command")
        .and_then(|v| v.as_str())
        .ok_or_else(mismatch)?;
    let mut tx = st.db.pool().begin().await?;
    let c: Command = sqlx::query_as(&format!(
        "{COMMAND_SELECT} WHERE command_id=? OR stop_command_id=? FOR UPDATE"
    ))
    .bind(command)
    .bind(command)
    .fetch_optional(&mut *tx)
    .await?
    .ok_or_else(mismatch)?;
    if c.device_id != frame.device_id || Some(c.port_no) != frame.port_no {
        return Err(mismatch());
    }
    let stop = command == c.stop_command_id;
    if (stop && (action != "STOP" || c.stop_session_id.as_deref() != Some(session)))
        || (!stop && (action != "START" || c.session_id.as_deref() != Some(session)))
    {
        return Err(mismatch());
    }
    if stop {
        if success && c.status == "stopping" {
            sqlx::query("UPDATE charge_command SET status='rejected' WHERE command_id=?")
                .bind(&c.command_id)
                .execute(&mut *tx)
                .await?;
        }
    } else if c.status == "sent" {
        sqlx::query("UPDATE charge_command SET status=?,error=? WHERE command_id=?")
            .bind(if success { "acked" } else { "rejected" })
            .bind(if success {
                None
            } else {
                Some("device_rejected_start")
            })
            .bind(&c.command_id)
            .execute(&mut *tx)
            .await?;
        if success {
            let changed = sqlx::query(
                "UPDATE device_port SET status='charging' WHERE id=? AND current_order_id=?",
            )
            .bind(c.port_id)
            .bind(&c.order_no)
            .execute(&mut *tx)
            .await?
            .rows_affected();
            if changed != 1 {
                return Err(mismatch());
            }
        }
    } // Late START ACKs cannot cancel STOP compensation or reverse a final outcome.
    tx.commit().await?;
    Ok(())
}

pub fn spawn_recovery(st: AppState) {
    tokio::spawn(async move {
        let mut interval = tokio::time::interval(Duration::from_secs(2));
        let mut cursor = 0_u64;
        loop {
            interval.tick().await;
            let ids:Result<Vec<u64>,_>=sqlx::query_scalar("SELECT charge_order_id FROM charge_command WHERE result_reported=FALSE AND charge_order_id>? ORDER BY charge_order_id LIMIT 50").bind(cursor).fetch_all(st.db.pool()).await;
            match ids {
                Ok(ids) => {
                    cursor = ids.last().copied().unwrap_or(0);
                    for id in ids {
                        if drive(&st, id).await.is_err() {
                            tracing::warn!(
                                charge_order_id = id,
                                "device command recovery deferred"
                            );
                        }
                    }
                }
                Err(_) => tracing::warn!("device command recovery query failed"),
            }
        }
    });
}
