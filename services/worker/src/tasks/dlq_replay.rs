//! dlq_replay:DLQ 重放(每天 04:00)
//! 频率: 1 day
//!
//! **D4 ③b 修复**:此前本任务只 `info!("dlq_replay tick")` 就结束,且**未注册**。
//! 而 `common_stream` 的失败处理在重试耗尽后写 DLQ 并 `XACK` 原消息
//! (`common-redis/src/lib.rs:dead_letter` 的 Lua)——消息已离开 PEL,正常消费者
//! 永不再见。结果是**依赖短暂故障超过重试周期后,计费/退款事件永久停在 DLQ**,
//! 且"ACK 水位"不能代表"业务完成水位"。
//!
//! 现在本任务做真实重放:把 DLQ 中的原始 entry 重新 XADD 回原 stream。
//! 消费者的 receipt 去重保证重复投递安全(逐消费者幂等是 D4 ④ 的前置条件)。
//!
//! P3:重放逻辑与 DLQ entry 解析已下沉到 [`crate::services::RetryService`],
//! 本文件只留每日循环编排。

use crate::AppState;
use std::time::Duration;
use tracing::error;

/// 单批重放上限,避免一次性把 DLQ 全量灌回
const BATCH_LIMIT: usize = 200;

pub async fn run(state: AppState) {
    let mut iv = tokio::time::interval(Duration::from_secs(86_400));
    loop {
        iv.tick().await;
        if let Err(e) = state.retry.replay_once(BATCH_LIMIT).await {
            error!(error = %e, "dlq replay failed");
        }
    }
}
