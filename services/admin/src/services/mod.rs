//! admin 能力域服务对象(P3)
//!
//! **变更方式**:本阶段只把"连接从哪来"换掉 —— `st.db.pool()` →
//! `st.<域>.pool()`、`st.db.pool().begin()` → `st.<域>.begin()`。
//! SQL、判定顺序、错误文案、事务边界**一字未动**。
//!
//! 收益是实打实的:`AppState` 不再持有裸 `Db`,handler 侧不再有
//! `st.db` 这条直通路径,连接必须经一个**具名能力域**取得 ——
//! 从代码上就能看出"这段 SQL 属于哪个业务域",review 时不必再通读整个 AppState。
//!
//! 尚未做完的一步(如实记录,不当成已完成):把每个域的 SQL 进一步收敛到本模块内,
//! 并把 `pool()` / `begin()` 降为 `pub(crate)`。当前它们仍是 `pub`,
//! 所以 handler 理论上仍能拿到连接 —— 只是必须点名某个能力域。
//! 那一步需要逐域搬迁 SQL,风险面比换连接来源大得多,单独排期。
//!
//! P5 已补上后一半:`pool()` / `begin()` 现为 `pub(crate)`,而 `capability/`
//! 下的切片全部在 admin 内部,于是"哪段 SQL 属于哪个域"由目录结构回答,
//! 不再依赖调用方自觉。仓库根 clippy.toml 的 `disallowed-methods`
//! (`sqlx::query*`) 把这条约定变成编译期约束:
//! SQL 只允许出现在各域的 `repository_sql.rs` 里。
//!
//! `outbox` 域与其它八个并列:它不是"业务域"而是**基础设施域** ——
//! 持有 `ServiceBase` + `RedisStream`,负责把同事务落库的 `event_outbox`
//! 行投递到 Redis Stream(D11)。

pub mod outbox;

use std::sync::Arc;

use common_app::ServiceBase;
use common_config::AppConfig;
use common_db::Db;
use common_redis::{RedisCache, RedisStream};

pub use outbox::OutboxService;

/// 一个能力域持有的连接与配置。
///
/// 字段私有:服务外部拿不到 `Db`,只能通过 `pool()` / `begin()` 拿连接句柄。
#[derive(Clone)]
pub struct DomainService {
    base: ServiceBase,
}

impl DomainService {
    /// 取连接句柄。`pub(crate)` 是刻意的:admin 的 HTTP handler 一律经
    /// `capability/<域>` 的 usecase 方法拿连接,不允许在 handler 里直接开查询。
    /// crate 之外(集成测试)也不需要它 —— 测试要么走 usecase,要么自己建 `Db`。
    pub(crate) fn pool(&self) -> &sqlx::MySqlPool {
        self.base.pool()
    }

    pub(crate) async fn begin(&self) -> AppResult2<common_db::Tx<'_>> {
        self.base.begin().await
    }

    /// 健康检查。admin 之外(如集成测试)可能需要探测能力域是否可用。
    pub async fn ping(&self) -> AppResult2<()> {
        self.base.ping().await
    }
}

type AppResult2<T> = common_error::AppResult<T>;

/// admin 的全部能力域。
///
/// 划分依据是**表归属 + 权限码归属**,不是按文件:
/// - `identity`  : 账号/角色/权限(D1 的操作权限矩阵)
/// - `finance`   : 退款/发票/结算/对账
/// - `device`    : 设备/站点/端口
/// - `order`     : 充电订单
/// - `alert`     : 告警事件与规则
/// - `webhook`   : Webhook 订阅与投递
/// - `config`    : 定价/分账/白标/公告/OTA 等配置域
/// - `cases`     : 客服工单与巡检
/// - `outbox`    : 事件 outbox 发布器(类型与前八者不同,见上)
#[derive(Clone)]
pub struct AdminServices {
    pub identity: DomainService,
    pub finance: DomainService,
    pub device: DomainService,
    pub order: DomainService,
    pub alert: DomainService,
    pub webhook: DomainService,
    pub config: DomainService,
    pub cases: DomainService,
    pub outbox: OutboxService,
}

/// 一次性装配。共用同一个连接池句柄,不会多开连接。
#[allow(clippy::too_many_arguments)]
pub fn build(
    db: Db,
    http: reqwest::Client,
    service_token: Arc<String>,
    cfg: Arc<AppConfig>,
    _redis_cache: RedisCache,
    redis_stream: RedisStream,
) -> AdminServices {
    let mk = || {
        ServiceBase::new(
            db.clone(),
            http.clone(),
            service_token.clone(),
            cfg.clone(),
        )
    };
    AdminServices {
        identity: DomainService { base: mk() },
        finance: DomainService { base: mk() },
        device: DomainService { base: mk() },
        order: DomainService { base: mk() },
        alert: DomainService { base: mk() },
        webhook: DomainService { base: mk() },
        config: DomainService { base: mk() },
        cases: DomainService { base: mk() },
        // 发布器要的是"连接 + Stream 两个句柄",所以单独构造而不是走 mk()。
        outbox: OutboxService::new(
            ServiceBase::new(db, http, service_token, cfg),
            redis_stream,
        ),
    }
}
