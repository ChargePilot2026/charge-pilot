# Go 本地开发

设备协议端口 `9100/TCP`、`central` 的 `8080/HTTP`、`gateway` 内部端口 `8083`、`worker` 健康端口 `8085` 均只绑定本机。PC 后台在 `5173`，Taro 用户 H5 在 `5174`。完整启动与测试顺序见 [双模拟器本地验收](local-test-guide.md)。

## 首次启动

使用 MySQL 8.4、Redis 8 和 Go 1.27。Compose 的测试密码仅用于本机。迁移程序要求空数据库或已有 Goose 记录；若本机仍是旧代码创建的测试库，应先备份需要保留的内容并清空测试卷，再运行：

```bash
./scripts/start-dev.ps1
```

开发库尚未发布。需要从修改后的 init 重建时执行 `./scripts/reset-dev-db.ps1 -ResetDevelopmentData`；脚本先备份五个 schema，再重建表并重新初始化。不要把增量 migration 或 ALTER 加回当前初始化目录。

## 单独验证

```bash
go test ./...
go vet ./...
docker compose -f compose.dev.yaml config --quiet
```

集成测试在指定**一次性测试 schema**时才运行，例如：

```bash
TEST_USER_DATABASE_URL='mysql://chargepilot:chargepilot_dev@127.0.0.1:3306/go_user_verify' \
TEST_REDIS_URL='redis://127.0.0.1:6379/14' \
go test -count=1 ./internal/central/identity
```

Compose 显式配置 `LOGIN_MODE=development`、`PAYMENT_MODE=simulation`。H5 使用 `dev:<账号>` 登录，后端返回真实本地用户会话；模拟支付仍经过支付订单、确认回调、启动、结算及退款链路。开发登录只能与模拟支付配合开启。正式微信登录需要真实 AppID/Secret，正式支付与 OTA 实机仍需外部验收。

## 后台登录（Go）

`central` 提供 `/api/v1/admin/auth/login`、`refresh`、`logout`、`me`、`change-password`。
开发 Compose 首次启动使用 `admin / ChangeMe!Admin2026`，可用 `.env` 中的
`ADMIN_BOOTSTRAP_USER` / `ADMIN_BOOTSTRAP_PASSWORD` 覆盖。初始化只在后台账号表为空时执行，
重启服务不会重置已有密码。生产 Compose 没有默认密码，切换门禁仍关闭。

仅重启应用（保留当前 init 数据）：

```bash
docker compose -f compose.dev.yaml up -d --no-deps --force-recreate central gateway worker
```

登录页地址为 `http://localhost:5173/admin/login`。Vite 默认将后台 API 代理到
`localhost:8080`；容器内通过 `DEV_API_PROXY_TARGET=http://central:8080` 转发。
若登录接口返回 404，请检查是否仍运行旧的 central 进程；Go 的 `go run` 不自动热重载。

## 一次性集成验收

```bash
scripts/test-integration.sh
# 含竞争检测
scripts/test-integration.sh -race
```

脚本创建独立 MySQL 8.4 和 Redis 8 容器、随机本机端口、从空库迁移五个 schema，
设置全部 `TEST_*_DATABASE_URL` 与 Redis 测试变量，顺序运行各包集成测试并清理测试容器。
不复用或清空正在运行的开发库。单独 `go test ./...` 未配置这些变量时会跳过集成测试。
