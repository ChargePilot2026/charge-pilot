# Runbook:数据备份 / 恢复(P0-5 重写)

> **引用**:`docs/技术规格.md` § 14.4(备份策略)
> **触发场景**:MySQL 数据丢失 / 误操作 / 机房级故障需要从备份恢复
> **前置**:`examples/docker-compose.yml` 已采用 `chargepilot-mysql` 容器名 + 启用 binlog(§ 7 校验通过)
> **部署目录假设**:将 Compose 文件放在 `/opt/chargepilot/` 并从该目录启动,因此 `./mysql/binlog` 与 `./mysql/conf.d` 分别对应宿主机 `/opt/chargepilot/mysql/binlog/`、`/opt/chargepilot/mysql/conf.d/`;若实际目录不同,下文宿主机路径必须同步修改。
> **密钥来源**:MySQL 容器已由 Compose 注入 `MYSQL_ROOT_PASSWORD`;以下自动脚本在容器内读取该变量。手工命令若在宿主机使用 `$MYSQL_ROOT_PASSWORD`,需先从受限权限的密钥存储加载,不要把真实密码写进 runbook 或提交仓库。

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
| `/opt/chargepilot/mysql/binlog/` | 宿主机 binlog(`mysql-bin.NNNNNN`);容器内挂载为 `/var/lib/mysql/binlog/` | 7 天(配合异地 OSS) | **异地 OSS 同步** |
| `/opt/chargepilot/mysql/conf.d/` | 宿主机自定义配置;容器内挂载为 `/etc/mysql/conf.d/` | — | 每周全量 tar 备份 |

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
rclone lsl remote:chargepilot-backup/mysqldump/$(date +%Y-%m-%d)/ | head -20
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
# 步骤 1:选定全量备份,从该备份的 --source-data=2 注释读取一致性位点
DUMP=/var/backup/mysql/full_2026-09-25.sql
grep -m1 'CHANGE REPLICATION SOURCE TO' "$DUMP"
# 将输出中的 SOURCE_LOG_FILE / SOURCE_LOG_POS 填入以下变量,不可用当前主库位点替代
START_BINLOG=mysql-bin.000120
START_POS=4589271

# 步骤 2:确定目标恢复时间(mysqlbinlog 容器使用 Asia/Shanghai 时区)
TARGET_TIME="2026-09-25 22:30:00"
echo "从 ${START_BINLOG}:${START_POS} 开始 replay 到 ${TARGET_TIME}"
```

### 4.3 导入 mysqldump

```bash
# 下载与上述位点对应的全量备份(来自本机 /var/backup/mysql 或异地 OSS)
docker exec -i mysql-restore mysql -uroot -prestore_temp_pwd \
  < "$DUMP"
```

### 4.4 Replay binlog 到目标时间

```bash
# 在恢复机先把 OSS 各日期目录中从 START_BINLOG 起的文件下载到
# /opt/chargepilot/mysql/binlog/ 同一目录,按文件名去重并核对连续编号
# 只回放与所选 dump 对应的起始文件及后续文件;首文件从精确位点开始
set -euo pipefail
BINLOG_DIR=/opt/chargepilot/mysql/binlog
test -f "$BINLOG_DIR/$START_BINLOG" || { echo "缺少起始 binlog: $START_BINLOG"; exit 1; }
first=1
for binlog_file in $(find "$BINLOG_DIR" -maxdepth 1 -type f -name 'mysql-bin.[0-9]*' | LC_ALL=C sort); do
  binlog_basename=$(basename "$binlog_file")
  [[ "$binlog_basename" < "$START_BINLOG" ]] && continue
  if [ "$first" -eq 1 ]; then
    docker run --rm -e TZ=Asia/Shanghai -v "$BINLOG_DIR:/binlog:ro" mysql:8.4 \
      mysqlbinlog --start-position="$START_POS" --stop-datetime="$TARGET_TIME" "/binlog/$binlog_basename" \
      | docker exec -i mysql-restore mysql -uroot -prestore_temp_pwd
    first=0
  else
    docker run --rm -e TZ=Asia/Shanghai -v "$BINLOG_DIR:/binlog:ro" mysql:8.4 \
      mysqlbinlog --stop-datetime="$TARGET_TIME" "/binlog/$binlog_basename" \
      | docker exec -i mysql-restore mysql -uroot -prestore_temp_pwd
  fi
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

# 1. 每日 02:00 mysqldump 全量(保留 7 天;必须带 --source-data=2 位点)
0 2 * * * root /opt/chargepilot/scripts/backup-mysqldump.sh

# 2. 每日 03:00 rclone 同步到异地 OSS(先确认当天 dump 存在,再复制 dump + 已关闭 binlog + config)
0 3 * * * root /opt/chargepilot/scripts/sync-to-oss.sh

# 3. 每周日 04:00 配置文件 tar 备份
0 4 * * 0 root /opt/chargepilot/scripts/backup-config.sh

# 4. 每日 05:00 校验 binlog 完整性(binlog 文件大小异常告警)
0 5 * * * root /opt/chargepilot/scripts/check-binlog-integrity.sh
```

---

## 七、全量备份与 binlog 异地同步脚本(参考)

`backup-mysqldump.sh` 必须在 02:00 完成并检查非空;`--source-data=2` 把与快照一致的 binlog 文件/位点写入 dump 头部。恢复时从该位点开始,不能从文件头或当前主库位点开始。

```bash
#!/bin/bash
set -euo pipefail
DATE=$(date +%Y-%m-%d)
mkdir -p /var/backup/mysql
docker exec chargepilot-mysql sh -c 'exec mysqldump --user=root -p"$MYSQL_ROOT_PASSWORD" \
  --single-transaction --flush-logs --source-data=2 --all-databases' \
  > "/var/backup/mysql/full_$DATE.sql.tmp"
test -s "/var/backup/mysql/full_$DATE.sql.tmp"
grep -q 'CHANGE REPLICATION SOURCE TO' "/var/backup/mysql/full_$DATE.sql.tmp"
mv "/var/backup/mysql/full_$DATE.sql.tmp" "/var/backup/mysql/full_$DATE.sql"
```

03:00 的 `sync-to-oss.sh` 只上传已生成的 dump;先执行 `FLUSH BINARY LOGS` 关闭当前 binlog,再排除新打开的活动文件,避免复制正在写入的文件。生产脚本应记录同步清单与校验和,并在任何上传失败时报警。

```bash
#!/bin/bash
# /opt/chargepilot/scripts/sync-to-oss.sh
# 同步 mysqldump + binlog + config 到客户 OSS

set -euo pipefail
OSS_REMOTE="remote:chargepilot-backup"
DATE=$(date +%Y-%m-%d)
BINLOG_DIR=/opt/chargepilot/mysql/binlog
test -s "/var/backup/mysql/full_$DATE.sql"
docker exec chargepilot-mysql sh -c 'exec mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -e "FLUSH BINARY LOGS;"'
CURRENT_BINLOG=$(docker exec chargepilot-mysql sh -c 'exec mysql -N -uroot -p"$MYSQL_ROOT_PASSWORD" -e "SHOW BINARY LOG STATUS;"' | awk '{print $1}')
test -n "$CURRENT_BINLOG"

# 1. 上传当日已完成的全量备份
rclone copyto "/var/backup/mysql/full_$DATE.sql" "$OSS_REMOTE/mysqldump/$DATE/full_$DATE.sql"

# 2. 上传已关闭的 binlog(不复制当前正在写入的文件)
for binlog_file in "$BINLOG_DIR"/mysql-bin.[0-9]*; do
  test -f "$binlog_file" || continue
  binlog_basename=$(basename "$binlog_file")
  [ "$binlog_basename" = "$CURRENT_BINLOG" ] && continue
  rclone copyto "$binlog_file" "$OSS_REMOTE/binlog/$DATE/$binlog_basename"
done

# 3. config
rclone copy /opt/chargepilot/mysql/conf.d/ "$OSS_REMOTE/config/$DATE/"

# 4. 清理 30 天前的远程备份
rclone delete "$OSS_REMOTE/mysqldump/$(date -d '30 days ago' +%Y-%m-%d)/" || true

echo "[$(date)] OSS sync OK"
```

---

## 八、备份完整性校验(每日 05:00)

```bash
#!/bin/bash
# /opt/chargepilot/scripts/check-binlog-integrity.sh

# 1. 检查宿主机目录存在近期 binlog(-mtime +7 会选出过旧文件,不能用于此检查)
BINLOG_DIR=/opt/chargepilot/mysql/binlog
RECENT_COUNT=$(find "$BINLOG_DIR" -maxdepth 1 -type f -name 'mysql-bin.[0-9]*' -mmin -180 | wc -l)
if [ "$RECENT_COUNT" -lt 1 ]; then
  echo "[ERROR] 最近 3 小时无 binlog 文件更新"
  exit 1
fi

# 2. 检查 binlog 是否同步到了 OSS
REMOTE_COUNT=$(rclone lsl remote:chargepilot-backup/binlog/$(date +%Y-%m-%d)/ | wc -l)
if [ "$REMOTE_COUNT" -lt 1 ]; then
  echo "[ERROR] 今日 binlog 未同步到 OSS"
  exit 1
fi

# 3. 检查 mysql-bin.index 文件是否存在
if [ ! -f "$BINLOG_DIR/mysql-bin.index" ]; then
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
