//! Calculate once from the user-owned immutable pricing and meter receipts.
use crate::{api_types::CalculateResponse, AppState};
use api_contracts::pricing::MeteredOrder;
use common_error::{AppError, AppResult};
use sqlx::Row;

pub async fn calculate(st: &AppState, cid: u64, order_no: &str) -> AppResult<CalculateResponse> {
    if cid == 0 || order_no.is_empty() {
        return Err(AppError::BadRequest("缺少充电订单标识".into()));
    }
    let client = common_http::internal::ApiClient::new(st.http.clone(), st.service_token.clone());
    let source: MeteredOrder = client
        .get(
            st.cfg.service_urls.user.as_deref(),
            &api_contracts::paths::USER_INTERNAL_METERED_ORDER
                .replace(":order_id", &cid.to_string()),
            &(),
        )
        .await?;
    if source.charge_order_id != cid || source.order_no != order_no {
        return Err(AppError::Conflict("计费订单身份不匹配".into()));
    }
    let meter = &source.meter;
    let mut fee = crate::metered_pricing::calculate(
        &source.quote.pricing,
        source.started_at,
        meter.ended_at,
        meter.charged_wh,
        meter.charged_seconds,
    )?;
    // Current end receipts are confirmed user-requested STOPs. Requirement §8.4:
    // a stop within 60 seconds, or charge exceeding ten hours, gets a full refund.
    if (meter.ended_at - source.started_at).num_milliseconds() <= 60000
        || meter.charged_seconds > 36000
    {
        fee = api_contracts::QuoteResponse {
            electric_cents: 0,
            service_cents: 0,
            total_cents: 0,
        };
    }
    let snapshot = serde_json::to_value(&source)?;
    let mut tx = st.db.pool().begin().await?;
    sqlx::query("INSERT IGNORE INTO fee_receipt (charge_order_id,source_json) VALUES (?,?)")
        .bind(cid)
        .bind(&snapshot)
        .execute(&mut *tx)
        .await?;
    let receipt=sqlx::query("SELECT source_json,calculation_id,calculation_no FROM fee_receipt WHERE charge_order_id=? FOR UPDATE").bind(cid).fetch_one(&mut *tx).await?;
    if receipt.try_get::<serde_json::Value, _>("source_json")? != snapshot {
        return Err(AppError::Conflict("已计费订单的原始快照发生变化".into()));
    }
    if let Some(id) = receipt.try_get::<Option<u64>, _>("calculation_id")? {
        let no = receipt.try_get::<String, _>("calculation_no")?;
        let row=sqlx::query("SELECT electric_cents,service_cents,total_cents FROM fee_calculation WHERE id=? AND calculation_no=? AND charge_order_id=?").bind(id).bind(&no).bind(cid).fetch_one(&mut *tx).await?;
        let result = CalculateResponse {
            calculation_id: id,
            calculation_no: no,
            electric_cents: row.try_get("electric_cents")?,
            service_cents: row.try_get("service_cents")?,
            total_cents: row.try_get("total_cents")?,
        };
        tx.commit().await?;
        return Ok(result);
    }
    let no = common_db::IdGen::new("FEE").next();
    let rule = &source.quote.pricing;
    let kwh = format!("{}.{:03}", meter.charged_wh / 1000, meter.charged_wh % 1000);
    let id=sqlx::query("INSERT INTO fee_calculation (calculation_no,order_no,charge_order_id,user_id,station_id,pricing_rule_id,pricing_rule_version,charged_kwh,charged_seconds,electric_cents,service_cents,total_cents,breakdown_json,created_month) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
        .bind(&no).bind(order_no).bind(cid).bind(source.user_id).bind(rule.station_id).bind(rule.rule_id).bind(rule.version).bind(kwh).bind(meter.charged_seconds)
        .bind(fee.electric_cents).bind(fee.service_cents).bind(fee.total_cents).bind(&snapshot).bind(chrono::Utc::now().format("%Y-%m-01").to_string())
        .execute(&mut *tx).await?.last_insert_id();
    sqlx::query("UPDATE fee_receipt SET calculation_id=?,calculation_no=? WHERE charge_order_id=?")
        .bind(id)
        .bind(&no)
        .bind(cid)
        .execute(&mut *tx)
        .await?;
    let delivery = api_contracts::pricing::FeeResult {
        calculation_no: no.clone(),
        source,
        electric_cents: fee.electric_cents,
        service_cents: fee.service_cents,
        total_cents: fee.total_cents,
    };
    sqlx::query("INSERT INTO fee_delivery (charge_order_id,payload_json) VALUES (?,?)")
        .bind(cid)
        .bind(serde_json::to_value(delivery)?)
        .execute(&mut *tx)
        .await?;
    tx.commit().await?;
    Ok(CalculateResponse {
        calculation_id: id,
        calculation_no: no,
        electric_cents: fee.electric_cents,
        service_cents: fee.service_cents,
        total_cents: fee.total_cents,
    })
}
