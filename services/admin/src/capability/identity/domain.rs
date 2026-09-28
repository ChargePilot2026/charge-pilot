//! identity 域纯逻辑(零 I/O)
//!
//! 这里只放**不需要连接**的判定:账号能否登录、失败次数是否达阈值。
//! 判定顺序与文案是 D2 的验收口径,一字未改;改这里等于改安全行为。

/// 达到阈值则锁定。
pub const MAX_FAILED_LOGINS: i64 = 5;
/// 锁定时长(分钟),契约 `docs/api/admin.md`:失败 5 次锁定 30 分钟。
pub const LOCK_MINUTES: i64 = 30;

/// 账号是否禁止登录。`active` 之外一律禁止——原先只判 `locked` 且只在
/// `locked_until > now` 时拒绝,`disabled` 完全没被拦。
pub fn is_login_blocked(
    status: &str,
    locked_until: Option<chrono::DateTime<chrono::Utc>>,
    now: chrono::DateTime<chrono::Utc>,
) -> bool {
    match status {
        "active" => false,
        // 锁定到期后自动放行,由成功登录时复位状态
        "locked" => locked_until.is_some_and(|lu| lu > now),
        // disabled / 任何未知状态都拒绝
        _ => true,
    }
}

/// 达到阈值则锁定。
pub fn should_lock(failed_count: i64) -> bool {
    failed_count >= MAX_FAILED_LOGINS
}
