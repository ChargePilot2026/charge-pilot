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
migrations/        三个 MySQL schema 的 init SQL
admin-web/         PC 前端
miniprogram/       Taro + React 用户端（H5 / 微信小程序）
docs/              需求、接口、数据库和迁移验收记录
```

`dc589` 对应当前提供的设备协议 PDF。新协议应实现 `internal/gateway/protocol.Adapter`，可以在新 TCP 端口监听。MQTT 当前暂缓。

## 辅助目录

- docker/：唯一的服务 Dockerfile、Caddy 配置和 MySQL 初始化脚本。
- scripts/dev/：开发栈、双模拟器与本地联调数据准备。
- scripts/db/：三库备份、恢复和明确清空开发数据的重建工具。
- scripts/test/：隔离 MySQL/Redis 的完整集成测试。
- scripts/git/：安装 Go 静态检查与单元测试提交钩子。
- scripts/ops/：校验根目录的生产 Compose 与 Caddy 配置。

部署说明见 [部署配置](docs/deployment.md)。重复 examples、副本检查器、旧 HTTP 探针和一次性迁移脚本已删除。

## 本地验证

双模拟器联调入口见[双模拟器本地验收](docs/local-test-guide.md)。运行 `./scripts/dev/start.ps1`，在两个终端分别运行 `./scripts/dev/simulator.ps1 -Number 1` 和 `-Number 2`。后台在 `5173`，用户 H5 在 `5174`。

后端检查由 Makefile 直接调用 `go fmt`、`go vet` 和 `go test`，需要安装 Go 与 Make。首次克隆后执行 `./scripts/git/install-hooks.ps1`（PowerShell）或 `sh scripts/git/install-hooks.sh`，启用仓库 pre-commit；提交前运行 `make lint test`，失败则阻止提交。格式化请在暂存前执行 `make fmt`；CI 执行格式化后检查 Git 差异，格式不符合要求时失败，再运行静态检查与单元测试。

```powershell
make fmt   # 格式化；之后重新暂存
make lint  # go vet ./...
make test  # go test ./...
make check # 依次执行 fmt、lint、test
```

Windows 未安装 Make 时，可在开发容器中运行 `docker compose -f compose.dev.yaml run --rm --no-deps -T central make check`，或直接执行上述 Go 命令。

数据库尚未发布，直接修改 [init](migrations/README.md)。五库合并已完成，当前使用三个数据库，实施记录见 [数据库精简清单](docs/db/central.md)。设备默认拥有所选协议声明的全部能力，不需要逐台人工核验。[双模拟器与完整 TUI](docs/simulator-dc589.md) 可用于后台和充电用户流程测试。

```bash
go test ./...
go vet ./...
# 完整隔离数据库回归：scripts/test/integration.sh；Windows 使用 ./scripts/test/integration.ps1
docker compose -f compose.dev.yaml config --quiet
```

本地开发容器使用 MySQL 8.4、Redis 8、Go 1.27。首次运行需要空的测试数据库卷；旧 Rust 版本建表没有 Goose 迁移记录，迁移程序会拒绝在这种库上继续写入。详细进度和待验收项目见 [Go 重建清单](docs/migration/go-rebuild.md)。

生产部署前须完成 [Go 重建清单](docs/migration/go-rebuild.md) 中的业务与外部联调验收，并运行 `node scripts/ops/check-deploy.mjs` 校验实际部署配置。
