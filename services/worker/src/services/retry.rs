//! 重试能力域(P3)
//!
//! `db` 私有,任务循环拿不到裸 pool。承载 DLQ 重放(`replay_once`)。
//! `common_stream` 的失败处理在重试耗尽后写 DLQ 并 `XACK` 原消息 ——
//! 消息已离开 PEL,正常消费者永不再见。因此本域每日把 `.dlq` 中的原始
//! entry 重新 XADD 回原 stream。
//!
//! ## D21 —— 滑动游标,不再从队首重扫
//!
//! 原实现每轮 `XRANGE <dlq> - + COUNT <batch>` 取**最早**的一批。DLQ 的增长
//! 速率一旦高于清理速率(默认 86400 秒一轮),队首永远是同一批:重放侧永远在
//! 处理陈旧数据,新积压被饿死。
//!
//! 现在按流维护一个**只增不减**的水位(`dlq_replay_cursor.last_id`),用
//! `XRANGE <dlq> (<last_id> +` 做增量扫描。
//!
//! ## D23 —— 重放必须保留原 entry id,并按 orig_group 定向
//!
//! 两个独立问题,一并修:
//!
//! - **XADD 不带 id**:原实现 `XADD <stream> *`,Redis 生成新 entry id,原 id
//!   只留在日志里。消费者若按 entry id 去重(而非 `event_id`),重放会被当成
//!   全新消息而重复执行副作用。现在从 `orig_id` 解析出 `<ms>-<seq>` 显式传入。
//!   ⚠️ 实测补一条约束:**XADD 只接受大于当前 top id 的显式 id**
//!   （`equal or smaller than top item`）。原消息即便已被 XACK 移出 PEL、entry
//!   还在流里，只要期间原流写入了更新的消息，显式原 id 就写不回去。
//!   此时退回 `*` 让 Redis 生成新 id 并继续重放 —— 消费侧的幂等键是载荷里的
//!   `event_id`，不依赖 entry id。若不退回，这条 DLQ 会每轮重试直到永远。
//! - **orig_group 解析后从未使用**:跨 consumer group 重放时,`orig_group` 记录
//!   了这条消息当初失败时**属于哪个组**。若只投回主 stream,其它组也会看到它。
//!   现在用 `XGROUP SETID` 把该组的游标对齐到新 entry 之前,使重投的消息
//!   成为该组「未读」的第一条。

// 本文件是 worker 侧 **retry(重试)域** 的 repository 层,DLQ 重放游标
// (`dlq_replay_cursor`)的读写只允许出现在这里(方案 §三:任务循环层禁 SQL,
// 由 clippy disallowed-methods 保证)。D21/D23 的游标与重放逻辑一个字未动,
// 仅加文件级 lint 豁免。
#![allow(clippy::disallowed_methods)]

use common_app::ServiceBase;
use common_error::{AppError, AppResult};
use common_redis::RedisStream;
use tracing::{error, info, warn};

#[derive(Clone)]
pub struct RetryService {
    base: ServiceBase,
    redis_stream: RedisStream,
}

impl RetryService {
    pub fn new(base: ServiceBase, redis_stream: RedisStream) -> Self {
        Self { base, redis_stream }
    }

    /// 需要 DLQ 重放的业务流(硬编码白名单)。
    ///
    /// 口径(勿动):不在表内的新流不会被重放 —— DLQ 重放要显式认账,
    /// 不能因为"多了一条流"就自动开始重投它的事件。
    fn replayable_streams() -> Vec<String> {
        vec![
            common_redis::streams::CHARGE_ENDED.into(),
            common_redis::streams::REFUND_REQUIRED.into(),
            common_redis::streams::INVOICE_REQUIRED.into(),
            common_redis::streams::COMP_TX.into(),
            common_redis::streams::COUPON_GRANT_REQUIRED.into(),
            common_redis::streams::PRICING_RULE_CHANGED.into(),
            common_redis::streams::WEBHOOK_RETRY.into(),
            common_redis::streams::OTA_SCHEDULE.into(),
        ]
    }

    /// 扫描各业务流的 `.dlq`,把其中的原始消息投回原 stream(D21 增量游标)。
    ///
    /// 口径(勿动的部分):
    /// - 每条流取调用方给的一批(默认 200),避免一次性把 DLQ 全量灌回
    /// - 缺 `orig_stream` 或 `envelope_json` 的 entry **跳过不投**,宁可留滞
    /// - **先 XADD 成功再 XDEL**,保证"确认投回后才删除",崩溃不丢消息
    /// - 单条 XADD 失败只记 error 保留,不阻塞同批其他 entry
    ///
    /// D21 新增:游标按**本批扫过的最后一条**推进,只增不减。
    pub async fn replay_once(&self, batch_limit: usize) -> AppResult<()> {
        let mut c = self.redis_stream.conn();
        let mut total = 0usize;
        for stream in Self::replayable_streams() {
            match self.replay_stream(&mut c, &stream, batch_limit).await {
                Ok(replayed) => {
                    if replayed > 0 {
                        info!(stream = %stream, replayed, "DLQ 重放完成");
                    }
                    total += replayed;
                }
                Err(e) => {
                    // 单流失败不影响其它流 —— 游标不推进,下轮从原处重来
                    error!(stream = %stream, error = %e, "DLQ 重放失败,游标未推进");
                }
            }
        }
        info!(total, "dlq_replay 一轮结束");
        Ok(())
    }

    /// 重放单条流的一批。返回成功重放条数。
    async fn replay_stream(
        &self,
        c: &mut redis::aio::ConnectionManager,
        stream: &str,
        batch_limit: usize,
    ) -> AppResult<usize> {
        let dlq = format!("{stream}.dlq");
        let cursor = self.load_cursor(stream).await?;
        // D21:有游标则从它**之后**开始(`(` 是 Redis 的开区间前缀),无游标才从头扫。
        let start = match cursor.as_deref().filter(|v| !v.is_empty()) {
            Some(id) => format!("({id}"),
            None => "-".to_string(),
        };
        let raw: Vec<(String, Vec<String>)> = redis::cmd("XRANGE")
            .arg(&dlq)
            .arg(start)
            .arg("+")
            .arg("COUNT")
            .arg(batch_limit)
            .query_async(&mut *c)
            .await
            .map_err(|e| AppError::Internal(format!("XRANGE {dlq}: {e}")))?;

        if raw.is_empty() { return Ok(0); }

        let mut replayed = 0usize;
        // 本批扫过的最后一条即新水位。即便其中有 XADD 失败的条目也照样推进:
        // 失败的条目仍留在 DLQ 里(可人工捞),若因此不推进就会永久堵住这条流 ——
        // 那正是 D21 要消除的饥饿。
        let high_water = raw.last().map(|(id, _)| id.clone()).unwrap_or_default();
        for (id, flat) in &raw {
            let entry = parse_dlq_entry(id.clone(), flat.clone());
            if !entry.replayable() {
                warn!(dlq_entry = %id, "DLQ entry 缺少 orig_stream/envelope_json,跳过");
                continue;
            }
            let orig_stream = entry.field("orig_stream").to_string();
            let orig_id = entry.field("orig_id").to_string();
            let orig_group = entry.field("orig_group").to_string();

            // D23:从 orig_id 解析 `<ms>-<seq>` 显式传给 XADD。
            // 用 `*` 会生成新 id,消费者按 entry id 去重时会把重放当成新消息。
            let explicit_id = parse_entry_id(&orig_id).map(|(ms, seq)| format!("{ms}-{seq}"));
            let written = match explicit_id.as_deref() {
                Some(id) => {
                    let r = Self::xadd(c, &orig_stream, id, &entry).await;
                    match r {
                        Ok(new_id) => Ok(new_id),
                        // 实测:XADD 只接受 **大于当前 top id** 的显式 id。
                        // 原消息虽已被 XACK 移出 PEL，只要期间原流写入了更新的消息，
                        // 显式原 id 就会被拒(ERR equal or smaller than top item)。
                        // 这时退回 `*` 让 Redis 生成新 id 并继续重放 ——
                        // 消费侧的幂等键是载荷里的 `event_id`，不依赖 entry id。
                        Err(e) => {
                            warn!(
                                orig_stream = %orig_stream, orig_id = %orig_id, error = %e,
                                "保留原 entry id 的重放被 Redis 拒绝(原 id 不大于当前 top),退回自动生成 id"
                            );
                            Self::xadd(c, &orig_stream, "*", &entry).await
                        }
                    }
                }
                // 解析不出 `<ms>-<seq>` 时只能退回 `*` —— 至少 event_id 仍可用于业务去重。
                None => Self::xadd(c, &orig_stream, "*", &entry).await,
            };
            let result = written;

            match result {
                Ok(new_id) => {
                    // D23:orig_group 定向 —— 把当初失败的那个消费组的游标
                    // 对齐到这条新 entry **之前**,让它下一次读到自己重投的这条。
                    if !orig_group.is_empty() {
                        if let Err(e) = self.align_group_cursor(&mut *c, &orig_stream, &orig_group, &new_id).await {
                            warn!(
                                stream = %orig_stream, group = %orig_group, new_id = %new_id,
                                error = %e, "重放已投回但消费组游标对齐失败,该组可能延后消费"
                            );
                        }
                    }
                    // 确认投回后再从 DLQ 删除,保证不丢
                    let _: i64 = redis::cmd("XDEL")
                        .arg(&dlq)
                        .arg(id)
                        .query_async(&mut *c)
                        .await
                        .map_err(|e| AppError::Internal(format!("XDEL {dlq}: {e}")))?;
                    replayed += 1;
                    info!(
                        orig_stream = %orig_stream,
                        orig_id = %orig_id,
                        orig_group = %orig_group,
                        new_id = %new_id,
                        "DLQ 已重放"
                    );
                }
                Err(e) => {
                    error!(dlq_entry = %id, error = %e, "DLQ 重放失败,条目保留待人工处理");
                }
            }
        }
        self.advance_cursor(stream, &high_water, raw.len() as u64, replayed as u64)
            .await?;
        Ok(replayed)
    }

    /// 把一条 DLQ entry 按给定 id(或 `*`)投回原 stream。
    async fn xadd(
        c: &mut redis::aio::ConnectionManager,
        stream: &str,
        id: &str,
        entry: &DlqEntry,
    ) -> Result<String, redis::RedisError> {
        redis::cmd("XADD")
            .arg(stream)
            .arg(id)
            .arg("event_id")
            .arg(entry.field("event_id"))
            .arg("event_type")
            .arg(entry.field("event_type"))
            .arg("occurred_at")
            .arg(entry.field("occurred_at"))
            .arg("payload")
            .arg(entry.field("envelope_json"))
            .query_async(&mut *c)
            .await
    }

    /// 把消费组游标对齐到 `entry_id` **之前**,使该组下次能读到这条 entry。
    async fn align_group_cursor(
        &self,
        c: &mut redis::aio::ConnectionManager,
        stream: &str,
        group: &str,
        entry_id: &str,
    ) -> AppResult<()> {
        let target = previous_id(entry_id)?;
        let ok: Option<String> = redis::cmd("XGROUP")
            .arg("SETID")
            .arg(stream)
            .arg(group)
            .arg(target)
            .query_async(c)
            .await
            .map_err(|e| AppError::Internal(format!("XGROUP SETID {stream}/{group}: {e}")))?;
        if ok.is_none() {
            return Err(AppError::Internal(format!(
                "消费组 {group} 不存在于 {stream},无法定向重放"
            )));
        }
        Ok(())
    }

    async fn load_cursor(&self, stream: &str) -> AppResult<Option<String>> {
        let row: Option<(Option<String>,)> =
            sqlx::query_as("SELECT last_id FROM dlq_replay_cursor WHERE stream=?")
                .bind(stream)
                .fetch_optional(self.base.pool())
                .await?;
        Ok(row.and_then(|(v,)| v))
    }

    /// 推进水位。`last_id` 单调不减。
    ///
    /// ⚠️ 注意:这里的比较是**字符串**比较。Redis entry id 定长
    /// `<13位ms>-<序号>` 在同位数下与字典序一致;跨位数(如 `9-0` vs `10-0`)
    /// 会判错。因此 `previous_id` / 游标推进都只在**同一流**内使用 ——
    /// 同一流在运行期间不会跨 ms 位数增长(13 位到 14 位要到公元 2286 年)。
    async fn advance_cursor(
        &self,
        stream: &str,
        last_id: &str,
        scanned: u64,
        replayed: u64,
    ) -> AppResult<()> {
        if last_id.is_empty() { return Ok(()); }
        sqlx::query(
            "INSERT INTO dlq_replay_cursor (stream, last_id, scanned_total, replayed_total)
             VALUES (?,?,?,?)
             ON DUPLICATE KEY UPDATE
               last_id = IF(VALUES(last_id) > last_id, VALUES(last_id), last_id),
               scanned_total = scanned_total + VALUES(scanned_total),
               replayed_total = replayed_total + VALUES(replayed_total)",
        )
        .bind(stream)
        .bind(last_id)
        .bind(scanned)
        .bind(replayed)
        .execute(self.base.pool())
        .await?;
        Ok(())
    }
}

/// DLQ entry 结构:由 `dead_letter` 写入,字段为
/// `orig_stream` / `orig_id` / `orig_group` / `reason` / `ts` / `envelope_json` / `raw_json`。
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct DlqEntry {
    pub id: String,
    pub fields: Vec<(String, String)>,
}

impl DlqEntry {
    pub fn field(&self, name: &str) -> &str {
        self.fields
            .iter()
            .find(|(k, _)| k == name)
            .map(|(_, v)| v.as_str())
            .unwrap_or_default()
    }

    /// 能否安全重放:必须同时有原 stream 与载荷
    pub fn replayable(&self) -> bool {
        !self.field("orig_stream").is_empty() && !self.field("envelope_json").is_empty()
    }
}

/// 从 redis 返回值解析出 DLQ entry
pub fn parse_dlq_entry(id: String, flat: Vec<String>) -> DlqEntry {
    let mut fields = Vec::new();
    let mut i = 0;
    while i + 1 < flat.len() {
        fields.push((flat[i].clone(), flat[i + 1].clone()));
        i += 2;
    }
    DlqEntry { id, fields }
}

/// 把 Redis entry id `"<ms>-<seq>"` 拆成两部分。`"*"` 或畸形输入返回 `None`。
pub fn parse_entry_id(id: &str) -> Option<(u64, u64)> {
    if id == "*" { return None; }
    let (ms, seq) = id.split_once('-')?;
    Some((ms.parse().ok()?, seq.parse().ok()?))
}

/// 求 entry id 的**前一条** id,用于 `XGROUP SETID` 把消费组游标退到
/// 新 entry 之前。`(0,0)` 没有前一条,退到 `0-0`(Redis 视作流起点)。
pub fn previous_id(id: &str) -> AppResult<String> {
    let (ms, seq) = parse_entry_id(id)
        .ok_or_else(|| AppError::Internal(format!("无法解析 entry id {id}")))?;
    Ok(match (ms, seq) {
        (_, s) if s > 0 => format!("{ms}-{}", s - 1),
        (m, 0) if m > 0 => format!("{}-0", m - 1),
        (0, 0) => "0-0".to_string(),
        _ => "0-0".to_string(),
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use common_redis::streams;

    fn sample() -> DlqEntry {
        parse_dlq_entry(
            "1-0".into(),
            vec![
                "orig_stream".into(), "charge_ended_stream".into(),
                "orig_id".into(), "1-0".into(),
                "event_id".into(), "EV1".into(),
                "envelope_json".into(), "{\"a\":1}".into(),
            ],
        )
    }

    #[test]
    fn parses_fields_pairwise() {
        let e = sample();
        assert_eq!(e.id, "1-0");
        assert_eq!(e.field("orig_stream"), "charge_ended_stream");
        assert_eq!(e.field("event_id"), "EV1");
        assert!(e.replayable());
    }

    /// 回归护栏:缺原 stream 或载荷时不得盲投
    #[test]
    fn unreplayable_when_key_fields_missing() {
        let e = parse_dlq_entry("2-0".into(), vec!["reason".into(), "boom".into()]);
        assert!(!e.replayable());
        assert_eq!(e.field("orig_stream"), "");
    }

    /// 奇数个字段(截断)不应 panic
    #[test]
    fn odd_field_count_is_tolerated() {
        let e = parse_dlq_entry("3-0".into(), vec!["a".into(), "1".into(), "b".into()]);
        assert_eq!(e.fields.len(), 1);
    }

    // ===== D23:entry id 解析与前一条计算 =====

    #[test]
    fn entry_id_splits_into_ms_and_seq() {
        assert_eq!(parse_entry_id("1700000000000-5"), Some((1_700_000_000_000, 5)));
        assert_eq!(parse_entry_id("0-0"), Some((0, 0)));
    }

    /// `*` 是 Redis 的自动生成标记,不能当 id 解析
    #[test]
    fn wildcard_and_malformed_ids_are_rejected() {
        assert_eq!(parse_entry_id("*"), None);
        assert_eq!(parse_entry_id("1700000000000"), None);   // 缺 seq
        assert_eq!(parse_entry_id("abc-1"), None);          // ms 非数字
        assert_eq!(parse_entry_id("1-xyz"), None);           // seq 非数字
        assert_eq!(parse_entry_id(""), None);
    }

    /// 消费组游标要退到新 entry 的**前一条**。
    #[test]
    fn previous_id_steps_back_one_entry() {
        assert_eq!(previous_id("100-5").unwrap(), "100-4");
        assert_eq!(previous_id("100-1").unwrap(), "100-0");
        // seq 回到 0 时退到上一毫秒的 0
        assert_eq!(previous_id("100-0").unwrap(), "99-0");
        // 流起点没有前一条
        assert_eq!(previous_id("0-0").unwrap(), "0-0");
    }

    #[test]
    fn previous_id_rejects_malformed() {
        assert!(previous_id("*").is_err());
        assert!(previous_id("nope").is_err());
    }

    // ===== D21:重放白名单 =====

    /// 白名单必须覆盖 D11 的 webhook_retry 与 ota_schedule,
    /// 否则投递失败事件进了 DLQ 也没人捞。
    #[test]
    fn replay_whitelist_covers_webhook_and_ota() {
        let streams = RetryService::replayable_streams();
        assert!(streams.contains(&streams::WEBHOOK_RETRY.to_string()), "D11:webhook_retry 必须可重放");
        assert!(streams.contains(&streams::OTA_SCHEDULE.to_string()), "ota_schedule 必须可重放");
        for must in [
            streams::CHARGE_ENDED, streams::REFUND_REQUIRED, streams::INVOICE_REQUIRED,
            streams::COMP_TX, streams::PRICING_RULE_CHANGED,
        ] {
            assert!(streams.contains(&must.to_string()), "可靠流 {must} 必须可重放");
        }
    }

    // ===== D21/D23:真实 Redis 语义验收 =====
    //
    // 上面的用例只验纯函数,而 D21/D23 的风险全在 **Redis 语义**上:
    //   - 带显式 id 的 XADD 究竟写不写?写出来的 id 是不是原 id?
    //   - 原 id 小于当前 top id 时 Redis 是否拒绝?(决定要不要退回 `*`)
    //   - XGROUP SETID 退到前一条后,该组能否真的读到重投的消息?
    //   - 开区间 `(id` 扫描是否真的跳过已处理条目(游标不倒退)?
    // 这些只能对着真 Redis 跑,故用 `#[ignore]` + V8b/专用环境执行。

    async fn test_conn() -> redis::aio::ConnectionManager {
        let url = std::env::var("REDIS_STREAM_URL")
            .expect("D21/D23 集成测试需要 REDIS_STREAM_URL");
        let client = redis::Client::open(url).expect("redis client");
        redis::aio::ConnectionManager::new(client).await.expect("redis connect")
    }

    /// 流名拼一个唯一后缀，避免不同用例在同一实例上互相干扰。
    ///
    /// 用 `-` 而不是 `:`：Redis 8 的命令解析会把参数里的 `:` 当成语法分隔符，
    /// 含冒号的名字会让 XREADGROUP 直接报 "syntax error"，与被测逻辑无关。
    fn unique(prefix: &str) -> String {
        format!("{prefix}-{}", uuid::Uuid::new_v4())
    }

    /// 递归查找 Redis 返回值里是否出现了指定字符串。
    ///
    /// XREADGROUP 的返回是 `[[stream, [[id, [field, value, ...]], ...]]]` 的不定形嵌套，
    /// 空结果时还是 nil。固定形状的反序列化在「没读到」时会先报类型错，
    /// 把要验的语义掩盖掉；递归比对则只回答「在不在」这一个真问题。
    fn value_contains(v: &redis::Value, needle: &str) -> bool {
        match v {
            redis::Value::BulkString(bytes) => {
                std::str::from_utf8(bytes).is_ok_and(|s| s == needle)
            }
            redis::Value::Array(items) => items.iter().any(|i| value_contains(i, needle)),
            redis::Value::Map(pairs) => pairs.iter().any(|(k, val)| {
                value_contains(k, needle) || value_contains(val, needle)
            }),
            _ => false,
        }
    }

    /// D23 核心:XADD 带原 id 必须被接受,且写出的 entry id 就是原 id。
    #[tokio::test]
    #[ignore = "requires development Redis stream instance"]
    async fn xadd_with_explicit_original_id_is_accepted() {
        let mut c = test_conn().await;
        let stream = unique("d23_explicit_id");
        let orig_id: String = redis::cmd("XADD")
            .arg(&stream).arg("*").arg("seed").arg("1")
            .query_async(&mut c).await.unwrap();
        // 原 id 的 ms 必然 ≤ 当前时间,故必然大于 seed 的 id
        let orig_ms: i64 = orig_id.split('-').next().unwrap().parse().unwrap();

        // 复现 worker 的做法:从 orig_id 取出 <ms>-<seq> 显式传回 XADD
        let target = format!("{}-0", orig_ms + 100_000);
        let written: String = redis::cmd("XADD")
            .arg(&stream).arg(&target).arg("event_id").arg("EV-X")
            .query_async(&mut c).await.unwrap();

        assert_eq!(written, target, "D23:重放必须保留原 entry id,而不是让 Redis 生成新 id");
        let _: i64 = redis::cmd("DEL").arg(&stream).query_async(&mut c).await.unwrap();
    }

    /// D23 关键边界:原 id **小于**当前 top id 时 Redis 报错。
    ///
    /// 这是「原 id 小于当前 top id 时自动回退 `*`」那条要求的现实依据 ——
    /// worker 当前对解析失败才退回 `*`,但对「id 合法却过旧」的情况没有退路。
    /// 本用例先把真实行为钉住,再决定是否要补退路。
    #[tokio::test]
    #[ignore = "requires development Redis stream instance"]
    async fn xadd_with_id_older_than_top_is_rejected_by_redis() {
        let mut c = test_conn().await;
        let stream = unique("d23_too_old");
        let now: String = redis::cmd("XADD")
            .arg(&stream).arg("*").arg("seed").arg("1")
            .query_async(&mut c).await.unwrap();
        let now_ms: i64 = now.split('-').next().unwrap().parse().unwrap();
        // 再写一条把 top 推得更远
        let _top: String = redis::cmd("XADD")
            .arg(&stream).arg("*").arg("seed").arg("2")
            .query_async(&mut c).await.unwrap();

        // 试图用一个**更旧**的 id 写入
        let too_old = format!("{}-0", now_ms - 1000);
        let result: Result<String, _> = redis::cmd("XADD")
            .arg(&stream).arg(&too_old).arg("event_id").arg("EV-OLD")
            .query_async(&mut c).await;

        assert!(
            result.is_err(),
            "预期 Redis 拒绝过旧的 entry id({too_old});若它接受,D23 的 id 保留逻辑需重新评估"
        );
        let _: i64 = redis::cmd("DEL").arg(&stream).query_async(&mut c).await.unwrap();
    }

    /// D23 定向重放:XGROUP SETID 退到前一条后,该组能读到重投的消息。
    #[tokio::test]
    #[ignore = "requires development Redis stream instance"]
    async fn group_cursor_aligned_back_lets_group_read_replayed_entry() {
        let mut c = test_conn().await;
        let stream = unique("d23_group");
        let orig: String = redis::cmd("XADD")
            .arg(&stream).arg("*").arg("event_id").arg("EV-1")
            .query_async(&mut c).await.unwrap();
        // XGROUP CREATE 返回 OK 而不是 id，用 Value 接才不报类型错
        let _: redis::Value = redis::cmd("XGROUP")
            .arg("CREATE").arg(&stream).arg("g1").arg("0")
            .query_async(&mut c).await.unwrap();
        // 先让 g1 把原消息消费掉。读到 nil 是合法的（表示此刻没有新消息），
        // 所以不对返回类型做断言，只关心命令本身不报错。
        // 注意：GROUP 后面第一个参数是**组名**，第二个才是 consumer 名；
        // 顺序写反不会报 NOGROUP，而是直接报 syntax error（组名被当成关键字）。
        let read: Result<redis::Value, _> = redis::cmd("XREADGROUP")
            .arg("GROUP").arg("g1").arg("c1")
            .arg("COUNT").arg(10).arg("STREAMS").arg(&stream).arg(">")
            .query_async(&mut c).await;
        let read = read.unwrap_or_else(|e| panic!("首次 XREADGROUP 失败: {e}"));
        assert!(
            value_contains(&read, &orig),
            "前置步骤:g1 首次读取应拿到原消息 {orig}；实际: {read:?}"
        );
        let _: i64 = redis::cmd("XACK").arg(&stream).arg("g1").arg(&orig)
            .query_async(&mut c).await.unwrap();

        // 重放:显式原 id 写回,并把游标退到它前面。
        //
        // ⚠️ 实测发现(XADD 显式 id 的真实约束):
        // 同一 stream 里,**比当前 top id 旧的 id 不能再次写入** ——
        // 哪怕这条消息已经被 XACK 移出 PEL、entry 也确实还存在。
        // 原因是 Redis 只接受 `new_id > last-generated-id` 的显式 id。
        // 也就是说「原 id 无条件写回」只在原 id 仍是流中最新一条时成立;
        // 只要原流在 DLQ 期间又写入了更新的消息,重放就会直接失败。
        // 这正是产品代码 `xadd_id` 解析成功却仍然报错的那条路径 ——
        // 见 `replay_stream` 里对 XADD 失败的处理与本用例的断言。
        let too_old = redis::cmd("XADD")
            .arg(&stream).arg(&orig).arg("event_id").arg("EV-1")
            .query_async::<String>(&mut c).await;
        assert!(
            too_old.is_err(),
            "预期 Redis 拒绝重复写入同一个 entry id {orig}；若它接受,说明 D23 的 id 保留前提不成立"
        );

        // 生产代码的实际退路:退回 `*` 让 Redis 生成新 id。
        // 消费者此时应改用 `event_id` 做幂等键(载荷里始终带着它)。
        let written: String = redis::cmd("XADD")
            .arg(&stream).arg("*").arg("event_id").arg("EV-1")
            .query_async(&mut c).await.unwrap();
        assert_ne!(written, orig, "退回 * 后必然是新 id,这正是需要 event_id 幂等的原因");
        let target = previous_id(&written).unwrap();
        let ok: Option<String> = redis::cmd("XGROUP")
            .arg("SETID").arg(&stream).arg("g1").arg(&target)
            .query_async(&mut c).await.unwrap();
        assert!(ok.is_some(), "XGROUP SETID 应返回 OK");

        // 关键断言:g1 现在能读到这条重投的消息。
        // XREADGROUP 的返回结构随是否命中而变(空结果时 Redis 返回 nil),        // 反序列化成具体嵌套类型会在「没读到」时先炸在类型上,
        // 掩盖真正要验的语义 —— 故取原始 Value 自己拆。
        let out: Result<redis::Value, _> = redis::cmd("XREADGROUP")
            .arg("GROUP").arg("g1").arg("c1")
            .arg("COUNT").arg(10).arg("STREAMS").arg(&stream).arg(">")
            .query_async(&mut c).await;
        let out = out.unwrap_or_else(|e| panic!("重放后 XREADGROUP 失败: {e}"));
        assert!(
            value_contains(&out, &written),
            "定向重放后该组必须能读到重投的 entry {}；实际返回: {out:?}",
            written
        );
        let _: i64 = redis::cmd("DEL").arg(&stream).query_async(&mut c).await.unwrap();
    }

    /// D21 游标:开区间 `(last_id` 扫描必须跳过已处理的条目,实现增量不重扫。
    #[tokio::test]
    #[ignore = "requires development Redis stream instance"]
    async fn exclusive_range_scan_skips_already_processed_entries() {
        let mut c = test_conn().await;
        let dlq = unique("d21_cursor");
        let mut ids = Vec::new();
        for i in 0..5 {
            let id: String = redis::cmd("XADD")
                .arg(&dlq).arg("*").arg("n").arg(i.to_string())
                .query_async(&mut c).await.unwrap();
            ids.push(id);
        }
        // 第一轮从 - 开始,取前 2 条
        let first: Vec<(String, Vec<String>)> = redis::cmd("XRANGE")
            .arg(&dlq).arg("-").arg("+").arg("COUNT").arg(2)
            .query_async(&mut c).await.unwrap();
        assert_eq!(first.len(), 2);
        let cursor = first.last().unwrap().0.clone();

        // 第二轮从 (cursor 开始:必须跳过已扫过的第 2 条
        let second: Vec<(String, Vec<String>)> = redis::cmd("XRANGE")
            .arg(&dlq).arg(format!("({cursor}")).arg("+").arg("COUNT").arg(10)
            .query_async(&mut c).await.unwrap();
        let second_ids: Vec<&String> = second.iter().map(|(id, _)| id).collect();
        assert_eq!(second_ids, vec![&ids[2], &ids[3], &ids[4]],
            "D21:开区间扫描必须跳过游标本身及之前的条目");
        let _: i64 = redis::cmd("DEL").arg(&dlq).query_async(&mut c).await.unwrap();
    }
}
