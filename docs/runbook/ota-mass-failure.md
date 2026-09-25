# Runbook:OTA 全量升级失败 / 大量设备回滚

> **引用**:`技术规格.md` § 6.6 / `docs/需求分析.md` § 5.6
> **触发场景**:客户管理员 PC 后台发起 OTA 推送 → 全量刷固件 → 大量设备回滚 → `alert_stream(fatal)` 风暴

---

## 一、症状

- 短时间内 `alert_event` 表大量 `level=fatal`,type=`ota_rollback_done` / `ota_failed`
- admin 后台"设备列表"出现一批 `firmware_version=上一版本` 的设备,固件包没刷上
- 客户运维投诉:"昨晚升级后桩全变砖"

---

## 二、立即决策树

```
OTA 推送告警风暴
  │
  ├─ 是否所有设备失败?(看 alert_event.level=fatal 的比例)
  │    ├─ 是(< 50% 失败)
  │    │    └─ 通常是单台 / 型号问题 → 自动回滚已生效 → 等客户运维触发现场
  │    │
  │    └─ 否(>= 80% 失败)  ← 本 Runbook 处理路径
  │
  ├─ 立即停止后续 OTA 调度
  │    # P1-10:admin 暂未提供 /internal/ota/abort-all 端点(本期未实现)
  │    # 临时方案:客户运维手动在 PC 后台"OTA 计划"页 → 取消所有 status='pending' 的 ota_schedule
  │    # 或代码动工后用此命令(预占):
  │    # docker exec chargepilot-admin curl -s -X POST http://chargepilot-admin:8082/internal/ota/abort-all
  │
  └─ 评估是否影响充电业务
       ├─ 是(回滚的设备无法充电)→ 启动应急通知(见 § 五)
       └─ 否(回滚成功且桩仍可用)→ 只发事故报告
```

---

## 三、根因排查(30 min 内)

| 根因 | 排查方法 |
| --- | --- |
| **固件包损坏**(SHA-256 不匹配) | 重新计算 `ota_package.sha256` 与供应商提供值比对 |
| **设备 RTC 漂移过大** | `gateway_db.telemetry` 看 `timestamp_drift_ms` 历史 |
| **客户上报了错误设备型号** | `ota_schedule.target_devices` 与实际 `device_meta.model` 不一致 |
| **备份区故障**(双备份区均坏) | 设备上报 `ota_failed(reason=self_check_failed)` / 看 `device_meta.firmware_status` |
| **网关侧推送链路问题** | MQTT QoS 1 重试耗尽 → 看 `ota_command.attempt_count` |

---

## 四、回滚补救(已经发生)

```bash
# 1. 立即停止后续 OTA 推送
# P1-10:本期未实现 /internal/ota/abort-all(需代码动工后补 admin API)
# 临时手动方案:在 PC 后台"OTA 计划"页批量取消 pending 项,或:
docker exec chargepilot-mysql mysql -uroot -p"$MYSQL_ROOT_PASSWORD" \
  -e "UPDATE admin_db.ota_schedule SET status='cancelled', cancelled_at=NOW() WHERE status='pending';"

# 2. 下线带新固件失败的设备(强制标 fault)
# P1-10:gateway_db.device 表只有 status 字段,没有 reason 列(见 db/gateway.md 表 2)
# 故障原因写 alert_event(已有),设备表 status=fault 已足够表达
docker exec chargepilot-mysql mysql -uroot -p"$MYSQL_ROOT_PASSWORD" \
  -e "UPDATE gateway_db.device SET status='fault' WHERE firmware_version='新版本号' AND status<>'fault';"
# 同步写告警事件(代码动工后,worker 周期任务自动做):
# INSERT admin_db.alert_event (device_id, alert_type='ota_failure', severity='fatal', ...)

# 3. 通知客户运维:这些设备**不要重启桩** → 联系现场维护
```

---

## 五、客户应急通知(给客服)

```
[OTA 故障应对] 2026-09-25 ...
- 现象:XXX 站点 N 台设备升级失败已自动回滚
- 影响范围:[站点名称 / 设备型号 / 数量]
- 现状态:充电业务正常 / 部分桩需要现场维护
- 处理建议:
  1. 暂停在该批设备上推新活动(如满减)
  2. 客服电话告知住户:"X 站充电桩临时维护"
  3. 故障期间订单 → 系统自动检测 → 自动退款,无需手动干预
- 恢复时间:预计 [具体时间]
```

---

## 六、永久预防(P1-10:标注代码未实现)

> **P1-10 状态说明**:以下项目为**计划提供**,代码动工后按文档落地。

- **强制签名校验**:`ota_package.signature` 用厂商私钥签名 + SHA-256,设备端严格验证(见 `docs/技术规格.md` § 6.6,**本期未实现**,代码动工后由 `ota_signature` crate 完成)
- **升级窗口显式标注**:在 admin PC 后台发 OTA 时必须确认"凌晨 02:00-04:00 维护窗口"+ 短信通知到客户运维(本期未集成短信网关,仅 Webhook 通道)
- **预发布灰度**:`docs/技术规格.md` § 6.6 说明本期不做灰度,**但客户在自有小范围预发布后**,需要客户在系统外记录"灰度结果" → 上线前必须看到这份记录
- **失败率告警**:升级窗口内如果失败率 > 5% → 自动 abort,告警推送给客户管理员(代码动工后由 worker 周期任务实现)
- **客户运维 SOP**:每台新设备入网后,**先空跑 1 周不上 OTA**,观察设备健康度
