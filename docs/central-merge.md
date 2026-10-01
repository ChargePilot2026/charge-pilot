# central 服务边界

后端运行进程为 `gateway`、`central`、`worker`。`central` 在一个 Gin HTTP 端口 `:8080` 下承载 `/api/v1/public`、`/api/v1/user`、`/api/v1/admin` 与财务路由；`user`、`admin`、`billing` 是领域模块，不再各自运行进程。

Go 源码使用根模块和 `cmd/`、`internal/` 结构。当前 `internal/central/identity` 已实现部分登录与资料接口；其余领域进度见 [Go 重建清单](migration/go-rebuild.md)。不应将三进程启动成功理解为全部功能合并完成。

MySQL 使用三个 schema：gateway_db、central_db、worker_db。central 的用户、运营及计费模块通过 DATABASE_URL_CENTRAL 共用一个连接池。迁移由 cmd/migrate 独立执行；旧开发库保留数据的合并方式见 [数据库精简清单](db/central.md)。

跨服务共用的配置、鉴权、数据库连接与 HTTP 包装放在 `internal/platform`；领域规则保留在对应目录。新增设备协议实现 `internal/gateway/protocol.Adapter`，可使用独立 TCP 端口。当前 `dc589` 默认使用 `:9100`，MQTT 暂缓。
