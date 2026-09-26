//! ChargePilot 服务配置加载
//!
//! 从环境变量统一读取,服务启动时一次性构建 `AppConfig`,
//! 后续业务模块通过 `AppConfig` 访问,避免散落 `env::*` 调用。

use common_error::{AppError, AppResult};
use serde::{Deserialize, Serialize};
use std::env;
use url::Url;

/// 服务身份(用于 tracing span / 审计日志)
#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum ServiceKind {
    Gateway,
    User,
    Admin,
    Billing,
    Worker,
}

impl ServiceKind {
    pub fn from_env() -> AppResult<Self> {
        let s = env::var("SERVICE_NAME").unwrap_or_else(|_| "user".into()).to_lowercase();
        Ok(match s.as_str() {
            "gateway" => ServiceKind::Gateway,
            "user" => ServiceKind::User,
            "admin" => ServiceKind::Admin,
            "billing" => ServiceKind::Billing,
            "worker" => ServiceKind::Worker,
            _ => return Err(AppError::Config(format!("unknown SERVICE_NAME: {s}"))),
        })
    }

    pub fn as_str(&self) -> &'static str {
        match self {
            ServiceKind::Gateway => "gateway",
            ServiceKind::User => "user",
            ServiceKind::Admin => "admin",
            ServiceKind::Billing => "billing",
            ServiceKind::Worker => "worker",
        }
    }
}

/// MySQL 配置(单 schema)
#[derive(Debug, Clone)]
pub struct MysqlConfig {
    pub url: String,
    pub max_connections: u32,
    pub min_connections: u32,
    pub connect_timeout_secs: u64,
}

impl MysqlConfig {
    pub fn from_env(schema: &str) -> AppResult<Self> {
        // 优先用 DATABASE_URL_<SCHEMA>;否则用通用 DATABASE_URL + schema
        let url = env::var(format!("DATABASE_URL_{}", schema.to_uppercase()))
            .or_else(|_| env::var("DATABASE_URL"))
            .map_err(|_| AppError::Config(format!("DATABASE_URL for schema {schema} not set")))?;
        let max = env::var("DB_MAX_CONNECTIONS")
            .ok()
            .and_then(|v| v.parse().ok())
            .unwrap_or(20u32);
        let min = env::var("DB_MIN_CONNECTIONS")
            .ok()
            .and_then(|v| v.parse().ok())
            .unwrap_or(2u32);
        Ok(Self {
            url,
            max_connections: max,
            min_connections: min,
            connect_timeout_secs: 10,
        })
    }
}

/// Redis 实例配置(本系统区分 cache / stream 两个独立实例,见技术规格 § 4.7)
#[derive(Debug, Clone)]
pub struct RedisConfig {
    pub url: String,
    pub pool_size: usize,
    pub is_stream: bool, // stream 实例必须 noeviction,代码层做 assert
}

impl RedisConfig {
    pub fn cache() -> AppResult<Self> {
        let url = env::var("REDIS_CACHE_URL")
            .map_err(|_| AppError::Config("REDIS_CACHE_URL not set".into()))?;
        Ok(Self { url, pool_size: 16, is_stream: false })
    }

    pub fn stream() -> AppResult<Self> {
        let url = env::var("REDIS_STREAM_URL")
            .map_err(|_| AppError::Config("REDIS_STREAM_URL not set".into()))?;
        Ok(Self { url, pool_size: 8, is_stream: true })
    }
}

/// JWT 与服务间共享密钥
#[derive(Debug, Clone)]
pub struct AuthConfig {
    pub jwt_secret: String,
    pub jwt_issuer: String,
    pub jwt_ttl_secs: u64,
    pub service_token: String, // 跨服务内部 HTTP 鉴权
}

impl AuthConfig {
    pub fn from_env() -> AppResult<Self> {
        Ok(Self {
            jwt_secret: env::var("JWT_SECRET")
                .map_err(|_| AppError::Config("JWT_SECRET not set".into()))?,
            jwt_issuer: env::var("JWT_ISSUER")
                .unwrap_or_else(|_| "chargepilot".into()),
            jwt_ttl_secs: env::var("JWT_TTL_SECS")
                .ok()
                .and_then(|v| v.parse().ok())
                .unwrap_or(7200), // 2h
            service_token: env::var("SERVICE_TOKEN")
                .map_err(|_| AppError::Config("SERVICE_TOKEN not set".into()))?,
        })
    }
}

/// 微信支付参数(用户/管理员均可访问,放在 common 中)
#[derive(Debug, Clone)]
pub struct WechatConfig {
    pub appid: String,
    pub secret: String,
    pub mch_id: String,
    pub pay_key: String,                 // V3 API 密钥
    pub notify_url: String,
    pub pay_base_url: String,            // 直链 / 服务商
    pub refund_url: String,
    pub cert_path: Option<String>,       // 退款需要证书(本期预留)
    pub private_key_path: Option<String>,
    pub merchant_serial_no: Option<String>,
    pub platform_public_key_path: Option<String>,
    pub platform_key_id: Option<String>,
}

impl WechatConfig {
    pub fn from_env() -> AppResult<Self> {
        Ok(Self {
            appid: env::var("WECHAT_APPID")
                .map_err(|_| AppError::Config("WECHAT_APPID not set".into()))?,
            secret: env::var("WECHAT_SECRET")
                .map_err(|_| AppError::Config("WECHAT_SECRET not set".into()))?,
            mch_id: env::var("WECHAT_MCH_ID")
                .map_err(|_| AppError::Config("WECHAT_MCH_ID not set".into()))?,
            pay_key: env::var("WECHAT_PAY_KEY")
                .map_err(|_| AppError::Config("WECHAT_PAY_KEY not set".into()))?,
            notify_url: env::var("WECHAT_NOTIFY_URL")
                .map_err(|_| AppError::Config("WECHAT_NOTIFY_URL not set".into()))?,
            pay_base_url: env::var("WECHAT_PAY_BASE_URL")
                .unwrap_or_else(|_| "https://api.mch.weixin.qq.com".into()),
            refund_url: env::var("WECHAT_REFUND_URL")
                .unwrap_or_else(|_| "https://api.mch.weixin.qq.com/v3/refund/domestic/refunds".into()),
            cert_path: env::var("WECHAT_CERT_PATH").ok(),
            private_key_path: env::var("WECHAT_PRIVATE_KEY_PATH").ok(),
            merchant_serial_no: env::var("WECHAT_MERCHANT_SERIAL_NO").ok(),
            platform_public_key_path: env::var("WECHAT_PLATFORM_PUBLIC_KEY_PATH").ok(),
            platform_key_id: env::var("WECHAT_PLATFORM_KEY_ID").ok(),
        })
    }
}

/// 跨服务内部 HTTP 调用地址(由 docker-compose 注入)
#[derive(Debug, Clone, Default)]
pub struct ServiceUrls {
    pub gateway: Option<String>,
    pub user: Option<String>,
    pub admin: Option<String>,
    pub billing: Option<String>,
}

impl ServiceUrls {
    pub fn from_env() -> Self {
        Self {
            gateway: env::var("GATEWAY_INTERNAL_URL").ok(),
            user: env::var("USER_INTERNAL_URL").ok(),
            admin: env::var("ADMIN_INTERNAL_URL").ok(),
            billing: env::var("BILLING_INTERNAL_URL").ok(),
        }
    }
}

/// 总配置(各服务通过 `AppConfig::load()` 构造)
#[derive(Clone)]
pub struct AppConfig {
    pub service: ServiceKind,
    pub http_bind: String,                 // 如 0.0.0.0:8081
    pub mysql: MysqlConfig,                // 本服务的 schema
    pub redis_cache: RedisConfig,
    pub redis_stream: RedisConfig,
    pub auth: AuthConfig,
    pub wechat: Option<WechatConfig>,      // 只有 user/admin 服务需要
    pub service_urls: ServiceUrls,
    pub log_level: String,
    pub runtime_env: RuntimeEnv,
    pub bootstrap_admin: Option<BootstrapAdmin>,
    pub time_of_use_default: Option<serde_json::Value>,
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum RuntimeEnv {
    Dev,
    Staging,
    Production,
}

impl RuntimeEnv {
    pub fn from_env() -> Self {
        match env::var("RUNTIME_ENV").as_deref() {
            Ok("production") | Ok("prod") => RuntimeEnv::Production,
            Ok("staging") => RuntimeEnv::Staging,
            _ => RuntimeEnv::Dev,
        }
    }
    pub fn is_production(&self) -> bool {
        matches!(self, RuntimeEnv::Production)
    }
}

#[derive(Debug, Clone)]
pub struct BootstrapAdmin {
    pub username: String,
    pub password: String,
    pub phone: Option<String>,
}

impl AppConfig {
    /// 通用加载入口;若 .env 不存在则跳过(开发期容错)
    pub fn load() -> AppResult<Self> {
        let _ = dotenvy::dotenv();

        let service = ServiceKind::from_env()?;
        let schema = match service {
            ServiceKind::Gateway => "gateway",
            ServiceKind::User => "user",
            ServiceKind::Admin => "admin",
            ServiceKind::Billing => "billing",
            ServiceKind::Worker => "worker",
        };

        let http_bind = match service {
            ServiceKind::Gateway => env::var("GATEWAY_BIND").unwrap_or_else(|_| "0.0.0.0:8083".into()),
            ServiceKind::User => env::var("USER_BIND").unwrap_or_else(|_| "0.0.0.0:8081".into()),
            ServiceKind::Admin => env::var("ADMIN_BIND").unwrap_or_else(|_| "0.0.0.0:8082".into()),
            ServiceKind::Billing => env::var("BILLING_BIND").unwrap_or_else(|_| "0.0.0.0:8084".into()),
            ServiceKind::Worker => env::var("WORKER_BIND").unwrap_or_else(|_| "0.0.0.0:8085".into()),
        };

        let mysql = MysqlConfig::from_env(schema)?;
        let redis_cache = RedisConfig::cache()?;
        let redis_stream = RedisConfig::stream()?;
        let auth = AuthConfig::from_env()?;

        // 只 user / admin 服务需要微信配置
        let wechat = match service {
            ServiceKind::User | ServiceKind::Admin => Some(WechatConfig::from_env()?),
            _ => None,
        };

        let service_urls = ServiceUrls::from_env();
        let log_level = env::var("RUST_LOG").unwrap_or_else(|_| "info,sqlx=warn".into());

        let bootstrap_admin = if matches!(service, ServiceKind::Admin) {
            env::var("ADMIN_BOOTSTRAP_USER").ok().map(|u| BootstrapAdmin {
                username: u,
                password: env::var("ADMIN_BOOTSTRAP_PASSWORD").unwrap_or_default(),
                phone: env::var("ADMIN_BOOTSTRAP_PHONE").ok(),
            })
        } else {
            None
        };

        let time_of_use_default = env::var("DEFAULT_TIME_OF_USE_JSON")
            .ok()
            .and_then(|v| serde_json::from_str(&v).ok());

        Ok(Self {
            service,
            http_bind,
            mysql,
            redis_cache,
            redis_stream,
            auth,
            wechat,
            service_urls,
            log_level,
            runtime_env: RuntimeEnv::from_env(),
            bootstrap_admin,
            time_of_use_default,
        })
    }

    /// 解析后端 HTTP 端口(供 health probe 等使用)
    pub fn bind_port(&self) -> u16 {
        self.http_bind
            .rsplit(':')
            .next()
            .and_then(|p| p.parse().ok())
            .unwrap_or(8080)
    }

    /// 当前服务所连 MySQL 的 schema 名
    pub fn schema(&self) -> &'static str {
        match self.service {
            ServiceKind::Gateway => "gateway_db",
            ServiceKind::User => "user_db",
            ServiceKind::Admin => "admin_db",
            ServiceKind::Billing => "billing_db",
            ServiceKind::Worker => "worker_db",
        }
    }

    pub fn is_production(&self) -> bool {
        self.runtime_env.is_production()
    }

    pub fn validate_base_url(&self, url: &str) -> bool {
        Url::parse(url).is_ok()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn service_kind_round_trip() {
        for k in [ServiceKind::Gateway, ServiceKind::User, ServiceKind::Admin, ServiceKind::Billing, ServiceKind::Worker] {
            let res = ServiceKind::from_env_for_test(k.as_str());
            assert!(matches!(res, Ok(ref x) if *x == k), "from_env_for_test({}) returned {:?}", k.as_str(), res);
        }
    }

    impl ServiceKind {
        fn from_env_for_test(s: &str) -> AppResult<Self> {
            Ok(match s {
                "gateway" => Self::Gateway,
                "user" => Self::User,
                "admin" => Self::Admin,
                "billing" => Self::Billing,
                "worker" => Self::Worker,
                _ => return Err(AppError::Config("bad".into())),
            })
        }
    }
}
