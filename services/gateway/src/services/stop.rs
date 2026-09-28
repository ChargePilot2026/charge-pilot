//! 用户主动 STOP 能力域(P3)
//!
//! 语义(改动前必读):STOP 是**持久**的 —— 端口在设备计量 ACK 回来之前
//! 一直保持占用,断连与部分写入都只做重发,绝不假装已停止。
//! `meter_required: true` 的 STOP 帧必须带回 `meter`,否则不认。

// P5:本文件是 gateway 侧 **停止域的 repository 层**,SQL 只允许出现在这里
// (方案 §三)。本文件内剩下的 `Value` / `json!` 均落在方案 §三 例外清单第 1、2 类:
//   - 第 2 类:`charge_stop_command.meter_json` 原样透出(同一 STOP 重复 ACK
//     必须逐字节一致,重新序列化会破坏该判定);
//   - 第 1 类:`charge_ended` Redis Stream 事件载荷;
//   - 下发给设备的 STOP 帧载荷与 user 服务响应体同理,producer 侧不建模。
#![allow(clippy::disallowed_methods)]
#![allow(clippy::disallowed_types)]
#![allow(clippy::disallowed_macros)]

use api_contracts::{ChargeEndMeter, ChargeEndRequest, ChargeMeterSegment, ChargeStopResponse};
use chrono::{DateTime, Utc};
use common_app::ServiceBase;
use common_error::{AppError, AppResult};
use common_http::internal::ApiClient;
use common_redis::StreamEnvelope;
use serde_json::Value;
use sqlx::Row;

use crate::protocol::Frame;

#[derive(sqlx::FromRow)]
pub struct Stop {
    pub command_id: String,
    pub start_command_id: String,
    pub charge_order_id: u64,
    pub order_no: String,
    pub user_id: u64,
    pub device_id: String,
    pub port_no: u8,
    pub port_id: u64,
    pub status: String,
    pub session_id: Option<String>,
    pub sent_at: Option<chrono::NaiveDateTime>,
    pub meter_json: Option<Value>,
    pub result_reported: bool,
}

pub fn conflict() -> AppError {
    AppError::Conflict("停止请求与充电订单不一致".into())
}

/// 分段上限。与 billing `quote_pricing::daily_rates` 的 96 对齐 ——
/// 分段比这个数还多时,单价分段的实际意义已经消失,宁可交给人工也不猜。
pub const MAX_CHARGE_SEGMENTS: usize = 96;

/// 取窗口内 `meter_kwh` 累计读数,并额外带回**起始时刻之前最近的一条**。
///
/// 为什么要锚点:分段由相邻采样的读数差得到,而充电起点来自 START 指令、
/// 并不与任何一次采样对齐。不带锚点就会丢掉「起点 → 第一次采样」这段电量,
/// 而那段电量会被末段按**结束时刻**的费率计价 —— 方向性错价。锚点把这段
/// 差值保留下来,段边界钉在 `started_at` 上。
///
/// `value_num` 必须 CAST 成 DOUBLE:`sqlx` 的 `f64::compatible` 明确排除
/// DECIMAL(浮点语义不同),直接 `try_get::<f64>` 会在运行时 ColumnDecode 失败。
const SEGMENT_SAMPLES_SQL: &str = "SELECT CAST(value_num AS DOUBLE) AS value_num, ts FROM telemetry \
     WHERE device_id=? AND port_no=? AND metric='meter_kwh' AND ts<=? \
       AND ts>=COALESCE((SELECT MAX(a.ts) FROM telemetry a \
             WHERE a.device_id=? AND a.port_no=? AND a.metric='meter_kwh' AND a.ts<?),?) \
     ORDER BY ts ASC LIMIT ?";

/// 一条 `meter_kwh` 采样(累计读数,单位 kWh)。
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct MeterSample {
    pub ts: DateTime<Utc>,
    pub reading_kwh: f64,
}

/// `value_num` 是 DECIMAL(18,6),6 位小数;而 1 kWh = 10^6 mWh ——
/// 所以「读数 × 10^6」正好是整数 mWh。用整数刻度差分可完全避开浮点累积误差:
/// 直接对 f64 差值取整会在长会话里越漂越多,而漂掉的每一瓦时最终都要
/// 从某一段的金额里扣或加。
const KWH_TO_MWH: f64 = 1_000_000.0;

/// 累计读数(kWh)→ mWh 整数。非法读数(非有限 / 负数)返回 `None`。
fn reading_to_mwh(kwh: f64) -> Option<i64> {
    if !kwh.is_finite() || kwh < 0.0 {
        return None;
    }
    let scaled = (kwh * KWH_TO_MWH).round();
    if scaled > i64::MAX as f64 {
        return None;
    }
    Some(scaled as i64)
}

/// mWh → Wh 四舍五入(半进位)。全程整数,不再引入浮点。
fn mwh_to_wh(mwh: i64) -> u64 {
    ((mwh + 500) / 1000) as u64
}

/// 从 `meter_kwh` 采样序列推导分段电量 —— 纯函数,不碰数据库。
///
/// 返回 `None` = **无法可靠推导**,调用方应让 `segments` 留空;
/// 返回 `Some(vec![])` = 推导成功但这段时间确实没电(全零读数)。
/// 两者对 billing 的意义完全不同,所以不能用同一个空值表达。
///
/// 推导规则(逐条理由见各分支注释):
///   1. 窗口内(外加起点锚点)按 `ts` 升序
///   2. 至少 2 个采样点才能差分
///   3. 读数必须单调不减
///   4. 末段能量 = 总电量 − 各段能量(取整后)
///   5. 段边界 = 相邻采样 `ts`,末段延伸到 `ended_at`
///   6. 丢掉零能量段(重复上报同一读数是常态)
///   7. 段数超上限返回 `None`
pub fn segments_from_samples(
    started_at: DateTime<Utc>,
    ended_at: DateTime<Utc>,
    samples: &[MeterSample],
    charged_wh: u64,
) -> Option<Vec<ChargeMeterSegment>> {
    if started_at >= ended_at {
        return None;
    }
    // 同一毫秒的多条采样只留第一条:留哪条都是任意的,但必须确定性地
    // 只留一条,否则相邻同 `ts` 的采样会切出零长度段、且该段能量无法归属。
    let mut points: Vec<(DateTime<Utc>, i64)> = Vec::with_capacity(samples.len());
    for s in samples {
        if points.last().is_some_and(|(ts, _)| *ts == s.ts) {
            continue;
        }
        points.push((s.ts, reading_to_mwh(s.reading_kwh)?));
    }
    // 少于两个点就没有差分,推导不出任何电量。
    if points.len() < 2 {
        return None;
    }
    // 超上限不截断:截断出来的分段会看起来合法却算错钱。
    if points.len() > MAX_CHARGE_SEGMENTS {
        return None;
    }
    // 读数回退 = 设备重启或计量清零,后续差值全部不可信。
    // 宁可不做分段(billing 走单费率/人工),也不拿一段错误的电量去计价。
    if points.windows(2).any(|w| w[1].1 < w[0].1) {
        return None;
    }
    // 最后一个采样点晚于窗口上界 = 数据自相矛盾,不做猜测。
    if points.last().is_some_and(|(ts, _)| *ts > ended_at) {
        return None;
    }

    let mut segments: Vec<ChargeMeterSegment> = Vec::with_capacity(points.len());
    for pair in points.windows(2) {
        // 起点锚点早于 `started_at` 时必须钉回 `started_at`:
        // 充电前那段待机时间不该算进订单。
        let from = pair[0].0.max(started_at);
        let delta_wh = mwh_to_wh(pair[1].1 - pair[0].1);
        // 锚点落在窗口边界上（钳位后**终点**也等于 `started_at`，即该对
        // 采样时间为零）才会产生一段**零时长却带电量**的区间。费率按段起始
        // 分钟取,时长为零不等于电量为零,留着就等于让 billing 按一个查不到的
        // 费率计一整段电 —— 故先记成零时长段,由下一轮并入。
        // `ts` 已升序去重,`pair[0].0` 单调不减,所以这个条件只可能出现在第一轮。
        if pair[0].0 <= started_at && pair[1].0 <= started_at {
            segments.push(ChargeMeterSegment {
                started_at,
                ended_at: started_at,
                energy_wh: delta_wh,
            });
            continue;
        }
        match segments.last_mut() {
            // 上一轮留在窗口边界上的**零时长带电段**，并入本段起点。
            // 只认零时长：非零时长的段是合法边界切分，并进去会把真实费率切分抹平。
            Some(last) if last.started_at == last.ended_at => {
                last.energy_wh += delta_wh;
                last.ended_at = pair[1].0;
            }
            _ => segments.push(ChargeMeterSegment {
                started_at: from,
                ended_at: pair[1].0,
                energy_wh: delta_wh,
            }),
        }
    }
    let sampled_wh: u64 = segments.iter().map(|s| s.energy_wh).sum();
    // 采样差分之和超过设备上报的总电量 = 两套读数互相矛盾,不能「挑一个像对的」。
    if sampled_wh > charged_wh {
        return None;
    }
    // 末段吸收两件事:采样最后一次之后的电量,以及逐段取整的舍入残差。
    // 这样 `sum(segments) == charged_wh` **恒成立** —— billing 的
    // `calculate_segmented` 对这个等式做硬校验,差 1 Wh 就得转人工。
    segments.push(ChargeMeterSegment {
        started_at: points[points.len() - 1].0,
        ended_at,
        energy_wh: charged_wh - sampled_wh,
    });
    segments.retain(|s| s.energy_wh > 0);
    Some(segments)
}

#[derive(Clone)]
pub struct ChargeStopService {
    base: ServiceBase,
}

impl ChargeStopService {
    pub fn new(base: ServiceBase) -> Self {
        Self { base }
    }

    pub async fn load(&self, order_no: &str) -> AppResult<Option<Stop>> {
        Ok(sqlx::query_as("SELECT * FROM charge_stop_command WHERE order_no=?")
            .bind(order_no)
            .fetch_optional(self.base.pool())
            .await?)
    }

    /// 受理一次 STOP 请求(幂等)。
    pub async fn request(&self, req: &api_contracts::ChargeStopCommand) -> AppResult<ChargeStopResponse> {
        if req.order_no.is_empty()
            || req.order_no.len() > 64
            || req.user_id == 0
            || req.source != "user_app"
        {
            return Err(AppError::BadRequest("停止请求无效".into()));
        }
        let mut saved = self.load(&req.order_no).await?;
        if saved.is_none() {
            let start = sqlx::query(
                "SELECT charge_order_id,user_id FROM charge_command WHERE order_no=? AND result_reported=TRUE AND status='acked' AND owns_port=TRUE",
            )
            .bind(&req.order_no)
            .fetch_optional(self.base.pool())
            .await?
            .ok_or_else(conflict)?;
            let cid: u64 = start.try_get("charge_order_id")?;
            if start.try_get::<u64, _>("user_id")? != req.user_id {
                return Err(conflict());
            }
            let order: api_contracts::orders::OrderDetail = self
                .base
                .new_client()
                .get(
                    self.base.cfg().service_urls.user.as_deref(),
                        &api_contracts::fill_path(
                            api_contracts::paths::USER_INTERNAL_ORDER_DETAIL,
                            "order_id",
                            &cid.to_string(),
                        ),
                    &(),
                )
                .await?;
            if order.order.order_no != req.order_no
                || order.order.user_id != req.user_id
                || order.order.status != "charging"
            {
                return Err(conflict());
            }
            let count = self.insert_stop_command(cid).await?;
            saved = self.load(&req.order_no).await?;
            if count == 0 && saved.is_none() {
                return Err(conflict());
            }
        }
        let saved = saved.ok_or_else(conflict)?;
        if saved.user_id != req.user_id || saved.order_no != req.order_no {
            return Err(conflict());
        }
        Ok(ChargeStopResponse {
            accepted: true,
            // `stopped` 只在计量结果已上报时为真,受理 ≠ 已停止。
            stopped: saved.result_reported,
            command_id: saved.command_id,
        })
    }

    /// 从已确认的 START 指令派生 STOP 指令。返回插入行数。
    async fn insert_stop_command(&self, charge_order_id: u64) -> AppResult<u64> {
        let mut tx = self.base.begin().await?;
        let count = sqlx::query(
            "INSERT IGNORE INTO charge_stop_command (command_id,start_command_id,charge_order_id,order_no,user_id,device_id,port_no,port_id) \
             SELECT ?,c.command_id,c.charge_order_id,c.order_no,c.user_id,c.device_id,c.port_no,c.port_id \
             FROM charge_command c JOIN device_port p ON p.id=c.port_id \
             WHERE c.charge_order_id=? AND c.status='acked' AND c.result_reported=TRUE \
             AND p.current_order_id=c.order_no AND p.status='charging'",
        )
        .bind(uuid::Uuid::new_v4().to_string())
        .bind(charge_order_id)
        .execute(tx.executor())
        .await?
        .rows_affected();
        tx.commit().await?;
        Ok(count)
    }

    /// 推进一步 STOP 状态机。
    pub async fn drive(
        &self,
        connections: &crate::protocol::connections::Connections,
        order_no: &str,
    ) -> AppResult<()> {
        let c = self.load(order_no).await?.ok_or_else(conflict)?;
        if c.result_reported {
            return Ok(());
        }
        if c.status == "pending" || c.status == "sent" {
            if c.sent_at
                .is_some_and(|t| chrono::Utc::now().naive_utc() - t < chrono::Duration::seconds(2))
            {
                return Ok(());
            }
            if let Some(session) = connections.get(&c.device_id).await {
                let changed = sqlx::query(
                    "UPDATE charge_stop_command SET status='sent',session_id=?,sent_at=UTC_TIMESTAMP(3) WHERE command_id=? AND status IN ('pending','sent') AND (sent_at IS NULL OR sent_at<UTC_TIMESTAMP(3)-INTERVAL 2 SECOND)",
                )
                .bind(&session.id)
                .bind(&c.command_id)
                .execute(self.base.pool())
                .await?
                .rows_affected();
                if changed == 1 {
                    let frame = Frame {
                        device_id: c.device_id,
                        port_no: Some(c.port_no),
                        msg_type: "cmd".into(),
                        ts: chrono::Utc::now(),
                        payload: serde_json::json!({"command":"STOP","command_id":c.command_id,"order_no":c.order_no,"meter_required":true}),
                    };
                    // 断连/部分写入之后重发同一条 STOP,绝不假装已停止。
                    let _ = session.send(&frame).await;
                }
            }
            return Ok(());
        }
        let meter: ChargeEndMeter =
            serde_json::from_value(c.meter_json.clone().ok_or_else(conflict)?)?;
        let req = ChargeEndRequest {
            order_no: c.order_no.clone(),
            start_command_id: c.start_command_id.clone(),
            stop_command_id: c.command_id.clone(),
            device_id: c.device_id.clone(),
            port_no: c.port_no,
            port_id: c.port_id,
            meter: meter.clone(),
        };
        let result: Value = self
            .base
            .new_client()
            .post(
                self.base.cfg().service_urls.user.as_deref(),
                // D24:走 fill_path —— 占位符名与常量对不上时直接 panic,
                // 不再静默发出带字面量 `:order_no` 的坏 URL。
                &api_contracts::fill_path(
                    api_contracts::paths::USER_INTERNAL_END_RESULT,
                    "order_no",
                    &c.order_no,
                ),
                &req,
            )
            .await?;
        if result.get("ok").and_then(|v| v.as_bool()) != Some(true) {
            return Err(conflict());
        }
        self.finish(&c, &meter).await
    }

    /// 释放端口、投 `charge_ended` 事件、标记已上报 —— 同一事务。
    async fn finish(&self, c: &Stop, meter: &ChargeEndMeter) -> AppResult<()> {
        let mut tx = self.base.begin().await?;
        let reported: bool = sqlx::query_scalar(
            "SELECT result_reported FROM charge_stop_command WHERE command_id=? FOR UPDATE",
        )
        .bind(&c.command_id)
        .fetch_one(tx.executor())
        .await?;
        if !reported {
            sqlx::query("UPDATE device_port SET status=IF(status='charging','idle',status),current_order_id=NULL WHERE id=? AND current_order_id=?")
                .bind(c.port_id).bind(&c.order_no).execute(tx.executor()).await?;
            let mut event = StreamEnvelope::new(
                "charge_ended",
                "gateway",
                serde_json::json!({
                    "charge_order_id": c.charge_order_id, "order_no": c.order_no, "user_id": c.user_id,
                    "device_id": c.device_id, "port_no": c.port_no, "stop_command_id": c.command_id,
                    "charged_wh": meter.charged_wh,
                    "charged_kwh": format!("{}.{:03}", meter.charged_wh / 1000, meter.charged_wh % 1000),
                    "charged_seconds": meter.charged_seconds, "ended_at": meter.ended_at,
                }),
            );
            event.event_id = c.command_id.clone();
            sqlx::query("INSERT INTO event_outbox (event_id,stream,envelope_json) VALUES (?,?,?)")
                .bind(&event.event_id)
                .bind(common_redis::streams::CHARGE_ENDED)
                .bind(serde_json::to_value(&event)?)
                .execute(tx.executor())
                .await?;
            sqlx::query("UPDATE charge_stop_command SET result_reported=TRUE WHERE command_id=?")
                .bind(&c.command_id)
                .execute(tx.executor())
                .await?;
        }
        tx.commit().await?;
        Ok(())
    }

    /// 从 telemetry 的 meter_kwh 累计读数推导分段电量。
    ///
    /// 返回 `None` 表示**无法可靠推导**(设备未上报 meter_kwh / 采样不足 /
    /// 读数非单调),此时 `segments` 留空,由 billing 决定后续处理。
    ///
    /// 只读一次 telemetry,不开事务:ACK 路径已有的锁是 `charge_stop_command`
    /// 那一行,为多一次 SELECT 包事务只会拉长锁持有时间,没有收益。
    ///
    /// gateway 侧 repository 层,SQL 归属于此(见仓库根 clippy.toml)。
    #[allow(clippy::disallowed_methods)]
    pub async fn derive_segments(
        &self,
        device_id: &str,
        port_no: u8,
        started_at: DateTime<Utc>,
        ended_at: DateTime<Utc>,
        charged_wh: u64,
    ) -> AppResult<Option<Vec<ChargeMeterSegment>>> {
        if started_at >= ended_at {
            return Ok(None);
        }
        let rows = sqlx::query(SEGMENT_SAMPLES_SQL)
            .bind(device_id)
            .bind(port_no)
            .bind(ended_at)
            .bind(device_id)
            .bind(port_no)
            .bind(started_at)
            .bind(started_at)
            // +1:给起点锚点留位置,否则锚点本身会把上限挤掉一个。
            .bind(MAX_CHARGE_SEGMENTS as u64 + 1)
            .fetch_all(self.base.pool())
            .await?;
        let samples: Vec<MeterSample> = rows
            .iter()
            .filter_map(|r| {
                let ts: DateTime<Utc> = r.try_get("ts").ok()?;
                // value_num 允许为 NULL;跳过即可,不因单条脏数据丢掉整段推导。
                let value_num: Option<f64> = r.try_get("value_num").ok()?;
                Some(MeterSample { ts, reading_kwh: value_num? })
            })
            .collect();
        Ok(segments_from_samples(started_at, ended_at, &samples, charged_wh))
    }

    /// START 指令的发出时刻(服务端时钟),即分段推导窗口的起点。
    ///
    /// gateway 侧 repository 层,SQL 归属于此(见仓库根 clippy.toml)。
    #[allow(clippy::disallowed_methods)]
    async fn started_at(&self, command_id: &str) -> AppResult<Option<DateTime<Utc>>> {
        let row = sqlx::query("SELECT created_at FROM charge_command WHERE command_id=?")
            .bind(command_id)
            .fetch_optional(self.base.pool())
            .await?;
        Ok(row.and_then(|r| r.try_get::<DateTime<Utc>, _>("created_at").ok()))
    }

    /// 处理设备 STOP ACK。返回 `true` 表示这条 ACK 属于 STOP 指令。
    pub async fn acknowledge(&self, session: &str, frame: &Frame) -> AppResult<bool> {
        let Some(id) = frame.payload.get("command_id").and_then(|v| v.as_str()) else {
            return Ok(false);
        };
        let mut tx = self.base.begin().await?;
        let c: Option<Stop> = sqlx::query_as(
            "SELECT * FROM charge_stop_command WHERE command_id=? FOR UPDATE",
        )
        .bind(id)
        .fetch_optional(tx.executor())
        .await?;
        let Some(c) = c else {
            return Ok(false);
        };
        if c.device_id != frame.device_id
            || Some(c.port_no) != frame.port_no
            || c.session_id.as_deref() != Some(session)
            || frame.payload.get("command").and_then(|v| v.as_str()) != Some("STOP")
        {
            return Err(conflict());
        }
        let success = frame
            .payload
            .get("success")
            .and_then(|v| v.as_bool())
            .ok_or_else(conflict)?;
        if !success {
            return Ok(true);
        }
        let meter: ChargeEndMeter =
            serde_json::from_value(frame.payload.get("meter").cloned().ok_or_else(conflict)?)
                .map_err(|_| conflict())?;
        if meter.charged_wh > 100_000_000
            || meter.charged_seconds > 604800
            || meter.ended_at > chrono::Utc::now() + chrono::Duration::minutes(5)
        {
            return Err(conflict());
        }
        // 设备固件当前只回报总量。分段由服务端从 telemetry 累计读数差分推导,
        // **只在 ACK 这一刻算一次**:之后 telemetry 会被新的 30s 采样填满,
        // 重算会与已入库的 meter_json 不一致,进而被下面「重复 ACK 必须一致」拦下。
        let mut meter = meter;
        if meter.segments.is_empty() {
            // 起点取 START 指令的创建时刻(charge_command.created_at):那是
            // gateway 自己写的 UTC 时间戳,不受设备时钟漂移影响。推导失败
            // 不阻断 ACK —— 停止本身是既成事实,计费降级由 billing 处理。
            if let Ok(Some(started_at)) = self.started_at(&c.start_command_id).await {
                if let Ok(Some(segments)) = self
                    .derive_segments(
                        &c.device_id,
                        c.port_no,
                        started_at,
                        meter.ended_at,
                        meter.charged_wh,
                    )
                    .await
                {
                    meter.segments = segments;
                }
            }
        }
        let value = serde_json::to_value(&meter)?;
        // 同一 STOP 重复 ACK 时计量读数必须完全一致,否则拒绝覆盖。
        if c.meter_json.as_ref().is_some_and(|v| v != &value) {
            return Err(conflict());
        }
        sqlx::query("UPDATE charge_stop_command SET status='acked',meter_json=? WHERE command_id=?")
            .bind(value)
            .bind(id)
            .execute(tx.executor())
            .await?;
        tx.commit().await?;
        Ok(true)
    }

    /// 重启后捞回未上报的 STOP 继续推进。
    pub async fn unreported_after(&self, cursor: u64) -> AppResult<Vec<(u64, String)>> {
        Ok(sqlx::query_as(
            "SELECT charge_order_id,order_no FROM charge_stop_command WHERE result_reported=FALSE AND charge_order_id>? ORDER BY charge_order_id LIMIT 50",
        )
        .bind(cursor)
        .fetch_all(self.base.pool())
        .await?)
    }
}

#[cfg(test)]
mod segment_tests {
    use super::*;

    fn at(text: &str) -> DateTime<Utc> {
        text.parse().unwrap()
    }

    fn sample(offset_secs: i64, reading_kwh: f64) -> MeterSample {
        MeterSample { ts: at("2026-09-26T11:00:00Z") + chrono::Duration::seconds(offset_secs), reading_kwh }
    }

    fn energies(segments: &[ChargeMeterSegment]) -> Vec<u64> {
        segments.iter().map(|s| s.energy_wh).collect()
    }

    fn total(segments: &[ChargeMeterSegment]) -> u64 {
        segments.iter().map(|s| s.energy_wh).sum()
    }

    /// 正常路径:采样单调递增 → 段边界落在相邻采样上,末段补齐到结束时刻。
    #[test]
    fn monotonic_samples_split_into_segments() {
        let start = at("2026-09-26T11:00:00Z");
        let end = at("2026-09-26T11:10:00Z");
        let segments = segments_from_samples(
            start,
            end,
            &[sample(0, 1.0), sample(120, 1.1), sample(240, 1.5)],
            800,
        )
        .expect("读数单调,应能推导");
        // 前两段是采样差值,末段 = 总电量 − 差值之和(=最后一次采样之后的电量)。
        assert_eq!(energies(&segments), vec![100, 400, 300]);
        assert_eq!(total(&segments), 800);
        assert_eq!(segments[0].started_at, start);
        assert_eq!(segments[1].ended_at, at("2026-09-26T11:04:00Z"));
        assert_eq!(segments[2].started_at, at("2026-09-26T11:04:00Z"));
        assert_eq!(segments[2].ended_at, end);
    }

    /// 段间不允许重叠/跳空:末一段从上一段结束时刻接上,直到 `ended_at`。
    #[test]
    fn segments_are_contiguous_and_end_at_window_end() {
        let start = at("2026-09-26T11:00:00Z");
        let end = at("2026-09-26T11:05:00Z");
        let segments = segments_from_samples(
            start,
            end,
            &[sample(0, 0.5), sample(60, 0.6), sample(120, 0.7)],
            250,
        )
        .unwrap();
        assert_eq!(segments.first().unwrap().started_at, start);
        assert_eq!(segments.last().unwrap().ended_at, end);
        for pair in segments.windows(2) {
            assert_eq!(pair[0].ended_at, pair[1].started_at, "相邻段必须首尾相接");
        }
    }

    /// 只有 1 个采样点 → 无法差分 → `None`。
    #[test]
    fn single_sample_is_not_derivable() {
        let start = at("2026-09-26T11:00:00Z");
        let end = at("2026-09-26T11:10:00Z");
        assert!(segments_from_samples(start, end, &[sample(0, 1.0)], 500).is_none());
        assert!(segments_from_samples(start, end, &[], 500).is_none());
    }

    /// 读数回退(设备重启/计量清零)→ `None`,不能静默截断为 0。
    #[test]
    fn reading_going_backwards_is_rejected() {
        let start = at("2026-09-26T11:00:00Z");
        let end = at("2026-09-26T11:10:00Z");
        assert!(segments_from_samples(
            start,
            end,
            &[sample(0, 3.0), sample(120, 1.0), sample(240, 1.2)],
            900
        )
        .is_none());
    }

    /// 全程同一读数(设备重复上报)→ 推导成功但为空 vec,**不是** `None`:
    /// 两者对 billing 的意义不同。
    #[test]
    fn identical_readings_yield_empty_segments() {
        let start = at("2026-09-26T11:00:00Z");
        let end = at("2026-09-26T11:10:00Z");
        let segments = segments_from_samples(
            start,
            end,
            &[sample(0, 7.5), sample(120, 7.5), sample(240, 7.5)],
            0,
        )
        .expect("读数一致是可解释的情形,不是推导失败");
        assert!(segments.is_empty());
    }

    /// 段数超上限 → `None`。边界值 96 个采样点必须仍然能推导。
    #[test]
    fn too_many_samples_is_rejected_instead_of_truncated() {
        let start = at("2026-09-26T11:00:00Z");
        let end = at("2026-09-26T13:00:00Z");
        let many: Vec<MeterSample> = (0..=MAX_CHARGE_SEGMENTS as i64)
            .map(|i| sample(i * 60, 1.0 + i as f64 * 0.01))
            .collect();
        assert!(
            segments_from_samples(start, end, &many, 2000).is_none(),
            "97 个采样点超出上限,必须整体放弃而不是截断"
        );
        let at_limit = &many[..=MAX_CHARGE_SEGMENTS - 1];
        assert!(segments_from_samples(start, end, at_limit, 2000).is_some());
    }

    /// kWh → Wh 的单位换算含小数:1.2345 kWh 应得到 1235 Wh(四舍五入)。
    #[test]
    fn kwh_to_wh_conversion_keeps_decimals() {
        assert_eq!(reading_to_mwh(1.2345), Some(1_234_500));
        assert_eq!(mwh_to_wh(1_234_500), 1235);
        assert_eq!(mwh_to_wh(1_234_499), 1234);
        assert_eq!(mwh_to_wh(1_234_500), 1235);
        assert_eq!(mwh_to_wh(0), 0);
        // 整个窗口只涨了 0.0005 kWh = 0.5 Wh,四舍五入后仍为 0 段。
        let start = at("2026-09-26T11:00:00Z");
        let end = at("2026-09-26T11:02:00Z");
        let segments = segments_from_samples(
            start,
            end,
            &[sample(0, 1.2345), sample(120, 1.2350)],
            1,
        )
        .unwrap();
        assert_eq!(energies(&segments), vec![1]);
        assert_eq!(segments[0].ended_at, at("2026-09-26T11:02:00Z"));
    }

    /// 总电量与采样差分矛盾时必须放弃:无论信哪一套都会算错钱。
    #[test]
    fn total_smaller_than_sampled_sum_is_rejected() {
        let start = at("2026-09-26T11:00:00Z");
        let end = at("2026-09-26T11:10:00Z");
        assert!(segments_from_samples(
            start,
            end,
            &[sample(0, 1.0), sample(120, 2.0)],
            999
        )
        .is_none());
    }

    /// 起点锚点早于充电开始时,首段起点必须钉回 `started_at`,
    /// 否则会把充电前的待机时间计到订单头上。
    ///
    /// 本用例:锚点在 −120s(读数 1.0)、充电起点 0s(1.2)、300s(1.5)。
    /// 锚点→起点 的 200 Wh 落在 `started_at` **之前**,钳位后并入 [0s, 300s]
    /// 这一段(而不是变成一段零时长带电量的区间);300s 之后末段为 0,
    /// 被 `retain(energy_wh > 0)` 滤除。
    #[test]
    fn anchor_before_window_start_is_clamped() {
        let start = at("2026-09-26T11:00:00Z");
        let end = at("2026-09-26T11:06:00Z");
        let segments = segments_from_samples(
            start,
            end,
            &[sample(-120, 1.0), sample(0, 1.2), sample(300, 1.5)],
            500,
        )
        .unwrap();
        assert_eq!(segments[0].started_at, start);
        assert_eq!(segments[0].ended_at, at("2026-09-26T11:05:00Z"));
        // 锚点与充电起点之间的 200 Wh 并进第一段,总量仍守恒。
        assert_eq!(energies(&segments), vec![500]);
        assert_eq!(total(&segments), 500);
    }

    /// 非法读数(非有限值 / 负数)不得参与推导。
    #[test]
    fn invalid_readings_are_rejected() {
        assert_eq!(reading_to_mwh(f64::NAN), None);
        assert_eq!(reading_to_mwh(f64::INFINITY), None);
        assert_eq!(reading_to_mwh(-0.5), None);
        let start = at("2026-09-26T11:00:00Z");
        let end = at("2026-09-26T11:06:00Z");
        assert!(segments_from_samples(
            start,
            end,
            &[sample(0, 1.0), sample(120, f64::NAN)],
            300
        )
        .is_none());
    }

    /// 同一毫秒的多条采样必须被合并,否则会切出零长度段。
    #[test]
    fn duplicate_timestamps_are_deduplicated() {
        let start = at("2026-09-26T11:00:00Z");
        let end = at("2026-09-26T11:06:00Z");
        let segments = segments_from_samples(
            start,
            end,
            &[sample(0, 1.0), sample(0, 1.0), sample(60, 1.4)],
            400,
        )
        .unwrap();
        for pair in segments.windows(2) {
            assert!(pair[1].started_at > pair[0].ended_at, "不允许零长度段");
        }
        assert_eq!(total(&segments), 400);
    }

    /// 窗口空转(`started_at >= ended_at`)没有可推导的区间。
    #[test]
    fn empty_window_is_not_derivable() {
        let start = at("2026-09-26T11:00:00Z");
        assert!(segments_from_samples(start, start, &[sample(0, 1.0), sample(60, 2.0)], 1000).is_none());
        assert!(segments_from_samples(start, start - chrono::Duration::seconds(1), &[sample(0, 1.0)], 1000).is_none());
    }

    /// 推导成功时 `sum(segments) == charged_wh` **恒成立** ——
    /// billing 的 `calculate_segmented` 对这条等式做硬校验。
    #[test]
    fn segments_always_sum_to_total_wh() {
        let start = at("2026-09-26T11:00:00Z");
        let end = at("2026-09-26T11:40:00Z");
        // 大量非整瓦的差值,逼出取整舍入残差。
        let samples: Vec<MeterSample> = (0..20)
            .map(|i| sample(i * 120, 1.0 + i as f64 * 0.00333))
            .collect();
        let charged = 1337;
        let segments = segments_from_samples(start, end, &samples, charged).unwrap();
        assert_eq!(total(&segments), charged);
    }
}
