//! gateway 设备域的内部契约 DTO(P2)
//!
//! 替代原先的 `ApiEnvelope<Value>` + `json!` 响应。响应形状在 handler 里本就是
//! 确定的,固化到契约层后,字段改名/漏字段会在编译期暴露,而不是在运行时。
//!
//! **未改变任何字段名与语义** —— 见 `docs/api-change-list.md` §3。

use serde::{Deserialize, Serialize};

// ===== 会话清理 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CleanupSessionsResponse {
    pub closed_count: u64,
}

// ===== 设备 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DeviceSummary {
    pub device_id: String,
    pub vendor_id: u64,
    pub port_count: u8,
    pub status: String,
    pub firmware_version: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DevicePort {
    pub id: u64,
    pub port_no: u8,
    pub port_code: String,
    pub status: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DevicePortsResponse {
    pub items: Vec<DevicePort>,
}

/// 设备遥测快照。七重防护遥测字段(技术规格 §6.4)的可用子集;
/// `ts` 是该快照的采样时间。缺失指标不序列化为 null——
/// user 侧 `charge_snapshot` 按 key 是否存在来判定 `telemetry_available`。
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct TelemetrySnapshot {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub power_w: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub voltage_v: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub current_a: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub temperature_c: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub battery_soc: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ts: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DeviceSnapshotResponse {
    pub device_id: String,
    pub order_id: String,
    pub snapshot: TelemetrySnapshot,
}

/// 曲线上的一个采样点:`ts` 固定存在,其余指标按实际采集情况可选。
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct CurvePoint {
    pub ts: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub power_w: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub voltage_v: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub current_a: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub temperature_c: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub battery_soc: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub meter_kwh: Option<f64>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct CurveSummary {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_power_w: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_current_a: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_temperature_c: Option<f64>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DeviceCurveResponse {
    pub order_id: String,
    pub window: String,
    pub sample_interval_seconds: u32,
    pub series: Vec<CurvePoint>,
    pub summary: CurveSummary,
}

// ===== 补传 / 指令 =====

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BackfillResponse {
    pub inserted: usize,
}

/// 尚未接入实际设备传输的能力统一用这个形状,避免每次临时造匿名结构。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct NotImplementedResponse {
    pub reason: String,
}

/// 固件推送请求(尚未接线,但**契约必须显式** —— 不能用 `Json<Value>` 收任意体)。
///
/// 固件升级协议会随厂商/型号变化,当前尚无任何实现消费这些字段,
/// 因此全部为可选且 `deny_unknown_fields` **关闭**:真实固件可能带额外字段,
/// 收窄到具名字段的同时不应把未知的合法字段变成 400。
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct FirmwarePushRequest {
    #[serde(default)]
    pub package_id: Option<u64>,
    #[serde(default)]
    pub version: Option<String>,
    #[serde(default)]
    pub force: Option<bool>,
}

/// 设备指令请求(尚未接线)。
///
/// `params` 刻意保持 `Map<String, Value>` 而非裸 `Value`:它是**指令参数集合**,
/// 键名由具体 `cmd` 决定(重启/读取/设置…),无法在契约层枚举;
/// 但「必须是对象」这一条可以约束住 —— 用 `Map` 后,`params: 123` 这类
/// 畸形请求会在反序列化时就被拒,而不是流到下发逻辑里。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DeviceCommandRequest {
    pub cmd: String,
    #[serde(default)]
    /// 键名由 `cmd` 决定(重启/读取/设置…),**无法在契约层枚举**,故整体豁免。
    /// 契约层唯一能约束的「必须是对象」已由 `Map` 表达(见结构体文档)。
    pub params: Option<serde_json::Map<String, crate::OpaqueJson>>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn snapshot_omits_absent_metrics() {
        let s = TelemetrySnapshot { power_w: Some(3500.0), ..Default::default() };
        let v = serde_json::to_value(&s).unwrap();
        assert_eq!(v["power_w"], 3500.0);
        // 缺失指标不应出现为 null:user 侧按 key 存在与否判定 telemetry_available
        assert!(v.get("voltage_v").is_none(), "缺失指标不应序列化: {v}");
    }

    #[test]
    fn cleanup_response_field_name_frozen() {
        let v = serde_json::to_value(CleanupSessionsResponse { closed_count: 3 }).unwrap();
        assert_eq!(v["closed_count"], 3);
    }

    /// P2 残尾的类型化收益：`params` 必须是**对象**。
    /// 改回裸 `Value` 后 `params: 123` 这类畸形请求会静默通过反序列化，
    /// 一路流到设备下发逻辑才出错。
    #[test]
    fn device_command_params_must_be_an_object() {
        let ok: DeviceCommandRequest =
            serde_json::from_str(r#"{"cmd":"reboot","params":{"delay":3}}"#).unwrap();
        assert_eq!(ok.cmd, "reboot");
        assert_eq!(ok.params.unwrap()["delay"], 3);

        assert!(
            serde_json::from_str::<DeviceCommandRequest>(r#"{"cmd":"reboot","params":123}"#).is_err(),
            "params 必须是对象，标量应被拒"
        );
        // params 整体可省略
        let bare: DeviceCommandRequest = serde_json::from_str(r#"{"cmd":"ping"}"#).unwrap();
        assert!(bare.params.is_none());
    }

    /// 未知的额外字段不得导致 400 —— 固件协议会随厂商演进，
    /// 收窄到具名字段的同时不能把合法的新字段变成错误。
    #[test]
    fn firmware_push_ignores_unknown_fields() {
        let r: FirmwarePushRequest = serde_json::from_str(
            r#"{"package_id":7,"version":"1.2.3","vendor_specific":{"a":1}}"#,
        )
        .unwrap();
        assert_eq!(r.package_id, Some(7));
        assert_eq!(r.version.as_deref(), Some("1.2.3"));
    }

    #[test]
    fn device_summary_round_trip() {
        let d = DeviceSummary {
            device_id: "D1".into(), vendor_id: 7, port_count: 2,
            status: "active".into(), firmware_version: None,
        };
        let back: DeviceSummary = serde_json::from_value(serde_json::to_value(&d).unwrap()).unwrap();
        assert_eq!(back.device_id, "D1");
        assert_eq!(back.firmware_version, None);
    }

    #[test]
    fn curve_point_requires_ts() {
        let p = CurvePoint { ts: "2026-09-28T00:00:00Z".into(), ..Default::default() };
        let v = serde_json::to_value(&p).unwrap();
        assert!(v["ts"].is_string());
    }
}

impl TelemetrySnapshot {
    /// 按遥测字段名写入(与 gateway 的 `metric_field` 映射一致)。
    /// 未知字段忽略,便于向前兼容新增指标。
    pub fn set(&mut self, field: &str, value: f64) {
        match field {
            "power_w" => self.power_w = Some(value),
            "voltage_v" => self.voltage_v = Some(value),
            "current_a" => self.current_a = Some(value),
            "temperature_c" => self.temperature_c = Some(value),
            "battery_soc" => self.battery_soc = Some(value),
            "ts" => self.ts = Some(value.to_string()),
            _ => {}
        }
    }

    pub fn get(&self, field: &str) -> Option<f64> {
        match field {
            "power_w" => self.power_w,
            "voltage_v" => self.voltage_v,
            "current_a" => self.current_a,
            "temperature_c" => self.temperature_c,
            "battery_soc" => self.battery_soc,
            _ => None,
        }
    }
}

impl CurvePoint {
    pub fn set(&mut self, field: &str, value: f64) {
        match field {
            "power_w" => self.power_w = Some(value),
            "voltage_v" => self.voltage_v = Some(value),
            "current_a" => self.current_a = Some(value),
            "temperature_c" => self.temperature_c = Some(value),
            "battery_soc" => self.battery_soc = Some(value),
            "meter_kwh" => self.meter_kwh = Some(value),
            _ => {}
        }
    }
}

// ===== 历史曲线 =====

/// 历史曲线的一个桶。
///
/// 字段名形如 `{metric}_avg/_min/_max`,看着动态,但 `metric_field` 的指标集合是
/// **封闭的 6 个**,因此可以完全枚举——这正是类型化该做的事,而不是留 `Value`。
/// 电池与电量的聚合语义不同(取区间末值),故为 `_end`。
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct HistoricalCurvePoint {
    pub bucket_start: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub power_w_avg: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub power_w_min: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub power_w_max: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub voltage_v_avg: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub voltage_v_min: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub voltage_v_max: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub current_a_avg: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub current_a_min: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub current_a_max: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub temperature_c_avg: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub temperature_c_min: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub temperature_c_max: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub battery_soc_end: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub meter_kwh_end: Option<f64>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct HistoricalCurveSummary {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_power_w: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_temperature_c: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub avg_power_w: Option<f64>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DeviceHistoricalCurveResponse {
    pub granularity: String,
    pub series: Vec<HistoricalCurvePoint>,
    pub summary: HistoricalCurveSummary,
}

impl HistoricalCurvePoint {
    /// 写入 `{field}_avg/_min/_max`;电池与电量落到 `_end`。
    /// 字段名与原 `json!` 动态拼接完全一致,响应格式不变。
    pub fn set_stat(&mut self, field: &str, avg: Option<f64>, min: Option<f64>, max: Option<f64>) {
        match (field, avg, min, max) {
            ("battery_soc", a, _, _) => self.battery_soc_end = a,
            ("meter_kwh", _, _, m) => self.meter_kwh_end = m,
            _ => {
                macro_rules! slot {
                    ($avg:expr, $min:expr, $max:expr) => {{
                        $avg = avg;
                        $min = min;
                        $max = max;
                    }};
                }
                match field {
                    "power_w" => slot!(self.power_w_avg, self.power_w_min, self.power_w_max),
                    "voltage_v" => slot!(self.voltage_v_avg, self.voltage_v_min, self.voltage_v_max),
                    "current_a" => slot!(self.current_a_avg, self.current_a_min, self.current_a_max),
                    "temperature_c" => {
                        slot!(self.temperature_c_avg, self.temperature_c_min, self.temperature_c_max)
                    }
                    _ => {}
                }
            }
        }
    }
}

#[cfg(test)]
mod historical_tests {
    use super::*;

    #[test]
    fn stat_field_names_match_legacy_keys() {
        let mut p = HistoricalCurvePoint {
            bucket_start: "2026-09-28T00:00:00Z".into(),
            ..Default::default()
        };
        p.set_stat("power_w", Some(1.0), Some(0.5), Some(2.0));
        p.set_stat("battery_soc", Some(80.0), Some(1.0), Some(3.0));
        // 语义与旧实现一致:电量取区间**最大**值,不是平均
        p.set_stat("meter_kwh", Some(9.0), Some(4.0), Some(5.0));
        let v = serde_json::to_value(&p).unwrap();
        // 与原先 json! 动态拼接的 key 完全一致
        assert_eq!(v["power_w_avg"], 1.0);
        assert_eq!(v["power_w_min"], 0.5);
        assert_eq!(v["power_w_max"], 2.0);
        // 电池/电量取区间末值,不产出 _avg/_min/_max
        assert_eq!(v["battery_soc_end"], 80.0);
        assert_eq!(v["meter_kwh_end"], 5.0, "电量按旧语义取 max");
        assert!(v.get("battery_soc_avg").is_none());
    }

    #[test]
    fn unknown_metric_is_ignored() {
        let mut p = HistoricalCurvePoint { bucket_start: "t".into(), ..Default::default() };
        p.set_stat("not_a_metric", Some(1.0), Some(1.0), Some(1.0));
        let v = serde_json::to_value(&p).unwrap();
        assert_eq!(v.as_object().unwrap().len(), 1);
    }
}

/// 原始遥测采样行(内部,不直接对外;对外由快照/曲线聚合成别的形状)。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TelemetrySample {
    pub metric: String,
    pub value_num: Option<f64>,
    pub ts: String,
}

/// 历史聚合表的原始行。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HistoricalSampleRow {
    pub metric: String,
    pub avg_v: Option<f64>,
    pub min_v: Option<f64>,
    pub max_v: Option<f64>,
    pub sample_count: u64,
    pub bucket_start: String,
}
