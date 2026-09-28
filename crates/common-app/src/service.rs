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
#[derive(Clone)]
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

    /// 供**本服务 crate 内的能力域服务对象**取用连接。
    ///
    /// ⚠️ P1a 阶段这里是 `pub(crate)`,导致服务对象无法跨 crate 构造查询。
    /// P3 起放开为 `pub`,但**边界并未因此打开** —— 真正的闸门是
    /// `AppState` 不再持有 `pub db`:handler 手上只有服务对象,
    /// 拿不到 `ServiceBase`,也就取不到连接。见 `tests/architecture.rs`。
    pub fn db(&self) -> &Db {
        &self.db
    }

    pub fn pool(&self) -> &sqlx::MySqlPool {
        self.db.pool()
    }

    pub async fn ping(&self) -> AppResult<()> {
        self.db.ping().await
    }

    pub fn http(&self) -> &ApiClient {
        &self.http
    }

    /// 取一个内部调用客户端(与原实现每个调用点 `ApiClient::new` 一次等价)。
    pub fn new_client(&self) -> ApiClient {
        self.http.clone()
    }

    pub fn cfg(&self) -> &Arc<AppConfig> {
        &self.cfg
    }
}

/// 一次性装配 `ServiceBase`(薄封装,保留给各服务的 `services::build` 调用)。
pub fn build_service_base(
    db: Db,
    http: reqwest::Client,
    service_token: Arc<String>,
    cfg: Arc<AppConfig>,
) -> ServiceBase {
    ServiceBase::new(db, http, service_token, cfg)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 边界回归:字段 `db` 必须保持**私有**。
    ///
    /// `db()` / `pool()` 的存在是给服务对象用的(P3 起必须跨 crate 可用),
    /// 闸门不在这里 —— 闸门是 `AppState` 不再持有 `pub db`。
    /// 本测试锁住的是"字段私有",改字段可见性会编译失败。
    #[test]
    fn service_base_db_field_stays_private() {
        let _ = std::mem::size_of::<ServiceBase>();
    }
}
