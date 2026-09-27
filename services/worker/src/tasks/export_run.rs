//! export_run: 导出任务执行器(由 admin /export 端点触发,在 scheduled_task.config_json 中存 export 元数据)
//! 频率: 30 s
//!
//! P3:`scheduled_task` 的探活查询已下沉到 [`crate::services::RetryService`],
//! 本文件只留循环编排。**查询失败不外泄** —— 原实现里 `matches!(&r, Ok(Some(_)))`
//! 把 `Err` 也归为 `has=false`,这里保留同一口径。

use crate::AppState;
use std::time::Duration;
use tracing::info;

pub async fn run(state: AppState) {
    let mut iv = tokio::time::interval(Duration::from_secs(30));
    loop {
        iv.tick().await;
        let has = state.retry.export_run_configured().await;
        info!(has, "export_run tick");
    }
}
