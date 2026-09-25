# Runbook:数据备份 / 恢复

> **引用**:`技术规格.md` § 14.4
> **触发场景**:MySQL 数据丢失 / 误操作 / 机房级故障需要从备份恢复

---

## 一、RPO / RTO 承诺(再次确认)

- **RPO ≤ 1 小时**(依赖 binlog;最坏情况退回最近 mysqldump 整点 = 最坏 24h)
- **RTO ≤ 4 小时**(从触发恢复 → 服务可用)
- **日常目标**:每日 02:00 mysqldump + 实时 binlog + 异地 OSS 同步

---

## 二、立即判断:可用哪些备份?

```bash
# 1. 确认数据丢失范围
docker exec mysql mysql -uroot -p$MYSQL_ROOT_PASSWORD -e "SHOW DATABASES;"
docker exec mysql mysql -uroot -p$MYSQL_ROOT_PASSWORD \
  -e "SELECT COUNT(*) FROM user_db.charge_order WHERE deleted_at IS NULL;"

# 2. 列本机备份(7 天滚动)
ls -la /var/backup/mysql/ | head -20

# 3. 列 binlog(本机 7 天)
docker exec mysql ls -la /var/lib/mysql/mysql-bin.*

# 4. 列异地备份(30 天,OSS / scp)
# (按客户实际异地方案:OSS 用 rclone ls remote:bucket;scp 走 ssh)
```

---

## 三、恢复路径选择

| 场景 | 推荐路径 |
| --- | --- |
| 单表误操作(误 DELETE / UPDATE) | binlog point-in-time recovery(精确到秒) |
| 整库损坏(MySQL 进程崩溃启动不起来) | mysqldump 全量 + binlog replay |
| 机房级故障(整机 down)| 异地备份恢复 → 启用备用机服务 |
| 单条订单 / 单条记录出错 | 直接修复(无需完整恢复)|

---

## 四、完整恢复流程

### 4.1 准备临时恢复实例

```bash
# 在备用机 / 临时 ECS 上拉起一个新 MySQL(避免覆盖主库)
docker run -d --name mysql-restore \
  -e MYSQL_ROOT_PASSWORD=restore_temp_pwd \
  -v /tmp/mysql-restore:/var/lib/mysql \
  -p 3307:3306 mysql:8.4

# 等待 healthy
until docker exec mysql-restore mysqladmin ping -h localhost -uroot -prestore_temp_pwd 2>/dev/null; do
  sleep 2
done
```

### 4.2 导入 mysqldump

```bash
# 下载最新全量备份(来自本机 /var/backup/mysql 或 OSS)
docker exec -i mysql-restore mysql -uroot -prestore_temp_pwd \
  < /path/to/chargepilot_full_2026-09-25.sql
```

### 4.3 重放 binlog 到目标时间点

```bash
# 找到目标时间点之前最近的 binlog 文件
LATEST_BINLOG=mysql-bin.000123
TARGET_TIME="2026-09-25 22:30:00"

# 解析 binlog 到 SQL
docker exec mysql mysqlbinlog \
  --read-from-remote-server --host=mysql-restore -uroot -p$MYSQL_ROOT_PASSWORD \
  --stop-datetime="$TARGET_TIME" \
  /var/lib/mysql/$LATEST_BINLOG > /tmp/binlog.sql

# 把 SQL 应用到临时实例(只 replay 增量)
docker exec -i mysql-restore mysql -uroot -prestore_temp_pwd < /tmp/binlog.sql
```

### 4.4 校验数据后切换

```bash
# 校验关键表行数
docker exec mysql-restore mysql -uroot -prestore_temp_pwd -e "
  SELECT 'charge_order', COUNT(*) FROM user_db.charge_order WHERE deleted_at IS NULL;
  SELECT 'refund_record', COUNT(*) FROM user_db.refund_record;
  SELECT 'device', COUNT(*) FROM gateway_db.device;
"

# 比对与 lost 前一刻的预期行数(从监控 Grafana 取)

# 校验通过 → 在维护窗口切换流量:
# 1. docker compose down 停原服务
# 2. mysql 容器数据目录替换为恢复实例目录
# 3. docker compose up -d 重启
# 4. 跑 smoke test(见 checklists/first-deploy.md 第 6 节)
```

---

## 五、误操作快速回滚

```bash
# 用 mysqlbinlog 解析出反向 SQL(仅限 UPDATE / DELETE,INSERT 不可逆)
docker exec mysql mysqlbinlog \
  --start-datetime="2026-09-25 22:00:00" --stop-datetime="2026-09-25 22:30:00" \
  -vv /var/lib/mysql/mysql-bin.000123 > /tmp/binlog_v.sql

# 人工核对后跑反向 SQL
docker exec -i mysql mysql -uroot -p$MYSQL_ROOT_PASSWORD < /tmp/reverse.sql
```

---

## 六、演练纪律

- 每半年一次完整恢复演练(`技术规格.md` § 14.4)
- 报告:`docs/runbook/backup-drill-YYYY-MM.md`(运维订阅交付物)
- 演练发现的问题(RTO 超时 / binlog 损坏 / 异地同步失败)→ 列入下个迭代修复
