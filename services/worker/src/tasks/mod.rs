//! 已启用的后台循环。未接入的计划任务不注册，避免空循环制造运行正常的假象。

pub mod announcement_expire;
pub mod snapshot_warmer;
pub mod device_session_clean;
