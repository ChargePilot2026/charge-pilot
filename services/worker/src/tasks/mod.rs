//! 已启用的后台循环。未接入的计划任务不注册，避免空循环制造运行正常的假象。
//!
//! D22 处置:原 4 个未注册循环 `export_run` / `reconcile_daily` /
//! `billing_cycle_daily` / `alert_scan` 已**整块删除**,理由如下:
//!
//! - 后三者只有一行 `info!`,没有任何业务动作 —— 接线它们只会得到
//!   「每 30 秒/1 小时打印一次 tick」的循环,是纯噪音。
//! - `export_run` 看似有真实查询(读 `scheduled_task` 表),但该表
//!   **全仓无任何写入方**(admin 的 `api/export.rs` 不写它,migrations
//!   只有建表语句),查询恒为 false。接线它等于每 30 秒打印一次
//!   「export_run 未配置」,且它本身也不执行任何导出动作 —— 它依赖的
//!   上游数据从不存在。
//!
//! 若将来要上导出/对账/告警扫描,应重新设计后实现(需要真正的数据源
//! 与业务定义),而不是恢复这些空壳。

pub mod announcement_expire;
pub mod snapshot_warmer;
pub mod device_session_clean;
// D4 ③b:必须注册 —— 未注册时失败事件永久滞留 DLQ
pub mod dlq_replay;
