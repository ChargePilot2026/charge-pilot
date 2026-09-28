# Runbook 索引

> **目的**:运维订阅内必备的 SOP 与应急手册。本目录文档被 `技术规格.md` 多个章节引用,首次部署上线前必须补齐。
> **配套**:`checklists/customer-onboarding.md`(主动预防)+ `diagrams/data-lineage.md`(故障定位定位)+ `技术规格.md` § 9 / § 14

---

## 已存在的 Runbook

| 文件 | 触发条件 | 责任角色 | 引用技术规格 |
| --- | --- | --- | --- |
| `cert-renew-failed.md` | Caddy 证书续期失败 | 客户运维 | § 9.1 |
| `backup-restore.md` | MySQL 备份失败 / 数据丢失恢复 | 客户运维 / 我们远程 | § 14.4 |
| `gateway-crash.md` | gateway 进程崩溃 / 设备全离线 | 客户运维 | § 3.1 |
| `ota-mass-failure.md` | OTA 全量推送后大量设备回滚 | 客户运维 / 我们远程 | § 6.6 |
| `webhook-delivery-failure.md` | Webhook 没收到告警 / 投递记录大量失败 | 客户运维 | § 7 |

---

## 待补充 Runbook(下次迭代)

- `mysql-down.md`:MySQL 主进程崩溃应急预案(RTO ≤ 4h)
- ~~`redis-stream-lag.md`:Redis Stream 累积延迟排查~~ —— **DLQ 积压部分已并入** `webhook-delivery-failure.md` §4
- `wechat-pay-outage.md`:微信支付侧故障 → 降级到余额支付?
- `data-retention-failed.md`:周期任务未执行 → 紧急手工归档
  （⚠️ **D22 已删除 4 个未注册的空壳循环**,其中含 `data_retention` 同类;
  本项仍待补,但**「周期任务未执行」目前不是已知故障**——已注册的只有
  `announcement_expire` / `snapshot_warmer` / `device_session_clean` / `dlq_replay`)
- `device-firmware-corrupt.md`:用户桩端变砖 → 现场刷机 SOP
