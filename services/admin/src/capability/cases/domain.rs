//! cases 域纯逻辑(零 I/O)
//!
//! 客服工单的输入校验。判定顺序与中文文案保持原样。

use common_error::{AppError, AppResult};

/// 反馈 / 报修编号必须是纯数字。
pub fn ensure_numeric_id(id: &str, message: &'static str) -> AppResult<()> {
    if id.parse::<u64>().is_err() {
        return Err(AppError::BadRequest(message.into()));
    }
    Ok(())
}

/// 派单:编号合法且目标账号非零。
pub fn ensure_dispatch(id: &str, assigned_to: u64) -> AppResult<()> {
    if id.parse::<u64>().is_err() || assigned_to == 0 {
        return Err(AppError::BadRequest("报修编号或指派账号无效".into()));
    }
    Ok(())
}

/// 处理报修:编号合法且状态在 `fixed` / `closed` 之内。
pub fn ensure_resolve(id: &str, status: &str) -> AppResult<()> {
    if id.parse::<u64>().is_err() || !["fixed", "closed"].contains(&status) {
        return Err(AppError::BadRequest("报修编号或处理状态无效".into()));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ids_must_be_numeric() {
        assert!(ensure_numeric_id("42", "反馈编号无效").is_ok());
        assert!(
            matches!(
                ensure_numeric_id("abc", "反馈编号无效").unwrap_err(),
                AppError::BadRequest(ref m) if m == "反馈编号无效"
            )
        );
    }

    #[test]
    fn dispatch_rejects_zero_target() {
        assert!(ensure_dispatch("7", 3).is_ok());
        assert!(ensure_dispatch("7", 0).is_err());
        assert!(ensure_dispatch("x", 3).is_err());
    }

    #[test]
    fn resolve_accepts_only_terminal_statuses() {
        for s in ["fixed", "closed"] {
            assert!(ensure_resolve("7", s).is_ok(), "{s}");
        }
        for s in ["open", "processing", ""] {
            assert!(ensure_resolve("7", s).is_err(), "{s}");
        }
        assert!(ensure_resolve("x", "fixed").is_err());
    }
}
