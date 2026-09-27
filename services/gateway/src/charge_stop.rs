//! User-requested STOP is durable and keeps the port occupied until a metered device ACK.
//!
//! P3:状态机已下沉到 `ChargeStopService`,本文件只留 handler 与后台任务的编排。
use crate::{protocol::Frame, AppState};
use api_contracts::{ChargeStopCommand, ChargeStopResponse};
use axum::{extract::State, Json};
use common_error::{ApiEnvelope, AppResult};
use std::time::Duration;

pub async fn request(
    State(st): State<AppState>,
    Json(req): Json<ChargeStopCommand>,
) -> AppResult<Json<ApiEnvelope<ChargeStopResponse>>> {
    let data = st.stop.request(&req).await?;
    Ok(Json(ApiEnvelope::ok(data, common_error::current_request_id())))
}

/// 仅在收到该 STOP 指令的那条已校验连接上调用。
pub async fn acknowledge(st: &AppState, session: &str, frame: &Frame) -> AppResult<bool> {
    st.stop.acknowledge(session, frame).await
}

pub fn spawn(st: AppState) {
    tokio::spawn(async move {
        let mut tick = tokio::time::interval(Duration::from_secs(2));
        let mut cursor = 0_u64;
        loop {
            tick.tick().await;
            match st.stop.unreported_after(cursor).await {
                Ok(rows) => {
                    cursor = rows.last().map(|v| v.0).unwrap_or(0);
                    for (_, order) in rows {
                        if st.stop.drive(&st.connections, &order).await.is_err() {
                            tracing::warn!(order_no=%order,"stop confirmation deferred");
                        }
                    }
                }
                Err(_) => tracing::warn!("stop recovery query failed"),
            }
        }
    });
}
