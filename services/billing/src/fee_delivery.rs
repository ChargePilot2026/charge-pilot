//! Durable at-least-once HTTP delivery; user receipt makes the effects idempotent.
use crate::AppState;
use common_error::{AppError, AppResult};
use sqlx::Row;

pub fn spawn(st: AppState) {
    tokio::spawn(async move {
        let mut tick = tokio::time::interval(std::time::Duration::from_secs(2));
        loop {
            tick.tick().await;
            for _ in 0..50 {
                match deliver_one(&st).await {
                    Ok(true) => {}
                    Ok(false) => break,
                    Err(error) => {
                        tracing::warn!(%error,"fee delivery failed; retrying");
                        break;
                    }
                }
            }
        }
    });
}
async fn deliver_one(st: &AppState) -> AppResult<bool> {
    let mut tx = st.db.pool().begin().await?;
    let row=sqlx::query("SELECT charge_order_id,payload_json FROM fee_delivery WHERE delivered=0 AND scheduled_at<=UTC_TIMESTAMP(3) ORDER BY scheduled_at,charge_order_id LIMIT 1 FOR UPDATE SKIP LOCKED").fetch_optional(&mut *tx).await?;
    let Some(row) = row else {
        tx.rollback().await?;
        return Ok(false);
    };
    let cid: u64 = row.try_get("charge_order_id")?;
    let payload: api_contracts::pricing::FeeResult =
        serde_json::from_value(row.try_get("payload_json")?)?;
    let client = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone());
    let result: AppResult<serde_json::Value> = client
        .post(
            st.cfg.service_urls.user.as_deref(),
            &api_contracts::paths::USER_INTERNAL_FEE_RESULT.replace(":order_id", &cid.to_string()),
            &payload,
        )
        .await;
    let result = result.and_then(|v| {
        if v.get("ok").and_then(|v| v.as_bool()) == Some(true) {
            Ok(())
        } else {
            Err(AppError::Conflict("用户服务未确认实结费用".into()))
        }
    });
    if result.is_ok() {
        sqlx::query("UPDATE fee_delivery SET delivered=1,delivered_at=UTC_TIMESTAMP(3),attempts=attempts+1 WHERE charge_order_id=?").bind(cid).execute(&mut *tx).await?;
    } else {
        sqlx::query("UPDATE fee_delivery SET attempts=attempts+1,scheduled_at=DATE_ADD(UTC_TIMESTAMP(3),INTERVAL 30 SECOND) WHERE charge_order_id=?").bind(cid).execute(&mut *tx).await?;
    }
    tx.commit().await?;
    if let Err(error) = result {
        tracing::warn!(charge_order_id=cid,%error,"fee delivery retained for retry");
    }
    Ok(true)
}
