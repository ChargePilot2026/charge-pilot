# Go 本地开发

当前后端仍在重建，不应以开发容器启动成功作为业务功能验收。设备协议端口 `9100/TCP`、`central` 的 `8080/HTTP`、`gateway` 内部端口 `8083`、`worker` 健康端口 `8085` 均只绑定本机。PC 前端在 `5173`。

## 首次启动

使用 MySQL 8.4、Redis 8 和 Go 1.27。Compose 的测试密码仅用于本机。迁移程序要求空数据库或已有 Goose 记录；若本机仍是旧代码创建的测试库，应先备份需要保留的内容并清空测试卷，再运行：

```bash
docker compose -f compose.dev.yaml up --build
```

本次用户已确认 MySQL 中只有测试数据、允许重建。`docker compose -f compose.dev.yaml down -v` 会删除该开发栈的所有数据库和 Redis 卷，执行前确认没有需要保留的本地测试结果。

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

微信小程序登录需要真实 `WECHAT_APPID` 和 `WECHAT_SECRET` 才能与微信联调；开发占位值只允许用假交换器执行本地 HTTP 集成测试。支付、OTA 实机及完整运营页面仍处于待实现或待联调状态，以 [Go 重建清单](migration/go-rebuild.md) 为准。

## 后台登录（Go）

`central` 提供 `/api/v1/admin/auth/login`、`refresh`、`logout`、`me`、`change-password`。
开发 Compose 首次启动使用 `admin / ChangeMe!Admin2026`，可用 `.env` 中的
`ADMIN_BOOTSTRAP_USER` / `ADMIN_BOOTSTRAP_PASSWORD` 覆盖。初始化只在后台账号表为空时执行，
重启服务不会重置已有密码。生产 Compose 没有默认密码，切换门禁仍关闭。

已有开发库升级（保留数据）：

```bash
docker compose -f compose.dev.yaml run --rm migrate
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
