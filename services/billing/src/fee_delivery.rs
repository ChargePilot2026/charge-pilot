//! Durable at-least-once HTTP delivery; user receipt makes the effects idempotent.
//!
//! P3:取件 / 投递 / 重试的 SQL 与事务已下沉到 [`crate::services::FeeService`],
//! 本文件只留后台循环编排(2 秒一次,每轮最多连投 50 条)。
//!
//! 循环口径原样保留:单条出错(`Err`)即 `break` 掉本轮内层循环,
//! 避免一条坏单把队列卡死的同时不打日志;`Ok(false)`(无待投递项)同样 `break`。
use crate::AppState;

pub fn spawn(st: AppState) {
    tokio::spawn(async move {
        let mut tick = tokio::time::interval(std::time::Duration::from_secs(2));
        loop {
            tick.tick().await;
            for _ in 0..50 {
                match st.fee.deliver_one().await {
                    Ok(true) => {}
                    Ok(false) => break,
                    Err(error) => {
                        tracing::warn!(%error,"fee delivery failed; retrying");
                        break;
                    }
                }
            }
        }
    });
}
