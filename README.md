# ChargePilot

二轮车充电运营平台。Go 后端、PC 后台与 Taro 用户端已具备本地双设备联调环境，支持模拟登录、支付、充电、结算与退款。正式发布仍需微信商户、实机协议与外部平台验收，具体范围见[需求分析](docs/需求分析.md)和[Go 重建清单](docs/migration/go-rebuild.md)。

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
migrations/        五个 MySQL schema 的 init SQL
admin-web/         PC 前端
miniprogram/       Taro + React 用户端（H5 / 微信小程序）
docs/              需求、接口、数据库和迁移验收记录
```

`dc589` 对应当前提供的设备协议 PDF。新协议应实现 `internal/gateway/protocol.Adapter`，可以在新 TCP 端口监听。MQTT 当前暂缓。

## 本地验证

明早测试入口见[双模拟器本地验收](docs/local-test-guide.md)。运行 `./scripts/start-dev.ps1`，在两个终端分别运行 `./scripts/start-simulator.ps1 -Number 1` 和 `-Number 2`。后台在 `5173`，用户 H5 在 `5174`。

后端格式化使用 gofmt，lint 使用 go vet。首次克隆后执行 `./scripts/install-hooks.ps1`（PowerShell）或 `sh scripts/install-hooks.sh`，启用仓库 pre-commit；提交前强制检查工作区及已暂存 Go 源码格式，再运行 go vet，失败则阻止提交。CI 使用同一检查入口。

```powershell
go run ./tools/backend-check -fix -fmt-only # 格式化；之后重新暂存
go run ./tools/backend-check                # fmt + lint
```

也可使用 `make fmt`、`make lint`、`make check`。数据库尚未发布，直接修改 [init](migrations/README.md) 后重建开发表结构。设备默认拥有所选协议声明的全部能力，不需要逐台人工核验。[双模拟器与完整 TUI](docs/simulator-dc589.md) 可用于后台和充电用户流程测试。

```bash
go test ./...
go vet ./...
docker compose -f compose.dev.yaml config --quiet
```

本地开发容器使用 MySQL 8.4、Redis 8、Go 1.27。首次运行需要空的测试数据库卷；旧 Rust 版本建表没有 Goose 迁移记录，迁移程序会拒绝在这种库上继续写入。详细进度和待验收项目见 [Go 重建清单](docs/migration/go-rebuild.md)。

`scripts/start.sh` 暂时阻止生产启动，以执行“全部迁移后一次切换”的决定。支付商户资料和 `dc589` 设备 / OTA 补充资料未到位，外部联调尚不可执行。
