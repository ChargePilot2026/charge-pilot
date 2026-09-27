//! Calculate once from the user-owned immutable pricing and meter receipts.
use crate::{api_types::CalculateResponse, AppState};
use api_contracts::{pricing::{DevicePricing, MeteredOrder}, ChargeEndMeter, QuoteResponse};
use chrono::{DateTime, Utc};
use common_error::{AppError, AppResult};
use sqlx::Row;

/// 技术规格 §8.4:60 秒内停止,或充电超过 10 小时,全额退款。
fn is_full_refund_eligible(started_at: DateTime<Utc>, ended_at: DateTime<Utc>, seconds: u32) -> bool {
    (ended_at - started_at).num_milliseconds() <= 60000 || seconds > 36000
}

/// D10:分离**计量合法性校验**与**计价**。
///
/// 原实现先 `metered_pricing::calculate(...)?` 再判全额退款,于是跨分时电价时
/// 计价先行报错、归零分支不可达。这里改为三步:
/// 1. 校验计量是否合法(与电价无关,失败即拒);
/// 2. 符合全额退款条件 → **直接归零,不依赖分段计价成功**;
/// 3. 其余情况才计价(跨电价等无法计价的场景仍按原样报错,属 D16 待决策项)。
fn resolve_fee(
    rule: &DevicePricing,
    started_at: DateTime<Utc>,
    meter: &ChargeEndMeter,
) -> AppResult<QuoteResponse> {
    crate::metered_pricing::validate_meter(started_at, meter.ended_at, meter.charged_wh, meter.charged_seconds)?;
    if is_full_refund_eligible(started_at, meter.ended_at, meter.charged_seconds) {
        return Ok(QuoteResponse {
            electric_cents: 0,
            service_cents: 0,
            total_cents: 0,
        });
    }
    crate::metered_pricing::calculate(
        rule,
        started_at,
        meter.ended_at,
        meter.charged_wh,
        meter.charged_seconds,
    )
}

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
    let fee = resolve_fee(&source.quote.pricing, source.started_at, meter)?;
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

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn rule() -> DevicePricing {
        // 白天 08:00-20:00 电价 100,夜间 20:00-08:00 电价 50 —— 20:00 是分界点
        DevicePricing {
            station_id: 1,
            station_name: "station".into(),
            rule_id: 1,
            name: "tariff".into(),
            version: 1,
            mode: "kwh".into(),
            time_of_use: json!([
                {"period":"day","start":"08:00","end":"20:00","electric_price_cents":100,"service_price_cents":40},
                {"period":"night","start":"20:00","end":"08:00","electric_price_cents":50,"service_price_cents":20}
            ]),
            service_fee_cents_per_kwh: 30,
            service_fee_cents_per_min: 2,
            min_charge_cents: 0,
        }
    }

    fn meter(wh: u64, seconds: u32, ended_at: &str) -> ChargeEndMeter {
        ChargeEndMeter {
            charged_wh: wh,
            charged_seconds: seconds,
            ended_at: ended_at.parse().unwrap(),
        }
    }

    /// D10 验收①:跨电价边界的 30 秒订单 → 归零,不是 Conflict
    #[test]
    fn cross_tariff_short_session_refunds_to_zero() {
        // 11:59:45Z 起 30 秒,跨过 12:00:00Z 分界(计价在此会报跨分时电价)
        let start: DateTime<Utc> = "2026-09-26T11:59:45Z".parse().unwrap();
        let m = meter(10, 30, "2026-09-26T12:00:15Z");
        let fee = resolve_fee(&rule(), start, &m).expect("符合全额退款条件,不应因跨电价而失败");
        assert_eq!((fee.electric_cents, fee.service_cents, fee.total_cents), (0, 0, 0));
    }

    /// D10 验收②:超 10 小时且跨电价的订单 → 归零
    #[test]
    fn over_ten_hours_cross_tariff_refunds_to_zero() {
        let start: DateTime<Utc> = "2026-09-26T10:00:00Z".parse().unwrap();
        let m = meter(5_000_000, 39601, "2026-09-27T00:00:01Z"); // 11h+1s
        let fee = resolve_fee(&rule(), start, &m).expect("超 10 小时应全额退款");
        assert_eq!(fee.total_cents, 0);
    }

    /// 计量非法时仍必须拒绝——不得因为"全额退款"绕过合法性校验
    #[test]
    fn invalid_meter_is_still_rejected_even_when_refund_eligible() {
        let start: DateTime<Utc> = "2026-09-26T11:59:45Z".parse().unwrap();
        // seconds==0 但有电量 → 非法
        let m = meter(10, 0, "2026-09-26T12:00:15Z");
        assert!(resolve_fee(&rule(), start, &m).is_err());
    }

    /// 正常订单行为不变(不回归)
    #[test]
    fn normal_order_still_priced() {
        let start: DateTime<Utc> = "2026-09-26T11:00:00Z".parse().unwrap();
        let m = meter(1000, 3600, "2026-09-26T12:00:00Z");
        let fee = resolve_fee(&rule(), start, &m).expect("普通订单应正常计价");
        assert!(fee.total_cents > 0);
    }

    /// D16 未决:非全额退款的跨电价订单仍应报错(本次不修,仅确认行为未变)
    /// 注意计价用 `(minute + 8*60)` 即本地时间——本地 19:00-21:00 = UTC 11:00-13:00,
    /// 跨过本地 20:00(= UTC 12:00Z)的分界。
    #[test]
    fn cross_tariff_non_refund_order_still_conflicts() {
        let start: DateTime<Utc> = "2026-09-26T11:00:00Z".parse().unwrap();
        let m = meter(1000, 7200, "2026-09-26T13:00:00Z");
        assert!(resolve_fee(&rule(), start, &m).is_err());
    }
}
