//! The gateway service publishes its own outbox; Redis failure never loses a receipt.
//!
//! P3:发布逻辑已下沉到 `OutboxService`,本文件只留后台任务编排。
//! P5:`enqueue` 的 SQL 进一步下沉到 `DeviceService::enqueue_event_in` ——
//! 入队必须发生在**发信方自己的事务**里,跨域调用一个自由函数无法保证这一点。
//! 本文件剩下的只有投递循环编排。
use crate::AppState;
use std::time::Duration;

pub fn spawn(st: AppState) {
    tokio::spawn(async move {
        let mut tick = tokio::time::interval(Duration::from_secs(2));
        loop {
            tick.tick().await;
            for _ in 0..50 {
                match st.outbox.publish_one().await {
                    Ok(true) => {}
                    Ok(false) => break,
                    Err(_) => {
                        tracing::warn!("gateway outbox publish failed; retrying next tick");
                        break;
                    }
                }
            }
        }
    });
}
