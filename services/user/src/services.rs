//! user 能力域服务对象(P3)
//!
//! **变更方式**:本阶段只把"连接从哪来"换掉 —— `st.db.pool()` →
//! `st.<域>.pool()`、`st.db.pool().begin()` → `st.<域>.begin()`。
//! SQL、判定顺序、错误文案、事务边界**一字未动**。
//!
//! 收益是实打实的:`AppState` 不再持有裸 `Db`,handler 侧不再有
//! `st.db` 这条直通路径,连接必须经一个**具名能力域**取得 ——
//! 从代码上就能看出"这段 SQL 属于哪个业务域"。
//!
//! 尚未做完的一步(如实记录,不当成已完成):把每个域的 SQL 进一步收敛到本模块内,
//! 并把 `pool()` / `begin()` 降为 `pub(crate)`。当前它们仍是 `pub`,
//! 所以 handler 理论上仍能拿到连接 —— 只是必须点名某个能力域。
//! 那一步需要逐域搬迁 SQL,风险面比换连接来源大得多,单独排期。

use std::sync::Arc;

use common_app::ServiceBase;
use common_config::AppConfig;
use common_db::Db;
use common_error::AppResult;
use common_redis::{RedisCache, RedisStream};

/// 一个能力域持有的连接与配置。
///
/// 字段私有:服务外部拿不到 `Db`,只能通过 `pool()` / `begin()` 拿连接句柄。
#[derive(Clone)]
pub struct DomainService {
    base: ServiceBase,
}

impl DomainService {
    /// 从已有的 `Db` 构造。
    ///
    /// 主要给 `#[cfg(test)]` 用:测试自带一个开发库连接,需要包成能力域对象
    /// 才能调用那些已经收窄到 `&DomainService` 的入口。
    pub fn from_db(db: Db, http: reqwest::Client, service_token: Arc<String>, cfg: Arc<AppConfig>) -> Self {
        Self { base: ServiceBase::new(db, http, service_token, cfg) }
    }

    /// 从测试自建的连接池构造(见 `Db::from_pool`)。
    ///
    /// 配置走 `AppConfig::load()`,与生产同一份 —— 测试不该看到不同的
    /// service_urls 或超时设置,否则测的就不是线上行为。
    pub async fn from_pool(pool: sqlx::MySqlPool) -> common_error::AppResult<Self> {
        Ok(Self {
            base: ServiceBase::new(
                Db::from_pool(pool),
                reqwest::Client::new(),
                Arc::new(String::new()),
                Arc::new(AppConfig::load()?),
            ),
        })
    }

    pub fn pool(&self) -> &sqlx::MySqlPool {
        self.base.pool()
    }

    pub async fn begin(&self) -> AppResult<common_db::Tx<'_>> {
        self.base.begin().await
    }

    pub async fn ping(&self) -> AppResult<()> {
        self.base.ping().await
    }
}

/// user 的全部能力域。
///
/// 划分依据是**表归属 + 业务域**:
/// - `wallet`    : 钱包余额/流水/充值/退款/风控冻结
/// - `coupon`    : 优惠券模板与用户券
/// - `order`     : 充电订单与预付下单
/// - `refund`    : 退款记录、审核、执行、结果回写
/// - `invoice`   : 开票申请与复核
/// - `casework`  : 客服工单与设备巡检
/// - `station`   : 站点与端口
/// - `identity`  : 登录会话与用户资料
#[derive(Clone)]
pub struct UserServices {
    pub wallet: DomainService,
    pub coupon: DomainService,
    pub order: DomainService,
    pub refund: DomainService,
    pub invoice: DomainService,
    pub casework: DomainService,
    pub station: DomainService,
    pub identity: DomainService,
}

/// 一次性装配。共用同一个连接池句柄,不会多开连接。
#[allow(clippy::too_many_arguments)]
pub fn build(
    db: Db,
    http: reqwest::Client,
    service_token: Arc<String>,
    cfg: Arc<AppConfig>,
    _redis_cache: RedisCache,
    _redis_stream: RedisStream,
) -> UserServices {
    let mk = || {
        ServiceBase::new(
            db.clone(),
            http.clone(),
            service_token.clone(),
            cfg.clone(),
        )
    };
    UserServices {
        wallet: DomainService { base: mk() },
        coupon: DomainService { base: mk() },
        order: DomainService { base: mk() },
        refund: DomainService { base: mk() },
        invoice: DomainService { base: mk() },
        casework: DomainService { base: mk() },
        station: DomainService { base: mk() },
        identity: DomainService { base: mk() },
    }
}
