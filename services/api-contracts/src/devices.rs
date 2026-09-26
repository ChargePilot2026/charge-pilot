use serde::{Deserialize, Serialize};
use std::collections::HashSet;

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct DeviceProvision {
    pub device_id: String,
    pub vendor_id: u64,
    pub station_id: u64,
    pub port_count: u8,
    pub model: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct DeviceProvisionBatch {
    pub devices: Vec<DeviceProvision>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProvisionedDevice {
    pub device_id: String,
    pub created: bool,
    pub ports: Vec<ProvisionedPort>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProvisionedPort {
    pub port_id: u64,
    pub port_no: u8,
    pub port_code: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DeviceProvisionResult {
    pub items: Vec<ProvisionedDevice>,
}

impl DeviceProvisionBatch {
    pub fn validate(&self) -> Result<(), String> {
        if self.devices.is_empty() || self.devices.len() > 100 {
            return Err("每批需包含 1 至 100 台设备".into());
        }
        let mut seen = HashSet::new();
        for (index, device) in self.devices.iter().enumerate() {
            let error = if !(8..=32).contains(&device.device_id.len())
                || !device
                    .device_id
                    .bytes()
                    .all(|c| c.is_ascii_alphanumeric() || c == b'-' || c == b'_')
            {
                Some("设备 ID 须为 8 至 32 位字母、数字、横线或下划线")
            } else if !seen.insert(device.device_id.to_ascii_lowercase()) {
                Some("同批设备 ID 重复")
            } else if device.vendor_id == 0 || device.station_id == 0 {
                Some("厂商和站点 ID 必须大于零")
            } else if device.port_count == 0 {
                Some("端口数量必须为 1 至 255")
            } else if device.model.as_ref().is_some_and(|v| {
                v.trim().is_empty() || v.chars().count() > 128 || v.chars().any(char::is_control)
            }) {
                Some("型号须为 1 至 128 个可见字符")
            } else {
                None
            };
            if let Some(message) = error {
                return Err(format!("第 {} 行：{message}", index + 1));
            }
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn device() -> DeviceProvision {
        DeviceProvision {
            device_id: "DEVICE_01".into(),
            vendor_id: 1,
            station_id: 2,
            port_count: 2,
            model: None,
        }
    }
    #[test]
    fn validates_bounds_and_case_insensitive_duplicates() {
        let mut batch = DeviceProvisionBatch {
            devices: vec![device()],
        };
        assert!(batch.validate().is_ok());
        let mut duplicate = device();
        duplicate.device_id = "device_01".into();
        batch.devices.push(duplicate);
        assert!(batch.validate().unwrap_err().contains("第 2 行"));
        batch.devices = vec![device(); 101];
        assert!(batch.validate().is_err());
        batch.devices.clear();
        assert!(batch.validate().is_err());
        batch.devices.push(device());
        batch.devices[0].port_count = 0;
        assert!(batch.validate().is_err());
        batch.devices[0].port_count = 1;
        batch.devices[0].device_id = "../../oops".into();
        assert!(batch.validate().is_err());
    }
}
