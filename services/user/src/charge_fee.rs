//! A fee, refund reservation and outbox event commit together under the payment lock.
use crate::AppState;
use api_contracts::pricing::FeeResult;
use axum::{
    extract::{Path, State},
    Json,
};
use common_error::{ApiEnvelope, AppError, AppResult};
use sqlx::Row;

fn conflict() -> AppError {
    AppError::Conflict("实结费用与订单、计量或退款记录不一致".into())
}
pub async fn receive(
    State(st): State<AppState>,
    Path(cid): Path<u64>,
    Json(req): Json<FeeResult>,
) -> AppResult<Json<ApiEnvelope<serde_json::Value>>> {
    let mut tx = st.db.pool().begin().await?;
    apply(&mut tx, cid, &req).await?;
    tx.commit().await?;
    Ok(Json(ApiEnvelope::ok(
        serde_json::json!({"ok":true}),
        common_error::current_request_id(),
    )))
}
pub async fn apply(
    tx: &mut sqlx::Transaction<'_, sqlx::MySql>,
    cid: u64,
    req: &FeeResult,
) -> AppResult<()> {
    if cid == 0
        || req.source.charge_order_id != cid
        || req.calculation_no.is_empty()
        || req.calculation_no.len() > 64
        || req.electric_cents < 0
        || req.service_cents < 0
        || !(0..=i64::from(i32::MAX)).contains(&req.total_cents)
        || req.electric_cents.checked_add(req.service_cents) != Some(req.total_cents)
    {
        return Err(conflict());
    }
    let ids: Vec<Option<u64>> = sqlx::query_scalar(
        "SELECT payment_order_id FROM charge_order WHERE id=? AND deleted_at IS NULL",
    )
    .bind(cid)
    .fetch_all(&mut **tx)
    .await?;
    if ids.len() != 1 {
        return Err(conflict());
    }
    let pid = ids[0].ok_or_else(conflict)?;
    let pays=sqlx::query("SELECT user_id,biz_id,biz_type,paid_cents,refunded_cents,status FROM payment_order WHERE id=? AND deleted_at IS NULL FOR UPDATE").bind(pid).fetch_all(&mut **tx).await?;
    if pays.len() != 1 {
        return Err(conflict());
    }
    let pay = &pays[0];
    let rows=sqlx::query("SELECT order_no,user_id,payment_order_id,status,started_at FROM charge_order WHERE id=? AND deleted_at IS NULL FOR UPDATE").bind(cid).fetch_all(&mut **tx).await?;
    if rows.len() != 1 {
        return Err(conflict());
    }
    let order = &rows[0];
    let uid = req.source.user_id;
    if order.try_get::<String, _>("order_no")? != req.source.order_no
        || order.try_get::<u64, _>("user_id")? != uid
        || order.try_get::<Option<u64>, _>("payment_order_id")? != Some(pid)
        || order.try_get::<String, _>("status")? != "completed"
        || pay.try_get::<u64, _>("user_id")? != uid
        || pay.try_get::<u64, _>("biz_id")? != cid
        || pay.try_get::<String, _>("biz_type")? != "charge"
        || !["paid", "partial_refunded", "refunded"]
            .contains(&pay.try_get::<String, _>("status")?.as_str())
    {
        return Err(conflict());
    }
    let payload = serde_json::to_value(req)?;
    let previous: Option<serde_json::Value> = sqlx::query_scalar(
        "SELECT result_json FROM charge_fee_receipt WHERE charge_order_id=? FOR UPDATE",
    )
    .bind(cid)
    .fetch_optional(&mut **tx)
    .await?;
    if let Some(saved) = previous {
        return if saved == payload {
            Ok(())
        } else {
            Err(conflict())
        };
    }
    let original:Option<(serde_json::Value,serde_json::Value)>=sqlx::query_as("SELECT r.meter_json,p.quote_snapshot FROM charge_end_receipt r JOIN charge_order_pricing p ON p.charge_order_id=r.charge_order_id AND p.user_id=? WHERE r.charge_order_id=? FOR UPDATE").bind(uid).bind(cid).fetch_optional(&mut **tx).await?;
    let (meter, quote) = original.ok_or_else(conflict)?;
    if serde_json::to_value(serde_json::from_value::<api_contracts::ChargeEndMeter>(
        meter,
    )?)? != serde_json::to_value(&req.source.meter)?
        || serde_json::to_value(
            serde_json::from_value::<api_contracts::pricing::PriceQuote>(quote)?,
        )? != serde_json::to_value(&req.source.quote)?
        || order
            .try_get::<chrono::NaiveDateTime, _>("started_at")?
            .and_utc()
            != req.source.started_at
    {
        return Err(conflict());
    }
    let paid: i64 = pay.try_get("paid_cents")?;
    let refunded: i64 = pay.try_get("refunded_cents")?;
    if paid < 0 || refunded < 0 || refunded > paid {
        return Err(conflict());
    }
    let refunds:Vec<(i64,String)>=sqlx::query_as("SELECT refund_cents,status FROM refund_record WHERE payment_order_id=? AND deleted_at IS NULL FOR UPDATE").bind(pid).fetch_all(&mut **tx).await?;
    let target = (paid - req.total_cents).max(0);
    let mut reserved = refunded;
    let mut successful = 0i64;
    for (amount, status) in refunds {
        if amount <= 0 {
            return Err(conflict());
        }
        if status != "success" {
            reserved = reserved.checked_add(amount).ok_or_else(conflict)?;
        } else {
            successful = successful.checked_add(amount).ok_or_else(conflict)?;
        }
    }
    if successful != refunded {
        return Err(conflict());
    }
    if reserved > target {
        return Err(conflict());
    }
    let amount = target - reserved;
    if amount > 0 {
        let no = common_db::IdGen::new("REF").next();
        sqlx::query("INSERT INTO refund_record (refund_no,payment_order_id,user_id,biz_type,biz_id,refund_cents,reason,status,created_month) VALUES (?,?,?,'charge',?,?,'充电实结差额退款','pending',DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
            .bind(&no).bind(pid).bind(uid).bind(cid).bind(amount).execute(&mut **tx).await?;
        let event = common_redis::StreamEnvelope::new(
            "refund_required",
            "user",
            serde_json::json!({"refund_no":no,"payment_order_id":pid,"charge_order_id":cid,"user_id":uid,"refund_cents":amount}),
        );
        sqlx::query("INSERT INTO event_outbox (event_id,stream,envelope_json) VALUES (?,?,?)")
            .bind(&event.event_id)
            .bind(common_redis::streams::REFUND_REQUIRED)
            .bind(serde_json::to_value(&event)?)
            .execute(&mut **tx)
            .await?;
    }
    sqlx::query(
        "UPDATE charge_order SET electric_cents=?,service_cents=?,total_cents=? WHERE id=?",
    )
    .bind(req.electric_cents)
    .bind(req.service_cents)
    .bind(req.total_cents)
    .bind(cid)
    .execute(&mut **tx)
    .await?;
    sqlx::query("INSERT INTO charge_fee_receipt (charge_order_id,calculation_no,result_json,shortfall_cents) VALUES (?,?,?,?)").bind(cid).bind(&req.calculation_no).bind(payload).bind((req.total_cents-paid).max(0)).execute(&mut **tx).await?;
    crate::order_events::record(
        tx,
        cid,
        "fee_calculated",
        "billing",
        "实结费用已确认，差额退款已按需登记",
    )
    .await?;
    Ok(())
}
