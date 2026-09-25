# 备份恢复演练记录模板

> **配套**:`docs/runbook/backup-restore.md`(恢复流程)
> **触发**:每半年一次完整 PITR 演练(详见 `技术规格.md` § 14.4)
> **命名**:`backup-drill-YYYY-Qn.md`(按季度归档)

---

## 演练元数据

| 项 | 值 |
| --- | --- |
| 演练日期 | ____________________ |
| 演练执行人 | ____________________ |
| 演练场景(单选) | □ P0 日常(binlog 健康) □ P1 binlog 缺失 □ P2 机房级故障 □ P3 人为误操作 |
| 演练环境 | □ 客户生产服务器 □ 备用机 □ 临时 ECS |
| 客户方配合 | ____________________ |

---

## 一、演练前准备

### 1.1 备份快照采集

```bash
# 记录当前备份状态
echo "演练开始时间: $(date)" > /tmp/drill_$(date +%Y%m%d).log
ls -la /var/backup/mysql/ | head -20 >> /tmp/drill_$(date +%Y%m%d).log
docker exec chargepilot-mysql ls -la /var/lib/mysql/binlog/ | head -20 >> /tmp/drill_$(date +%Y%m%d).log
rclone lsl remote:chargepilot-backup/$(date +%Y-%m)/ | head -20 >> /tmp/drill_$(date +%Y%m%d).log

# 记录当前主库关键表行数(对比基线)
docker exec chargepilot-mysql mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -e "
  SELECT 'user_db.charge_order' AS tbl, COUNT(*) AS n FROM user_db.charge_order WHERE deleted_at IS NULL;
  SELECT 'user_db.payment_order', COUNT(*) FROM user_db.payment_order WHERE deleted_at IS NULL;
  SELECT 'user_db.refund_record', COUNT(*) FROM user_db.refund_record;
  SELECT 'billing_db.fee_calculation', COUNT(*) FROM billing_db.fee_calculation;
  SELECT 'billing_db.settlement', COUNT(*) FROM billing_db.settlement;
  SELECT 'gateway_db.device', COUNT(*) FROM gateway_db.device;
" >> /tmp/drill_$(date +%Y%m%d).log
```

### 1.2 准备临时恢复实例

```bash
# 拉起隔离环境的临时 MySQL(避免覆盖主库)
docker run -d --name mysql-drill-restore \
  -e MYSQL_ROOT_PASSWORD=drill_temp_pwd \
  -v /tmp/mysql-drill:/var/lib/mysql \
  -p 3307:3306 mysql:8.4
```

---

## 二、演练过程(按时序记录)

| 步骤 | 起点时间 | 操作 | 终点时间 | 耗时 |
| --- | --- | --- | --- | --- |
| 1 | | 触发"数据丢失"模拟(可选:停主库 / 模拟表损坏) | | |
| 2 | | 选定目标恢复时间点 `TARGET_TIME` | | |
| 3 | | 提取 binlog 起始位点 | | |
| 4 | | 拉取最近 mysqldump 备份 | | |
| 5 | | mysqldump 导入临时实例 | | |
| 6 | | binlog replay 到目标时间点 | | |
| 7 | | 校验关键表行数(对比基线) | | |
| 8 | | 跑通冒烟用例(见 `checklists/first-deploy.md` § 6) | | |
| 9 | | (如需)切换主库流量 | | |

**RTO 实测**:________________(承诺 ≤ 4h,达标? □ 是 □ 否)
**RPO 实测**:________________(取决于演练场景:≤ 1h / ≤ 24h / 分钟级)

---

## 三、冒烟用例结果

| 用例 | 通过 |
| --- | --- |
| 1. 扫码 → 启动 → 结束 → 计费 | □ |
| 2. 退款触发 | □ |
| 3. Webhook 推送 | □ |
| 4. 分账计算 | □ |
| 5. PC 后台权限 | □ |
| 6. OTA 流程(可选) | □ |

---

## 四、问题记录

| # | 问题描述 | 影响 | 修复责任 / 计划 |
| --- | --- | --- | --- |
| 1 | | | |
| 2 | | | |
| 3 | | | |

---

## 五、改进建议

(从本次演练中提炼的可优化项,例如:)

- 备份脚本改进点
- 异地同步稳定性
- 切换流程 SOP 改进
- 团队培训 / 演练频次

---

## 六、签字

| 角色 | 姓名 | 日期 | 签字 |
| --- | --- | --- | --- |
| 演练执行人 | | | |
| 技术负责人 | | | |
| 客户运维(若生产演练) | | | |

---

## 七、附录:实测命令日志

```bash
# 完整命令日志粘在此处
cat /tmp/drill_$(date +%Y%m%d).log
```

---

**演练报告归档**:本文件提交到 `docs/runbook/backup-drill-YYYY-Qn.md` 并 push 到 main,作为运维订阅交付物的一部分。