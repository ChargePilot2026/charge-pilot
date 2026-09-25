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

---

## 待补充 Runbook(下次迭代)

- `mysql-down.md`:MySQL 主进程崩溃应急预案(RTO ≤ 4h)
- `redis-stream-lag.md`:Redis Stream 累积延迟排查
- `wechat-pay-outage.md`:微信支付侧故障 → 降级到余额支付?
- `data-retention-failed.md`:周期任务未执行 → 紧急手工归档
- `device-firmware-corrupt.md`:用户桩端变砖 → 现场刷机 SOP
