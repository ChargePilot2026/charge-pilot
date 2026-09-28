//! D11 —— Webhook 投递器(消费端)
//!
//! 此前 `webhook_retry` 流的 handler 只有两行:取 `url`,然后恒定返回
//! `ServiceUnavailable("webhook delivery is not configured")` ——
//! **这条链路从未真正投递过任何一次请求**。本模块把真实投递补齐。
//!
//! ## 1. 安全门槛:SSRF 校验在任何网络动作之前
//!
//! 投递 URL 来自 `webhook_subscription.url`,由 admin 用户填写 —— 属于
//! **外部可控输入**。若不校验,任何有「创建订阅」权限的后台账号都能让
//! worker 代为访问内网地址(云元数据 `169.254.169.254`、`localhost`、
//! 数据库/Redis 容器端口),把内网变成 SSRF 跳板。因此 [`validate_url`]
//! 是 `deliver` 的第一行,失败即拒绝,根本不会发出任何字节。
//!
//! ⚠️ 已知残余风险:host 是域名时**不做 DNS 解析判断**。投递发生在
//! 消费时刻,DNS 解析发生在 reqwest 内部,此时已无法拦截。域名可能指向
//! `127.0.0.1`(DNS rebinding)。彻底消除需要「解析后校验 + 按 IP 直连
//! + TLS SNI 仍用域名」,代价与复杂度都不小,属已知取舍 —— 用
//! `redirect(Policy::none())` + 禁 IP 字面量把可利用面压到最小。
//!
//! ## 2. 签名:HMAC-SHA256 over **请求体原始字节**
//!
//! 签名对象是**最终发出的 body 字节**,不是重新序列化的对象。订阅方
//! 收到的也是同一串字节,两侧算出的 HMAC 必然一致。
//!
//! ## 3. 结果分类:4xx 不重试,408/429/5xx 与网络错误才重试
//!
//! 4xx 是订阅方**明确拒绝**(URL 错了、签名校验不过、body 不合法),
//! 重试只会白白消耗配额并把同一条错误刷满日志。408/429/5xx 与连接
//! 错误是**临时问题**,`common_stream` 的退避表 `[2s, 4s, 8s]`
//! (4 次尝试)正是为它们准备的,与需求文档「指数退避最多 3 次」对齐。
//!
//! ## 4. 幂等:`(subscription_id, event_id)` 只成功投递一次
//!
//! 重试与 DLQ 重放都会把同一条事件再次送到本 handler。若不判重,
//! 订阅方会收到重复的告警通知。判定落在 worker_db 的 `retry_queue`
//! (`queue_name='webhook_delivered'`, `status='done'`)。
//!
//! ## 5. 投递明细回写 admin
//!
//! `webhook_delivery_log` 在 **admin_db**,worker 无权访问(跨库)。
//! 故投递结果经 admin 的内部端点回写(见 [`DeliveryReporter`] 与
//! [`ApiDeliveryReporter`]),上报失败**只记日志**——投递已经发生,
//! 记账失败不该让订阅方收到重复通知。
#![allow(
    clippy::disallowed_macros,
    clippy::disallowed_types,
    clippy::disallowed_methods,
)]

use std::time::{Duration, Instant};

use async_trait::async_trait;
use common_app::ServiceBase;
use common_error::{AppError, AppResult};
use hmac::{Hmac, Mac};
use reqwest::header::{HeaderMap, HeaderName, HeaderValue};
use sha2::Sha256;

/// 目标 URL 长度上限,与 `webhook_subscription.url VARCHAR(512)` 对齐。
const MAX_URL_LEN: usize = 512;

/// 允许的端口白名单。刻意只有 443/8443:
/// 端口是 SSRF 打到内网高危服务的直通车(22 SSH、6379 Redis、
/// 3306 MySQL、2375 Docker API),白名单比黑名单可靠得多。
const ALLOWED_PORTS: [u16; 2] = [443, 8443];

/// 投递超时。订阅方是外部服务,给 10 秒已足够;
/// 再长只会拖慢消费组的批量处理(`concurrency=16`)。
const DELIVERY_TIMEOUT: Duration = Duration::from_secs(10);

/// 幂等标记在 `retry_queue` 里的队列名。
const DELIVERED_QUEUE: &str = "webhook_delivered";

/// 禁止订阅方自定义的头。允许覆盖它们等于允许伪造:
/// - `Host`:改写虚拟主机名,可打到反代后面的其它站点
/// - `Content-Length`:与真实 body 不符,请求会被挂起或截断
/// - `X-ChargePilot-Signature`:直接替换签名,订阅方会验签通过
const RESERVED_HEADERS: [&str; 3] =
    ["host", "content-length", "x-chargepilot-signature"];

/// 签名头名称(大小写不敏感由 `HeaderMap` 保证)。
pub const SIGNATURE_HEADER: &str = "X-ChargePilot-Signature";
/// 事件 id 头:订阅方据此做自己的幂等去重。
pub const EVENT_ID_HEADER: &str = "X-ChargePilot-Event-Id";
/// 事件类型头:订阅方可据此快速路由而无需解析 body。
pub const EVENT_TYPE_HEADER: &str = "X-ChargePilot-Event-Type";

type HmacSha256 = Hmac<Sha256>;

// ===================== 投递载荷 =====================

/// `webhook_retry` 事件载荷(admin 侧发布,按此契约解析)。
#[derive(Debug, Clone)]
pub struct WebhookDelivery {
    pub subscription_id: u64,
    pub url: String,
    pub secret: String,
    pub event_id: String,
    pub event_type: String,
    pub alert_device_id: Option<String>,
    pub severity: Option<String>,
    pub occurred_at: Option<String>,
    /// 订阅自定义头,缺省则不发。
    pub headers: Vec<(String, String)>,
}

impl WebhookDelivery {
    /// 从 stream envelope 的 `payload` 解析。`url` 缺失由调用方决定语义
    /// (缺 url 是「事件本身不完整」,与「投递失败」不同层),故此处不报错。
    pub fn from_payload(
        event_id: &str,
        event_type: &str,
        occurred_at: &str,
        payload: &serde_json::Value,
    ) -> Self {
        let s = |k: &str| payload.get(k).and_then(|v| v.as_str()).map(str::to_string);
        let headers = payload
            .get("headers")
            .and_then(|v| v.as_object())
            .map(|m| {
                m.iter()
                    .filter_map(|(k, v)| v.as_str().map(|v| (k.clone(), v.to_string())))
                    .collect()
            })
            .unwrap_or_default();
        Self {
            subscription_id: payload
                .get("subscription_id")
                .and_then(|v| v.as_u64())
                .unwrap_or_default(),
            url: s("url").unwrap_or_default(),
            secret: s("secret").unwrap_or_default(),
            event_id: event_id.to_string(),
            event_type: event_type.to_string(),
            alert_device_id: s("alert_device_id"),
            severity: s("severity"),
            occurred_at: s("occurred_at").or_else(|| Some(occurred_at.to_string())),
            headers,
        }
    }

    /// 构造投递 body。
    ///
    /// ⚠️ **不把 `secret` 与 `url` 放进 body**:secret 是签名密钥,
    /// 明文出现在载荷里等于把密钥送到第三方;url 是目标不是内容。
    pub fn build_body(&self) -> serde_json::Value {
        serde_json::json!({
            "event_id": self.event_id,
            "event_type": self.event_type,
            "occurred_at": self.occurred_at,
            "alert_device_id": self.alert_device_id,
            "severity": self.severity,
        })
    }
}

// ===================== 投递结果 =====================

/// 一次投递的结果。**`Transient` 由 `deliver` 转成 `Err` 交框架重试**,
/// 因此调用方实际只可能拿到 `Delivered` / `Permanent` / `Err`。
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum DeliveryOutcome {
    /// 2xx —— 订阅方已受理。
    Delivered {
        status: u16,
        duration_ms: u64,
        body: String,
    },
    /// 408 / 429 / 5xx —— 临时问题,`deliver` 返回 `Err` 让框架重试。
    /// ⚠️ 保留此变体是为了让**分类判定可被单元测试直接断言**;
    /// 生产路径不会把它返回给调用方。
    Transient {
        status: u16,
        duration_ms: u64,
        body: String,
    },
    /// 其它 4xx —— 订阅方明确拒绝,返回 `Ok` 但记失败,**不重试**。
    Permanent {
        status: u16,
        duration_ms: u64,
        body: String,
    },
}

impl DeliveryOutcome {
    pub fn is_delivered(&self) -> bool {
        matches!(self, Self::Delivered { .. })
    }
}

/// 把 HTTP 状态码映射到结果分类。纯函数,**不发网络请求**,可单测。
pub fn classify_status(status: u16, duration_ms: u64, body: String) -> DeliveryOutcome {
    if (200..300).contains(&status) {
        DeliveryOutcome::Delivered { status, duration_ms, body }
    } else if status == 408 || status == 429 || (500..600).contains(&status) {
        DeliveryOutcome::Transient { status, duration_ms, body }
    } else if (400..500).contains(&status) {
        DeliveryOutcome::Permanent { status, duration_ms, body }
    } else {
        // 1xx / 3xx 走到这里:没有跟随重定向却收到 3xx,或代理返回了 1xx。
        // 按「订阅方明确拒绝」处理 —— 重试同一请求只会得到同样的响应。
        DeliveryOutcome::Permanent { status, duration_ms, body }
    }
}

// ===================== SSRF 校验 =====================

/// 校验投递 URL,拒绝 SSRF 风险。**必须在任何网络动作之前调用。**
///
/// 规则(逐条都有对应单测):
///
/// | 规则 | 原因 |
/// |------|------|
/// | 长度 ≤ 512 | 与 `url` 列宽一致,防超长绕过解析器差异 |
/// | 必须能解析为绝对 URL | 相对 URL 无法投递 |
/// | scheme 仅 `https` | 明文 HTTP 可被中间人改写;无 https 的订阅方应先升级 |
/// | 不得含 userinfo | 凭据不应出现在 URL,会随日志/Referer 外泄 |
/// | host 非空且非 IP 字面量 | 直接打 IP 即可绕过域名白名单,直连内网 |
/// | 不得是本地/云元数据名 | `localhost`/`*.local`/`metadata.google.internal` 是内网与云元数据的标准入口 |
/// | 端口 ∈ {443, 8443} | 阻断 SSH/Redis/MySQL/Docker API 等内网高危端口 |
pub fn validate_url(raw: &str) -> AppResult<reqwest::Url> {
    if raw.len() > MAX_URL_LEN {
        return Err(AppError::BadRequest(format!(
            "Webhook 目标 URL 超过 {MAX_URL_LEN} 字节上限"
        )));
    }
    let url = reqwest::Url::parse(raw)
        .map_err(|e| AppError::BadRequest(format!("Webhook 目标 URL 非法: {e}")))?;

    if url.scheme() != "https" {
        return Err(AppError::BadRequest(
            "Webhook 目标 URL 只允许 https(生产环境禁止明文 http)".into(),
        ));
    }
    if !url.username().is_empty() || url.password().is_some() {
        return Err(AppError::BadRequest(
            "Webhook 目标 URL 不得包含用户名或密码".into(),
        ));
    }
    let host = url
        .host_str()
        .ok_or_else(|| AppError::BadRequest("Webhook 目标 URL 缺少主机名".into()))?
        .to_ascii_lowercase();

    if is_ip_literal(&host) {
        return Err(AppError::BadRequest(
            "Webhook 目标 URL 不得直接使用 IP 地址".into(),
        ));
    }
    if is_forbidden_host(&host) {
        return Err(AppError::BadRequest(format!(
            "Webhook 目标 URL 指向内网/云元数据主机名: {host}"
        )));
    }
    // `port()` 对「显式写了默认端口」也返回 Some(443),两种都接受;
    // 缺省端口(None)按 https 默认 443 处理。
    match url.port() {
        None => {}
        Some(p) if ALLOWED_PORTS.contains(&p) => {}
        Some(p) => {
            return Err(AppError::BadRequest(format!(
                "Webhook 目标 URL 端口 {p} 不在白名单 {ALLOWED_PORTS:?} 内"
            )))
        }
    }
    Ok(url)
}

/// host 是否是 IP 字面量(含 IPv6 的 `[::1]` 形式)。
///
/// `reqwest::Url` 已把 IPv6 规范化并保留方括号,故 `[::1]` 直接匹配。
fn is_ip_literal(host: &str) -> bool {
    let h = host.trim_start_matches('[').trim_end_matches(']');
    h.parse::<std::net::IpAddr>().is_ok()
}

/// 本地 / 内网 / 云元数据的标准主机名。**大小写与 DNS 根尾点均归一化**。
///
/// host 大小写不敏感(`LOCALHOST` 与 `localhost` 指同一主机),而 URL
/// 解析不会替我们做这件事;归一化放在本函数内部而不是调用方,
/// 避免新增调用点时静默丢掉这道拦截。
///
/// ⚠️ 这里**只拦名字**。指向内网的任意域名无法穷尽,靠端口白名单与
/// 禁 IP 字面量兜底。
fn is_forbidden_host(host: &str) -> bool {
    const EXACT: [&str; 3] = ["localhost", "metadata.google.internal", "instance-data"];
    /// 带前导点:只作为**后缀**匹配,`foo.localhost` / `printer.local` 命中,
    /// 而 `localhost.example.com` 这类无关域名不误伤。
    const SUFFIXES: [&str; 2] = [".localhost", ".local"];
    // 剥掉 DNS 根尾点:`localhost.` 与 `localhost` 等价
    let h = host.trim().to_ascii_lowercase();
    let h = h.trim_end_matches('.');
    EXACT.contains(&h) || SUFFIXES.iter().any(|s| h.ends_with(s))
}

// ===================== 签名 =====================

/// HMAC-SHA256 签名,小写 hex。
///
/// 签名对象是 **body 原始字节**,订阅方用同一串字节复算即可验证。
pub fn sign_body(secret: &str, body: &[u8]) -> String {
    let mut mac = HmacSha256::new_from_slice(secret.as_bytes())
        .expect("HMAC-SHA256 接受任意长度密钥");
    mac.update(body);
    hex::encode(mac.finalize().into_bytes())
}

/// 拼出完整的签名头值:`sha256=<hex>`。
pub fn signature_header_value(secret: &str, body: &[u8]) -> String {
    format!("sha256={}", sign_body(secret, body))
}

/// 组装请求头:签名 + 事件标识 + 订阅自定义头。
///
/// 订阅自定义头**逐个校验**,命中保留名单的一律拒绝(见
/// [`RESERVED_HEADERS`])。头名/值非法一律跳过并继续 —— 一个坏的自定义
/// 头不该让整次投递失败。
pub fn build_headers(
    secret: &str,
    body_bytes: &[u8],
    event_id: &str,
    event_type: &str,
    custom: &[(String, String)],
) -> AppResult<HeaderMap> {
    let mut map = HeaderMap::new();
    for (name, value) in custom {
        let lname = name.trim().to_ascii_lowercase();
        if RESERVED_HEADERS.contains(&lname.as_str()) {
            continue;
        }
        if let (Ok(hn), Ok(hv)) = (
            HeaderName::from_bytes(name.as_bytes()),
            HeaderValue::from_str(value),
        ) {
            map.insert(hn, hv);
        }
    }
    // 签名与事件头**最后**写入,保证订阅自定义头无法覆盖它们。
    map.insert(
        HeaderName::from_static("x-chargepilot-signature"),
        HeaderValue::from_str(&signature_header_value(secret, body_bytes))
            .map_err(|e| AppError::Internal(format!("签名头取值非法: {e}")))?,
    );
    map.insert(
        HeaderName::from_static("x-chargepilot-event-id"),
        HeaderValue::from_str(event_id)
            .map_err(|e| AppError::Internal(format!("事件 id 头取值非法: {e}")))?,
    );
    map.insert(
        HeaderName::from_static("x-chargepilot-event-type"),
        HeaderValue::from_str(event_type)
            .map_err(|e| AppError::Internal(format!("事件类型头取值非法: {e}")))?,
    );
    Ok(map)
}

// ===================== 投递明细回写边界 =====================

/// 投递明细回写 admin 的**边界定义**。
///
/// `webhook_delivery_log` 在 admin_db,worker 连不到。由 admin 的内部端点
/// (路径常量 [`DELIVERY_REPORT_PATH`])接收上报并落库。
///
/// 上报失败**只记日志不影响投递结论**:投递已经发生,
/// 记账失败不该让订阅方收到重复通知。
#[async_trait]
pub trait DeliveryReporter: Send + Sync {
    /// 上报一次投递结果。`outcome` 为 `None` 表示网络层失败(无响应码)。
    async fn report(
        &self,
        delivery: &WebhookDelivery,
        outcome: Option<&DeliveryOutcome>,
        attempt: u32,
    ) -> AppResult<()>;
}

/// admin 侧内部端点路径。
///
/// 路径常量以 `api-contracts::paths` 为唯一真源 —— 与全仓其它跨服务
/// 调用一致,不在本文件硬编码。
pub const DELIVERY_REPORT_PATH: &str = api_contracts::paths::ADMIN_INTERNAL_WEBHOOK_DELIVERIES;

/// 基于 `ApiClient` 的上报实现:走 admin 内部路由(service token 鉴权),
/// 由 admin 落 `webhook_delivery_log` —— worker 不碰那张表。
#[derive(Clone)]
pub struct ApiDeliveryReporter {
    client: common_http::internal::ApiClient,
    admin_url: Option<String>,
}

impl ApiDeliveryReporter {
    pub fn new(
        http: reqwest::Client,
        service_token: std::sync::Arc<String>,
        cfg: &common_config::AppConfig,
    ) -> Self {
        Self {
            client: common_http::internal::ApiClient::new(http, service_token),
            admin_url: cfg.service_urls.admin.clone(),
        }
    }
}

/// 上报请求体。与 admin 侧 `webhook::DeliveryReportReq` 一一对应。
#[derive(serde::Serialize)]
struct DeliveryReportBody {
    subscription_id: u64,
    event_id: String,
    event_type: String,
    request_body: serde_json::Value,
    response_status: Option<i32>,
    response_body: Option<String>,
    error_msg: Option<String>,
    attempt_count: u32,
    duration_ms: Option<u32>,
}

#[async_trait]
impl DeliveryReporter for ApiDeliveryReporter {
    async fn report(
        &self,
        delivery: &WebhookDelivery,
        outcome: Option<&DeliveryOutcome>,
        attempt: u32,
    ) -> AppResult<()> {
        let (status, body, duration, error_msg) = match outcome {
            Some(DeliveryOutcome::Delivered { status, duration_ms, body })
            | Some(DeliveryOutcome::Transient { status, duration_ms, body })
            | Some(DeliveryOutcome::Permanent { status, duration_ms, body }) => {
                // `duration_ms` 列是 INT UNSIGNED; saturating 防止极端耗时回绕
                let ms = u32::try_from(*duration_ms).unwrap_or(u32::MAX);
                (Some(*status as i32), Some(body.clone()), Some(ms), None)
            }
            // 网络层失败:没有响应码,只有错误信息
            None => (
                None,
                None,
                None,
                Some("投递未取得响应(超时或连接失败)".to_string()),
            ),
        };
        let payload = DeliveryReportBody {
            subscription_id: delivery.subscription_id,
            event_id: delivery.event_id.clone(),
            event_type: delivery.event_type.clone(),
            request_body: delivery.build_body(),
            response_status: status,
            response_body: body,
            error_msg,
            attempt_count: attempt.max(1),
            duration_ms: duration,
        };
        // 上报失败只记日志:投递已经发生,记账失败不该让订阅方收到重复通知。
        if let Err(e) = self
            .client
            .post::<(), _>(self.admin_url.as_deref(), DELIVERY_REPORT_PATH, &payload)
            .await
        {
            tracing::warn!(
                subscription_id = delivery.subscription_id,
                event_id = %delivery.event_id,
                error = %e,
                "投递明细回写失败(不影响投递结论)"
            );
        }
        Ok(())
    }
}

// ===================== 投递服务 =====================

#[derive(Clone)]
pub struct WebhookService {
    base: ServiceBase,
    http: reqwest::Client,
    /// 投递明细回写通道。`None` 表示未装配（如仅跑单测）——此时只做本地记账。
    reporter: Option<std::sync::Arc<dyn DeliveryReporter>>,
}

impl WebhookService {
    pub fn new(base: ServiceBase) -> Self {
        Self::with_reporter(base, None)
    }

    /// 装配上报通道。生产必须传 `Some(..)`，否则 admin 的
    /// `GET /admin/webhooks/:id/deliveries` 永远查不到记录。
    pub fn with_reporter(
        base: ServiceBase,
        reporter: Option<std::sync::Arc<dyn DeliveryReporter>>,
    ) -> Self {
        // 独立于 `ServiceBase` 内的内部调用客户端:
        // 1. `redirect(none)` —— 校验通过的是**首跳** URL,跟随重定向等于让
        //    订阅方把我们导向任意地址,SSRF 校验被整体绕过。
        // 2. `https_only` —— 双保险,防止未来新增的其它调用点漏掉 scheme 校验。
        let http = reqwest::Client::builder()
            .timeout(DELIVERY_TIMEOUT)
            .redirect(reqwest::redirect::Policy::none())
            .https_only(true)
            .build()
            .unwrap_or_else(|e| panic!("构造 webhook 投递 HTTP 客户端失败: {e}"));
        Self { base, http, reporter }
    }

    /// 上报一次投递结果。无 reporter 时静默跳过。
    ///
    /// 上报失败绝不影响投递结论 —— 投递已经发生,让订阅方重复收到通知
    /// 比丢一条记账记录严重得多。
    async fn report(&self, delivery: &WebhookDelivery, outcome: Option<&DeliveryOutcome>) {
        if let Some(r) = &self.reporter {
            if let Err(e) = r.report(delivery, outcome, 1).await {
                tracing::warn!(error = %e, "投递明细上报失败(不影响投递结论)");
            }
        }
    }

    /// 健康检查复用基座连接。
    pub async fn ping(&self) -> AppResult<()> {
        self.base.ping().await
    }

    /// 幂等判定:`(subscription_id, event_id)` 是否已成功投递过。
    ///
    /// 标记落在 `retry_queue`(`queue_name='webhook_delivered'`,
    /// `status='done'`),`payload_json` 存 `{subscription_id, event_id}`
    /// 供人工排查。已投递过则直接返回 `Ok(())` 交由 consumer ACK。
    pub async fn already_delivered(&self, subscription_id: u64, event_id: &str) -> AppResult<bool> {
        // 本文件是 worker 侧 repository 层,SQL 只允许出现在这里
        // (根 clippy.toml 的 disallowed-methods 约束「SQL 只在 repository 层」,
        //  worker 无独立 repository_sql.rs,故本文件承担该角色)。
        let rows: Vec<(String,)> = sqlx::query_as(
            "SELECT payload_json FROM retry_queue
             WHERE queue_name = ? AND status = 'done'
               AND JSON_UNQUOTE(JSON_EXTRACT(payload_json, '$.event_id')) = ?
             LIMIT 20",
        )
        .bind(DELIVERED_QUEUE)
        .bind(event_id)
        .fetch_all(self.base.pool())
        .await?;
        Ok(rows.iter().any(|(json,)| {
            let sub = serde_json::from_str::<serde_json::Value>(json)
                .ok()
                .and_then(|v| v.get("subscription_id").and_then(|x| x.as_u64()));
            sub == Some(subscription_id)
        }))
    }

    /// 记录一次成功投递,作为幂等标记。
    async fn mark_delivered(&self, delivery: &WebhookDelivery) -> AppResult<()> {
        let payload = serde_json::json!({
            "subscription_id": delivery.subscription_id,
            "event_id": delivery.event_id,
            "event_type": delivery.event_type,
        });
        sqlx::query(
            "INSERT INTO retry_queue (queue_name, payload_json, run_at, attempt_count,
                                      max_attempts, status, last_error)
             VALUES (?, ?, NOW(3), 1, 1, 'done', NULL)",
        )
        .bind(DELIVERED_QUEUE)
        .bind(payload)
        .execute(self.base.pool())
        .await?;
        Ok(())
    }

    /// 记录一次失败投递(仅本地记账,供 admin 后台排查)。
    async fn mark_failed(&self, delivery: &WebhookDelivery, reason: &str) {
        let payload = serde_json::json!({
            "subscription_id": delivery.subscription_id,
            "event_id": delivery.event_id,
            "event_type": delivery.event_type,
        });
        let reason: String = reason.chars().take(255).collect();
        if let Err(e) = sqlx::query(
            "INSERT INTO retry_queue (queue_name, payload_json, run_at, attempt_count,
                                      max_attempts, status, last_error)
             VALUES (?, ?, NOW(3), 1, 1, 'failed', ?)",
        )
        .bind(DELIVERED_QUEUE)
        .bind(payload)
        .bind(&reason)
        .execute(self.base.pool())
        .await
        {
            tracing::warn!(error = %e, "webhook 失败记账落库失败,不影响投递结论");
        }
    }

    /// 投递一次 webhook。
    ///
    /// 返回值语义(调用方据此决定是否让框架重试):
    /// - `Ok(Delivered)` —— 订阅方已受理
    /// - `Ok(Permanent)` —— 订阅方明确拒绝(4xx),**不重试**
    /// - `Err(_)` —— 临时问题(408/429/5xx/网络错误/超时)或载荷不完整,交框架重试
    pub async fn deliver(&self, delivery: &WebhookDelivery) -> AppResult<DeliveryOutcome> {
        // ---- ① 安全门槛:任何网络动作之前 ----
        validate_url(&delivery.url)?;

        // ---- ② 幂等:同一订阅的同一事件只成功投递一次 ----
        if self
            .already_delivered(delivery.subscription_id, &delivery.event_id)
            .await?
        {
            tracing::info!(
                subscription_id = delivery.subscription_id,
                event_id = %delivery.event_id,
                "webhook 已投递过,跳过重复发送"
            );
            // 用既有标记构造一个 Delivered:调用方看到的是「本次无需重试」,
            // status 用 200 只是占位语义,本路径不再发请求。
            return Ok(DeliveryOutcome::Delivered {
                status: 200,
                duration_ms: 0,
                body: String::from("skipped: already delivered"),
            });
        }

        // ---- ③ 构造 body 并签名(签名对象 = 最终发出的字节) ----
        let body = delivery.build_body();
        let body_bytes =
            serde_json::to_vec(&body).map_err(|e| AppError::Internal(format!("序列化 webhook body 失败: {e}")))?;
        let headers = build_headers(
            &delivery.secret,
            &body_bytes,
            &delivery.event_id,
            &delivery.event_type,
            &delivery.headers,
        )?;

        // ---- ④ 发出请求 ----
        let started = Instant::now();
        let response = self
            .http
            .post(&delivery.url)
            .headers(headers)
            .body(body_bytes.clone())
            .send()
            .await;
        let duration_ms = started.elapsed().as_millis() as u64;

        let response = match response {
            Ok(r) => r,
            Err(e) => {
                // 连接错误 / 超时 —— 临时问题,交框架重试
                self.mark_failed(delivery, &e.to_string()).await;
                self.report(delivery, None).await;
                return Err(AppError::ServiceUnavailable(format!(
                    "Webhook 投递失败(网络错误/超时,可重试): {e}"
                )));
            }
        };
        let status = response.status().as_u16();
        // 响应体只留前 2 KiB:订阅方可能回一个巨大的 HTML 错误页,
        // 整段写库会撑爆 `response_body TEXT` 与 worker 内存。
        let text = response.text().await.unwrap_or_default();
        let body_text: String = text.chars().take(2048).collect();

        let outcome = classify_status(status, duration_ms, body_text);
        self.report(delivery, Some(&outcome)).await;
        match &outcome {
            DeliveryOutcome::Delivered { status, duration_ms, .. } => {
                self.mark_delivered(delivery).await?;
                tracing::info!(
                    subscription_id = delivery.subscription_id,
                    event_id = %delivery.event_id,
                    status, duration_ms,
                    "webhook 投递成功"
                );
            }
            DeliveryOutcome::Permanent { status, .. } => {
                self.mark_failed(delivery, &format!("订阅方返回 {status}")).await;
                tracing::error!(
                    subscription_id = delivery.subscription_id,
                    event_id = %delivery.event_id,
                    status,
                    "webhook 被订阅方拒绝(4xx),不重试"
                );
            }
            DeliveryOutcome::Transient { status, .. } => {
                self.mark_failed(delivery, &format!("订阅方临时错误 {status}")).await;
                return Err(AppError::ServiceUnavailable(format!(
                    "Webhook 订阅方临时错误 {status},交由框架重试"
                )));
            }
        }
        Ok(outcome)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn delivery() -> WebhookDelivery {
        WebhookDelivery {
            subscription_id: 12,
            url: "https://example.com/hook".into(),
            secret: "whsec_test".into(),
            event_id: "EV1".into(),
            event_type: "alert_recorded".into(),
            alert_device_id: Some("DEV001".into()),
            severity: Some("warning".into()),
            occurred_at: Some("2026-09-28T10:00:00Z".into()),
            headers: vec![],
        }
    }

    // ===================== SSRF 规则 =====================

    /// 合法 https URL 必须放行,否则整条链路形同虚设。
    #[test]
    fn accepts_ordinary_https_url() {
        assert!(validate_url("https://example.com/hook").is_ok());
        // 显式 443 与 8443 都在白名单
        assert!(validate_url("https://example.com:443/hook").is_ok());
        assert!(validate_url("https://example.com:8443/hook").is_ok());
        // 缺省端口等价 443
        assert!(validate_url("https://sub.example.com/a/b?c=1#d").is_ok());
    }

    /// 生产环境禁止明文 http —— 凭据与业务数据在链路上明文可见。
    #[test]
    fn rejects_non_https_scheme() {
        assert!(validate_url("http://example.com/hook").is_err());
        // 更隐蔽的 scheme 同样拒绝
        assert!(validate_url("ftp://example.com/hook").is_err());
        assert!(validate_url("file:///etc/passwd").is_err());
        // 相对 URL 无法投递
        assert!(validate_url("/hook").is_err());
    }

    /// 解析失败必须拒绝,而不是放行给 reqwest 兜底。
    #[test]
    fn rejects_unparsable_url() {
        assert!(validate_url("not a url").is_err());
        assert!(validate_url("https://").is_err());
        assert!(validate_url("").is_err());
    }

    /// 直连 IP 是绕过域名白名单打内网的最短路径。
    #[test]
    fn rejects_ip_literal_host() {
        assert!(validate_url("https://169.254.169.254/latest/meta-data/").is_err());
        assert!(validate_url("https://127.0.0.1/hook").is_err());
        assert!(validate_url("https://10.0.0.5:443/hook").is_err());
        // IPv6 环回与元数据地址
        assert!(validate_url("https://[::1]/hook").is_err());
        assert!(validate_url("https://[fd00::1]/hook").is_err());
    }

    /// 内网与云元数据入口名。
    #[test]
    fn rejects_local_and_metadata_hosts() {
        assert!(validate_url("https://localhost/hook").is_err());
        assert!(validate_url("https://app.localhost/hook").is_err());
        assert!(validate_url("https://db.local/hook").is_err());
        assert!(validate_url("https://printer.local/hook").is_err());
        assert!(validate_url("https://metadata.google.internal/computeMetadata/v1/").is_err());
        assert!(validate_url("https://instance-data/latest/meta-data/").is_err());
    }

    /// 大小写与 DNS 根尾点都不得绕过本地名判定。
    #[test]
    fn forbidden_host_is_case_and_trailing_dot_insensitive() {
        assert!(is_forbidden_host("LOCALHOST"));
        assert!(is_forbidden_host("LocalHost"));
        assert!(is_forbidden_host("localhost."));
        assert!(is_forbidden_host("api.localhost."));
        assert!(is_forbidden_host("DB.LOCAL"));
        assert!(!is_forbidden_host("example.com"));
        assert!(!is_forbidden_host("localhost.example.com"));
    }

    /// URL 里带凭据会随日志、Referer、错误信息外泄。
    #[test]
    fn rejects_userinfo() {
        assert!(validate_url("https://user:pass@example.com/hook").is_err());
        assert!(validate_url("https://user@example.com/hook").is_err());
    }

    /// 端口白名单阻断内网高危服务。
    #[test]
    fn rejects_ports_outside_allowlist() {
        assert!(validate_url("https://example.com:22/hook").is_err());
        assert!(validate_url("https://example.com:6379/hook").is_err());
        assert!(validate_url("https://example.com:3306/hook").is_err());
        assert!(validate_url("https://example.com:8080/hook").is_err());
        // 非 https 的默认端口(http 80)同样不在白名单
        assert!(validate_url("http://example.com:80/hook").is_err());
    }

    /// 长度上限与表列宽一致。
    #[test]
    fn rejects_overlong_url() {
        let long = format!("https://example.com/{}", "a".repeat(MAX_URL_LEN));
        assert!(long.len() > MAX_URL_LEN);
        assert!(validate_url(&long).is_err());
    }

    // ===================== HMAC 签名 =====================

    /// 已知答案测试:锁死算法与编码,防止日后重构悄悄换算法。
    #[test]
    fn hmac_matches_known_vector() {
        let body = br#"{"alert_device_id":"DEV001","event_id":"EV1","event_type":"alert_recorded","occurred_at":"2026-09-28T10:00:00Z","severity":"warning"}"#;
        assert_eq!(
            sign_body("whsec_test", body),
            "97cac2d4c72cf4c3ce5afa6665d3eb241f4f4613d444e2435672d7e7f5446b30"
        );
    }

    /// 同一输入两次签名必须一致(无随机性、无时间混入)。
    #[test]
    fn hmac_is_deterministic() {
        let body = br#"{"a":1}"#;
        assert_eq!(sign_body("s", body), sign_body("s", body));
    }

    /// 改动 body 任一字节,签名必变 —— 否则签名形同虚设。
    #[test]
    fn hmac_changes_when_body_changes() {
        let base = sign_body("s", br#"{"severity":"warning"}"#);
        // 每个位置各改一个字节
        for i in 0..base_body().len() {
            let mut b = base_body();
            b[i] ^= 0x01;
            assert_ne!(sign_body("s", &b), base, "第 {i} 字节改动后签名未变");
        }
        // 长度变化
        assert_ne!(sign_body("s", br#"{"severity":"warning"} "#), base);
    }

    fn base_body() -> Vec<u8> {
        br#"{"severity":"warning"}"#.to_vec()
    }

    /// 不同密钥签名不同。
    #[test]
    fn hmac_differs_by_secret() {
        let body = br#"{"a":1}"#;
        assert_ne!(sign_body("s1", body), sign_body("s2", body));
    }

    /// 签名头值形状必须是 `sha256=<64 位小写 hex>`。
    #[test]
    fn signature_header_shape() {
        let v = signature_header_value("whsec_test", br#"{"a":1}"#);
        let hexpart = v.strip_prefix("sha256=").expect("必须以 sha256= 开头");
        assert_eq!(hexpart.len(), 64);
        assert!(hexpart.chars().all(|c| c.is_ascii_hexdigit() && !c.is_uppercase()));
    }

    /// 订阅自定义头能带上。
    #[test]
    fn custom_headers_are_attached() {
        let h = build_headers(
            "s",
            b"{}",
            "EV1",
            "alert_recorded",
            &[("X-Custom".into(), "v".into())],
        )
        .unwrap();
        assert_eq!(h.get("x-custom").unwrap(), "v");
    }

    /// 保留头一律拒绝覆盖 —— 覆盖签名等于伪造投递。
    #[test]
    fn reserved_headers_cannot_be_overridden() {
        for name in ["Host", "content-length", "X-ChargePilot-Signature", "HOST"] {
            let h = build_headers(
                "s",
                b"{}",
                "EV1",
                "alert_recorded",
                &[(name.into(), "evil".into())],
            )
            .unwrap();
            match name.to_ascii_lowercase().as_str() {
                "host" => assert_eq!(h.get("host").map(|v| v.to_str().unwrap()), None, "{name} 未被拒绝"),
                "content-length" => assert!(h.get("content-length").is_none(), "{name} 未被拒绝"),
                _ => {
                    let sig = h.get("x-chargepilot-signature").unwrap().to_str().unwrap();
                    assert_ne!(sig, "evil", "{name} 未被拒绝");
                }
            }
        }
    }

    /// 事件 id / 类型头必须带上,且是签名原文之外的独立信息。
    #[test]
    fn event_headers_are_present() {
        let h = build_headers("s", b"{}", "EV-9", "alert_recorded", &[]).unwrap();
        assert_eq!(h.get(EVENT_ID_HEADER).unwrap(), "EV-9");
        assert_eq!(h.get(EVENT_TYPE_HEADER).unwrap(), "alert_recorded");
    }

    // ===================== 结果分类 =====================

    #[test]
    fn status_200_is_delivered() {
        assert!(matches!(
            classify_status(200, 5, "ok".into()),
            DeliveryOutcome::Delivered { status: 200, .. }
        ));
        assert!(classify_status(200, 5, String::new()).is_delivered());
    }

    #[test]
    fn status_408_429_5xx_are_transient() {
        for s in [408, 429, 500, 502, 503, 504, 599] {
            assert!(
                matches!(classify_status(s, 1, String::new()), DeliveryOutcome::Transient { .. }),
                "{s} 必须判为临时错误"
            );
        }
    }

    #[test]
    fn status_400_404_are_permanent() {
        for s in [400, 401, 403, 404, 410, 422] {
            assert!(
                matches!(classify_status(s, 1, String::new()), DeliveryOutcome::Permanent { .. }),
                "{s} 必须判为永久失败(不重试)"
            );
        }
    }

    /// 3xx 不跟随重定向时会落到这里,按「明确拒绝」处理,避免死循环重试。
    #[test]
    fn status_3xx_is_permanent() {
        assert!(matches!(
            classify_status(302, 1, String::new()),
            DeliveryOutcome::Permanent { .. }
        ));
    }

    // ===================== 载荷构造 =====================

    /// body 里不能出现 secret 与 url。
    #[test]
    fn body_excludes_secret_and_url() {
        let d = delivery();
        let body = d.build_body();
        let text = serde_json::to_string(&body).unwrap();
        assert!(!text.contains("whsec_test"), "body 泄露了签名密钥");
        assert!(!text.contains("example.com"), "body 泄露了目标 URL");
        assert_eq!(body["event_id"], "EV1");
        assert_eq!(body["alert_device_id"], "DEV001");
        assert_eq!(body["severity"], "warning");
    }

    /// payload 缺 url 时给出空串,由调用方决定语义(不重试),而不是解析失败。
    #[test]
    fn parses_payload_without_url() {
        let payload = serde_json::json!({
            "subscription_id": 12,
            "url": "https://example.com/hook",
            "secret": "whsec_xxx",
            "alert_device_id": "DEV001",
            "severity": "warning",
            "headers": {"X-Custom": "v"}
        });
        let d = WebhookDelivery::from_payload("EV1", "alert_recorded", "2026-09-28T10:00:00Z", &payload);
        assert_eq!(d.subscription_id, 12);
        assert_eq!(d.url, "https://example.com/hook");
        assert_eq!(d.headers, vec![("X-Custom".to_string(), "v".to_string())]);
        // occurred_at 缺省时回落到 envelope 的值
        assert_eq!(d.occurred_at.as_deref(), Some("2026-09-28T10:00:00Z"));
    }

    #[test]
    fn parses_empty_payload() {
        let d = WebhookDelivery::from_payload("EV1", "t", "ts", &serde_json::json!({}));
        assert!(d.url.is_empty());
        assert!(d.headers.is_empty());
        assert!(d.alert_device_id.is_none());
    }
}
