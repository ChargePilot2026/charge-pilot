//! 计费判定规则(纯函数层)。
//!
//! P3:`calculate` 的 IO / SQL / 事务已下沉到 [`crate::services::FeeService`],
//! 本文件只留**纯规则** —— 退款资格判定与 D10 的三步计价顺序,便于单测。
//! `resolve_fee` 供服务层调用,规则与顺序未变。

use api_contracts::{pricing::DevicePricing, ChargeEndMeter, QuoteResponse};
use chrono::{DateTime, Utc};
use common_error::AppResult;

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
pub fn resolve_fee(
    rule: &DevicePricing,
    started_at: DateTime<Utc>,
    meter: &ChargeEndMeter,
) -> AppResult<QuoteResponse> {
    if let Some(zero) = precheck(started_at, meter)? {
        return Ok(zero);
    }
    crate::metered_pricing::calculate(
        rule,
        started_at,
        meter.ended_at,
        meter.charged_wh,
        meter.charged_seconds,
    )
}

/// D16:有分段计量数据时走分段计价路径。
///
/// **D10 的两步顺序由 [`precheck`] 统一保证**，与 [`resolve_fee`] 走的是同一份代码
/// （而不是各写一遍）：先校验计量合法性（与电价无关），再判全额退款归零
/// （≤60 秒停止 / 超 10 小时）—— 归零判定在**计价之前**，所以跨电价订单也能归零，
/// 不会因为分段计价出错而被卡住。两条路径只差最后一步的算法。
pub fn resolve_fee_segmented(
    rule: &DevicePricing,
    started_at: DateTime<Utc>,
    meter: &ChargeEndMeter,
) -> AppResult<QuoteResponse> {
    if let Some(zero) = precheck(started_at, meter)? {
        return Ok(zero);
    }
    crate::metered_pricing::calculate_segmented(
        rule,
        started_at,
        meter.ended_at,
        meter.charged_wh,
        meter.charged_seconds,
        &meter.segments,
    )
}

/// D10 前两步：计量合法性校验 + 全额退款归零。
///
/// 两条计价路径（单费率 / 分段）共用，保证归零判定**永远在计价之前**、
/// 且**永远不会被电价配置或分段数据影响**。返回 `Some(零)` 即命中全额退款。
fn precheck(started_at: DateTime<Utc>, meter: &ChargeEndMeter) -> AppResult<Option<QuoteResponse>> {
    crate::metered_pricing::validate_meter(started_at, meter.ended_at, meter.charged_wh, meter.charged_seconds)?;
    if is_full_refund_eligible(started_at, meter.ended_at, meter.charged_seconds) {
        return Ok(Some(QuoteResponse {
            electric_cents: 0,
            service_cents: 0,
            total_cents: 0,
        }));
    }
    Ok(None)
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
            segments: vec![],
        }
    }

    fn meter_with_segments(
        wh: u64,
        seconds: u32,
        ended_at: &str,
        segments: Vec<api_contracts::ChargeMeterSegment>,
    ) -> ChargeEndMeter {
        ChargeEndMeter {
            charged_wh: wh,
            charged_seconds: seconds,
            ended_at: ended_at.parse().unwrap(),
            segments,
        }
    }

    fn seg(start: &str, end: &str, energy_wh: u64) -> api_contracts::ChargeMeterSegment {
        api_contracts::ChargeMeterSegment {
            started_at: start.parse().unwrap(),
            ended_at: end.parse().unwrap(),
            energy_wh,
        }
    }

    /// D16 验收③：分段路径下的 D10 归零判定**同样生效**——
    /// 跨电价的 30 秒订单即便带了分段数据也必须归零，不得走到计价再报错。
    #[test]
    fn segmented_path_preserves_zero_refund_rules() {
        // 跨本地 20:00(=12:00Z) 分界、时长 30 秒 → 归零
        let start: DateTime<Utc> = "2026-09-26T11:59:45Z".parse().unwrap();
        let m = meter_with_segments(10, 30, "2026-09-26T12:00:15Z", vec![
            seg("2026-09-26T11:59:45Z", "2026-09-26T12:00:00Z", 6),
            seg("2026-09-26T12:00:00Z", "2026-09-26T12:00:15Z", 4),
        ]);
        let fee = resolve_fee_segmented(&rule(), start, &m)
            .expect("符合全额退款条件,分段路径不应因跨电价而失败");
        assert_eq!((fee.electric_cents, fee.service_cents, fee.total_cents), (0, 0, 0));

        // 超 10 小时 → 归零
        let start2: DateTime<Utc> = "2026-09-26T10:00:00Z".parse().unwrap();
        let m2 = meter_with_segments(2_000_000, 39601, "2026-09-27T00:00:01Z", vec![
            seg("2026-09-26T10:00:00Z", "2026-09-26T12:00:00Z", 1_000_000),
            seg("2026-09-26T12:00:00Z", "2026-09-27T00:00:01Z", 1_000_000),
        ]);
        assert_eq!(resolve_fee_segmented(&rule(), start2, &m2).unwrap().total_cents, 0);
    }

    /// D16 验收④：非全额退款的跨电价订单，在分段路径下**终于能计上费了**。
    #[test]
    fn segmented_path_prices_cross_tariff_order() {
        let start: DateTime<Utc> = "2026-09-26T11:00:00Z".parse().unwrap();
        let m = meter_with_segments(2000, 7200, "2026-09-26T13:00:00Z", vec![
            seg("2026-09-26T11:00:00Z", "2026-09-26T12:00:00Z", 1000),
            seg("2026-09-26T12:00:00Z", "2026-09-26T13:00:00Z", 1000),
        ]);
        let fee = resolve_fee_segmented(&rule(), start, &m).expect("跨电价订单有了分段数据就应能计价");
        assert_eq!((fee.electric_cents, fee.service_cents, fee.total_cents), (150, 60, 210));
    }

    /// 分段路径同样不得绕过计量合法性校验
    #[test]
    fn segmented_path_still_rejects_invalid_meter() {
        let start: DateTime<Utc> = "2026-09-26T11:59:45Z".parse().unwrap();
        let m = meter_with_segments(10, 0, "2026-09-26T12:00:15Z", vec![
            seg("2026-09-26T11:59:45Z", "2026-09-26T12:00:15Z", 10),
        ]);
        assert!(resolve_fee_segmented(&rule(), start, &m).is_err());
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
