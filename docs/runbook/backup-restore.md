# Runbook:数据备份 / 恢复(P0-5 重写)

> **引用**:`docs/技术规格.md` § 14.4(备份策略)
> **触发场景**:MySQL 数据丢失 / 误操作 / 机房级故障需要从备份恢复
> **前置**:`examples/docker-compose.yml` 已采用 `chargepilot-mysql` 容器名 + 启用 binlog(§ 7 校验通过)

---

## 一、RPO / RTO 承诺(分层)

> **关键**:**RPO 取决于 binlog 是否持续可用**,不要给客户承诺单一数字。

| 场景 | 承诺 RPO | 承诺 RTO | 必备前提 |
| --- | --- | --- | --- |
| **日常**(binlog 健康 + mysqldump 成功) | **≤ 1 小时**(实测可达分钟级,依赖 binlog flush 频率) | ≤ 4 小时 | 异地 OSS 同步 + binlog 持续可用 |
| **binlog 缺失**(如磁盘损坏 / binlog 未同步) | **≤ 24 小时**(退回最近 mysqldump 时点) | ≤ 4 小时 | 至少 mysqldump 完整 |
| **机房级故障**(主机不可用) | **≤ 24 小时** | **≤ 8 小时**(含启动备用实例 + 跑通冒烟) | 异地 OSS + 备用机随时可启 |
| **人为误操作**(误 DELETE / UPDATE) | 分钟级(用 binlog point-in-time recovery) | ≤ 4 小时 | binlog 完整可读 |

**严禁**:在销售 / SLA 合同中只写"RPO ≤ 1h"而不说明分层前提,会被等保测评 / 客户法务驳回。

---

## 二、备份基础设施

### 2.1 本机目录布局

| 目录 | 内容 | 保留 | 同步 |
| --- | --- | --- | --- |
| `/var/backup/mysql/` | mysqldump 全量 | 7 天滚动 | 异地 OSS(`rclone` 每日 03:00) |
| `/var/lib/mysql/binlog/` | binlog ROW 格式(`mysql-bin.NNNNNN`) | 7 天(配合异地 OSS) | **异地 OSS 同步**(P0-5 新增,旧版缺) |
| `/etc/mysql/conf.d/` | 自定义配置 | — | 每周全量 tar 备份 |

### 2.2 异地备份(客户可选二选一)

- **OSS / S3**:每日 03:00 `rclone sync` 到客户自购 OSS 桶
- **备用机 scp**:每日 04:00 `rsync -avz` 到备用机(仅在主服务器宕机时启用)

**两者必须包含 mysqldump + binlog + config**,不只是 mysqldump。

---

## 三、立即判断:可用哪些备份?

```bash
# 1. 数据丢失范围确认
docker exec chargepilot-mysql mysql -uroot -p"$MYSQL_ROOT_PASSWORD" \
  -e "SHOW DATABASES;"
docker exec chargepilot-mysql mysql -uroot -p"$MYSQL_ROOT_PASSWORD" \
  -e "SELECT COUNT(*) FROM user_db.charge_order WHERE deleted_at IS NULL;"

# 2. 列本机 mysqldump 备份(7 天滚动)
ls -la /var/backup/mysql/ | head -20

# 3. 列 binlog(本机 7 天,容器内 /var/lib/mysql/binlog/)
docker exec chargepilot-mysql ls -la /var/lib/mysql/binlog/ | head -20

# 4. 列异地 OSS 同步结果(检查 rclone 上次同步时间)
rclone lsl remote:chargepilot-backup/$(date +%Y-%m)/ | head -20
```

---

## 四、完整恢复流程(PITR)

### 4.1 准备临时恢复实例(避免覆盖主库)

```bash
# 在备用机 / 临时 ECS 上拉起一个新 MySQL 8.4
docker run -d --name mysql-restore \
  -e MYSQL_ROOT_PASSWORD=restore_temp_pwd \
  -v /tmp/mysql-restore:/var/lib/mysql \
  -p 3307:3306 mysql:8.4 \
  --log-bin=/var/lib/mysql/binlog/mysql-bin \
  --server-id=2 \
  --binlog-format=ROW

# 等待 healthy
until docker exec mysql-restore mysqladmin ping -h localhost -uroot -prestore_temp_pwd 2>/dev/null; do
  sleep 2
done
```

### 4.2 提取 binlog 位点(关键!)

```bash
# 步骤 1:在主库执行,记录当前 binlog 位点 + 文件
docker exec chargepilot-mysql mysql -uroot -p"$MYSQL_ROOT_PASSWORD" \
  -e "SHOW MASTER STATUS;"
# 输出示例:
# +------------------+----------+--------------+------------------+
# | File             | Position | Binlog_Do_DB | Binlog_Ignore_DB |
# +------------------+----------+--------------+------------------+
# | mysql-bin.000123 | 4589271  |              |                  |
# +------------------+----------+--------------+------------------+

# 步骤 2:确定目标恢复时间(从备份应用 + binlog replay)
TARGET_TIME="2026-09-25 22:30:00"

# 步骤 3:找出 binlog 起始位置(从最近全量备份后第一个 binlog)
LATEST_DUMP_BINLOG=$(cat /var/backup/mysql/latest_dump_binlog.txt 2>/dev/null || echo "mysql-bin.000120")
echo "从 ${LATEST_DUMP_BINLOG} 开始 replay 到 ${TARGET_TIME}"
```

### 4.3 导入 mysqldump

```bash
# 下载最新全量备份(来自本机 /var/backup/mysql 或异地 OSS)
docker exec -i mysql-restore mysql -uroot -prestore_temp_pwd \
  < /path/to/chargepilot_full_2026-09-25.sql
```

### 4.4 Replay binlog 到目标时间

```bash
# 从起始 binlog 开始,把所有 binlog 转成 SQL,过滤到目标时间
for binlog_file in $(ls /var/lib/mysql/binlog/mysql-bin.00* | sort); do
  binlog_basename=$(basename $binlog_file)
  docker exec chargepilot-mysql mysqlbinlog \
    --read-from-remote-server --host=localhost -uroot -p"$MYSQL_ROOT_PASSWORD" \
    --stop-datetime="$TARGET_TIME" \
    --verbose \
    /var/lib/mysql/binlog/$binlog_basename > /tmp/$binlog_basename.sql 2>/dev/null
done

# 批量应用到临时实例(注意顺序)
for sql_file in $(ls /tmp/mysql-bin.*.sql | sort); do
  docker exec -i mysql-restore mysql -uroot -prestore_temp_pwd < $sql_file
done
```

### 4.5 校验数据后切换

```bash
# 校验关键表行数
docker exec mysql-restore mysql -uroot -prestore_temp_pwd -e "
  SELECT 'charge_order' AS tbl, COUNT(*) AS n FROM user_db.charge_order WHERE deleted_at IS NULL;
  SELECT 'refund_record', COUNT(*) FROM user_db.refund_record;
  SELECT 'payment_order', COUNT(*) FROM user_db.payment_order;
  SELECT 'fee_calculation', COUNT(*) FROM billing_db.fee_calculation;
  SELECT 'device', COUNT(*) FROM gateway_db.device;
"

# 比对与 lost 前一刻的预期行数(从监控 Grafana / 业务告警取)

# 校验通过 → 切换流量:
# 1. 维护窗口前:暂停对外服务(Caddyfile 临时返回 503,或 nginx maintenance 模式)
# 2. docker compose down 停原服务
# 3. mysql 容器数据目录替换为恢复实例目录
# 4. docker compose up -d 重启
# 5. 跑 smoke test(见 checklists/first-deploy.md § 6)
```

---

## 五、误操作快速回滚(分钟级 RPO)

```bash
# 1. 找出误操作的精确 binlog 位点
docker exec chargepilot-mysql mysqlbinlog \
  --start-datetime="2026-09-25 22:00:00" --stop-datetime="2026-09-25 22:30:00" \
  -vv /var/lib/mysql/binlog/mysql-bin.000123 \
  | grep -A 5 "### DELETE FROM charge_order WHERE" \
  | head -50

# 2. 提取反向 SQL(只支持 UPDATE / DELETE;INSERT 不可逆 → 必须用全量回滚)
docker exec chargepilot-mysql mysqlbinlog \
  --start-datetime="2026-09-25 22:00:00" --stop-datetime="2026-09-25 22:30:00" \
  -vv --database=user_db \
  /var/lib/mysql/binlog/mysql-bin.000123 > /tmp/binlog_v.sql

# 3. 人工核对后跑反向 SQL
docker exec -i chargepilot-mysql mysql -uroot -p"$MYSQL_ROOT_PASSWORD" < /tmp/reverse.sql
```

---

## 六、备份策略定时任务(客户服务器 cron)

```bash
# /etc/cron.d/chargepilot-backup

# 1. 每日 02:00 mysqldump 全量(保留 7 天)
0 2 * * * root /opt/chargepilot/scripts/backup-mysqldump.sh

# 2. 每日 03:00 rclone 同步到异地 OSS(包含 mysqldump + binlog + config)
0 3 * * * root /opt/chargepilot/scripts/sync-to-oss.sh

# 3. 每周日 04:00 配置文件 tar 备份
0 4 * * 0 root /opt/chargepilot/scripts/backup-config.sh

# 4. 每日 05:00 校验 binlog 完整性(binlog 文件大小异常告警)
0 5 * * * root /opt/chargepilot/scripts/check-binlog-integrity.sh
```

---

## 七、binlog 异地同步脚本(参考)

```bash
#!/bin/bash
# /opt/chargepilot/scripts/sync-to-oss.sh
# 同步 mysqldump + binlog + config 到客户 OSS

set -eu
OSS_REMOTE="remote:chargepilot-backup"
DATE=$(date +%Y-%m-%d)

# 1. mysqldump
rclone copy /var/backup/mysql/ "$OSS_REMOTE/mysqldump/$DATE/" --progress

# 2. binlog(关键!旧版漏掉)
docker exec chargepilot-mysql mysqldump --host=localhost --user=root -p"$MYSQL_ROOT_PASSWORD" \
  --single-transaction --flush-logs --master-data=2 \
  --all-databases > /var/backup/mysql/full_$DATE.sql
rclone copy /var/lib/mysql/binlog/ "$OSS_REMOTE/binlog/$DATE/" --progress

# 3. config
rclone copy /etc/mysql/conf.d/ "$OSS_REMOTE/config/$DATE/" --progress

# 4. 清理 30 天前的远程备份
rclone delete "$OSS_REMOTE/mysqldump/$(date -d '30 days ago' +%Y-%m-%d)/" || true

echo "[$(date)] OSS sync OK"
```

---

## 八、备份完整性校验(每日 05:00)

```bash
#!/bin/bash
# /opt/chargepilot/scripts/check-binlog-integrity.sh

# 1. 检查最近 7 天每天都有 binlog 文件
MISSING=$(find /var/lib/mysql/binlog/ -name "mysql-bin.*" -mtime +7 -type f | wc -l)
if [ $MISSING -lt 7 ]; then
  echo "[ERROR] binlog 文件少于 7 个(实际 $MISSING)"
  exit 1
fi

# 2. 检查 binlog 是否同步到了 OSS
REMOTE_COUNT=$(rclone lsl remote:chargepilot-backup/binlog/$(date +%Y-%m-%d)/ | wc -l)
if [ $REMOTE_COUNT -lt 1 ]; then
  echo "[ERROR] 今日 binlog 未同步到 OSS"
  exit 1
fi

# 3. 检查 mysql-bin.index 文件是否存在
if [ ! -f /var/lib/mysql/binlog.index ]; then
  echo "[ERROR] mysql-bin.index 缺失"
  exit 1
fi

echo "[$(date)] binlog integrity OK"
```

---

## 九、演练纪律

- 每半年一次完整 PITR 演练(详见 `docs/runbook/backup-drill-YYYY-Q.md` 模板)
- 演练记录:实测的 RTO / RPO / 失败场景
- 演练发现的问题(RTO 超时 / binlog 损坏 / 异地同步失败)→ 列入下个迭代修复
- 严禁不演练就承诺客户"RPO ≤ 1h"

---

## 十、跨服务数据一致性注意

- 备份只覆盖 MySQL 5 个 schema
- **Redis Stream 不备份**:Stream 是事件总线,丢消息可通过 `comp_tx_stream` 幂等补偿(详见 `docs/技术规格.md` § 5.6)
- **Redis cache 不备份**:缓存丢失后用户首次访问会 cache miss → 回填,对业务透明
- 业务缓存(`snapshot:{order_id}`)丢失 → 小程序下次轮询会 1 次 cache miss → 不影响最终结果