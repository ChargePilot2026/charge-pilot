# gateway_db 当前表结构

以 [初始化 SQL](../../migrations/gateway_db/0001_init.sql) 为准。当前有 15 张业务表，另有 goose_db_version。全库清理与迁移说明见 [当前数据库及精简清单](central.md)。

| 表 | 用途 |
| --- | --- |
| `card_event_delivery` | 原生刷卡事件投递与重试 |
| `charge_command` | 设备启动命令和确认状态 |
| `charge_end_delivery` | 设备结束事件投递与重试 |
| `charge_process` | 已确认充电订单的 A4 心跳过程记录，无自动保留期清理 |
| `charge_stop_command` | 设备停止命令和确认状态 |
| `device` | 设备元数据 |
| `device_event` | 设备事件及原始上下文 |
| `device_port` | 设备端口(每端口独立二维码) |
| `device_provision` | 设备开通请求幂等记录 |
| `device_session` | 设备长连接会话 |
| `event_outbox` | 与业务事务一起写入的待投递事件 |
| `telemetry` | 设备遥测原始记录(按 ts 分区,1 月保留) |
| `telemetry_aggregate_15min` | 遥测 15 分钟聚合 |
| `telemetry_aggregate_hourly` | 遥测小时聚合 |
| `vendor` | 硬件厂适配器注册表 |

设备端口、控制指令、投递凭据与遥测保留。设备级聚合唯一键使用 port_key 生成列，将 NULL 映射为 0；实际端口从 1 开始。原始遥测与聚合桶均按月分区，历史分区策略保留。
