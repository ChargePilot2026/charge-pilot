//! export_run: 导出任务执行器(由 admin /export 端点触发,在 scheduled_task.config_json 中存 export 元数据)
//! 频率: 30 s

use crate::AppState;
use std::time::Duration;
use tracing::info;

pub async fn run(state: AppState) {
    let mut iv = tokio::time::interval(Duration::from_secs(30));
    loop {
        iv.tick().await;
        let r = sqlx::query("SELECT config_json FROM scheduled_task WHERE task_code='export_run' LIMIT 1")
            .fetch_optional(state.db.pool()).await;
        let has = matches!(&r, Ok(Some(_)));
        info!(has, "export_run tick");
    }
}