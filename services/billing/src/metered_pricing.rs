//! Actual charges use measured energy; never distribute energy uniformly across tariffs.
use api_contracts::{pricing::DevicePricing, ChargeMeterSegment, QuoteResponse};
use chrono::{DateTime, Utc};
use common_error::{AppError, AppResult};

/// 计量合法性校验——**只依赖时间与电量,与电价配置无关**。
///
/// D10:原先这段校验内联在 `calculate` 里,导致"计价失败"会连带吞掉调用方
/// (如 charge_fee 的全额退款归零判定)对"计量是否合法"的独立判断。
/// 抽出后调用方可先校验计量、再决定是否需要计价。
pub fn validate_meter(
    started_at: DateTime<Utc>,
    ended_at: DateTime<Utc>,
    wh: u64,
    seconds: u32,
) -> AppResult<()> {
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
    Ok(())
}

pub fn calculate(
    rule: &DevicePricing,
    started_at: DateTime<Utc>,
    ended_at: DateTime<Utc>,
    wh: u64,
    seconds: u32,
) -> AppResult<QuoteResponse> {
    validate_meter(started_at, ended_at, wh, seconds)?;
    let invalid = || AppError::BadRequest("最终计量或充电时间无效".into());
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
                "跨分时电价订单缺少分段计量数据，无法自动计费（需按人工异常流程处理）".into(),
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

/// 按分段计量计价（跨分时电价的正确算法，D16）。
///
/// 与 `calculate` 的分工：`calculate` 是**单费率**路径，它要求整段充电期间
/// 费率恒定，费率一变就报错；本函数处理费率跨段的情况，每段按各自费率计价。
///
/// `segments` 为空时**不报错**，而是委托给 `calculate` —— 这样「没有分段数据」
/// 的订单走的还是原来那条单费率路径，不会因为多了一个空分支而改变任何金额。
///
/// 为什么取**每段起始分钟**的费率：Redis entry 边界、采样时间、电价切换点三者
/// 不可能完全对齐。按起始分钟取是确定性最强、无需额外假设的选择；按结束分钟取
/// 会把边界电量归给下一段，按加权取又会引入「段边界落在分钟中间」时的歧义。
pub fn calculate_segmented(
    rule: &DevicePricing,
    started_at: DateTime<Utc>,
    ended_at: DateTime<Utc>,
    wh: u64,
    seconds: u32,
    segments: &[ChargeMeterSegment],
) -> AppResult<QuoteResponse> {
    if segments.is_empty() {
        return calculate(rule, started_at, ended_at, wh, seconds);
    }
    validate_meter(started_at, ended_at, wh, seconds)?;
    let invalid = || AppError::BadRequest("最终计量或充电时间无效".into());

    // 分段电量必须与总电量严格相等：不等说明分段推导有 bug 或数据被篡改。
    // 此时无论信分段还是信总量都会算错钱，所以宁可拒绝也不能「挑一个看起来对的」，
    // 且报错要带出两个数字，便于运维直接定位到哪一笔对不上。
    let segmented_wh: u64 = segments.iter().map(|seg| seg.energy_wh).sum();
    if segmented_wh != wh {
        return Err(AppError::Conflict(format!(
            "分段电量合计 {segmented_wh} Wh 与总计量 {wh} Wh 不一致，无法自动计费（需按人工异常流程处理）"
        )));
    }

    let rates = crate::quote_pricing::daily_rates(rule)?;
    // 舍入一律**只在最后做一次**，与 `calculate` 保持同一口径。
    //
    // 备选方案是「每段各自 round 后求和」（把每段当独立计量），但那会与关键回归
    // 验收项「分段全部落在同一费率时，结果与单费率 `calculate` 逐分相等」直接矛盾：
    // 舍入不可加，`Σround(xᵢ)` 与 `round(Σxᵢ)` 经常差 1 分。因此取总分子再 round：
    //   - 单费率分段 → 分子恰好等于 `calculate` 的分子 → 金额逐分相同，不回归；
    //   - 非跨电价订单 → 走的还是原 `calculate` 路径 → 现有金额完全不动。
    // 跨电价订单今天 100% 无法计费（没有已成立的金额需要保持），不存在「静默改价」。
    let rounded = |n: i128, d: i128| (n + d / 2) / d;
    let mut electric_numerator = 0i128;
    let mut service_energy_numerator = 0i128;
    for seg in segments {
        let index = (seg.started_at.timestamp().div_euclid(60) + 8 * 60).rem_euclid(1440) as usize;
        let (electric_rate, service_rate) = rates[index].ok_or_else(|| {
            AppError::Conflict(format!(
                "分段起始时刻 {} 缺少电价配置，无法自动计费（需按人工异常流程处理）",
                seg.started_at.format("%Y-%m-%dT%H:%M:%SZ")
            ))
        })?;
        electric_numerator += i128::from(seg.energy_wh) * i128::from(electric_rate);
        // mode=="minute" 时按分钟计价，电量项不计入服务费 —— 与 `calculate` 的 `(0, ...)` 口径一致。
        let effective_service_rate = if rule.mode == "minute" { 0 } else { service_rate };
        service_energy_numerator += i128::from(seg.energy_wh) * i128::from(effective_service_rate) * 60;
    }
    let electric = rounded(electric_numerator, 1000);
    // 按分钟的时长项**整段只算一次**：它与分段无关（用总时长而非分段时长之和），
    // 拆开算会重复计入边界时间。
    let duration_numerator = if rule.mode == "kwh" {
        0
    } else {
        i128::from(seconds) * i128::from(rule.service_fee_cents_per_min) * 1000
    };
    let service = rounded(service_energy_numerator + duration_numerator, 60000)
        .max(i128::from(rule.min_charge_cents) - electric);
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
// 测试夹具用 `json!` 搭分时电价(`DevicePricing.time_of_use` 是数据库
// JSON 列,形状就是配置原文),走契约 DTO 反而会掩盖配置形状变化。
#[allow(clippy::disallowed_macros, clippy::disallowed_types)]
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

    // ===== D16 分段计价 =====

    fn seg(start: &str, end: &str, energy_wh: u64) -> ChargeMeterSegment {
        ChargeMeterSegment {
            started_at: at(start),
            ended_at: at(end),
            energy_wh,
        }
    }

    /// D16 关键回归①：分段全在同一费率时，金额必须与单费率 `calculate` **逐分相等**。
    /// （选了 60/65 这种拆分——正是 `Σround` 与 `round(Σ)` 最容易分歧的形状。）
    #[test]
    fn single_rate_segments_equal_single_rate_calculate() {
        let r = rule();
        // 11:00Z→13:00Z 全在白天（本地 19:00→21:00… 注意 12:00Z 是本地 20:00 分界，
        // 故取 10:00Z→12:00Z，全落在本地 18:00→20:00 的白天时段）
        let (s, e) = (at("2026-09-26T10:00:00Z"), at("2026-09-26T12:00:00Z"));
        for (segs, wh) in [
            (vec![seg("2026-09-26T10:00:00Z", "2026-09-26T11:00:00Z", 60),
                  seg("2026-09-26T11:00:00Z", "2026-09-26T12:00:00Z", 65)], 125u64),
            (vec![seg("2026-09-26T10:00:00Z", "2026-09-26T10:30:00Z", 1),
                  seg("2026-09-26T10:30:00Z", "2026-09-26T12:00:00Z", 124)], 125),
        ] {
            let single = calculate(&r, s, e, wh, 7200).unwrap();
            let segmented = calculate_segmented(&r, s, e, wh, 7200, &segs).unwrap();
            assert_eq!(
                (segmented.electric_cents, segmented.service_cents, segmented.total_cents),
                (single.electric_cents, single.service_cents, single.total_cents),
                "单费率分段必须与单费率路径逐分相等"
            );
        }
    }

    /// D16 关键回归②：`segments` 为空 → 与 `calculate` 完全一致（零回归）
    #[test]
    fn empty_segments_fall_back_to_calculate() {
        let r = rule();
        let (s, e) = (at("2026-09-26T10:00:00Z"), at("2026-09-26T12:00:00Z"));
        let single = calculate(&r, s, e, 125, 7200).unwrap();
        let segmented = calculate_segmented(&r, s, e, 125, 7200, &[]).unwrap();
        assert_eq!(segmented.total_cents, single.total_cents);
        // 且跨电价订单在没有分段数据时仍然报错（不是静默改成某种估算）
        assert!(calculate_segmented(&r, at("2026-09-26T11:00:00Z"),
            at("2026-09-26T13:00:00Z"), 1000, 7200, &[]).is_err());
    }

    /// D16 验收：跨两个费率（白天 100/40、夜间 50/20）→ 各段按各自费率计价
    #[test]
    fn cross_tariff_segments_priced_per_segment() {
        let r = rule();
        // 11:00Z→13:00Z 跨过本地 20:00(=12:00Z) 分界：前 1h 白天，后 1h 夜间
        let segs = vec![
            seg("2026-09-26T11:00:00Z", "2026-09-26T12:00:00Z", 1000),
            seg("2026-09-26T12:00:00Z", "2026-09-26T13:00:00Z", 1000),
        ];
        let out = calculate_segmented(&r, at("2026-09-26T11:00:00Z"),
            at("2026-09-26T13:00:00Z"), 2000, 7200, &segs).unwrap();
        // 电费 1000*100/1000 + 1000*50/1000 = 100 + 50 = 150
        // 服务费 1000*40*60/60000 + 1000*20*60/60000 = 40 + 20 = 60（mode=kwh 无时长项）
        assert_eq!((out.electric_cents, out.service_cents, out.total_cents), (150, 60, 210));
    }

    /// Σsegments.energy_wh ≠ wh → Conflict（按哪边算都会算错钱，必须拒绝）
    #[test]
    fn segment_sum_mismatch_conflicts() {
        let r = rule();
        let segs = vec![
            seg("2026-09-26T11:00:00Z", "2026-09-26T12:00:00Z", 1000),
            seg("2026-09-26T12:00:00Z", "2026-09-26T13:00:00Z", 999),
        ];
        let err = calculate_segmented(&r, at("2026-09-26T11:00:00Z"),
            at("2026-09-26T13:00:00Z"), 2000, 7200, &segs).unwrap_err();
        assert!(matches!(err, AppError::Conflict(_)), "电量不一致必须是 Conflict");
    }

    /// 计量非法时仍必须拒绝（合法性独立于计价，D10 原则）
    #[test]
    fn invalid_meter_rejected_before_pricing() {
        let r = rule();
        let segs = vec![seg("2026-09-26T11:00:00Z", "2026-09-26T12:00:00Z", 10)];
        // seconds==0 但 wh>0 → 非法
        assert!(calculate_segmented(&r, at("2026-09-26T11:00:00Z"),
            at("2026-09-26T12:00:00Z"), 10, 0, &segs).is_err());
    }

    /// mode="minute"：服务费的电量项不计（rate 置 0），只按整段时长计费
    #[test]
    fn minute_mode_charges_duration_only() {
        let mut r = rule();
        r.mode = "minute".into();
        let segs = vec![
            seg("2026-09-26T11:00:00Z", "2026-09-26T12:00:00Z", 1000),
            seg("2026-09-26T12:00:00Z", "2026-09-26T13:00:00Z", 1000),
        ];
        let out = calculate_segmented(&r, at("2026-09-26T11:00:00Z"),
            at("2026-09-26T13:00:00Z"), 2000, 7200, &segs).unwrap();
        // 电费仍按各段电价：100 + 50 = 150
        // 服务费 = 7200 * 2 * 1000 / 60000 = 240（不含量电项）
        assert_eq!((out.electric_cents, out.service_cents, out.total_cents), (150, 240, 390));
        // 与单费率路径对齐：同样这两小时的订单走 minute 口径应得相同的服务费
        let single = calculate_segmented(&r, at("2026-09-26T10:00:00Z"),
            at("2026-09-26T12:00:00Z"), 2000, 7200,
            &[seg("2026-09-26T10:00:00Z", "2026-09-26T12:00:00Z", 2000)]).unwrap();
        assert_eq!(single.service_cents, 240);
    }

    /// mode="kwh"：服务费只按电量，不含按分钟时长项（与 calculate 同口径）
    #[test]
    fn kwh_mode_excludes_duration_component() {
        let r = rule(); // mode 已是 "kwh"
        let segs = vec![seg("2026-09-26T10:00:00Z", "2026-09-26T12:00:00Z", 1000)];
        let out = calculate_segmented(&r, at("2026-09-26T10:00:00Z"),
            at("2026-09-26T12:00:00Z"), 1000, 7200, &segs).unwrap();
        // 1000*100/1000=100 电费；1000*40*60/60000=40 服务费；无时长项
        assert_eq!((out.electric_cents, out.service_cents, out.total_cents), (100, 40, 140));
    }

    /// min_charge_cents 兜底在分段路径同样生效
    #[test]
    fn min_charge_applies_to_segmented_path() {
        let mut r = rule();
        r.min_charge_cents = 100;
        let segs = vec![
            seg("2026-09-26T11:00:00Z", "2026-09-26T12:00:00Z", 1000),
            seg("2026-09-26T12:00:00Z", "2026-09-26T13:00:00Z", 1000),
        ];
        // 电费 150 > min 100，此处应不被抬升；改用小电量验证抬升
        assert_eq!(calculate_segmented(&r, at("2026-09-26T11:00:00Z"),
            at("2026-09-26T13:00:00Z"), 2000, 7200, &segs).unwrap().total_cents, 210);
        // 小电量：电费远低于 min_charge → 总价被抬到 100
        let small = vec![
            seg("2026-09-26T11:00:00Z", "2026-09-26T12:00:00Z", 1),
            seg("2026-09-26T12:00:00Z", "2026-09-26T13:00:00Z", 1),
        ];
        let out = calculate_segmented(&r, at("2026-09-26T11:00:00Z"),
            at("2026-09-26T13:00:00Z"), 2, 7200, &small).unwrap();
        assert_eq!(out.total_cents, 100, "min_charge_cents 兜底必须在分段路径生效");
    }

    /// 某段起始分钟无电价配置 → 报错（不留静默按 0 计）
    #[test]
    fn segment_without_rate_errors() {
        // 只配 10:00-11:00 一小时，段起始落在 11:00 → 该分钟无配置
        let r = DevicePricing {
            time_of_use: json!([{"period":"only","start":"10:00","end":"11:00",
                "electric_price_cents":100,"service_price_cents":40}]),
            ..rule()
        };
        let segs = vec![seg("2026-09-26T11:00:00Z", "2026-09-26T12:00:00Z", 1000)];
        assert!(calculate_segmented(&r, at("2026-09-26T11:00:00Z"),
            at("2026-09-26T12:00:00Z"), 1000, 3600, &segs).is_err());
    }
}
