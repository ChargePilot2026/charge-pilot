//! 充电指令能力域(P3):`charge_command` 状态机与端口占用联动。
//!
//! 状态机语义(改动前务必读):
//! `pending → sent → acked/rejected`,失败与超时走 `stopping` 补偿,
//! `result_reported=TRUE` 之前绝不 ACK 掉 Redis Stream —— 用户服务
//! 持久化确认结果之后才算完成。

// P5:本文件是 gateway 侧 **充电指令域的 repository 层**,SQL 只允许出现在
// 这里(方案 §三:handler/usecase 层禁 SQL,由 clippy `disallowed-methods`
// 保证)。`charge_command` 状态机与端口占用联动必须与 SQL 同处一层:
//! 「判定 → 写状态 → 释放 Redis 锁」跨两个存储,拆开会让补偿路径失去原子性。
// 本文件内剩下的 `Value` / `json!` 均落在方案 §三 例外清单第 1、2 类:
//   - 第 2 类:`charge_command` / `charge_stop_command` 的 `*_json` 列原样透出;
//   - 第 1 类:`charge_ended` 等 Redis Stream 事件载荷(schema 归消费方约定);
//   - user 服务 start-result 响应体只读 `ok` 一个键,建模整个信封无收益。
#![allow(clippy::disallowed_methods)]
#![allow(clippy::disallowed_types)]
#![allow(clippy::disallowed_macros)]

use api_contracts::StartResultRequest;
use common_app::ServiceBase;
use common_db::Tx;
use common_error::{AppError, AppResult};
use common_http::internal::ApiClient;
use common_redis::{PortLock, StreamEntry};
use serde::Deserialize;
use sqlx::Row;
use std::time::Duration;

use crate::protocol::Frame;

#[derive(Debug, Deserialize)]
pub struct Started {
    pub charge_order_id: u64,
    pub payment_order_id: u64,
    pub user_id: u64,
    pub order_no: String,
    pub device_id: String,
    pub port_no: u8,
    pub port_code: String,
}

#[derive(Debug, sqlx::FromRow)]
pub struct Command {
    pub command_id: String,
    pub stop_command_id: String,
    pub charge_order_id: u64,
    pub payment_order_id: u64,
    pub order_no: String,
    pub user_id: u64,
    pub device_id: String,
    pub port_no: u8,
    pub port_code: String,
    pub port_id: Option<u64>,
    pub owns_port: bool,
    pub status: String,
    pub session_id: Option<String>,
    pub stop_session_id: Option<String>,
    pub sent_at: Option<chrono::NaiveDateTime>,
    pub stop_sent_at: Option<chrono::NaiveDateTime>,
    pub error: Option<String>,
    pub result_reported: bool,
}

const COMMAND_SELECT: &str = "SELECT CAST(command_id AS CHAR CHARACTER SET utf8mb4) AS command_id,CAST(stop_command_id AS CHAR CHARACTER SET utf8mb4) AS stop_command_id,charge_order_id,payment_order_id,order_no,user_id,device_id,port_no,port_code,port_id,owns_port,status,session_id,stop_session_id,sent_at,stop_sent_at,error,result_reported FROM charge_command";

pub fn mismatch() -> AppError {
    AppError::Conflict("启动事件与设备或订单不一致".into())
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

fn frame(c: &Command, stop: bool) -> Frame {
    Frame {
        device_id: c.device_id.clone(),
        port_no: Some(c.port_no),
        msg_type: "cmd".into(),
        ts: chrono::Utc::now(),
        payload: serde_json::json!({
            "command": if stop { "STOP" } else { "START" },
            "command_id": if stop { &c.stop_command_id } else { &c.command_id },
            "order_no": c.order_no,
        }),
    }
}

#[derive(Clone)]
pub struct ChargeCommandService {
    base: ServiceBase,
    /// 端口预留锁在 Redis 里,状态机判定 `reservation_lost` 依赖它。
    redis_cache: common_redis::RedisCache,
}

impl ChargeCommandService {
    pub fn new(base: ServiceBase, redis_cache: common_redis::RedisCache) -> Self {
        Self { base, redis_cache }
    }

    pub async fn load(&self, charge_order_id: u64) -> AppResult<Option<Command>> {
        Ok(sqlx::query_as(&format!("{COMMAND_SELECT} WHERE charge_order_id=?"))
            .bind(charge_order_id)
            .fetch_optional(self.base.pool())
            .await?)
    }

    /// Stream 消费入口。返回 `Ok(())` 表示可 ACK 掉这条 Stream 消息。
    pub async fn handle(
        &self,
        connections: &crate::protocol::connections::Connections,
        entry: &StreamEntry,
    ) -> AppResult<()> {
        let p: Started = common_stream::parse_payload(&entry.envelope)?;
        if p.charge_order_id == 0
            || p.payment_order_id == 0
            || p.user_id == 0
            || p.port_no == 0
            || [&p.order_no, &p.device_id, &p.port_code]
                .iter()
                .any(|v| {
                    v.is_empty()
                        || v.len() > 64
                        || !v
                            .bytes()
                            .all(|c| c.is_ascii_alphanumeric() || b"-_:.".contains(&c))
                })
        {
            return Err(mismatch());
        }
        if let Some(c) = self.load(p.charge_order_id).await? {
            if !same(&c, &p) {
                return Err(mismatch());
            }
        } else {
            self.create(connections, &p).await?;
        }
        // 在用户服务持久化接受真实结果之前,Stream 一律不 ACK。
        for _ in 0..20 {
            if self.drive(connections, p.charge_order_id).await? {
                return Ok(());
            }
            tokio::time::sleep(Duration::from_millis(250)).await;
        }
        Err(AppError::ServiceUnavailable("设备启动结果尚未确认".into()))
    }

    async fn create(
        &self,
        connections: &crate::protocol::connections::Connections,
        p: &Started,
    ) -> AppResult<()> {
        let client = self.base.new_client();
        let order: api_contracts::orders::OrderDetail = client
            .get(
                self.base.cfg().service_urls.user.as_deref(),
                &api_contracts::fill_path(
                    api_contracts::paths::USER_INTERNAL_ORDER_DETAIL,
                    "order_id",
                    &p.charge_order_id.to_string(),
                ),
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
        let cache = self.redis_cache.clone();
        let holder = format!("{}:{}", p.user_id, p.order_no);
        let held = PortLock::new(cache.clone())
            .check_holder(&p.port_code, &holder)
            .await?;
        let (port_id, rejection) = self.occupy_port(&p.port_code, &p.device_id, p.port_no, &p.order_no, held).await?;
        let mut tx = self.base.begin().await?;
        sqlx::query(
            "INSERT INTO charge_command (command_id,stop_command_id,charge_order_id,payment_order_id,order_no,user_id,device_id,port_no,port_code,port_id,owns_port,status,error) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)",
        )
        .bind(uuid::Uuid::new_v4().to_string())
        .bind(uuid::Uuid::new_v4().to_string())
        .bind(p.charge_order_id)
        .bind(p.payment_order_id)
        .bind(&p.order_no)
        .bind(p.user_id)
        .bind(&p.device_id)
        .bind(p.port_no)
        .bind(&p.port_code)
        .bind(port_id)
        .bind(rejection.is_none())
        .bind(if rejection.is_some() { "rejected" } else { "pending" })
        .bind(rejection)
        .execute(tx.executor())
        .await?;
        tx.commit().await?;
        Ok(())
    }

    /// 加锁读端口并在允许时占用。返回 `(port_id, rejection)`。
    async fn occupy_port(
        &self,
        port_code: &str,
        device_id: &str,
        port_no: u8,
        order_no: &str,
        reservation_held: bool,
    ) -> AppResult<(Option<u64>, Option<&'static str>)> {
        let mut tx = self.base.begin().await?;
        let ports = sqlx::query(
            "SELECT p.id,p.status,p.current_order_id,d.status AS device_status,v.status AS vendor_status FROM device_port p JOIN device d ON d.device_id=p.device_id AND d.deleted_at IS NULL JOIN vendor v ON v.id=d.vendor_id AND v.deleted_at IS NULL WHERE p.port_code=? AND p.device_id=? AND p.port_no=? AND p.deleted_at IS NULL FOR UPDATE",
        )
        .bind(port_code).bind(device_id).bind(port_no).fetch_all(tx.executor()).await?;
        if ports.len() > 1 {
            return Err(mismatch());
        }
        let mut error = None;
        let port_id = if let Some(port) = ports.first() {
            let id: u64 = port.try_get("id")?;
            let current: Option<String> = port.try_get("current_order_id")?;
            if !reservation_held {
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
                    .bind(order_no).bind(id).execute(tx.executor()).await?;
            }
            Some(id)
        } else {
            error = Some("port_missing");
            None
        };
        tx.commit().await?;
        Ok((port_id, error))
    }

    /// 推进一步状态机。返回 `true` 表示本轮已完成、可 ACK Stream。
    pub async fn drive(
        &self,
        connections: &crate::protocol::connections::Connections,
        id: u64,
    ) -> AppResult<bool> {
        let c = self.load(id).await?.ok_or_else(mismatch)?;
        if c.result_reported {
            return Ok(true);
        }
        match c.status.as_str() {
            "pending" => {
                let cache = self.redis_cache.clone();
                let holder = format!("{}:{}", c.user_id, c.order_no);
                let lock = PortLock::new(cache);
                if !lock.check_holder(&c.port_code, &holder).await? {
                    self.mark_pending_rejected(&c.command_id, "reservation_lost").await?;
                    return Ok(false);
                }
                let Some(session) = connections.get(&c.device_id).await else {
                    self.mark_pending_rejected(&c.command_id, "device_offline").await?;
                    return Ok(false);
                };
                if !lock.try_lock(&c.port_code, &holder).await? {
                    return Ok(false);
                }
                let changed = sqlx::query(
                    "UPDATE charge_command SET status='sent',sent_at=UTC_TIMESTAMP(3),session_id=? WHERE command_id=? AND status='pending'",
                )
                .bind(&session.id)
                .bind(&c.command_id)
                .execute(self.base.pool())
                .await?
                .rows_affected();
                if changed == 1 && session.send(&frame(&c, false)).await.is_err() {
                    // 部分 socket 写入的物理结果未知,必须走 STOP 补偿确认。
                    self.mark(&c.command_id, "stopping", "start_write_uncertain", Some("sent")).await?;
                }
            }
            "sent" => {
                if c
                    .sent_at
                    .is_some_and(|t| chrono::Utc::now().naive_utc() - t > chrono::Duration::seconds(20))
                {
                    self.mark(&c.command_id, "stopping", "start_ack_timeout", Some("sent")).await?;
                }
            }
            "stopping" => {
                if c.stop_sent_at
                    .is_some_and(|t| chrono::Utc::now().naive_utc() - t < chrono::Duration::seconds(2))
                {
                    return Ok(false);
                }
                if let Some(session) = connections.get(&c.device_id).await {
                    let changed = sqlx::query(
                        "UPDATE charge_command SET stop_session_id=?,stop_sent_at=UTC_TIMESTAMP(3) WHERE command_id=? AND status='stopping' AND (stop_sent_at IS NULL OR stop_sent_at<UTC_TIMESTAMP(3)-INTERVAL 2 SECOND)",
                    )
                    .bind(&session.id)
                    .bind(&c.command_id)
                    .execute(self.base.pool())
                    .await?
                    .rows_affected();
                    if changed == 1 {
                        let _ = session.send(&frame(&c, true)).await;
                    }
                }
            }
            "acked" | "rejected" => {
                let req = StartResultRequest {
                    order_no: c.order_no.clone(),
                    command_id: c.command_id.clone(),
                    device_id: c.device_id.clone(),
                    port_no: c.port_no,
                    port_id: c.port_id,
                    success: c.status == "acked",
                    error: c.error.clone(),
                };
                let client = self.base.new_client();
                let response: AppResult<serde_json::Value> = client
                    .post(
                        self.base.cfg().service_urls.user.as_deref(),
                        &api_contracts::fill_path(
                            api_contracts::paths::USER_INTERNAL_START_RESULT,
                            "order_id",
                            &c.order_no,
                        ),
                        &req,
                    )
                    .await;
                match response {
                    Ok(value) if value.get("ok").and_then(|v| v.as_bool()) == Some(true) => {
                        self.finish(&c).await?;
                        let cache = self.redis_cache.clone();
                        let lock = PortLock::new(cache);
                        let holder = format!("{}:{}", c.user_id, c.order_no);
                        let _ = lock.release_lock_if_match(&c.port_code, &holder).await;
                        return Ok(true);
                    }
                    Err(AppError::Conflict(_)) if c.status == "acked" => {
                        self.mark(&c.command_id, "stopping", "order_confirmation_conflict", Some("acked")).await?;
                    }
                    Err(error) => return Err(error),
                    _ => return Err(AppError::ServiceUnavailable("启动结果未持久化确认".into())),
                }
            }
            _ => return Err(mismatch()),
        }
        Ok(false)
    }

    /// 用户服务已确认:释放端口并标记结果已上报,同一事务内完成。
    async fn finish(&self, c: &Command) -> AppResult<()> {
        let mut tx = self.base.begin().await?;
        if c.status == "rejected" && c.owns_port {
            sqlx::query(
                "UPDATE device_port SET current_order_id=NULL,status=IF(status='charging','idle',status) WHERE id=? AND current_order_id=?",
            )
            .bind(c.port_id)
            .bind(&c.order_no)
            .execute(tx.executor())
            .await?;
        }
        sqlx::query("UPDATE charge_command SET result_reported=TRUE WHERE command_id=? AND status=?")
            .bind(&c.command_id)
            .bind(&c.status)
            .execute(tx.executor())
            .await?;
        tx.commit().await?;
        Ok(())
    }

    async fn mark_pending_rejected(&self, command_id: &str, error: &str) -> AppResult<()> {
        sqlx::query(
            "UPDATE charge_command SET status='rejected',error=? WHERE command_id=? AND status='pending'",
        )
        .bind(error)
        .bind(command_id)
        .execute(self.base.pool())
        .await?;
        Ok(())
    }

    /// `from` 为 `Some` 时附带状态守卫,避免覆盖已被推进的状态。
    async fn mark(
        &self,
        command_id: &str,
        status: &str,
        error: &str,
        from: Option<&str>,
    ) -> AppResult<()> {
        let n = match from {
            Some(from) => {
                sqlx::query("UPDATE charge_command SET status=?,error=? WHERE command_id=? AND status=?")
                    .bind(status).bind(error).bind(command_id).bind(from)
                    .execute(self.base.pool()).await?.rows_affected()
            }
            None => {
                sqlx::query("UPDATE charge_command SET status=?,error=? WHERE command_id=?")
                    .bind(status).bind(error).bind(command_id)
                    .execute(self.base.pool()).await?.rows_affected()
            }
        };
        let _ = n;
        Ok(())
    }

    /// 设备 ACK。**只**在收到该指令的那条已校验连接上调用。
    pub async fn acknowledge(&self, session: &str, f: &Frame) -> AppResult<()> {
        let command = f
            .payload
            .get("command_id")
            .and_then(|v| v.as_str())
            .ok_or_else(mismatch)?;
        let success = f
            .payload
            .get("success")
            .and_then(|v| v.as_bool())
            .ok_or_else(mismatch)?;
        let action = f
            .payload
            .get("command")
            .and_then(|v| v.as_str())
            .ok_or_else(mismatch)?;
        let mut tx = self.base.begin().await?;
        let c: Command = sqlx::query_as(&format!(
            "{COMMAND_SELECT} WHERE command_id=? OR stop_command_id=? FOR UPDATE"
        ))
        .bind(command)
        .bind(command)
        .fetch_optional(tx.executor())
        .await?
        .ok_or_else(mismatch)?;
        if c.device_id != f.device_id || Some(c.port_no) != f.port_no {
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
                    .execute(tx.executor())
                    .await?;
            }
        } else if c.status == "sent" {
            sqlx::query("UPDATE charge_command SET status=?,error=? WHERE command_id=?")
                .bind(if success { "acked" } else { "rejected" })
                .bind(if success { None } else { Some("device_rejected_start") })
                .bind(&c.command_id)
                .execute(tx.executor())
                .await?;
            if success {
                let changed = sqlx::query(
                    "UPDATE device_port SET status='charging' WHERE id=? AND current_order_id=?",
                )
                .bind(c.port_id)
                .bind(&c.order_no)
                .execute(tx.executor())
                .await?
                .rows_affected();
                if changed != 1 {
                    return Err(mismatch());
                }
            }
        } // 迟到的 START ACK 不能取消 STOP 补偿,也不能反转终态。
        tx.commit().await?;
        Ok(())
    }

    /// 网关重启后捞回未上报结果的指令继续推进。
    pub async fn unreported_after(&self, cursor: u64) -> AppResult<Vec<u64>> {
        Ok(sqlx::query_scalar(
            "SELECT charge_order_id FROM charge_command WHERE result_reported=FALSE AND charge_order_id>? ORDER BY charge_order_id LIMIT 50",
        )
        .bind(cursor)
        .fetch_all(self.base.pool())
        .await?)
    }
}

/// 供服务层读取事务类型(避免 handler 直接依赖 common-db)。
pub type CommandTx<'a> = Tx<'a>;
