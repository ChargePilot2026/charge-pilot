//! Actual charges use measured energy; never distribute energy uniformly across tariffs.
use api_contracts::{pricing::DevicePricing, QuoteResponse};
use chrono::{DateTime, Utc};
use common_error::{AppError, AppResult};

pub fn calculate(
    rule: &DevicePricing,
    started_at: DateTime<Utc>,
    ended_at: DateTime<Utc>,
    wh: u64,
    seconds: u32,
) -> AppResult<QuoteResponse> {
    let invalid = || AppError::BadRequest("最终计量或充电时间无效".into());
    let elapsed = (ended_at - started_at).num_seconds();
    if elapsed < 0
        || elapsed > 604800
        || seconds > 604800
        || i64::from(seconds) > elapsed + 300
        || wh > 100_000_000
        || (seconds == 0 && wh != 0)
    {
        return Err(invalid());
    }
    let rates = crate::quote_pricing::daily_rates(rule)?;
    let mut selected = None;
    // The final timestamp is exclusive: ending exactly at a boundary adds no next-period energy.
    let first = started_at.timestamp().div_euclid(60);
    let last = if ended_at > started_at {
        (ended_at.timestamp_millis() - 1).div_euclid(60000)
    } else {
        first
    };
    for minute in first..=last {
        let index = (minute + 8 * 60).rem_euclid(1440) as usize;
        let (electric, service) = rates[index]
            .ok_or_else(|| AppError::Conflict("实际充电时段缺少电价配置，需审核".into()))?;
        let rate = (electric, if rule.mode == "minute" { 0 } else { service });
        if wh > 0 && selected.is_some_and(|prior| prior != rate) {
            return Err(AppError::Conflict(
                "跨分时电价缺少分段电量读数，需审核".into(),
            ));
        }
        selected = Some(rate);
    }
    let (electric_rate, service_rate) = selected.ok_or_else(invalid)?;
    let rounded = |n: i128, d: i128| (n + d / 2) / d;
    let electric = rounded(i128::from(wh) * i128::from(electric_rate), 1000);
    // Combine energy and duration components before rounding service cents once.
    let service_numerator = i128::from(wh) * i128::from(service_rate) * 60
        + if rule.mode == "kwh" {
            0
        } else {
            i128::from(seconds) * i128::from(rule.service_fee_cents_per_min) * 1000
        };
    let service =
        rounded(service_numerator, 60000).max(i128::from(rule.min_charge_cents) - electric);
    let total = electric + service;
    if total < 0 || total > i128::from(i32::MAX) {
        return Err(invalid());
    }
    Ok(QuoteResponse {
        electric_cents: electric as i64,
        service_cents: service as i64,
        total_cents: total as i64,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    fn rule() -> DevicePricing {
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
    fn at(s: &str) -> DateTime<Utc> {
        s.parse().unwrap()
    }
    #[test]
    fn exact_boundary_and_midnight() {
        let r = calculate(
            &rule(),
            at("2026-09-26T11:00:00Z"),
            at("2026-09-26T12:00:00Z"),
            125,
            3600,
        )
        .unwrap();
        assert_eq!(
            (r.electric_cents, r.service_cents, r.total_cents),
            (13, 5, 18)
        );
        assert_eq!(
            calculate(
                &rule(),
                at("2026-09-26T15:00:00Z"),
                at("2026-09-26T17:00:00Z"),
                1000,
                7200
            )
            .unwrap()
            .total_cents,
            70
        );
    }
    #[test]
    fn cross_tariff_is_not_estimated() {
        assert!(calculate(
            &rule(),
            at("2026-09-26T11:00:00Z"),
            at("2026-09-26T12:00:00.001Z"),
            1000,
            3600
        )
        .is_err());
        assert!(calculate(
            &rule(),
            at("2026-09-26T00:00:00Z"),
            at("2026-09-27T00:00:00Z"),
            1000,
            86400
        )
        .is_err());
    }
    #[test]
    fn seconds_rounding_minimum_and_zero_energy() {
        let mut p = rule();
        p.mode = "mixed".into();
        let start = at("2026-09-26T01:00:00Z");
        let end = start + chrono::Duration::seconds(15);
        assert_eq!(calculate(&p, start, end, 10, 15).unwrap().total_cents, 2);
        p.min_charge_cents = 100;
        assert_eq!(calculate(&p, start, end, 10, 15).unwrap().total_cents, 100);
        p.min_charge_cents = 0;
        p.mode = "kwh".into();
        assert_eq!(
            calculate(&p, start, start + chrono::Duration::hours(24), 0, 86400)
                .unwrap()
                .total_cents,
            0
        );
        assert!(calculate(&p, start, end, 1, 0).is_err());
        assert!(calculate(&p, start, end, 1, 316).is_err());
    }
}
