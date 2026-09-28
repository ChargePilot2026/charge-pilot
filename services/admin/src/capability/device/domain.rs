//! device 域纯逻辑(零 I/O)
//!
//! 站点 / 设备 / OTA 的输入校验与筛选判定。中文文案与判定顺序保持原样。

use common_error::{AppError, AppResult};

/// 站点文本(编码 / 名称 / 地址 / 营业时间 / 电话)的通用校验。
///
/// `nonempty` 为真时空白串也拒绝 —— 编码与名称必须非空,地址可留空。
pub fn validate_text(value: Option<&str>, max: usize, nonempty: bool) -> AppResult<()> {
    if let Some(value) = value {
        if value.chars().count() > max
            || value.chars().any(char::is_control)
            || (nonempty && value.trim().is_empty())
        {
            return Err(AppError::BadRequest("站点文本为空、过长或包含控制字符".into()));
        }
    }
    Ok(())
}

/// 站点的经纬度 / 状态 / 文本字段校验。
#[allow(clippy::too_many_arguments)]
pub fn validate_fields(
    lng: Option<f64>,
    lat: Option<f64>,
    status: Option<&str>,
    address: Option<&str>,
    hours: Option<&str>,
    phone: Option<&str>,
) -> AppResult<()> {
    if lng.is_some_and(|v| !v.is_finite() || !(-180.0..=180.0).contains(&v))
        || lat.is_some_and(|v| !v.is_finite() || !(-90.0..=90.0).contains(&v))
    {
        return Err(AppError::BadRequest("经纬度超出范围".into()));
    }
    if status.is_some_and(|v| !["active", "disabled", "construction"].contains(&v)) {
        return Err(AppError::BadRequest("站点状态无效".into()));
    }
    validate_text(address, 255, false)?;
    validate_text(hours, 64, false)?;
    validate_text(phone, 32, false)
}

/// 站点列表的分页参数。越界即拒绝,不静默夹取。
pub fn station_page(page: Option<u32>, page_size: Option<u32>) -> AppResult<(u32, u32)> {
    let page = page.unwrap_or(1);
    let page_size = page_size.unwrap_or(20);
    if page == 0 || !(1..=100).contains(&page_size) {
        return Err(AppError::BadRequest("分页参数无效".into()));
    }
    Ok((page, page_size))
}

/// 设备查询参数(分页 + 过滤)的校验。
#[derive(Debug, Clone, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct DeviceQuery {
    pub page: Option<u32>,
    pub page_size: Option<u32>,
    pub keyword: Option<String>,
    pub status: Option<String>,
    pub station_id: Option<u64>,
    pub vendor_id: Option<u64>,
}

impl DeviceQuery {
    /// 返回 `(page, page_size)`。
    pub fn validate(&self) -> AppResult<(u32, u32)> {
        let page = self.page.unwrap_or(1);
        let size = self.page_size.unwrap_or(20);
        if page == 0
            || !(1..=100).contains(&size)
            || self.station_id == Some(0)
            || self.vendor_id == Some(0)
            || self
                .keyword
                .as_ref()
                .is_some_and(|s| s.chars().count() > 128 || s.chars().any(char::is_control))
            || self
                .status
                .as_deref()
                .is_some_and(|s| !["", "enabled", "disabled", "retired", "fault"].contains(&s))
        {
            return Err(AppError::BadRequest("设备查询参数无效".into()));
        }
        Ok((page, size))
    }

    /// 追加 WHERE 片段。`QueryBuilder` 负责转义 bind 参数,片段本身是常量。
    ///
    /// `disallowed_types` 的窄豁免：条件个数随可选过滤项变化，用 `QueryBuilder`
    /// 拼片段才能让 bind 参数（而非字符串拼接）落到 SQL 里 —— 这正是本方法存在
    /// 的理由，拆成固定条数反而会把 bind 退化成拼接。
    #[allow(clippy::disallowed_types)]
    pub fn filter<'a>(&'a self, sql: &mut sqlx::QueryBuilder<'a, sqlx::MySql>) {
        sql.push(" WHERE d.deleted_at IS NULL");
        if let Some(keyword) = self.keyword.as_deref().map(str::trim).filter(|s| !s.is_empty()) {
            sql.push(" AND (LOCATE(").push_bind(keyword).push(",d.device_id)>0 OR LOCATE(")
                .push_bind(keyword).push(",d.model)>0 OR LOCATE(").push_bind(keyword)
                .push(",s.name)>0 OR LOCATE(").push_bind(keyword).push(",s.code)>0)");
        }
        if let Some(status) = self.status.as_deref().filter(|s| !s.is_empty()) {
            sql.push(" AND d.status=").push_bind(status);
        }
        if let Some(id) = self.station_id { sql.push(" AND d.station_id=").push_bind(id); }
        if let Some(id) = self.vendor_id { sql.push(" AND d.vendor_id=").push_bind(id); }
    }
}

/// 设备查询用到的常量 SQL 片段(由 repository 层拼装)。
pub const DEVICE_FROM: &str =
    " FROM device_meta d LEFT JOIN station s ON s.id=d.station_id AND s.deleted_at IS NULL";
pub const DEVICE_COLUMNS: &str = "SELECT d.id,d.device_id,d.station_id,s.name AS station_name,s.code AS station_code,d.vendor_id,d.model,d.status,d.install_at";

/// 附近站点的半径校验(经纬度有限且在合法区间,半径 0.1–50 km)。
pub fn nearby_radius(q: &api_contracts::NearbyStationsQuery) -> AppResult<f64> {
    let radius = q.radius_km.unwrap_or(5.0);
    if !q.lat.is_finite()
        || !q.lng.is_finite()
        || !(-90.0..=90.0).contains(&q.lat)
        || !(-180.0..=180.0).contains(&q.lng)
        || !radius.is_finite()
        || !(0.1..=50.0).contains(&radius)
    {
        return Err(AppError::BadRequest(
            "经纬度无效或搜索半径不在 0.1–50 公里之间".into(),
        ));
    }
    Ok(radius)
}

/// 设备导入的失败重试退避:仅瞬时类错误可重试,且次数有上限。
pub fn retry_delay(error: &AppError, attempts: u32) -> Option<u32> {
    if attempts >= 8 {
        return None;
    }
    match error {
        AppError::ServiceUnavailable(_) | AppError::HttpClient(_) => {
            Some((5 * 2u32.pow(attempts.min(6))).min(300))
        }
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn station_text_rejects_blank_when_required_and_control_chars_always() {
        assert!(validate_text(Some("abc"), 64, true).is_ok());
        assert!(validate_text(Some("   "), 64, true).is_err());
        assert!(validate_text(Some("   "), 64, false).is_ok());
        assert!(validate_text(Some("a\nb"), 64, false).is_err());
        assert!(validate_text(Some(&"字".repeat(65)), 64, false).is_err());
        assert!(validate_text(None, 64, true).is_ok());
    }

    #[test]
    fn station_fields_reject_out_of_range_coordinates_and_unknown_status() {
        assert!(validate_fields(Some(180.0), Some(90.0), Some("active"), None, None, None).is_ok());
        assert!(validate_fields(Some(180.1), None, None, None, None, None).is_err());
        assert!(validate_fields(None, Some(-90.1), None, None, None, None).is_err());
        assert!(validate_fields(None, None, Some("unknown"), None, None, None).is_err());
        assert!(validate_fields(None, None, Some("construction"), None, None, None).is_ok());
    }

    #[test]
    fn station_paging_rejects_zero_and_out_of_range_sizes() {
        assert_eq!(station_page(None, None).unwrap(), (1, 20));
        assert!(station_page(Some(0), Some(20)).is_err());
        assert!(station_page(Some(1), Some(0)).is_err());
        assert!(station_page(Some(1), Some(101)).is_err());
        assert!(station_page(Some(2), Some(100)).is_ok());
    }

    #[test]
    fn device_query_rejects_zero_ids_and_unknown_status() {
        let ok = DeviceQuery {
            page: Some(1), page_size: Some(20), keyword: None,
            status: None, station_id: Some(1), vendor_id: Some(2),
        };
        assert_eq!(ok.validate().unwrap(), (1, 20));
        let zero_station = DeviceQuery { station_id: Some(0), ..ok.clone() };
        assert!(zero_station.validate().is_err());
        let bad_status = DeviceQuery { status: Some("broken".into()), ..ok.clone() };
        assert!(bad_status.validate().is_err());
        let long_keyword = DeviceQuery { keyword: Some("字".repeat(129)), ..ok };
        assert!(long_keyword.validate().is_err());
    }

    #[test]
    fn import_retries_only_transient_errors_with_a_bound() {
        let transient = AppError::ServiceUnavailable("network".into());
        assert_eq!(retry_delay(&transient, 1), Some(10));
        assert_eq!(retry_delay(&transient, 7), Some(300));
        assert_eq!(retry_delay(&transient, 8), None);
        assert_eq!(retry_delay(&AppError::Forbidden("revoked".into()), 1), None);
        assert_eq!(
            retry_delay(&AppError::Conflict("different device".into()), 1),
            None
        );
    }
}
