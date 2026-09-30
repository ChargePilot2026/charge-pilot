# 数据库初始化

项目尚未发布，五个 schema 各保留一份 `0001_init.sql`，直接定义完整表结构、索引、权限及计划任务种子。结构修改直接编辑对应 CREATE TABLE；发布前不新增递增迁移，不用 ALTER 兼容旧开发库。

初始化空库：配置 `DATABASE_URL_GATEWAY`、`DATABASE_URL_USER`、`DATABASE_URL_ADMIN`、`DATABASE_URL_BILLING`、`DATABASE_URL_WORKER`，执行 `go run ./cmd/migrate -schema all`。同一份 init 重复执行不会重复建表。数据库 URL 必须对应指定 schema。

旧开发库不能沿用历史 Goose 版本：先备份，然后停止 central/gateway/worker，删除并重建这五个 schema（或清空本项目开发 MySQL 卷），再初始化并重启服务。初始化工具会拒绝包含旧迁移版本或没有 Goose 记录的旧表。**不要只删除 Goose 版本表**，业务表也必须重建。

`Down` 用于可丢弃开发库的完整回滚，不用于保留业务数据。MySQL DDL 不支持事务回滚；初始化中断时应重新建立空 schema 后重试。发布后再确定正式迁移版本及兼容策略。

设备的 `protocol_adapter` 在创建/导入时从所选厂商协议写入，执行和计量能力由 Go 协议注册表推导；不再保存人工核验字段。设备运营状态与连接状态分开保存。
