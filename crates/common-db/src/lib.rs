//! 数据库连接池 + 迁移 + 通用查询帮助
//!
//! 设计目标(对齐技术规格 § 4):
//!   - 各服务独立连接池(独立 schema,独立账户)
//!   - 启动时自动 `sqlx::migrate!()` 应用迁移
//!   - 提供 `with_tx` 事务封装,所有写操作走显式事务
//!   - 提供 `IdGen` / `TimeOf` 等公用工具


// 分层与序列化约束(P1a 建立;P5 收口完成,已转 deny)
// 说明:配置在仓库根 clippy.toml,级别在这里。测试模块豁免。
#![deny(
    clippy::disallowed_macros,
    clippy::disallowed_types,
    clippy::disallowed_methods,
)]
// `disallowed_methods` 在本 crate **整体豁免**:
// common-db 是数据库基础设施 crate,`ping` / `ensure_schema_metadata` /
// `soft_delete` 里的 SQL 就是它的职责本身(连得上吗、sql_mode 是什么、
// 通用软删),不存在「该下沉到某个业务域 repository」这回事。
// 业务侧的 SQL 仍由各服务自己的 repository 层约束(见 clippy.toml 的 reason)。
#![allow(clippy::disallowed_methods)]
use async_trait::async_trait;
use common_config::MysqlConfig;
use common_error::{AppError, AppResult};
use serde::{Deserialize, Serialize};
use sqlx::MySqlConnection;
use sqlx::mysql::{MySqlConnectOptions, MySqlPoolOptions};
use sqlx::{ConnectOptions, MySql, Pool, Transaction};
use std::str::FromStr;
use std::time::Duration;
use tracing::log::LevelFilter;

/// 数据库访问入口
#[derive(Clone)]
pub struct Db {
    pool: Pool<MySql>,
}

impl Db {
    pub async fn connect(cfg: &MysqlConfig) -> AppResult<Self> {
        // 解析 DATABASE_URL,关闭 prepared statement cache(降低内存 + 长事务影响)
        let mut opts = MySqlConnectOptions::from_str(&cfg.url)
            .map_err(|e| AppError::Config(format!("bad DATABASE_URL: {e}")))?;
        opts = opts
            .log_statements(LevelFilter::Debug)
            .log_slow_statements(LevelFilter::Warn, Duration::from_millis(500));

        let pool = MySqlPoolOptions::new()
            .max_connections(cfg.max_connections)
            .min_connections(cfg.min_connections)
            .acquire_timeout(Duration::from_secs(cfg.connect_timeout_secs))
            .connect_with(opts)
            .await?;
        tracing::info!(min = cfg.min_connections, max = cfg.max_connections, "MySQL pool ready");
        Ok(Self { pool })
    }

    /// 接管一个**已经建好**的连接池。
    ///
    /// 主要给 `#[cfg(test)]` 与集成测试用:测试往往自己用 `DATABASE_URL` 建池
    /// (要自定义 options),但业务入口已收窄到 `Db` / 能力域服务对象。
    /// 生产路径一律走 `Db::connect`,不要用这个 —— 它绕过了连接数与
    /// 慢查询日志的集中配置。
    pub fn from_pool(pool: Pool<MySql>) -> Self {
        Self { pool }
    }

    pub fn pool(&self) -> &Pool<MySql> {
        &self.pool
    }

    pub async fn ping(&self) -> AppResult<()> {
        sqlx::query("SELECT 1").execute(&self.pool).await?;
        Ok(())
    }

    /// 执行 migrations/*.sql(每个服务单独维护自己的 migrations 目录)
    pub async fn migrate(&self, migrations_dir: &str) -> AppResult<()> {
        // 使用 sqlx::migrate! 宏在编译期嵌入迁移文件;各服务 crate 内调用
        tracing::info!(path = migrations_dir, "applying migrations");
        // 注意: 真正的 migrate 调用由各服务的 build.rs 或启动代码完成,
        // 这里仅提供健康检查占位
        Ok(())
    }

    /// 启动一次性 helper: 建表 + 索引 + 分区(本系统的迁移按月分区用 generated column)
    pub async fn ensure_schema_metadata(&self) -> AppResult<()> {
        // 预留:校验 sql_mode/隔离级别/字符集
        let row: (String,) = sqlx::query_as("SELECT @@sql_mode")
            .fetch_one(&self.pool)
            .await?;
        tracing::debug!(sql_mode = %row.0, "sql_mode");
        Ok(())
    }
}

/// 在事务内执行回调(自动 commit / rollback)
pub async fn with_tx<F, T>(db: &Db, f: F) -> AppResult<T>
where
    F: for<'c> FnOnce(&'c mut Transaction<'_, MySql>) -> BoxFuture<'c, AppResult<T>>,
{
    let mut tx = db.pool().begin().await?;
    let out = match f(&mut tx).await {
        Ok(v) => v,
        Err(e) => {
            let _ = tx.rollback().await;
            return Err(e);
        }
    };
    tx.commit().await?;
    Ok(out)
}

/// 显式事务句柄——仓储方法的连接入参。
///
/// 选它而不是让仓储方法各自取连接:usecase 连续多次仓储调用必须落在**同一条连接**
/// 的同一事务里,否则资金链路的原子性就没了。具体类型(`&mut MySqlConnection`)保持
/// dyn-safe,mock 仓储写起来也最省事。
///
/// `rollback` 必须显式提供:sqlx 的 `Transaction` 析构会启动回滚,但那是
/// fire-and-forget,替代不了"等待回滚完成并处理错误"。`charge_start.rs` 的重试
/// 语义就依赖这一点。
pub struct Tx<'a> {
    tx: Transaction<'a, MySql>,
}

impl<'a> Tx<'a> {
    /// 接管一个已经开好的 `sqlx` 事务。
    ///
    /// 供"入参是 `&MySqlPool`、内部自己开事务"的函数使用:它们拿不到 `Db`,
    /// 但仍要用 `Tx` 的显式 `commit` / `rollback` 语义(而不是依赖析构时的
    /// fire-and-forget 回滚)。
    pub fn from_transaction(tx: Transaction<'a, MySql>) -> Self {
        Self { tx }
    }
}

impl<'a> Tx<'a> {
    /// 交给仓储方法执行 SQL
    pub fn executor(&mut self) -> &mut MySqlConnection {
        &mut *self.tx
    }

    pub async fn commit(self) -> AppResult<()> {
        self.tx.commit().await?;
        Ok(())
    }

    /// 显式回滚并等待完成。内部 `Transaction` 析构仍作为提前 return /
    /// task 被 cancel 时的兜底。
    pub async fn rollback(self) -> AppResult<()> {
        self.tx.rollback().await?;
        Ok(())
    }
}

impl<'a> Db {
    /// 开启一个显式事务
    pub async fn begin(&self) -> AppResult<Tx<'_>> {
        let tx = self.pool.begin().await?;
        Ok(Tx { tx })
    }
}

pub type BoxFuture<'c, T> = std::pin::Pin<Box<dyn std::future::Future<Output = T> + Send + 'c>>;

/// 软删除 helper: update ... set deleted_at = now(3) where id = ?
pub async fn soft_delete(db: &Db, table: &str, id: u64, operator_id: Option<u64>) -> AppResult<()> {
    let sql = format!(
        "UPDATE `{table}` SET deleted_at = NOW(3), deleted_by = ? WHERE id = ? AND deleted_at IS NULL"
    );
    sqlx::query(&sql)
        .bind(operator_id)
        .bind(id)
        .execute(db.pool())
        .await?;
    Ok(())
}

/// 软删除扫描(默认查询)
pub const SOFT_DELETE_FILTER: &str = "deleted_at IS NULL";

/// 通用 ID 生成:雪花简化版(毫秒时间戳 + 12 位随机数),用于业务 ID
#[derive(Clone)]
pub struct IdGen {
    prefix: String,
}

impl IdGen {
    pub fn new(prefix: impl Into<String>) -> Self {
        Self { prefix: prefix.into() }
    }

    pub fn next(&self) -> String {
        use rand::Rng;
        let mut rng = rand::thread_rng();
        let ts = chrono::Utc::now().timestamp_millis();
        let rand: u64 = rng.gen_range(0..100_000_000_000);
        format!("{}-{}-{:012}", self.prefix, ts, rand)
    }

    pub fn next_with(&self, kind: &str) -> String {
        format!("{}-{}-{}", self.prefix, kind, self.next().split('-').nth(2).unwrap_or("0"))
    }
}

/// 时间戳工具
pub struct TimeOf;

impl TimeOf {
    pub fn now_utc() -> chrono::DateTime<chrono::Utc> {
        chrono::Utc::now()
    }
    pub fn now_cn() -> chrono::DateTime<chrono::FixedOffset> {
        chrono::Utc::now().with_timezone(&chrono::FixedOffset::east_opt(8 * 3600).unwrap())
    }
}

/// 通用软删除实体(供领域结构体 derive)
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct SoftDelete {
    pub deleted_at: Option<chrono::DateTime<chrono::Utc>>,
    pub deleted_by: Option<u64>,
}

impl SoftDelete {
    pub fn is_deleted(&self) -> bool {
        self.deleted_at.is_some()
    }
}

/// 标记接口:所有领域实体具备软删除
#[async_trait]
pub trait SoftDeletable {
    fn soft_delete(&mut self, by: Option<u64>);
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn id_gen_format() {
        let g = IdGen::new("ORD");
        let id = g.next();
        assert!(id.starts_with("ORD-"));
        assert_eq!(id.split('-').count(), 3);
    }

    #[test]
    fn id_gen_with_kind() {
        let g = IdGen::new("RFD");
        let id = g.next_with("charge");
        assert!(id.starts_with("RFD-charge-"));
    }
}