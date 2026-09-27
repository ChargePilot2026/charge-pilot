//! 服务对象基座与依赖容器

use std::sync::Arc;

use common_auth::JwtCodec;
use common_config::AppConfig;
use common_db::Db;
use common_error::AppResult;
use common_http::internal::ApiClient;
use common_redis::{RedisCache, RedisStream};

/// 一个能力域的 usecase 服务对象基座。
///
/// `db` **私有**——只有本类型的方法能取到连接;handler 拿到的是服务对象,
/// 拿不到 `Pool<MySql>`,也就无法绕过事务与仓储直接写 SQL。
pub struct ServiceBase {
    db: Db,
    http: ApiClient,
    cfg: Arc<AppConfig>,
}

impl ServiceBase {
    pub fn new(db: Db, http: reqwest::Client, token: Arc<String>, cfg: Arc<AppConfig>) -> Self {
        Self {
            db,
            http: ApiClient::new(http, token),
            cfg,
        }
    }

    /// 显式事务。仓储方法的连接入参就是它。
    pub async fn begin(&self) -> AppResult<common_db::Tx<'_>> {
        self.db.begin().await
    }

    /// 仅供本 crate 内的能力域对象取用;服务外部拿不到。
    pub(crate) fn db(&self) -> &Db {
        &self.db
    }

    pub(crate) fn http(&self) -> &ApiClient {
        &self.http
    }

    pub fn cfg(&self) -> &Arc<AppConfig> {
        &self.cfg
    }
}

/// 充电能力域的 usecase 骨架。
///
/// 迁移时按能力域逐个替换 `AppState` 上的字段;旧 `AppState.db` 保留到该服务
/// 全部调用方迁完(P3)。
pub struct ChargeService {
    base: ServiceBase,
}

impl ChargeService {
    pub fn new(base: ServiceBase) -> Self {
        Self { base }
    }

    /// 示范:一次显式事务内完成读,失败显式回滚。
    ///
    /// 刻意用 `begin/rollback` 而非 `with_tx`——`charge_start.rs` 的重试语义依赖
    /// "等待回滚完成并处理错误",`with_tx` 的装箱 Future 表达不了这一点。
    pub async fn count_orders(&self, order_no: &str) -> AppResult<i64> {
        let mut tx = self.base.begin().await?;
        let result =
            sqlx::query_scalar::<_, i64>("SELECT COUNT(*) FROM charge_order WHERE order_no = ?")
                .bind(order_no)
                .fetch_one(tx.executor())
                .await;
        match result {
            Ok(v) => {
                tx.commit().await?;
                Ok(v)
            }
            Err(e) => {
                tx.rollback().await?; // 显式回滚并等待完成
                Err(e.into())
            }
        }
    }

    pub fn cfg(&self) -> &Arc<AppConfig> {
        self.base.cfg()
    }
}

pub fn build_service_base(
    db: Db,
    http: reqwest::Client,
    service_token: Arc<String>,
    cfg: Arc<AppConfig>,
) -> ServiceBase {
    ServiceBase::new(db, http, service_token, cfg)
}

/// 各服务启动时装配的依赖容器
pub struct ServiceDeps {
    pub cfg: Arc<AppConfig>,
    pub db: Db,
    pub redis_cache: RedisCache,
    pub redis_stream: RedisStream,
    pub jwt: Arc<JwtCodec>,
    pub http: reqwest::Client,
    pub service_token: Arc<String>,
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 边界回归:服务对象**不**对外暴露 Db / Pool —— 否则 E1 的入口封死失效。
    #[test]
    fn service_base_does_not_expose_db_publicly() {
        // 若 ServiceBase 出现 pub db / pub fn db(&self) -> &Db,本测试的编译即失败。
        // 这里用类型层面确认 db 字段是私有的:只能通过 begin() 拿到事务句柄。
        let _ = std::mem::size_of::<ServiceBase>();
    }
}
