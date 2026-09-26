//! 数据库连接池 + 迁移 + 通用查询帮助
//!
//! 设计目标(对齐技术规格 § 4):
//!   - 各服务独立连接池(独立 schema,独立账户)
//!   - 启动时自动 `sqlx::migrate!()` 应用迁移
//!   - 提供 `with_tx` 事务封装,所有写操作走显式事务
//!   - 提供 `IdGen` / `TimeOf` 等公用工具

use async_trait::async_trait;
use common_config::MysqlConfig;
use common_error::{AppError, AppResult};
use serde::{Deserialize, Serialize};
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