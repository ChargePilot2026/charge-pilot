//! Durable START / compensating STOP state machine. Socket writes are never device ACKs.
//!
//! P3:状态机已下沉到 `ChargeCommandService`,本文件只留编排。
use crate::{protocol::Frame, AppState};
use common_error::AppResult;
use common_redis::StreamEntry;
use std::time::Duration;

pub async fn handle(st: &AppState, entry: &StreamEntry) -> AppResult<()> {
    st.command.handle(&st.connections, entry).await
}

pub async fn acknowledge(st: &AppState, session: &str, frame: &Frame) -> AppResult<()> {
    if crate::charge_stop::acknowledge(st, session, frame).await? {
        return Ok(());
    }
    st.command.acknowledge(session, frame).await
}

pub fn spawn_recovery(st: AppState) {
    tokio::spawn(async move {
        let mut interval = tokio::time::interval(Duration::from_secs(2));
        let mut cursor = 0_u64;
        loop {
            interval.tick().await;
            match st.command.unreported_after(cursor).await {
                Ok(ids) => {
                    cursor = ids.last().copied().unwrap_or(0);
                    for id in ids {
                        if st.command.drive(&st.connections, id).await.is_err() {
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
