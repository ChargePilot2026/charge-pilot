//! 密码计算的背压执行器(P3 / **D13**)
//!
//! 底层 `common_auth::{hash_password, verify_password}` 是**同步 argon2id**,
//! 单次约 20ms 纯 CPU 计算。原先直接在 async handler 里调用,会把 Tokio
//! worker 线程占住:实测单线程运行时,20ms 定时器在并发验密时要 ~193ms 才到期,
//! 换 `spawn_blocking` 后约 21ms。
//!
//! 但 `spawn_blocking` 本身**没有背压**:它用的是阻塞线程池,无上限地往
//! 阻塞池里塞任务。argon2 的内存与 CPU 成本都高,并发改密码足以打爆内存。
//! 故这里用信号量把并发计算数量**限死**,超出部分在信号量上排队。
//!
//! 并发上限取 `available_parallelism()`:与机器核数一致,再多只是徒增内存。

use common_error::AppResult;
use std::sync::{Arc, OnceLock};
use tokio::sync::Semaphore;

fn permits() -> &'static Semaphore {
    static SEM: OnceLock<Semaphore> = OnceLock::new();
    SEM.get_or_init(|| {
        let n = std::thread::available_parallelism()
            .map(|v| v.get())
            .unwrap_or(2);
        Semaphore::new(n)
    })
}

/// 背压执行一段 CPU 密集的密码计算。
///
/// 与 `tokio::task::spawn_blocking` 的区别:先占一个并发许可再提交,
/// 保证同时在跑的 argon2 运算数不超过 `available_parallelism()`。
async fn compute<T, F>(f: F) -> AppResult<T>
where
    T: Send + 'static,
    F: FnOnce() -> AppResult<T> + Send + 'static,
{
    let _permit = permits()
        .acquire()
        .await
        .map_err(|_| common_error::AppError::ServiceUnavailable("密码服务已关闭".into()))?;
    match tokio::task::spawn_blocking(f).await {
        Ok(result) => result,
        Err(_) => Err(common_error::AppError::Internal("密码计算任务异常".into())),
    }
}

/// 异步算 argon2id 哈希。**离开执行线程**,且并发受限。
pub async fn hash(plain: String) -> AppResult<String> {
    compute(move || common_auth::hash_password(&plain)).await
}

/// 异步校验 argon2id。**离开执行线程**,且并发受限。
///
/// 校验失败返回 `false` 而**不是** `Err`:与原同步实现语义一致 ——
/// 哈希串本身解析不了也算"密码不对",不额外泄露信息。
pub async fn verify(plain: String, hash: String) -> bool {
    matches!(compute(move || Ok(common_auth::verify_password(&plain, &hash))).await, Ok(true))
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 回归护栏:**D13**。并发算密码时,定时器必须仍能及时到期。
    ///
    /// 若密码计算跑在执行线程上,这些 argon2 运算会把 worker 占死,
    /// 20ms 定时器会被推迟到秒级。用 `tokio::time::timeout` 卡上界。
    #[tokio::test(flavor = "current_thread")]
    async fn password_work_leaves_the_executor_thread() {
        let plain = "Passw0rd!fixture".to_string();
        let hash = hash(plain.clone()).await.unwrap();

        let started = std::time::Instant::now();
        let mut joins = Vec::new();
        for _ in 0..4 {
            let (p, h) = (plain.clone(), hash.clone());
            joins.push(tokio::spawn(async move { verify(p, h).await }));
        }
        // 单线程 runtime:若 argon2 跑在本线程,这 4 次验密会串行堵死调度。
        tokio::time::sleep(std::time::Duration::from_millis(20)).await;
        let elapsed = started.elapsed();
        for join in joins {
            assert!(join.await.unwrap(), "正确密码必须校验通过");
        }
        assert!(
            elapsed < std::time::Duration::from_millis(150),
            "20ms 定时器被推迟到 {elapsed:?},说明密码计算仍占用执行线程"
        );
    }

    /// 回归护栏:错误密码必须返回 `false`(不是 Err),不泄露"哈希串损坏"与
    /// "密码错误"的区别。
    #[tokio::test]
    async fn wrong_password_is_false_not_err() {
        let hash = hash("Passw0rd!fixture".to_string()).await.unwrap();
        assert!(!verify("wrong-password".into(), hash).await);
        assert!(!verify("Passw0rd!fixture".into(), "not-a-hash".into()).await);
    }

    /// 回归护栏:并发上限必须**真的限住**。
    ///
    /// 用计数器直接观测"同一时刻在跑的计算数"峰值,不得超过许可数。
    /// 只换 `spawn_blocking` 而不加上限,这个峰值会等于提交的任务总数。
    #[tokio::test(flavor = "current_thread")]
    async fn concurrency_is_capped_at_permit_count() {
        use std::sync::atomic::{AtomicUsize, Ordering};
        let in_flight = Arc::new(AtomicUsize::new(0));
        let peak = Arc::new(AtomicUsize::new(0));
        let limit = std::thread::available_parallelism().map(|v| v.get()).unwrap_or(2);
        let tasks = limit * 4;
        let mut joins = Vec::new();
        for _ in 0..tasks {
            let (in_flight, peak) = (in_flight.clone(), peak.clone());
            joins.push(tokio::spawn(async move {
                compute(move || {
                    let now = in_flight.fetch_add(1, Ordering::SeqCst) + 1;
                    peak.fetch_max(now, Ordering::SeqCst);
                    std::thread::sleep(std::time::Duration::from_millis(30));
                    in_flight.fetch_sub(1, Ordering::SeqCst);
                    Ok(())
                })
                .await
            }));
        }
        for join in joins {
            join.await.unwrap().unwrap();
        }
        let observed = peak.load(Ordering::SeqCst);
        assert!(
            observed <= limit,
            "同时在跑的密码计算峰值 {observed} 超过并发上限 {limit},背压没生效"
        );
    }
}
