//! 事件消费能力域(P3)
//!
//! `db` 私有,handler / 任务循环拿不到裸 pool。原 `CompTxHandler::handle` 的
//! 校验与落库逻辑**逐字搬移**,顺序与判定口径未改:
//!
//! - 事件类型必须是 `refund_completed`,否则 BadRequest。
//! - `refund_no` 必须非空、≤64 字节、不含控制字符。
//! - `success` 必须显式存在(缺字段即拒,不默认 false)。
//! - `event_id` 必须是 UUID;`occurred_at` 必须是 RFC3339。
//! - **幂等重放判定**:`(tx_id, created_month)` 命中已有行时,逐字比对
//!   `stream` / `payload_hash` / `status`;**三者任一不同即 `Conflict`**,
//!   不覆盖既有台账 —— 台账是审计凭据,静默改写等于抹掉对账证据。
//!   完全一致则 commit 后正常返回,交由 consumer ACK。
//! - `committed_at` 只在 `success` 时写入事件发生时刻;失败记 `NULL`。
//!
//! ⚠️ 本域是**审计台账**,不声称执行任何退款或补偿动作。

// 本文件是 worker 侧 **event(事件消费)域** 的 repository 层,SQL 只允许
// 出现在这里(方案 §三:handler / 任务循环层禁 SQL,由 clippy
// disallowed-methods 保证)。`AppState` 上已无裸 `db` 字段,消费入口拿不到
// 连接池。
#![allow(clippy::disallowed_methods)]

use common_app::ServiceBase;
use common_error::{AppError, AppResult};
use common_redis::StreamEntry;
use sha2::{Digest, Sha256};
use sqlx::Row;

#[derive(Clone)]
pub struct EventService {
    base: ServiceBase,
}

impl EventService {
    pub fn new(base: ServiceBase) -> Self {
        Self { base }
    }

    // ===== 健康检查 =====

    pub async fn ping(&self) -> AppResult<()> {
        self.base.ping().await
    }

    // ===== comp_tx 结果审计 =====

    /// 把已确认的跨服务结果持久化到 worker_db 的 `comp_tx_log`。
    pub async fn record_comp_tx(&self, entry: &StreamEntry) -> AppResult<()> {
        if entry.envelope.event_type != "refund_completed" {
            return Err(AppError::BadRequest("unsupported comp_tx event type".into()));
        }
        let payload = &entry.envelope.payload;
        let refund_no = payload.get("refund_no").and_then(|v| v.as_str())
            .filter(|v| !v.is_empty() && v.len() <= 64 && !v.chars().any(char::is_control))
            .ok_or_else(|| AppError::BadRequest("refund_completed event has no valid refund_no".into()))?;
        let success = payload.get("success").and_then(|v| v.as_bool())
            .ok_or_else(|| AppError::BadRequest("refund_completed event has no success result".into()))?;
        let event_id = uuid::Uuid::parse_str(&entry.envelope.event_id)
            .map_err(|_| AppError::BadRequest("comp_tx event id is not a UUID".into()))?.to_string();
        let occurred_at = chrono::DateTime::parse_from_rfc3339(&entry.envelope.occurred_at)
            .map_err(|_| AppError::BadRequest("comp_tx event timestamp is invalid".into()))?.to_utc();
        let created_month = occurred_at.format("%Y-%m-01").to_string();
        let payload_hash = hex::encode(Sha256::digest(serde_json::to_vec(&entry.envelope)?));
        let status = if success { "committed" } else { "failed" };

        let mut tx = self.base.begin().await?;
        let existing = sqlx::query("SELECT stream,payload_hash,status FROM comp_tx_log WHERE tx_id=? AND created_month=? FOR UPDATE")
            .bind(&event_id).bind(&created_month).fetch_optional(tx.executor()).await?;
        if let Some(row) = existing {
            let saved_stream: String = row.try_get("stream")?;
            let saved_hash: Option<String> = row.try_get("payload_hash")?;
            let saved_status: String = row.try_get("status")?;
            if saved_stream != entry.stream || saved_hash.as_deref() != Some(&payload_hash) || saved_status != status {
                return Err(AppError::Conflict("comp_tx event id replayed with different contents".into()));
            }
            tx.commit().await?;
            return Ok(());
        }
        sqlx::query("INSERT INTO comp_tx_log (tx_id,consumer_group,stream,payload_hash,status,created_month,committed_at) VALUES (?,'worker-cg',?,?,?,?,?)")
            .bind(&event_id).bind(&entry.stream).bind(payload_hash).bind(status).bind(&created_month)
            .bind(if success { Some(occurred_at.naive_utc()) } else { None::<chrono::NaiveDateTime> })
            .execute(tx.executor()).await?;
        tx.commit().await?;
        tracing::info!(event_id, refund_no, success, "recorded compensation outcome");
        Ok(())
    }
}
