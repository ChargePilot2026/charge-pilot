//! 设备能力域(P3)
//!
//! `db` 私有,handler 拿不到裸 pool。所有原 `st.db.pool()` 的调用点改为
//! 调用本对象的方法或取一个显式事务句柄(`Tx`),SQL 不再出现在 handler 里。
//!
//! 事务边界的取舍:**跨多表的写路径**(`port_occupy` / `record_frames` /
//! `close_idle_sessions` / `command_acknowledge`)由本对象自己开事务,
//! 保证"锁 + 判定 + 写"不会被调用方拆开;**只读查询**直接走 pool,
//! 不必为一次 SELECT 付出事务开销。

use api_contracts::gateway_devices as gd;
use common_app::ServiceBase;
use common_db::Tx;
use common_error::{AppError, AppResult};
use sqlx::Row;

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
            crate::telemetry_obs::aggregate_measurement(&mut tx, m.device_id, m.port_no, m.metric, m.value, m.ts).await?;
        }
        tx.commit().await?;
        Ok(measurements.len())
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
        crate::outbox::enqueue(&mut tx, stream, envelope).await?;
        tx.commit().await?;
        Ok(())
    }

    /// 设备批量建档的**整批**事务句柄。批量导入必须原子:
    /// 中途失败不能留下半批设备。
    pub async fn begin_provision(&self) -> AppResult<Tx<'_>> {
        self.base.begin().await
    }

    /// 设备注册事务。注册要"锁设备行 → 校验厂商 → 关旧会话 → 建新会话 → 更新设备",
    /// 跨 5 张表,故整段留在服务层。
    pub async fn begin_registration(&self) -> AppResult<Tx<'_>> {
        self.base.begin().await
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
