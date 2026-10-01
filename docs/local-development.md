# Go 本地开发

设备协议端口 `9100/TCP`、`central` 的 `8080/HTTP`、`gateway` 内部端口 `8083`、`worker` 健康端口 `8085` 均只绑定本机。PC 后台在 `5173`，Taro 用户 H5 在 `5174`。完整启动与测试顺序见 [双模拟器本地验收](local-test-guide.md)。

## 首次启动

使用 MySQL 8.4、Redis 8 和 Go 1.27。Compose 的测试密码仅用于本机。迁移程序要求空数据库或已有 Goose 记录；若本机仍是旧代码创建的测试库，应先备份需要保留的内容并清空测试卷，再运行：

```bash
./scripts/dev/start.ps1
```

开发库尚未发布。需要从修改后的 init 重建时执行 `./scripts/db/reset-dev.ps1 -ResetDevelopmentData`；脚本先备份三个 schema，再重建表并重新初始化。不要把增量 migration 或 ALTER 加回当前初始化目录。

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

Compose 显式配置 `LOGIN_MODE=development`、`PAYMENT_MODE=simulation`。H5 使用 `dev:<账号>` 登录，后端返回真实本地用户会话；模拟支付仍经过支付订单、确认回调、启动、结算及退款链路。开发登录只能与模拟支付配合开启。正式微信登录需要真实 AppID/Secret，正式支付与设备实机仍需外部验收。

## 后台登录（Go）

`central` 提供 `/api/v1/admin/auth/login`、`refresh`、`logout`、`me`、`change-password`。
开发 Compose 首次启动使用 `admin / ChangeMe!Admin2026`，可用 `.env` 中的
`ADMIN_BOOTSTRAP_USER` / `ADMIN_BOOTSTRAP_PASSWORD` 覆盖。初始化只在后台账号表为空时执行，
重启服务不会重置已有密码。生产 Compose 须配置独立凭证，并完成业务与外部联调验收。

仅重启应用（保留当前 init 数据）：

```bash
docker compose -f compose.dev.yaml up -d --no-deps --force-recreate central gateway worker
```

登录页地址为 `http://localhost:5173/admin/login`。Vite 默认将后台 API 代理到
`localhost:8080`；容器内通过 `DEV_API_PROXY_TARGET=http://central:8080` 转发。
若登录接口返回 404，请检查是否仍运行旧的 central 进程；Go 的 `go run` 不自动热重载。

## 一次性集成验收

```bash
scripts/test/integration.sh
# 含竞争检测
scripts/test/integration.sh -race
```

脚本创建独立 MySQL 8.4 和 Redis 8 容器、随机本机端口、从空库迁移三个 schema，
设置全部 `TEST_*_DATABASE_URL` 与 Redis 测试变量，顺序运行各包集成测试并清理测试容器。
不复用或清空正在运行的开发库。单独 `go test ./...` 未配置这些变量时会跳过集成测试。
# 网关日志与时区

开发 Go 容器设置 `TZ=Asia/Shanghai`，标准日志使用北京时间。Docker 自己附加的时间戳可能仍以 UTC 显示，请以应用日志中的时间和启动日志的时区为准。

后台订单列表与详情中，充电中的电量来自该订单开始后、对应设备端口的心跳累计计量；费用使用订单冻结方案计算当前估算，不写入最终结算字段。列表每 5 秒刷新；超过 30 秒没有读数则标注旧值，费用不按断网后的时间继续增加。无法可靠定价时明确等待计量，不以套餐支付金额替代实际费用。

金额套餐由 worker 每 10 秒检查预算，依据最近 30 秒内的设备心跳。首次心跳前或重连期间缺少功率片段时，按冻结费率计算费用下限；即使下限也达到预算，就记录 `budget_exhausted` 截止并下发 STOP。只有费用上下界一致时才冻结电费与服务费拆分，否则最终费用仍走计量复核，不将估算当成结算。订单收到设备结束上报后才变为已完成。worker 日志的 `charge budget exhausted` 会输出订单号、预算、费用下限、采样时间和拆分是否确定；无法定价会输出 `charge budget unavailable`，不会静默跳过。

新充电订单号为北京时间启动请求时间 `YYYYMMDDHHmmss` + 设备编号 + 端口号（至少两位，不足补 0）。编号在支付成功/刷卡授权后、向设备发起启动前生成并保持不变；设备回执时间仍单独记录在 `started_at`。历史订单号保留，同端口同秒冲突会拒绝创建，不追加随机后缀。

网关输出启动监听地址、时区、设备上线/离线、指令发送与回执、充电结束及故障日志。上下行报文包含指令中文名称和 `data` JSON 解析结果，使用 W、kWh、秒、分钟等明确单位；启动显示端口、设备订单号、模式和购买量，停止显示结果含义，结束显示计量和停止原因。心跳调试日志还包含电压、温度、每个端口状态和充电遥测。`device_order` 是协议中的 BCD 设备订单号，并非业务 `CH...` 订单号。

日志包含设备编号、命令号和关联 session，不输出卡号、余额、SIM 标识、面板密码或原始报文。解析失败明确标记 `invalid_payload`，未知指令标记 `unsupported_command`；不会把错误数据作为有效计量输出。

```powershell
docker compose -f compose.dev.yaml logs -f --tail 100 gateway
```

心跳收发日志默认关闭。调试时在当前 PowerShell 设置开关并重建 gateway 容器，模拟器会自动重连：

```powershell
$env:GATEWAY_DEBUG_HEARTBEAT = 'true'
docker compose -f compose.dev.yaml up -d --no-deps --force-recreate gateway
```

恢复为 `false` 后再次执行同一重建命令即可关闭。该操作重启网关，会短暂中断设备连接。
