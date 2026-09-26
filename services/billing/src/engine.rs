//! 计费引擎:价费分离(电费 + 服务费)+ 分时电价 + 扣减顺序
//!
//! 纯函数实现,所有 IO 通过参数显式传入,便于单测。

use serde_json::Value;
use std::collections::HashMap;

/// 计费规则快照
#[derive(Debug, Clone)]
pub struct PricingRule {
    pub mode: String,                 // kwh / minute / mixed
    pub service_fee_cents_per_kwh: i64,
    pub service_fee_cents_per_min: i64,
    pub min_charge_cents: i64,
    pub time_of_use: HashMap<String, i64>, // "peak"/"off" → cents/kWh
}

impl PricingRule {
    pub fn default_default() -> Self {
        let mut tou = HashMap::new();
        tou.insert("peak".into(), 80);   // 0.80 元/kWh
        tou.insert("off".into(), 40);    // 0.40 元/kWh
        Self {
            mode: "kwh".into(),
            service_fee_cents_per_kwh: 30,
            service_fee_cents_per_min: 0,
            min_charge_cents: 100,
            time_of_use: tou,
        }
    }

    /// 时段电价(分/kWh),未知时段回落 off
    pub fn rate_for(&self, period: &str) -> i64 {
        self.time_of_use
            .get(period)
            .copied()
            .or_else(|| self.time_of_use.get("off").copied())
            .unwrap_or(40)
    }
}

/// 计费输入(显式参数 — 便于 mock 测试)
#[derive(Debug, Clone, Copy)]
pub struct FeeInput {
    pub charged_kwh: f64,
    pub charged_minutes: i64,
    pub peak_kwh: f64,
    pub off_kwh: f64,
}

/// 计费输出
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct FeeResult {
    pub electric_cents: i64,
    pub service_cents: i64,
    pub total_cents: i64,
    pub peak_kwh: f64,
    pub off_kwh: f64,
    pub charged_kwh: f64,
    pub charged_minutes: i64,
}

/// 纯函数:根据规则 + 输入计算费用(价费分离 + min charge 兜底)
pub fn calculate_fee(rule: &PricingRule, input: FeeInput) -> FeeResult {
    let peak_rate = rule.rate_for("peak");
    let off_rate = rule.rate_for("off");
    let electric_cents = (input.peak_kwh * peak_rate as f64 + input.off_kwh * off_rate as f64).round() as i64;
    let service_cents = match rule.mode.as_str() {
        "kwh" => (input.charged_kwh * rule.service_fee_cents_per_kwh as f64).round() as i64,
        "minute" => (input.charged_minutes as f64 * rule.service_fee_cents_per_min as f64).round() as i64,
        "mixed" => {
            (input.charged_kwh * rule.service_fee_cents_per_kwh as f64).round() as i64
                + (input.charged_minutes as f64 * rule.service_fee_cents_per_min as f64).round() as i64
        }
        _ => 0,
    };
    let raw = electric_cents + service_cents;
    let total_cents = if raw < rule.min_charge_cents { rule.min_charge_cents } else { raw };
    FeeResult {
        electric_cents: electric_cents.max(0),
        service_cents: service_cents.max(0),
        total_cents,
        peak_kwh: input.peak_kwh,
        off_kwh: input.off_kwh,
        charged_kwh: input.charged_kwh,
        charged_minutes: input.charged_minutes,
    }
}

/// 兼容老调用
pub fn calculate_fee_compat(
    rule: &PricingRule,
    charged_kwh: f64,
    charged_minutes: i64,
    peak_kwh: f64,
    off_kwh: f64,
) -> FeeResult {
    calculate_fee(rule, FeeInput { charged_kwh, charged_minutes, peak_kwh, off_kwh })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn default_rule_pricing() {
        let r = calculate_fee_compat(&PricingRule::default_default(), 1.0, 60, 0.6, 0.4);
        assert_eq!(r.electric_cents, 64); // 0.6*80 + 0.4*40 = 48 + 16 = 64
        assert_eq!(r.service_cents, 30); // 1.0 * 30
        assert_eq!(r.total_cents, 100); // raw=94 < 100 → min charge
    }

    #[test]
    fn kwh_mode_no_min_charge() {
        let rule = PricingRule {
            mode: "kwh".into(),
            service_fee_cents_per_kwh: 30,
            service_fee_cents_per_min: 0,
            min_charge_cents: 0,
            time_of_use: HashMap::from([
                ("peak".into(), 80),
                ("off".into(), 40),
            ]),
        };
        let r = calculate_fee_compat(&rule, 5.0, 0, 3.0, 2.0);
        assert_eq!(r.electric_cents, (3.0 * 80.0 + 2.0 * 40.0) as i64);
        assert_eq!(r.service_cents, 150); // 5.0 * 30
    }

    #[test]
    fn minute_mode() {
        let rule = PricingRule {
            mode: "minute".into(),
            service_fee_cents_per_kwh: 0,
            service_fee_cents_per_min: 5,  // 5 分/分钟
            min_charge_cents: 0,
            time_of_use: HashMap::new(),
        };
        let r = calculate_fee_compat(&rule, 0.0, 120, 0.0, 0.0);
        assert_eq!(r.service_cents, 600); // 120 * 5
        assert_eq!(r.electric_cents, 0);
    }

    #[test]
    fn mixed_mode() {
        let rule = PricingRule {
            mode: "mixed".into(),
            service_fee_cents_per_kwh: 30,
            service_fee_cents_per_min: 5,
            min_charge_cents: 0,
            time_of_use: HashMap::new(),
        };
        let r = calculate_fee_compat(&rule, 2.0, 60, 1.0, 1.0);
        assert_eq!(r.service_cents, 60 + 300); // kwh + min
    }

    #[test]
    fn min_charge_floor() {
        let rule = PricingRule {
            mode: "kwh".into(),
            service_fee_cents_per_kwh: 1,
            service_fee_cents_per_min: 0,
            min_charge_cents: 500,
            time_of_use: HashMap::new(),
        };
        let r = calculate_fee_compat(&rule, 0.1, 0, 0.05, 0.05);
        assert!(r.total_cents >= 500);
    }

    #[test]
    fn rate_for_fallback() {
        let r = PricingRule::default_default();
        assert_eq!(r.rate_for("peak"), 80);
        assert_eq!(r.rate_for("off"), 40);
        assert_eq!(r.rate_for("unknown"), 40); // fallback to off
    }
}

#[allow(dead_code)]
pub fn placeholder_v(_: Value) {}