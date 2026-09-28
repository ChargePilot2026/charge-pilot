//! 用户主动 STOP 能力域(P3)
//!
//! 语义(改动前必读):STOP 是**持久**的 —— 端口在设备计量 ACK 回来之前
//! 一直保持占用,断连与部分写入都只做重发,绝不假装已停止。
//! `meter_required: true` 的 STOP 帧必须带回 `meter`,否则不认。

use api_contracts::{ChargeEndMeter, ChargeEndRequest, ChargeStopResponse};
use common_app::ServiceBase;
use common_error::{AppError, AppResult};
use common_http::internal::ApiClient;
use common_redis::StreamEnvelope;
use serde_json::Value;
use sqlx::Row;

use crate::protocol::Frame;

#[derive(sqlx::FromRow)]
pub struct Stop {
    pub command_id: String,
    pub start_command_id: String,
    pub charge_order_id: u64,
    pub order_no: String,
    pub user_id: u64,
    pub device_id: String,
    pub port_no: u8,
    pub port_id: u64,
    pub status: String,
    pub session_id: Option<String>,
    pub sent_at: Option<chrono::NaiveDateTime>,
    pub meter_json: Option<Value>,
    pub result_reported: bool,
}

pub fn conflict() -> AppError {
    AppError::Conflict("停止请求与充电订单不一致".into())
}

#[derive(Clone)]
pub struct ChargeStopService {
    base: ServiceBase,
}

impl ChargeStopService {
    pub fn new(base: ServiceBase) -> Self {
        Self { base }
    }

    pub async fn load(&self, order_no: &str) -> AppResult<Option<Stop>> {
        Ok(sqlx::query_as("SELECT * FROM charge_stop_command WHERE order_no=?")
            .bind(order_no)
            .fetch_optional(self.base.pool())
            .await?)
    }

    /// 受理一次 STOP 请求(幂等)。
    pub async fn request(&self, req: &api_contracts::ChargeStopCommand) -> AppResult<ChargeStopResponse> {
        if req.order_no.is_empty()
            || req.order_no.len() > 64
            || req.user_id == 0
            || req.source != "user_app"
        {
            return Err(AppError::BadRequest("停止请求无效".into()));
        }
        let mut saved = self.load(&req.order_no).await?;
        if saved.is_none() {
            let start = sqlx::query(
                "SELECT charge_order_id,user_id FROM charge_command WHERE order_no=? AND result_reported=TRUE AND status='acked' AND owns_port=TRUE",
            )
            .bind(&req.order_no)
            .fetch_optional(self.base.pool())
            .await?
            .ok_or_else(conflict)?;
            let cid: u64 = start.try_get("charge_order_id")?;
            if start.try_get::<u64, _>("user_id")? != req.user_id {
                return Err(conflict());
            }
            let order: api_contracts::orders::OrderDetail = self
                .base
                .new_client()
                .get(
                    self.base.cfg().service_urls.user.as_deref(),
                        &api_contracts::fill_path(
                            api_contracts::paths::USER_INTERNAL_ORDER_DETAIL,
                            "order_id",
                            &cid.to_string(),
                        ),
                    &(),
                )
                .await?;
            if order.order.order_no != req.order_no
                || order.order.user_id != req.user_id
                || order.order.status != "charging"
            {
                return Err(conflict());
            }
            let count = self.insert_stop_command(cid).await?;
            saved = self.load(&req.order_no).await?;
            if count == 0 && saved.is_none() {
                return Err(conflict());
            }
        }
        let saved = saved.ok_or_else(conflict)?;
        if saved.user_id != req.user_id || saved.order_no != req.order_no {
            return Err(conflict());
        }
        Ok(ChargeStopResponse {
            accepted: true,
            // `stopped` 只在计量结果已上报时为真,受理 ≠ 已停止。
            stopped: saved.result_reported,
            command_id: saved.command_id,
        })
    }

    /// 从已确认的 START 指令派生 STOP 指令。返回插入行数。
    async fn insert_stop_command(&self, charge_order_id: u64) -> AppResult<u64> {
        let mut tx = self.base.begin().await?;
        let count = sqlx::query(
            "INSERT IGNORE INTO charge_stop_command (command_id,start_command_id,charge_order_id,order_no,user_id,device_id,port_no,port_id) \
             SELECT ?,c.command_id,c.charge_order_id,c.order_no,c.user_id,c.device_id,c.port_no,c.port_id \
             FROM charge_command c JOIN device_port p ON p.id=c.port_id \
             WHERE c.charge_order_id=? AND c.status='acked' AND c.result_reported=TRUE \
             AND p.current_order_id=c.order_no AND p.status='charging'",
        )
        .bind(uuid::Uuid::new_v4().to_string())
        .bind(charge_order_id)
        .execute(tx.executor())
        .await?
        .rows_affected();
        tx.commit().await?;
        Ok(count)
    }

    /// 推进一步 STOP 状态机。
    pub async fn drive(
        &self,
        connections: &crate::protocol::connections::Connections,
        order_no: &str,
    ) -> AppResult<()> {
        let c = self.load(order_no).await?.ok_or_else(conflict)?;
        if c.result_reported {
            return Ok(());
        }
        if c.status == "pending" || c.status == "sent" {
            if c.sent_at
                .is_some_and(|t| chrono::Utc::now().naive_utc() - t < chrono::Duration::seconds(2))
            {
                return Ok(());
            }
            if let Some(session) = connections.get(&c.device_id).await {
                let changed = sqlx::query(
                    "UPDATE charge_stop_command SET status='sent',session_id=?,sent_at=UTC_TIMESTAMP(3) WHERE command_id=? AND status IN ('pending','sent') AND (sent_at IS NULL OR sent_at<UTC_TIMESTAMP(3)-INTERVAL 2 SECOND)",
                )
                .bind(&session.id)
                .bind(&c.command_id)
                .execute(self.base.pool())
                .await?
                .rows_affected();
                if changed == 1 {
                    let frame = Frame {
                        device_id: c.device_id,
                        port_no: Some(c.port_no),
                        msg_type: "cmd".into(),
                        ts: chrono::Utc::now(),
                        payload: serde_json::json!({"command":"STOP","command_id":c.command_id,"order_no":c.order_no,"meter_required":true}),
                    };
                    // 断连/部分写入之后重发同一条 STOP,绝不假装已停止。
                    let _ = session.send(&frame).await;
                }
            }
            return Ok(());
        }
        let meter: ChargeEndMeter =
            serde_json::from_value(c.meter_json.clone().ok_or_else(conflict)?)?;
        let req = ChargeEndRequest {
            order_no: c.order_no.clone(),
            start_command_id: c.start_command_id.clone(),
            stop_command_id: c.command_id.clone(),
            device_id: c.device_id.clone(),
            port_no: c.port_no,
            port_id: c.port_id,
            meter: meter.clone(),
        };
        let result: Value = self
            .base
            .new_client()
            .post(
                self.base.cfg().service_urls.user.as_deref(),
                // D24:走 fill_path —— 占位符名与常量对不上时直接 panic,
                // 不再静默发出带字面量 `:order_no` 的坏 URL。
                &api_contracts::fill_path(
                    api_contracts::paths::USER_INTERNAL_END_RESULT,
                    "order_no",
                    &c.order_no,
                ),
                &req,
            )
            .await?;
        if result.get("ok").and_then(|v| v.as_bool()) != Some(true) {
            return Err(conflict());
        }
        self.finish(&c, &meter).await
    }

    /// 释放端口、投 `charge_ended` 事件、标记已上报 —— 同一事务。
    async fn finish(&self, c: &Stop, meter: &ChargeEndMeter) -> AppResult<()> {
        let mut tx = self.base.begin().await?;
        let reported: bool = sqlx::query_scalar(
            "SELECT result_reported FROM charge_stop_command WHERE command_id=? FOR UPDATE",
        )
        .bind(&c.command_id)
        .fetch_one(tx.executor())
        .await?;
        if !reported {
            sqlx::query("UPDATE device_port SET status=IF(status='charging','idle',status),current_order_id=NULL WHERE id=? AND current_order_id=?")
                .bind(c.port_id).bind(&c.order_no).execute(tx.executor()).await?;
            let mut event = StreamEnvelope::new(
                "charge_ended",
                "gateway",
                serde_json::json!({
                    "charge_order_id": c.charge_order_id, "order_no": c.order_no, "user_id": c.user_id,
                    "device_id": c.device_id, "port_no": c.port_no, "stop_command_id": c.command_id,
                    "charged_wh": meter.charged_wh,
                    "charged_kwh": format!("{}.{:03}", meter.charged_wh / 1000, meter.charged_wh % 1000),
                    "charged_seconds": meter.charged_seconds, "ended_at": meter.ended_at,
                }),
            );
            event.event_id = c.command_id.clone();
            sqlx::query("INSERT INTO event_outbox (event_id,stream,envelope_json) VALUES (?,?,?)")
                .bind(&event.event_id)
                .bind(common_redis::streams::CHARGE_ENDED)
                .bind(serde_json::to_value(&event)?)
                .execute(tx.executor())
                .await?;
            sqlx::query("UPDATE charge_stop_command SET result_reported=TRUE WHERE command_id=?")
                .bind(&c.command_id)
                .execute(tx.executor())
                .await?;
        }
        tx.commit().await?;
        Ok(())
    }

    /// 处理设备 STOP ACK。返回 `true` 表示这条 ACK 属于 STOP 指令。
    pub async fn acknowledge(&self, session: &str, frame: &Frame) -> AppResult<bool> {
        let Some(id) = frame.payload.get("command_id").and_then(|v| v.as_str()) else {
            return Ok(false);
        };
        let mut tx = self.base.begin().await?;
        let c: Option<Stop> = sqlx::query_as(
            "SELECT * FROM charge_stop_command WHERE command_id=? FOR UPDATE",
        )
        .bind(id)
        .fetch_optional(tx.executor())
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
        // 同一 STOP 重复 ACK 时计量读数必须完全一致,否则拒绝覆盖。
        if c.meter_json.as_ref().is_some_and(|v| v != &value) {
            return Err(conflict());
        }
        sqlx::query("UPDATE charge_stop_command SET status='acked',meter_json=? WHERE command_id=?")
            .bind(value)
            .bind(id)
            .execute(tx.executor())
            .await?;
        tx.commit().await?;
        Ok(true)
    }

    /// 重启后捞回未上报的 STOP 继续推进。
    pub async fn unreported_after(&self, cursor: u64) -> AppResult<Vec<(u64, String)>> {
        Ok(sqlx::query_as(
            "SELECT charge_order_id,order_no FROM charge_stop_command WHERE result_reported=FALSE AND charge_order_id>? ORDER BY charge_order_id LIMIT 50",
        )
        .bind(cursor)
        .fetch_all(self.base.pool())
        .await?)
    }
}
