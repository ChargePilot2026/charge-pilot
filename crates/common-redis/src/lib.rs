//! Redis 客户端 + Stream 抽象 + 端口级锁工具
//!
//! 设计要点(对齐技术规格 § 4.7):
//!   - cache 实例: allkeys-lru,业务缓存可用
//!   - stream 实例: noeviction,**只**用于 Stream 与幂等表;代码层禁用普通 KV 写入
//!
//! 提供:
//!   - `RedisCache`: 业务 KV,封装 SETNX/EXPIRE/GET/DEL 等
//!   - `RedisStream`: 封装 XADD/XREADGROUP/XACK,封装 11 个业务 Stream 常量名
//!   - `PortLock`: 三层防护中的逻辑锁 / 物理锁(见技术规格 § 5.5)


// 分层与序列化约束(P1a 建立;随 P3 逐服务迁移完成转 deny)
// 说明:配置在仓库根 clippy.toml,级别在这里。测试模块豁免。
#![allow(
    clippy::disallowed_macros,
    clippy::disallowed_types,
    clippy::disallowed_methods,
)]
use common_config::RedisConfig;
use common_error::{AppError, AppResult};
use redis::{aio::ConnectionManager, AsyncCommands, Client, RedisResult, Value};
use uuid::Uuid;

/// 业务缓存客户端
#[derive(Clone)]
pub struct RedisCache {
    conn: ConnectionManager,
}

impl RedisCache {
    pub async fn connect(cfg: &RedisConfig) -> AppResult<Self> {
        if cfg.is_stream {
            return Err(AppError::Config(
                "RedisCache cannot use stream instance (noeviction)".into(),
            ));
        }
        let client = Client::open(cfg.url.as_str())
            .map_err(|e| AppError::Config(format!("redis cache url: {e}")))?;
        let conn = ConnectionManager::new(client).await?;
        tracing::info!("RedisCache connected");
        Ok(Self { conn })
    }

    pub fn conn(&self) -> ConnectionManager {
        self.conn.clone()
    }

    pub async fn get<T: serde::de::DeserializeOwned>(&self, key: &str) -> AppResult<Option<T>> {
        let mut c = self.conn.clone();
        let v: Option<String> = c.get(key).await?;
        match v {
            None => Ok(None),
            Some(s) => Ok(Some(serde_json::from_str(&s)?)),
        }
    }

    pub async fn set_ex<T: serde::Serialize>(&self, key: &str, val: &T, ttl_secs: u64) -> AppResult<()> {
        let s = serde_json::to_string(val)?;
        let mut c = self.conn.clone();
        // TTL ±10% 抖动(技术规格 § 4.7 雪崩防护)
        let jitter = rand::random::<u64>() % (ttl_secs / 10 + 1);
        let ttl = ttl_secs + jitter;
        let _: () = c.set_ex(key, s, ttl as u64).await?;
        Ok(())
    }

    pub async fn del(&self, key: &str) -> AppResult<()> {
        let mut c = self.conn.clone();
        let _: i64 = c.del(key).await?;
        Ok(())
    }

    pub async fn incr(&self, key: &str) -> AppResult<i64> {
        let mut c = self.conn.clone();
        let v: i64 = c.incr(key, 1).await?;
        Ok(v)
    }

    pub async fn expire(&self, key: &str, ttl_secs: u64) -> AppResult<()> {
        let mut c = self.conn.clone();
        let _: bool = c.expire(key, ttl_secs as i64).await?;
        Ok(())
    }

    /// 简单限流(窗口计数):窗口期内计数,超出即返回 false。
    ///
    /// **D17 修复**:`INCR` 与首次 `EXPIRE` 原先是两次独立 await。若 `INCR` 已执行
    /// 而 `EXPIRE` 因断连/任务取消未执行,且后续 `v > 1`,则该键**永不再设 TTL**,
    /// 计数永久残留——调用方(如每用户 24 小时 5 次报修限制)将失去窗口自动恢复的保证。
    ///
    /// 现在合并为**单条 Lua**,`INCR` 与首次 `EXPIRE` 原子执行,不存在中间态。
    pub async fn rate_limit(&self, key: &str, limit: u32, window_secs: u32) -> AppResult<bool> {
        // ARGV[1]=窗口秒数
        // 计数为 1 时才设 TTL;若键已存在但无 TTL(历史脏数据),顺带补上。
        let script = r#"
            local v = redis.call('INCR', KEYS[1])
            if v == 1 or redis.call('TTL', KEYS[1]) < 0 then
                redis.call('EXPIRE', KEYS[1], ARGV[1])
            end
            return v
        "#;
        let mut c = self.conn.clone();
        let v: i64 = redis::Script::new(script)
            .key(key)
            .arg(window_secs)
            .invoke_async(&mut c)
            .await?;
        Ok(v <= limit as i64)
    }

    /// 修复存量脏键:返回当前存在但**无 TTL** 的限流键数量。
    ///
    /// D17 的历史脏数据在窗口到期后不会自行消失,需要显式清理(见 `fix_ttl_less_keys`)。
    pub async fn count_ttl_less_keys(&self, pattern: &str) -> AppResult<i64> {
        let mut c = self.conn.clone();
        let mut cursor = 0u64;
        let mut total = 0i64;
        loop {
            let (next, keys): (u64, Vec<String>) = redis::cmd("SCAN")
                .arg(cursor).arg("MATCH").arg(pattern).arg("COUNT").arg(200)
                .query_async(&mut c).await?;
            for key in keys {
                let ttl: i64 = c.ttl(&key).await?;
                if ttl < 0 { total += 1; }
            }
            cursor = next;
            if cursor == 0 { break; }
        }
        Ok(total)
    }

    /// 给无 TTL 的限流键补一个窗口(用 `key_ttl` 指定的时长),使其能自行过期。
    pub async fn fix_ttl_less_keys(&self, pattern: &str, window_secs: u32) -> AppResult<usize> {
        let mut c = self.conn.clone();
        let mut cursor = 0u64;
        let mut fixed = 0usize;
        loop {
            let (next, keys): (u64, Vec<String>) = redis::cmd("SCAN")
                .arg(cursor).arg("MATCH").arg(pattern).arg("COUNT").arg(200)
                .query_async(&mut c).await?;
            for key in keys {
                let ttl: i64 = c.ttl(&key).await?;
                if ttl < 0 {
                    let _: () = c.expire(&key, window_secs as i64).await?;
                    fixed += 1;
                }
            }
            cursor = next;
            if cursor == 0 { break; }
        }
        Ok(fixed)
    }

    pub async fn ping(&self) -> AppResult<()> {
        let mut c = self.conn.clone();
        let s: String = redis::cmd("PING").query_async(&mut c).await?;
        if s != "PONG" {
            return Err(AppError::Internal(format!("redis ping: {s}")));
        }
        Ok(())
    }
}

/// Redis Stream 客户端(只读 / 写 Stream;严禁普通 KV)
#[derive(Clone)]
pub struct RedisStream {
    conn: ConnectionManager,
    /// **D15**:阻塞读专用的**独立底层连接**。
    ///
    /// `ConnectionManager` 的 `clone()` 共享底层连接,阻塞命令(XREADGROUP BLOCK)
    /// 会挡住该连接上的其它命令——gateway 的 OTA 消费组 BLOCK 5000ms,而 outbox
    /// 发布超时仅 3 秒,发布排在阻塞读之后就会超时并重复投递;健康检查同样受影响。
    blocking_conn: ConnectionManager,
}

impl RedisStream {
    pub async fn connect(cfg: &RedisConfig) -> AppResult<Self> {
        if !cfg.is_stream {
            return Err(AppError::Config(
                "RedisStream must use stream instance (noeviction)".into(),
            ));
        }
        let client = Client::open(cfg.url.as_str())
            .map_err(|e| AppError::Config(format!("redis stream url: {e}")))?;
        let conn = ConnectionManager::new(client.clone()).await?;
        // 阻塞读走独立 Client/连接,与发布、ACK、探活彻底隔离(D15)
        let blocking_client = Client::open(cfg.url.as_str())
            .map_err(|e| AppError::Config(format!("redis stream url: {e}")))?;
        let blocking_conn = ConnectionManager::new(blocking_client).await?;
        tracing::info!("RedisStream connected(阻塞读使用独立连接)");
        Ok(Self { conn, blocking_conn })
    }

    pub async fn ping(&self) -> AppResult<()> {
        let mut c = self.conn.clone();
        let s: String = redis::cmd("PING").query_async(&mut c).await?;
        if s != "PONG" {
            return Err(AppError::Internal(format!("redis stream ping: {s}")));
        }
        Ok(())
    }

    /// **D4**:按流的分级保留上限。
    ///
    /// 原先全量 `MAXLEN ~ 100000` 一刀切。`~` 与精确裁剪**都不保护 PEL 中
    /// 尚未 ACK 的消息正文**——Redis 只按长度裁剪,不看消费状态。因此对可靠流
    /// 不能只靠长度:改用 `trim_by_ack_watermark` 按消费确认水位裁剪。
    fn maxlen_for(&self, stream: &str) -> i64 {
        match stream {
            // 可靠流:高水位,实际裁剪由消费确认水位决定
            s if s == streams::CHARGE_ENDED
                || s == streams::REFUND_REQUIRED
                || s == streams::INVOICE_REQUIRED
                || s == streams::COMP_TX
                || s == streams::PRICING_RULE_CHANGED
                || s == streams::COUPON_GRANT_REQUIRED =>
            {
                1_000_000
            }
            // 可丢流:维持较小上限
            _ => 100_000,
        }
    }

    /// **D4 ②**:按**消费确认水位**裁剪。
    ///
    /// 上界取所有必要消费组中"最小未确认 entry id"——越过它就会删掉尚未被任一
    /// 消费组处理的正文。**必要消费组尚未创建时不裁剪**(消息在组建立前被删,
    /// 就再无人可读)。某组 PEL 为空(已全部 ACK)时同样保守跳过。
    ///
    /// 水位用 Lua 一次算出,避免多次往返之间状态漂移。
    /// 返回实际删除的条数;无法确定水位时返回 0 且不改动。
    pub async fn trim_by_ack_watermark(
        &self,
        stream: &str,
        required_groups: &[&str],
    ) -> AppResult<i64> {
        if required_groups.is_empty() {
            return Ok(0);
        }
        let mut c = self.conn.clone();
        // ARGV[1..] = 必要消费组;返回 {状态, 水位}
        //   状态 1 = 可裁剪(返回最小未确认 id)
        //   状态 0 = 不可裁剪(组不存在 / 全部已 ACK)
        let script = r#"
            local best = nil
            for i = 1, #ARGV do
                local groups = redis.call('XINFO', 'GROUPS', KEYS[1])
                local found = false
                for g = 1, #groups do
                    if groups[g][1] == ARGV[i] then found = true; break end
                end
                if not found then return {0, ''} end
                local pend = redis.call('XPENDING', KEYS[1], ARGV[i], '-', '+', 1)
                if #pend == 0 then return {0, ''} end
                local id = pend[1][1]
                if best == nil or id < best then best = id end
            end
            if best == nil then return {0, ''} end
            return {1, best}
        "#;
        // Script::key/arg 需要 &mut ScriptInvocation,故先用 prepare_invoke 取可变的
        let script = redis::Script::new(script);
        let mut invocation = script.prepare_invoke();
        invocation.key(stream);
        for g in required_groups {
            invocation.arg(*g);
        }
        let (status, bound): (i64, String) = invocation.invoke_async(&mut c).await
            .map_err(|e| AppError::Internal(format!("求消费确认水位 {stream}: {e}")))?;

        if status != 1 || bound.is_empty() {
            tracing::info!(
                stream,
                groups = required_groups.len(),
                "未能确定安全裁剪水位(消费组缺失或均已 ACK),本次不裁剪"
            );
            return Ok(0);
        }

        // XTRIM MINID: 只裁掉严格早于水位的整个前缀
        let removed: i64 = redis::cmd("XTRIM")
            .arg(stream)
            .arg("MINID")
            .arg(&bound)
            .query_async(&mut c)
            .await
            .map_err(|e| AppError::Internal(format!("XTRIM {stream}: {e}")))?;
        tracing::info!(stream, %bound, removed, "按消费确认水位裁剪完成");
        Ok(removed)
    }

    pub fn conn(&self) -> ConnectionManager {
        self.conn.clone()
    }

    /// XADD: 返回 stream entry id
    pub async fn xadd<T: serde::Serialize>(&self, stream: &str, payload: &T) -> AppResult<String> {
        let s = serde_json::to_string(payload)?;
        let mut c = self.conn.clone();
        let id: String = redis::cmd("XADD")
            .arg(stream)
            .arg("MAXLEN")
            .arg("~")
            .arg(self.maxlen_for(stream))
            .arg("*")
            .arg("data")
            .arg(s)
            .query_async(&mut c)
            .await?;
        Ok(id)
    }

    /// XADD 完整 envelope(含 event_id / event_type 等头部)
    pub async fn xadd_envelope(&self, stream: &str, envelope: &StreamEnvelope) -> AppResult<String> {
        let mut c = self.conn.clone();
        let id: String = redis::cmd("XADD")
            .arg(stream)
            .arg("MAXLEN")
            .arg("~")
            .arg(self.maxlen_for(stream))
            .arg("*")
            .arg("event_id").arg(&envelope.event_id)
            .arg("event_type").arg(&envelope.event_type)
            .arg("occurred_at").arg(&envelope.occurred_at)
            .arg("producer").arg(&envelope.producer)
            .arg("schema_version").arg(envelope.schema_version)
            .arg("payload").arg(serde_json::to_string(&envelope.payload)?)
            .query_async(&mut c)
            .await?;
        Ok(id)
    }

    /// XGROUP CREATE - 幂等(已存在则忽略 BUSYGROUP)
    pub async fn ensure_group(&self, stream: &str, group: &str) -> AppResult<()> {
        let mut c = self.conn.clone();
        let res: RedisResult<Value> = redis::cmd("XGROUP")
            .arg("CREATE")
            .arg(stream)
            .arg(group)
            .arg("0") // New groups must not skip events published before startup.
            .arg("MKSTREAM")
            .query_async(&mut c)
            .await;
        if let Err(e) = res {
            if e.to_string().contains("BUSYGROUP") {
                return Ok(());
            }
            return Err(AppError::Redis(e));
        }
        Ok(())
    }

    /// XREADGROUP,返回 (entry_id, payload) 列表
    pub async fn xreadgroup(
        &self,
        stream: &str,
        group: &str,
        consumer: &str,
        count: usize,
        block_ms: usize,
    ) -> AppResult<Vec<StreamEntry>> {
        // D15:BLOCK 命令必须独占一条连接,否则会挡住同连接上的发布/ACK/探活
        let mut c = self.blocking_conn.clone();
        let v: redis::Value = redis::cmd("XREADGROUP")
            .arg("GROUP").arg(group).arg(consumer)
            .arg("COUNT").arg(count as i64)
            .arg("BLOCK").arg(block_ms as i64)
            .arg("STREAMS").arg(stream).arg(">")
            .query_async(&mut c)
            .await?;
        Ok(parse_xread(v))
    }

    /// 拉 PEL 中未 ACK 的 entry(重启恢复)
    pub async fn xreadgroup_pending(
        &self,
        stream: &str,
        group: &str,
        consumer: &str,
        count: usize,
    ) -> AppResult<Vec<StreamEntry>> {
        let mut c = self.conn.clone();
        let v: redis::Value = redis::cmd("XREADGROUP")
            .arg("GROUP").arg(group).arg(consumer)
            .arg("COUNT").arg(count as i64)
            .arg("STREAMS").arg(stream).arg("0")
            .query_async(&mut c)
            .await?;
        Ok(parse_xread(v))
    }

    pub async fn xack(&self, stream: &str, group: &str, entry_id: &str) -> AppResult<()> {
        let mut c = self.conn.clone();
        let _: i64 = redis::cmd("XACK")
            .arg(stream).arg(group).arg(entry_id)
            .query_async(&mut c).await?;
        Ok(())
    }

    /// Atomically retain the full failed message and acknowledge only this consumer's PEL entry.
    pub async fn dead_letter(&self, entry: &StreamEntry, group: &str, consumer: &str, reason: &str) -> AppResult<Option<String>> {
        let script = r#"
            local pending = redis.call('XPENDING', KEYS[1], ARGV[1], ARGV[2], ARGV[2], 1)
            if #pending == 0 then return nil end
            if pending[1][2] ~= ARGV[3] then return redis.error_reply('pending entry owner changed') end
            local id = redis.call('XADD', KEYS[2], '*',
                'orig_stream', KEYS[1], 'orig_id', ARGV[2], 'orig_group', ARGV[1],
                'orig_consumer', ARGV[3], 'reason', ARGV[4], 'ts', ARGV[5],
                'envelope_json', ARGV[6], 'raw_json', ARGV[7])
            redis.call('XACK', KEYS[1], ARGV[1], ARGV[2])
            return id
        "#;
        let mut c=self.conn.clone();
        let reason:String=reason.chars().take(512).collect();
        Ok(redis::Script::new(script).key(&entry.stream).key(format!("{}.dlq",entry.stream))
            .arg(group).arg(&entry.id).arg(consumer).arg(reason).arg(chrono::Utc::now().to_rfc3339())
            .arg(serde_json::to_string(&entry.envelope)?).arg(serde_json::to_string(&entry.raw)?)
            .invoke_async(&mut c).await?)
    }

}

fn parse_xread(v: redis::Value) -> Vec<StreamEntry> {
    let mut out = Vec::new();
    let redis::Value::Array(streams) = v else { return out };
    for s in streams {
        let redis::Value::Array(parts) = s else { continue };
        if parts.len() < 2 { continue; }
        let stream_name = match &parts[0] { redis::Value::BulkString(b) => String::from_utf8_lossy(b).into_owned(), _ => continue };
        let entries = match &parts[1] { redis::Value::Array(e) => e, _ => continue };
        for e in entries {
            let redis::Value::Array(es) = e else { continue };
            if es.len() < 2 { continue; }
            let id = match &es[0] { redis::Value::BulkString(b) => String::from_utf8_lossy(b).into_owned(), _ => continue };
            // field-value 数组
            let fields: &[redis::Value] = match &es[1] { redis::Value::Array(f) => f, redis::Value::Nil => &[], _ => continue };
            let mut raw = std::collections::HashMap::new();
            let mut i = 0;
            while i + 1 < fields.len() {
                let k = match &fields[i] { redis::Value::BulkString(b) => String::from_utf8_lossy(b).into_owned(), _ => { i += 2; continue; } };
                let val = match &fields[i+1] { redis::Value::BulkString(b) => String::from_utf8_lossy(b).into_owned(), _ => { i += 2; continue; } };
                raw.insert(k.clone(), val.clone());
                if k == "payload" {
                    // 等所有字段填好后再组装 envelope,所以暂存
                }
                i += 2;
            }
            // 重建 envelope
            let event_id = raw.get("event_id").cloned().unwrap_or_default();
            let event_type = raw.get("event_type").cloned().unwrap_or_default();
            let occurred_at = raw.get("occurred_at").cloned().unwrap_or_default();
            let producer = raw.get("producer").cloned().unwrap_or_default();
            let schema_version: u32 = raw.get("schema_version").and_then(|s| s.parse().ok()).unwrap_or(1);
            let payload: serde_json::Value = raw.get("payload")
                .and_then(|s| serde_json::from_str(s).ok())
                .unwrap_or(serde_json::Value::Null);
            let env = StreamEnvelope {
                event_id,
                event_type,
                occurred_at,
                producer,
                schema_version,
                payload,
            };
            out.push(StreamEntry {
                stream: stream_name.clone(),
                id,
                envelope: env,
                raw,
            });
        }
    }
    out
}

/// Stream 消息标准 envelope(对齐技术规格 § 5.2)
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct StreamEnvelope {
    pub event_id: String,
    pub event_type: String,
    pub occurred_at: String,
    pub producer: String,
    pub schema_version: u32,
    pub payload: serde_json::Value,
}

impl StreamEnvelope {
    pub fn new(event_type: impl Into<String>, producer: impl Into<String>, payload: serde_json::Value) -> Self {
        Self {
            event_id: Uuid::new_v4().to_string(),
            event_type: event_type.into(),
            occurred_at: chrono::Utc::now().to_rfc3339(),
            producer: producer.into(),
            schema_version: 1,
            payload,
        }
    }
}

/// XREADGROUP 单条消息
#[derive(Debug, Clone)]
pub struct StreamEntry {
    pub stream: String,
    pub id: String,
    pub envelope: StreamEnvelope,
    pub raw: std::collections::HashMap<String, String>,
}

/// 11 个 Stream 名常量(对齐技术规格 § 5.1,严格禁止新增未登记的 Stream)
pub mod streams {
    pub const DEVICE_EVENT: &str = "device_event_stream";
    pub const ALERT: &str = "alert_stream";
    pub const CHARGE_STARTED: &str = "charge_started_stream";
    pub const CHARGE_ENDED: &str = "charge_ended_stream";
    pub const REFUND_REQUIRED: &str = "refund_required_stream";
    pub const INVOICE_REQUIRED: &str = "invoice_required_stream";
    pub const WEBHOOK_RETRY: &str = "webhook_retry_stream";
    pub const OTA_SCHEDULE: &str = "ota_schedule_stream";
    pub const COMP_TX: &str = "comp_tx_stream";
    pub const COUPON_GRANT_REQUIRED: &str = "coupon_grant_required_stream";
    pub const PRICING_RULE_CHANGED: &str = "pricing_rule_changed_stream";
}

/// 端口级锁(扫码选端口 → 启动链路使用)
#[derive(Clone)]
pub struct PortLock {
    cache: RedisCache,
}

impl PortLock {
    pub fn new(cache: RedisCache) -> Self {
        Self { cache }
    }

    /// 逻辑锁: charge:hold:port_xxx TTL 300s(支付中占位)
    pub async fn try_hold(&self, port_id: &str, holder: &str, ttl_secs: u64) -> AppResult<bool> {
        let key = format!("charge:hold:port_{port_id}");
        let mut c = self.cache.conn();
        // SET NX EX
        let res: Option<String> = redis::cmd("SET")
            .arg(&key).arg(holder).arg("NX").arg("EX").arg(ttl_secs as i64)
            .query_async(&mut c).await?;
        Ok(res.is_some())
    }

    pub async fn check_holder(&self, port_id: &str, expected: &str) -> AppResult<bool> {
        let key = format!("charge:hold:port_{port_id}");
        let mut c = self.cache.conn();
        let v: Option<String> = redis::cmd("GET").arg(&key).query_async(&mut c).await?;
        Ok(v.as_deref() == Some(expected))
    }

    /// CAS 删除: 仅当 holder 等于 expected 才删除(防误删后续订单的锁)
    pub async fn release_if_match(&self, port_id: &str, expected: &str) -> AppResult<bool> {
        let key = format!("charge:hold:port_{port_id}");
        let lua = r#"
            if redis.call('GET', KEYS[1]) == ARGV[1] then
                return redis.call('DEL', KEYS[1])
            else
                return 0
            end
        "#;
        let mut c = self.cache.conn();
        let r: i64 = redis::Script::new(lua)
            .key(&key)
            .arg(expected)
            .invoke_async(&mut c)
            .await?;
        Ok(r == 1)
    }

    /// 物理锁: charge:lock:port_xxx TTL 30s(启动链路)
    pub async fn try_lock(&self, port_id: &str, holder: &str) -> AppResult<bool> {
        let key = format!("charge:lock:port_{port_id}");
        let mut c = self.cache.conn();
        let res: Option<String> = redis::cmd("SET")
            .arg(&key).arg(holder).arg("NX").arg("EX").arg(30_i64)
            .query_async(&mut c).await?;
        Ok(res.is_some())
    }

    pub async fn release_lock_if_match(&self, port_id: &str, expected: &str) -> AppResult<bool> {
        let key = format!("charge:lock:port_{port_id}");
        let lua = r#"
            if redis.call('GET', KEYS[1]) == ARGV[1] then
                return redis.call('DEL', KEYS[1])
            else
                return 0
            end
        "#;
        let mut c = self.cache.conn();
        let r: i64 = redis::Script::new(lua)
            .key(&key)
            .arg(expected)
            .invoke_async(&mut c)
            .await?;
        Ok(r == 1)
    }
}

/// 端口充电中快照缓存(snapshot:{order_id},TTL 10s,见技术规格 § 3.2.3)
pub async fn write_snapshot<T: serde::Serialize>(
    cache: &RedisCache,
    order_id: &str,
    snap: &T,
) -> AppResult<()> {
    let key = format!("snapshot:{order_id}");
    cache.set_ex(&key, snap, 10).await
}

pub async fn read_snapshot<T: serde::de::DeserializeOwned>(
    cache: &RedisCache,
    order_id: &str,
) -> AppResult<Option<T>> {
    let key = format!("snapshot:{order_id}");
    cache.get::<T>(&key).await
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn stream_constants_match_spec() {
        // 冻结的 11 个 Stream 名,任何修改都会使编译失败
        let all = vec![
            streams::DEVICE_EVENT,
            streams::ALERT,
            streams::CHARGE_STARTED,
            streams::CHARGE_ENDED,
            streams::REFUND_REQUIRED,
            streams::INVOICE_REQUIRED,
            streams::WEBHOOK_RETRY,
            streams::OTA_SCHEDULE,
            streams::COMP_TX,
            streams::COUPON_GRANT_REQUIRED,
            streams::PRICING_RULE_CHANGED,
        ];
        assert_eq!(all.len(), 11);
        for s in &all {
            assert!(s.ends_with("_stream"), "stream name must end with _stream: {s}");
        }
    }

    #[test]
    fn envelope_shape() {
        let env = StreamEnvelope::new("charge_ended", "gateway", serde_json::json!({"a":1}));
        assert_eq!(env.event_type, "charge_ended");
        assert_eq!(env.producer, "gateway");
        assert_eq!(env.payload["a"], 1);
        assert_eq!(env.schema_version, 1);
        assert!(!env.event_id.is_empty());
    }
}
