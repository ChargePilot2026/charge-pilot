//! config 域纯逻辑(零 I/O)
//!
//! 白标配置的校验与字段清洗。这里的判定顺序与中文文案是硬契约。

use common_error::{AppError, AppResult};

/// URL 必须是空或合法 HTTPS。
pub fn valid_https(value: &Option<String>) -> bool {
    value.as_deref().is_none_or(|s| {
        s.trim().is_empty() || (s.len() <= 512 && !s.chars().any(char::is_control)
            && reqwest::Url::parse(s).is_ok_and(|url| url.scheme() == "https" && url.host_str().is_some()))
    })
}

/// 可选字符串清洗:去空白,空串归 `None`。
pub fn clean_optional(value: &mut Option<String>) {
    *value = value.take().map(|s| s.trim().to_owned()).filter(|s| !s.is_empty());
}

/// 白标配置的整体校验。
#[allow(clippy::too_many_arguments)]
pub fn validate_whitelabel(req: &crate::capability::config::WhitelabelUpdate) -> AppResult<()> {
    let name = req.miniprogram_name.trim();
    if name.is_empty() || name.chars().count() > 64 || name.chars().any(char::is_control) {
        return Err(AppError::BadRequest("小程序名称不能为空且最多 64 字".into()));
    }
    if !(req.theme_color.len() == 7 || req.theme_color.len() == 9)
        || !req.theme_color.starts_with('#')
        || !req.theme_color[1..].bytes().all(|b| b.is_ascii_hexdigit())
    {
        return Err(AppError::BadRequest("主题色必须为 #RRGGBB 或 #RRGGBBAA".into()));
    }
    if !valid_https(&req.miniprogram_logo_url) || !valid_https(&req.admin_logo_url)
        || !valid_https(&req.agreement_url) || !valid_https(&req.privacy_url)
    {
        return Err(AppError::BadRequest("Logo、协议和隐私链接必须使用 HTTPS".into()));
    }
    if req.service_phone.as_deref().is_some_and(|s| s.len() > 32 || s.chars().any(char::is_control))
        || req.service_wechat_id.as_deref().is_some_and(|s| s.trim().is_empty() || s.len() > 64 || s.chars().any(char::is_control))
        || req.icp_record_no.as_deref().is_some_and(|s| s.len() > 128 || s.chars().any(char::is_control))
        || req.about_us.as_deref().is_some_and(|s| s.chars().count() > 20_000 || s.chars().any(|c| c.is_control() && c != '\n' && c != '\r' && c != '\t'))
    {
        return Err(AppError::BadRequest("白标联系方式、备案信息或介绍内容无效".into()));
    }
    if req.custom_domain.as_deref().is_some_and(|domain| {
        domain.is_empty() || domain.len() > 253 || !domain.contains('.')
            || !domain.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'.' || b == b'-')
            || domain.split('.').any(|part| part.is_empty() || part.starts_with('-') || part.ends_with('-'))
    }) {
        return Err(AppError::BadRequest("自定义域名格式无效".into()));
    }
    Ok(())
}

/// 客服入口的配置校验(微信号 / 名称 / HTTPS 路径)。
pub fn validate_customer_service(
    agent_wechat: &str,
    agent_name: Option<&str>,
    path: Option<&str>,
) -> AppResult<()> {
    if agent_wechat.trim().is_empty() || agent_wechat.len() > 64 || agent_wechat.chars().any(char::is_control) {
        return Err(AppError::BadRequest("客服微信号无效".into()));
    }
    if agent_name.is_some_and(|name| name.trim().is_empty() || name.len() > 64 || name.chars().any(char::is_control)) {
        return Err(AppError::BadRequest("客服名称无效".into()));
    }
    if path.is_some_and(|path| path.len() > 512 || !path.starts_with("https://") || path.chars().any(char::is_control)) {
        return Err(AppError::BadRequest("客服入口必须是有效 HTTPS URL".into()));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn req() -> crate::capability::config::WhitelabelUpdate {
        crate::capability::config::WhitelabelUpdate {
            miniprogram_name: "小程序".into(),
            miniprogram_logo_url: None,
            admin_logo_url: None,
            theme_color: "#112233".into(),
            service_phone: None,
            service_wechat_id: None,
            icp_record_no: None,
            custom_domain: None,
            agreement_url: None,
            privacy_url: None,
            about_us: None,
        }
    }

    #[test]
    fn accepts_a_minimal_valid_whitelabel() {
        assert!(validate_whitelabel(&req()).is_ok());
    }

    #[test]
    fn rejects_blank_or_oversized_name() {
        let mut r = req();
        r.miniprogram_name = "   ".into();
        assert!(
            matches!(
                validate_whitelabel(&r).unwrap_err(),
                AppError::BadRequest(ref m) if m == "小程序名称不能为空且最多 64 字"
            )
        );
        r.miniprogram_name = "字".repeat(65);
        assert!(validate_whitelabel(&r).is_err());
    }

    #[test]
    fn theme_color_must_be_hex_with_hash() {
        let mut r = req();
        r.theme_color = "112233".into();
        assert!(
            matches!(
                validate_whitelabel(&r).unwrap_err(),
                AppError::BadRequest(ref m) if m == "主题色必须为 #RRGGBB 或 #RRGGBBAA"
            )
        );
        r.theme_color = "#11223".into();
        assert!(validate_whitelabel(&r).is_err());
        r.theme_color = "#1122334".into(); // 8 位 = # + 7 位，既不是 7 也不是 9
        assert!(validate_whitelabel(&r).is_err());
        r.theme_color = "#11223345".into(); // 9 位 = #RRGGBBAA
        assert!(validate_whitelabel(&r).is_ok());
    }

    #[test]
    fn links_must_be_https() {
        let mut r = req();
        r.privacy_url = Some("http://x.com".into());
        assert!(
            matches!(
                validate_whitelabel(&r).unwrap_err(),
                AppError::BadRequest(ref m) if m == "Logo、协议和隐私链接必须使用 HTTPS"
            )
        );
        r.privacy_url = Some("https://x.com".into());
        assert!(validate_whitelabel(&r).is_ok());
    }

    #[test]
    fn custom_domain_rejects_malformed_values() {
        let mut r = req();
        for bad in ["nodot", "-a.com", "a-.com", "a..com", ""] {
            r.custom_domain = Some(bad.into());
            assert!(
                matches!(
                    validate_whitelabel(&r).unwrap_err(),
                    AppError::BadRequest(ref m) if m == "自定义域名格式无效"
                ),
                "{bad}"
            );
        }
        r.custom_domain = Some("a.example.com".into());
        assert!(validate_whitelabel(&r).is_ok());
    }

    #[test]
    fn clean_optional_trims_and_drops_empty() {
        let mut v = Some("  x  ".into());
        clean_optional(&mut v);
        assert_eq!(v.as_deref(), Some("x"));
        let mut v = Some("   ".into());
        clean_optional(&mut v);
        assert_eq!(v, None);
        let mut v: Option<String> = None;
        clean_optional(&mut v);
        assert_eq!(v, None);
    }

    #[test]
    fn customer_service_requires_https_entry() {
        assert!(validate_customer_service("wxid", None, None).is_ok());
        assert!(
            matches!(
                validate_customer_service("  ", None, None).unwrap_err(),
                AppError::BadRequest(ref m) if m == "客服微信号无效"
            )
        );
        assert!(
            matches!(
                validate_customer_service("wxid", None, Some("http://x".into())).unwrap_err(),
                AppError::BadRequest(ref m) if m == "客服入口必须是有效 HTTPS URL"
            )
        );
    }
}
