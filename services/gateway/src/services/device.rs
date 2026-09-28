//! 设备能力域(P3)
//!
//! `db` 私有,handler 拿不到裸 pool。所有原 `st.db.pool()` 的调用点改为
//! 调用本对象的方法或取一个显式事务句柄(`Tx`),SQL 不再出现在 handler 里。
//!
//! 事务边界的取舍:**跨多表的写路径**(`port_occupy` / `record_frames` /
//! `close_idle_sessions` / `command_acknowledge`)由本对象自己开事务,
//! 保证"锁 + 判定 + 写"不会被调用方拆开;**只读查询**直接走 pool,
//! 不必为一次 SELECT 付出事务开销。
//!
//! P5:本文件是 gateway 侧**设备域的 repository 层**,SQL 只允许出现在这里
//! (方案 §三:handler/usecase 层禁 SQL,由 clippy `disallowed-methods` 保证)。
//! 编排层(`provision.rs` / `registration.rs` / `telemetry_obs.rs` /
//! `outbox.rs`)原本直写的 SQL 已按原样下沉到本文件,handler 只做校验与编排。
#![allow(clippy::disallowed_methods)]
// `Value` 仅用于 `device_provision.request_json` 列原样透出(方案 §三
// 例外清单第 2 类):该列存的是整份导入请求,重建档时必须逐字节与请求比对,
// 建模成 `DeviceProvision` 反而会在字段新增时把存量行卡成不可反序列化。
#![allow(clippy::disallowed_types)]

use api_contracts::devices::{DeviceProvision, ProvisionedDevice, ProvisionedPort};
use api_contracts::gateway_devices as gd;
use chrono::Datelike;
use common_app::ServiceBase;
use common_db::Tx;
use common_error::{AppError, AppResult};
use serde::Deserialize;
use sqlx::Row;

/// 物理设备注册请求(P5:从 `registration.rs` 搬入)。
///
/// 随请求下沉是因为 `register()` 的全部校验都要在拿到事务句柄**之后**
/// 按数据库实际行做判定(`station_id` / `model` 与建档值是否一致),
/// handler 无法拆开"拼装请求 → 开事务 → 比对"。DTO 在此定义、handler `pub use`
/// 转出,对外的请求体形状不变。
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RegisterRequest {
    pub device_id: String,
    pub vendor_id: u64,
    pub station_id: Option<u64>,
    pub port_count: u8,
    pub model: Option<String>,
    pub firmware_version: Option<String>,
    pub mac_addr: Option<String>,
    #[serde(default = "register_connect_type_default")]
    pub connect_type: String,
    pub client_ip: Option<String>,
}

/// `connect_type` 缺省值。放在 repository 层是因为 DTO 的
/// `#[serde(default)]` 必须指向一个可见的函数路径。
pub fn register_connect_type_default() -> String {
    "tcp".into()
}

/// 一次补传帧解析出的单条遥测测量。
pub struct Measurement<'a> {
    pub device_id: &'a str,
    pub port_no: u8,
    pub metric: &'static str,
    pub value: f64,
    pub ts: chrono::DateTime<chrono::Utc>,
}

#[derive(Clone)]
pub struct DeviceService {
    base: ServiceBase,
}

impl DeviceService {
    pub fn new(base: ServiceBase) -> Self {
        Self { base }
    }

    // ===== 健康检查 =====

    pub async fn ping(&self) -> AppResult<()> {
        self.base.ping().await
    }

    // ===== 设备 / 端口只读 =====

    pub async fn device_summary(&self, device_id: &str) -> AppResult<gd::DeviceSummary> {
        let row: Option<(String, u64, u8, String, Option<String>)> = sqlx::query_as(
            "SELECT device_id, vendor_id, port_count, status, firmware_version FROM device WHERE device_id = ? AND deleted_at IS NULL",
        )
        .bind(device_id)
        .fetch_optional(self.base.pool())
        .await?;
        let row = row.ok_or_else(|| AppError::NotFound("device".into()))?;
        Ok(gd::DeviceSummary {
            device_id: row.0,
            vendor_id: row.1,
            port_count: row.2,
            status: row.3,
            firmware_version: row.4,
        })
    }

    pub async fn device_ports(&self, device_id: &str) -> AppResult<Vec<gd::DevicePort>> {
        let rows = sqlx::query(
            "SELECT id, port_no, port_code, status FROM device_port WHERE device_id = ? AND deleted_at IS NULL",
        )
        .bind(device_id)
        .fetch_all(self.base.pool())
        .await?;
        rows.iter()
            .map(|r| {
                Ok(gd::DevicePort {
                    id: r.try_get("id")?,
                    port_no: r.try_get("port_no")?,
                    port_code: r.try_get("port_code")?,
                    status: r.try_get("status")?,
                })
            })
            .collect()
    }

    // ===== 扫码解析(只读,绝不预留端口或建单)=====

    async fn scan_ports(tx: &mut Tx<'_>, code: &str, by_device: bool) -> AppResult<Vec<api_contracts::ScanPortDetail>> {
        let filter = if by_device { "p.device_id" } else { "p.port_code" };
        let rows = sqlx::query(&format!(
            "SELECT p.device_id,p.port_no,p.port_code,IF(p.status='idle' AND p.current_order_id IS NOT NULL,'reserved',p.status) AS status FROM device_port p \
             WHERE {filter}=? AND p.deleted_at IS NULL AND EXISTS \
             (SELECT 1 FROM device d JOIN vendor v ON v.id=d.vendor_id \
              WHERE d.device_id=p.device_id AND d.deleted_at IS NULL AND d.status='enabled' \
              AND v.deleted_at IS NULL AND v.status='enabled') ORDER BY p.port_no,p.id"
        )).bind(code).fetch_all(tx.executor()).await?;
        if !by_device && rows.len() > 1 {
            return Err(AppError::Conflict("端口数据重复，请联系运营人员".into()));
        }
        let mut result = Vec::with_capacity(rows.len());
        for row in rows {
            let port_code: String = row.try_get("port_code")?;
            let port_no = row.try_get("port_no")?;
            if result.iter().any(|p: &api_contracts::ScanPortDetail| p.port_code == port_code || (by_device && p.port_no == port_no)) {
                return Err(AppError::Conflict("端口数据重复，请联系运营人员".into()));
            }
            result.push(api_contracts::ScanPortDetail { port_id: port_code.clone(), port_code,
                device_id: row.try_get("device_id")?, port_no, status: row.try_get("status")? });
        }
        Ok(result)
    }

    /// 扫码解析:先按 `port_code` 找端口,找不到再按 `device_id` 找设备及其全部端口。
    pub async fn scan_resolve(&self, code: &str) -> AppResult<api_contracts::ScanResolveResponse> {
        let mut tx = self.base.begin().await?;
        let mut found = Self::scan_ports(&mut tx, code, false).await?;
        let data = if let Some(port) = found.pop() {
            api_contracts::ScanResolveResponse::Port { port }
        } else {
            let devices: Vec<(String, String)> = sqlx::query_as(
                "SELECT d.device_id,d.status FROM device d JOIN vendor v ON v.id=d.vendor_id \
                 WHERE d.device_id=? AND d.deleted_at IS NULL AND d.status='enabled' \
                 AND v.deleted_at IS NULL AND v.status='enabled'"
            ).bind(code).fetch_all(tx.executor()).await?;
            if devices.len() > 1 { return Err(AppError::Conflict("设备数据重复，请联系运营人员".into())); }
            let (device_id, status) = devices.into_iter().next().ok_or_else(|| AppError::NotFound("设备或端口不存在或已停用".into()))?;
            let ports = Self::scan_ports(&mut tx, &device_id, true).await?;
            api_contracts::ScanResolveResponse::Device { device_id, status, ports }
        };
        tx.commit().await?;
        Ok(data)
    }

    /// 按 `port_code` 精确取单个端口。
    pub async fn scan_port(&self, code: &str) -> AppResult<api_contracts::ScanPortDetail> {
        let mut tx = self.base.begin().await?;
        let port = Self::scan_ports(&mut tx, code, false).await?.pop()
            .ok_or_else(|| AppError::NotFound("端口不存在或设备已停用".into()))?;
        tx.commit().await?;
        Ok(port)
    }

    // ===== 遥测 =====

    /// 落一批遥测并同步刷新聚合表。整批在**同一事务**内,避免部分落库。
    pub async fn record_measurements(&self, measurements: &[Measurement<'_>]) -> AppResult<usize> {
        let mut tx = self.base.begin().await?;
        for m in measurements {
            sqlx::query("INSERT INTO telemetry (device_id, port_no, metric, value_num, ts) VALUES (?, ?, ?, ?, ?)")
                .bind(m.device_id).bind(m.port_no).bind(m.metric).bind(m.value).bind(m.ts)
                .execute(tx.executor()).await?;
            Self::aggregate_measurement(&mut tx, m.device_id, m.port_no, m.metric, m.value, m.ts).await?;
        }
        tx.commit().await?;
        Ok(measurements.len())
    }

    /// 把一条**实际采样**写进 15 分钟与小时两张聚合表,与它的原始
    /// `telemetry` 行同事务。重放/补传的采样也计次。
    ///
    /// P5:原在 `telemetry_obs.rs`,与 `record_measurements` 是同一条写路径,
    /// 下沉到同一 repository 内避免 handler 反手开事务再回调。
    async fn aggregate_measurement(
        tx: &mut Tx<'_>,
        device_id: &str,
        port_no: u8,
        metric: &str,
        value: f64,
        at: chrono::DateTime<chrono::Utc>,
    ) -> AppResult<()> {
        if device_id.is_empty() || port_no == 0 || !value.is_finite() {
            return Err(AppError::BadRequest("遥测聚合数据无效".into()));
        }
        let seconds = at.timestamp();
        for (table, width) in [
            ("telemetry_aggregate_15min", 15 * 60),
            ("telemetry_aggregate_hourly", 60 * 60),
        ] {
            let bucket_seconds = seconds.div_euclid(width) * width;
            let bucket = chrono::DateTime::<chrono::Utc>::from_timestamp(bucket_seconds, 0)
                .ok_or_else(|| AppError::BadRequest("遥测时间超出支持范围".into()))?;
            let bucket_month = chrono::NaiveDate::from_ymd_opt(bucket.year(), bucket.month(), 1)
                .ok_or_else(|| AppError::Internal("invalid telemetry aggregate month".into()))?;
            let sql = format!(
                "INSERT INTO {table} (device_id, port_no, metric, bucket_start, bucket_month, avg_value, min_value, max_value, `count`)
                 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)
                 ON DUPLICATE KEY UPDATE
                   avg_value = ((avg_value * `count`) + VALUES(avg_value)) / (`count` + 1),
                   min_value = LEAST(min_value, VALUES(min_value)),
                   max_value = GREATEST(max_value, VALUES(max_value)),
                   `count` = `count` + 1"
            );
            sqlx::query(&sql)
                .bind(device_id)
                .bind(port_no)
                .bind(metric)
                .bind(bucket)
                .bind(bucket_month)
                .bind(value)
                .bind(value)
                .bind(value)
                .execute(tx.executor())
                .await?;
        }
        Ok(())
    }

    /// 近 2 分钟原始采样(取每个指标的最新值)。
    pub async fn recent_samples(
        &self,
        device_id: &str,
        port_no: u8,
    ) -> AppResult<Vec<gd::TelemetrySample>> {
        let rows = sqlx::query(
            "SELECT metric, value_num, ts FROM telemetry \
             WHERE device_id = ? AND port_no = ? AND ts >= NOW() - INTERVAL 2 MINUTE \
             ORDER BY ts DESC LIMIT 100",
        )
        .bind(device_id)
        .bind(port_no)
        .fetch_all(self.base.pool())
        .await?;
        rows.iter()
            .map(|r| {
                Ok(gd::TelemetrySample {
                    metric: r.try_get("metric")?,
                    value_num: r.try_get("value_num")?,
                    ts: r.try_get::<chrono::DateTime<chrono::Utc>, _>("ts")?.to_rfc3339(),
                })
            })
            .collect()
    }

    /// 按采样间隔分桶的曲线原始行。
    pub async fn curve_samples(
        &self,
        device_id: &str,
        port_no: u8,
        from: chrono::DateTime<chrono::Utc>,
        to: chrono::DateTime<chrono::Utc>,
        interval_seconds: i64,
    ) -> AppResult<Vec<(String, Option<f64>, chrono::DateTime<chrono::Utc>)>> {
        let rows = sqlx::query(
            "SELECT metric, AVG(value_num) AS value_num, FROM_UNIXTIME(bucket * ?) AS ts
             FROM (
               SELECT metric, value_num, FLOOR(UNIX_TIMESTAMP(ts) / ?) AS bucket
               FROM telemetry WHERE device_id = ? AND port_no = ? AND ts >= ? AND ts <= ?
             ) AS samples
             GROUP BY metric, bucket ORDER BY bucket ASC, metric ASC LIMIT 5000",
        )
        .bind(interval_seconds)
        .bind(interval_seconds)
        .bind(device_id)
        .bind(port_no)
        .bind(from)
        .bind(to)
        .fetch_all(self.base.pool())
        .await?;
        rows.iter()
            .map(|r| {
                Ok((
                    r.try_get("metric")?,
                    r.try_get("value_num")?,
                    r.try_get("ts")?,
                ))
            })
            .collect()
    }

    /// 历史聚合曲线原始行(`granularity` 决定读哪张聚合表)。
    pub async fn historical_samples(
        &self,
        table: &str,
        device_id: &str,
        port_no: u8,
        from: chrono::DateTime<chrono::Utc>,
        to: chrono::DateTime<chrono::Utc>,
    ) -> AppResult<Vec<gd::HistoricalSampleRow>> {
        // `table` 只能取自端点里对 granularity 的白名单映射,不是外部直传。
        let sql = format!(
            "SELECT metric, avg_value avg_v, min_value min_v, max_value max_v, `count` sample_count, bucket_start
             FROM {table} WHERE device_id = ? AND port_no = ? AND bucket_start >= ? AND bucket_start <= ?
             ORDER BY bucket_start ASC, metric ASC LIMIT 200"
        );
        let rows = sqlx::query(&sql)
            .bind(device_id)
            .bind(port_no)
            .bind(from)
            .bind(to)
            .fetch_all(self.base.pool())
            .await?;
        rows.iter()
            .map(|r| {
                Ok(gd::HistoricalSampleRow {
                    metric: r.try_get("metric")?,
                    avg_v: r.try_get("avg_v")?,
                    min_v: r.try_get("min_v")?,
                    max_v: r.try_get("max_v")?,
                    sample_count: r.try_get::<u64, _>("sample_count")?,
                    bucket_start: r.try_get::<chrono::DateTime<chrono::Utc>, _>("bucket_start")?
                        .to_rfc3339(),
                })
            })
            .collect()
    }

    /// 设备与其厂商是否都已启用。TCP 首次握手据此决定是否接受该连接。
    ///
    /// 返回**长度 1 的布尔数组**而不是单个布尔:重复设备记录必须被拒,
    /// 折叠成 bool 会把数据损坏掩盖掉。
    pub async fn device_vendor_enabled(&self, device_id: &str) -> AppResult<Vec<bool>> {
        Ok(sqlx::query_scalar(
            "SELECT d.status='enabled' AND v.status='enabled' FROM device d JOIN vendor v ON v.id=d.vendor_id AND v.deleted_at IS NULL WHERE d.device_id=? AND d.deleted_at IS NULL",
        )
        .bind(device_id)
        .fetch_all(self.base.pool())
        .await?)
    }

    /// 刷新设备最后在线时间与来源 IP。
    pub async fn touch_device(&self, device_id: &str, peer_ip: &str) -> AppResult<()> {
        sqlx::query(
            "UPDATE device SET last_seen_at=UTC_TIMESTAMP(3),last_ip=? WHERE device_id=? AND deleted_at IS NULL",
        )
        .bind(peer_ip)
        .bind(device_id)
        .execute(self.base.pool())
        .await?;
        Ok(())
    }

    /// 设备事件入 outbox(与业务写同事务,由发布器投递到 Redis Stream)。
    pub async fn enqueue_event(
        &self,
        stream: &str,
        envelope: &common_redis::StreamEnvelope,
    ) -> AppResult<()> {
        let mut tx = self.base.begin().await?;
        self.enqueue_event_in(&mut tx, stream, envelope).await?;
        tx.commit().await?;
        Ok(())
    }

    /// 在**调用方已有**的事务里入 outbox。发信与业务写必须同事务,
    /// 否则 Redis 故障会丢事件,或事务回滚后留下幽灵事件。
    pub async fn enqueue_event_in(
        &self,
        tx: &mut Tx<'_>,
        stream: &str,
        envelope: &common_redis::StreamEnvelope,
    ) -> AppResult<()> {
        sqlx::query("INSERT INTO event_outbox (event_id, stream, envelope_json) VALUES (?, ?, ?)")
            .bind(&envelope.event_id)
            .bind(stream)
            .bind(serde_json::to_value(envelope)?)
            .execute(tx.executor())
            .await?;
        Ok(())
    }

    // ===== 设备批量建档 =====

    /// 批量建档整批事务。**逐台设备**在同一事务内完成
    /// 「写 `device_provision` → 比对参数 → 校验厂商 → 锁设备行 →
    /// 建设备+端口 → 回读端口」,任一台失败即整批回滚:批量导入必须原子,
    /// 中途失败不能留下半批设备。
    ///
    /// 锁顺序按调用方已排序的 `device_id` 升序推进,避免并发批次互相等待。
    /// SQL / bind 顺序 / 判定顺序 / 错误文案与搬迁前逐字一致。
    pub async fn provision(&self, devices: &[DeviceProvision]) -> AppResult<Vec<ProvisionedDevice>> {
        let mut tx = self.base.begin().await?;
        let mut items = Vec::with_capacity(devices.len());
        for device in devices {
            let request = serde_json::to_value(device)?;
            sqlx::query("INSERT INTO device_provision (device_id,request_json) VALUES (?,?) ON DUPLICATE KEY UPDATE device_id=device_provision.device_id")
                .bind(&device.device_id).bind(&request).execute(tx.executor()).await?;
            let stored: serde_json::Value = sqlx::query_scalar(
                "SELECT request_json FROM device_provision WHERE device_id=? FOR UPDATE",
            )
            .bind(&device.device_id)
            .fetch_one(tx.executor())
            .await?;
            if stored != request {
                return Err(AppError::Conflict(format!(
                    "设备 {} 已使用不同参数导入",
                    device.device_id
                )));
            }
            let vendor: Option<u64> = sqlx::query_scalar(
                "SELECT id FROM vendor WHERE id=? AND status='enabled' AND deleted_at IS NULL",
            )
            .bind(device.vendor_id)
            .fetch_optional(tx.executor())
            .await?;
            if vendor.is_none() {
                return Err(AppError::BadRequest(format!(
                    "设备 {} 的厂商不存在或未启用",
                    device.device_id
                )));
            }
            let rows = sqlx::query("SELECT vendor_id,station_id,port_count,model,status,deleted_at FROM device WHERE device_id=? FOR UPDATE")
                .bind(&device.device_id).fetch_all(tx.executor()).await?;
            let created = rows.is_empty();
            if !created {
                if rows.len() != 1 {
                    return Err(AppError::Conflict(format!(
                        "设备 {} 存在重复记录，需先清理",
                        device.device_id
                    )));
                }
                let row = &rows[0];
                if row.try_get::<u64, _>("vendor_id")? != device.vendor_id
                    || row.try_get::<Option<u64>, _>("station_id")? != Some(device.station_id)
                    || row.try_get::<u8, _>("port_count")? != device.port_count
                    || row.try_get::<Option<String>, _>("model")? != device.model
                    || row.try_get::<String, _>("status")? != "enabled"
                    || row
                        .try_get::<Option<chrono::NaiveDateTime>, _>("deleted_at")?
                        .is_some()
                {
                    return Err(AppError::Conflict(format!(
                        "设备 {} 已存在且配置不同或已停用",
                        device.device_id
                    )));
                }
            } else {
                sqlx::query("INSERT INTO device (device_id,vendor_id,station_id,port_count,model) VALUES (?,?,?,?,?)")
                    .bind(&device.device_id).bind(device.vendor_id).bind(device.station_id).bind(device.port_count).bind(&device.model)
                    .execute(tx.executor()).await?;
                for port_no in 1..=device.port_count {
                    let port_code = format!("{}:{port_no}", device.device_id);
                    let existing: i64 = sqlx::query_scalar(
                        "SELECT COUNT(*) FROM device_port WHERE port_code=? AND deleted_at IS NULL",
                    )
                    .bind(&port_code)
                    .fetch_one(tx.executor())
                    .await?;
                    if existing > 0 {
                        return Err(AppError::Conflict(format!("端口码 {port_code} 已存在")));
                    }
                    sqlx::query("INSERT INTO device_port (device_id,port_no,port_code) VALUES (?,?,?)")
                        .bind(&device.device_id)
                        .bind(port_no)
                        .bind(port_code)
                        .execute(tx.executor())
                        .await?;
                }
            }
            let rows = sqlx::query("SELECT id,port_no,port_code FROM device_port WHERE device_id=? AND deleted_at IS NULL ORDER BY port_no,id")
                .bind(&device.device_id).fetch_all(tx.executor()).await?;
            if rows.len() != usize::from(device.port_count) {
                return Err(AppError::Conflict(format!(
                    "设备 {} 的端口记录不完整",
                    device.device_id
                )));
            }
            let mut ports = Vec::with_capacity(rows.len());
            for (index, row) in rows.iter().enumerate() {
                let port_no: u8 = row.try_get("port_no")?;
                if usize::from(port_no) != index + 1 {
                    return Err(AppError::Conflict("设备端口编号重复或缺失".into()));
                }
                ports.push(ProvisionedPort {
                    port_id: row.try_get("id")?,
                    port_no,
                    port_code: row.try_get("port_code")?,
                });
            }
            items.push(ProvisionedDevice {
                device_id: device.device_id.clone(),
                created,
                ports,
            });
        }
        tx.commit().await?;
        Ok(items)
    }

    // ===== 设备注册 =====

    /// 设备注册。要"锁设备行 → 校验厂商 → 关旧会话 → 建新会话 → 更新设备",
    /// 跨 5 张表且必须原子,故整段留在服务层。
    ///
    /// 返回 `(session_id, session_uuid)`:两者对应**同一行** `device_session`,
    /// 调用方回给设备的 UUID 必须是入库那一个,否则设备后续按 UUID 查会话
    /// 会查不到自己。
    pub async fn register(&self, req: &RegisterRequest) -> AppResult<(u64, String)> {
        let mut tx = self.base.begin().await?;
        let rows = sqlx::query("SELECT id,vendor_id,station_id,port_count,model,status FROM device WHERE device_id=? AND deleted_at IS NULL FOR UPDATE")
            .bind(&req.device_id).fetch_all(tx.executor()).await?;
        if rows.is_empty() {
            return Err(AppError::business(2001, "设备未建档"));
        }
        if rows.len() != 1 {
            return Err(AppError::Conflict("设备存在重复记录，需先清理".into()));
        }
        let device = &rows[0];
        let station_id: Option<u64> = device.try_get("station_id")?;
        if device.try_get::<String, _>("status")? != "enabled" {
            return Err(AppError::DeviceDisabled);
        }
        if device.try_get::<u64, _>("vendor_id")? != req.vendor_id
            || device.try_get::<u8, _>("port_count")? != req.port_count
            || req.station_id.is_some_and(|id| station_id != Some(id))
            || (req.model.is_some() && device.try_get::<Option<String>, _>("model")? != req.model)
        {
            return Err(AppError::Conflict("设备注册信息与运营配置不一致".into()));
        }
        let vendor: Option<(String, String)> = sqlx::query_as(
            "SELECT status,protocol FROM vendor WHERE id=? AND deleted_at IS NULL FOR SHARE",
        )
        .bind(req.vendor_id)
        .fetch_optional(tx.executor())
        .await?;
        let (status, protocol) = vendor.ok_or_else(|| AppError::BadRequest("厂商不存在".into()))?;
        if status != "enabled" {
            return Err(AppError::BadRequest("厂商未启用".into()));
        }
        if protocol != "hybrid" && protocol != req.connect_type {
            return Err(AppError::BadRequest("连接协议与厂商配置不符".into()));
        }
        sqlx::query("UPDATE device_session SET ended_at=UTC_TIMESTAMP(3),close_reason='re_register' WHERE device_id=? AND ended_at IS NULL")
            .bind(&req.device_id).execute(tx.executor()).await?;
        let session_uuid = uuid::Uuid::new_v4().to_string();
        let session_id = sqlx::query("INSERT INTO device_session (session_id,device_id,protocol,remote_addr,started_at,last_active_at,created_month) VALUES (?,?,?,?,UTC_TIMESTAMP(3),UTC_TIMESTAMP(3),DATE_FORMAT(UTC_DATE(),'%Y-%m-01'))")
            .bind(&session_uuid).bind(&req.device_id).bind(&req.connect_type).bind(&req.client_ip).execute(tx.executor()).await?.last_insert_id();
        sqlx::query("UPDATE device SET registered_at=UTC_TIMESTAMP(3),last_seen_at=UTC_TIMESTAMP(3),last_ip=?,firmware_version=COALESCE(?,firmware_version),mac_addr=COALESCE(?,mac_addr) WHERE id=?")
            .bind(&req.client_ip).bind(&req.firmware_version).bind(&req.mac_addr).bind(device.try_get::<u64,_>("id")?).execute(tx.executor()).await?;
        tx.commit().await?;
        Ok((session_id, session_uuid))
    }

    // ===== 会话 =====

    /// 关闭空闲会话,返回关闭条数。
    pub async fn close_idle_sessions(&self) -> AppResult<u64> {
        let result = sqlx::query(
            "UPDATE device_session SET ended_at = UTC_TIMESTAMP(3), close_reason = 'idle_timeout'
             WHERE ended_at IS NULL AND last_active_at < UTC_TIMESTAMP(3) - INTERVAL 10 MINUTE",
        )
        .execute(self.base.pool())
        .await?;
        Ok(result.rows_affected())
    }

    /// 开一个设备会话,返回 session_id。
    pub async fn open_session(&self, device_id: &str, peer: &str) -> AppResult<String> {
        let session_id = uuid::Uuid::new_v4().to_string();
        sqlx::query(
            "INSERT INTO device_session (session_id, device_id, peer, connected_at) VALUES (?, ?, ?, UTC_TIMESTAMP(3))",
        )
        .bind(&session_id)
        .bind(device_id)
        .bind(peer)
        .execute(self.base.pool())
        .await?;
        Ok(session_id)
    }

    pub async fn touch_session(&self, session_id: &str) -> AppResult<()> {
        sqlx::query("UPDATE device_session SET last_active_at = UTC_TIMESTAMP(3) WHERE session_id = ?")
            .bind(session_id)
            .execute(self.base.pool())
            .await?;
        Ok(())
    }

    pub async fn close_session(&self, session_id: &str, reason: &str) -> AppResult<()> {
        sqlx::query(
            "UPDATE device_session SET ended_at = UTC_TIMESTAMP(3), close_reason = ? WHERE session_id = ? AND ended_at IS NULL",
        )
        .bind(reason)
        .bind(session_id)
        .execute(self.base.pool())
        .await?;
        Ok(())
    }

    /// 网关重启后清理自身持有的在线会话标记。
    pub async fn reset_own_sessions(&self) -> AppResult<()> {
        sqlx::query(
            "UPDATE device_session SET ended_at = UTC_TIMESTAMP(3), close_reason = 'gateway_restart' \
             WHERE ended_at IS NULL",
        )
        .execute(self.base.pool())
        .await?;
        Ok(())
    }

}
