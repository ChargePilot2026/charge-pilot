# 数据库初始化

项目尚未发布，三个 schema 各保留一份 `0001_init.sql`：gateway_db（15 张业务表）、central_db（85 张）、worker_db（5 张），另有各库自己的 Goose 版本表。结构修改直接编辑 CREATE TABLE；发布前不叠加递增迁移。

初始化空库：配置 `DATABASE_URL_GATEWAY`、`DATABASE_URL_CENTRAL`、`DATABASE_URL_WORKER`，执行 `go run ./cmd/migrate -schema all`。URL 必须对应指定 schema；同一份 init 重复执行不会重复建表。

其他不兼容开发结构变更：先备份，再明确执行 `./scripts/db/reset-dev.ps1 -ResetDevelopmentData` 重建三个库。该命令会清空本项目数据库和 Redis；需要保留数据时应单独设计兼容迁移，不执行 reset。不要只删除 Goose 版本表。

每张表和字段均须注明 MySQL COMMENT；金额写明分，电量、时长写明单位，枚举写明取值，快照与幂等键注明用途。Down 仅用于可丢弃开发库；MySQL DDL 无事务回滚，初始化中断需重建空 schema 后重试。

数据库职责、完整表索引与备份恢复见根 [README](../README.md#数据库)，维护规则见 [AGENTS](../AGENTS.md#数据库变更)。
