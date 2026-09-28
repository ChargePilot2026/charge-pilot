# ChargePilot

二轮车充电运营平台。后端正在按[需求分析](docs/需求分析.md)从头用 Go 重建。当前代码**不能用于生产**；用户、订单、支付、退款、后台运营和财务闭环尚未完成。全部规划功能与外部联调完成后才一次切换。

## 结构

```text
cmd/gateway/       设备 TCP 接入与内部 HTTP
cmd/central/       用户、运营、财务 Gin API（单进程单端口）
cmd/worker/        Outbox 发布、计划任务与事件消费
cmd/migrate/       Goose 数据库迁移
internal/gateway/  设备协议适配器；dc589 独立监听 :9100
internal/central/  用户、订单、支付、后台及财务领域
internal/worker/   异步任务
internal/platform/ 配置、数据库、鉴权、HTTP 基础设施
migrations/        五个 MySQL schema 的 SQL 迁移
admin-web/         PC 前端
miniprogram/       微信小程序
docs/              需求、接口、数据库和迁移验收记录
```

`dc589` 对应当前提供的设备协议 PDF。新协议应实现 `internal/gateway/protocol.Adapter`，可以在新 TCP 端口监听。MQTT 当前暂缓。

## 本地验证

```bash
go test ./...
go tool sqlc generate
docker compose -f compose.dev.yaml config --quiet
```

本地开发容器使用 MySQL 8.4、Redis 8、Go 1.27。首次运行需要空的测试数据库卷；旧 Rust 版本建表没有 Goose 迁移记录，迁移程序会拒绝在这种库上继续写入。详细进度和待验收项目见 [Go 重建清单](docs/migration/go-rebuild.md)。

`scripts/start.sh` 暂时阻止生产启动，以执行“全部迁移后一次切换”的决定。支付商户资料和 `dc589` 设备 / OTA 补充资料未到位，外部联调尚不可执行。
