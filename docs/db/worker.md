# worker_db 当前表结构

以 [初始化 SQL](../../migrations/worker_db/0001_init.sql) 为准。当前有 5 张业务表，另有 goose_db_version。全库清理与迁移说明见 [当前数据库及精简清单](central.md)。

| 表 | 用途 |
| --- | --- |
| `comp_tx_log` | 退款完成事件的消费幂等与审计记录 |
| `dlq_log` | 死信队列日志 |
| `dlq_replay_cursor` | DLQ 重放滑动游标(D21) |
| `scheduled_task` | 定时任务定义 |
| `task_execution_log` | 定时任务执行日志 |

DLQ 使用 Redis 保存原事件，dlq_log 记录处理历史，dlq_replay_cursor 持久保存重放游标。scheduled_task 使用任务租约防止多实例重复执行；仅注册 webhook_dispatch。任务执行日志按一天无效果成功记录、三十天其他完成记录分批清理，运行状态保留。
