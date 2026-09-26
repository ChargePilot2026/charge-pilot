# Docker 本地开发与热更新

使用独立的 `compose.dev.yaml`。需要 Docker Desktop 正在运行，并使用 Linux containers。
宿主机无需安装 Rust、Node.js、MySQL 或 Redis。此配置的固定密码仅用于本机开发；映射端口均绑定 `127.0.0.1`。

## 启动

在项目根目录执行：

```powershell
docker compose -f compose.dev.yaml up --build
```

首次启动需要下载镜像、安装文件监听工具、编译 Rust 依赖和安装 npm 依赖。
出现前端 ready 不代表后端编译完成；等待 admin 日志出现 `admin listening` 后再登录。

- PC 后台：<http://localhost:5173/admin/>
- 开发管理员：`admin` / `DevAdmin2026!`
- 管理接口健康检查：<http://localhost:8082/api/v1/health>
- 小程序接口：<http://localhost:8081/api/v1>
- gateway / billing HTTP：`localhost:8083` / `localhost:8084`
- worker 健康检查：<http://localhost:8085/health>
- 设备接入：`localhost:9100`（TCP）、`localhost:1883`（MQTT）

如果当前 PowerShell 找不到 docker，但已安装在默认目录：

```powershell
$env:Path += ';C:\Program Files\Docker\Docker\resources\bin'
docker version
```

`docker version` 必须同时显示 Client 和 Server。仅显示 Client 或提示无法连接 named pipe 时，先启动 Docker Desktop。

只开发后台时可减少启动的服务：

```powershell
docker compose -f compose.dev.yaml up --build admin-web
```

这会自动启动 admin、MySQL 和两个 Redis；涉及 user、billing、gateway 的跨服务功能需要启动完整栈。

## 热更新机制

| 修改内容 | 行为 |
| --- | --- |
| `admin-web/src` 的 React、TS、CSS | Vite HMR 自动更新页面 |
| `services/admin` 等服务源码 | 对应 Rust 服务自动重新编译、重启 |
| `crates` 或 `services/api-contracts` | 所有运行中的 Rust 服务自动重新编译、重启 |
| 根目录 `Cargo.toml` / `Cargo.lock` | Rust 服务自动重新编译、重启 |
| `admin-web/package.json` / lockfile | 更新锁文件和依赖后重启 admin-web，见下文 |
| Compose 中的环境变量、端口 | 重新执行 `docker compose -f compose.dev.yaml up -d` |
| `docker/dev/Dockerfile.rust` | 重新执行 `up --build` |
| `docker/dev/watch-rust.sh` | 执行 `docker compose -f compose.dev.yaml restart admin` 等重启对应服务 |
| `migrations` | 不会自动修改已有数据库，须显式执行数据库迁移 |

Rust 使用 watchexec 监听，每秒轮询一次；这是重新编译和进程重启，会有短暂接口不可用，内存状态和设备长连接会丢失。
前端启用 500ms 轮询，兼容 Windows 编辑器与 Docker Desktop/WSL2 的文件挂载。
业务源码挂载进容器，Rust target 和 node_modules 放在 Docker 命名卷中，避免混用 Windows/Linux 产物。
每个 Rust 服务使用独立 target 子目录，避免五个服务争抢编译目录锁，但首次构建和磁盘开销也更大。

前端浏览器请求保持同源，由 Vite 在容器内代理到 `http://admin:8082`。
`DEV_API_PROXY_TARGET` 只配置代理；不要把 Docker 主机名写入浏览器使用的 `VITE_API_BASE`。

参考：[Vite 5 文件监听](https://v5.vite.dev/config/server-options#server-watch)、[watchexec 参数](https://github.com/watchexec/watchexec/blob/main/doc/watchexec.1.md)。

## 常用调试命令

```powershell
# 后台启动 / 查看状态 / 查看日志
docker compose -f compose.dev.yaml up --build -d
docker compose -f compose.dev.yaml ps
docker compose -f compose.dev.yaml logs -f admin admin-web

# 接口健康和登录检查
Invoke-RestMethod http://localhost:8082/api/v1/health
$body = @{ username = 'admin'; password = 'DevAdmin2026!' } | ConvertTo-Json
Invoke-RestMethod http://localhost:8082/api/v1/admin/auth/login -Method Post -ContentType 'application/json' -Body $body

# 在运行中的容器内执行后端测试（复用对应服务构建缓存）
docker compose -f compose.dev.yaml exec -e CARGO_TARGET_DIR=/cargo-target/admin admin cargo test --locked -p admin

# 添加前端依赖，package.json 和 package-lock.json 会写回宿主机
docker compose -f compose.dev.yaml exec admin-web npm install <包名>
docker compose -f compose.dev.yaml restart admin-web

# 修改了 Cargo 依赖后，用可写挂载更新宿主机锁文件，再正常启动
docker run --rm -v "${PWD}:/workspace" -w /workspace rust:1-bookworm cargo generate-lockfile

# 停止，保留数据库及依赖缓存
docker compose -f compose.dev.yaml down
```

前端可以使用浏览器开发者工具断点调试。Rust 默认提供 debug 构建、日志和 `RUST_BACKTRACE=1`；本配置不包含 IDE/LLDB 远程断点调试器。

## 数据与账号初始化

首次创建 MySQL 数据卷时，官方镜像创建 `chargepilot` 账号，`init-mysql.sh` 创建五个数据库、授权并导入各库的 `0001_init.sql`。
后续启动不会重导 SQL，也不会清空数据。这遵循 [MySQL 官方镜像初始化规则](https://hub.docker.com/_/mysql)。

admin 启动前运行 `seed_dev_admin`，仅在 `RUNTIME_ENV=dev` 下执行，并且不会覆盖已有同名管理员的密码。
开发账号用于当前管理接口调试，不会填充业务演示数据或完整角色权限目录。

如需在首次创建账号前自定义用户名、密码，可以在当前 PowerShell 设置：

```powershell
$env:DEV_ADMIN_USER = 'developer'
$env:DEV_ADMIN_PASSWORD = 'YourLocalPassword'
docker compose -f compose.dev.yaml up --build
```

已有账号不会因为修改变量被重置。后端容器不会读取根目录部署用 `.env`，连接地址均由开发 Compose 明确指定。
数据库工具可连接 `127.0.0.1:3306`，账号 `chargepilot`、密码 `chargepilot_dev`。
Redis Cache 在 `6379`，Stream 在 `6380`，本地开发均无密码。

确实需要丢弃本地开发数据、重新初始化时：

```powershell
# 删除本开发栈全部命名卷：数据库、Redis 事件和依赖/编译缓存均会丢失。
docker compose -f compose.dev.yaml down -v
docker compose -f compose.dev.yaml up --build
```

微信配置默认使用占位值，真实微信登录、支付和回调仍需有效配置及相应网络访问条件。
微信开发者工具可使用上述 user 接口地址；真机无法访问电脑的 localhost，本配置默认也不向局域网发布端口。

## 验证热更新

1. 打开后台页面，修改 `admin-web/src/pages/Login.tsx` 的标题，确认浏览器无需手动刷新便更新。
2. 修改 `services/admin/src/api/mod.rs` 的 health 返回值，观察 admin 日志重新编译，再请求健康接口确认返回值改变。
3. 撤销两处临时修改，确认页面和接口自动恢复。
4. 停止后再次启动，确认管理员账号和数据库数据保留。

容器端到端验证需要 Docker 引擎运行；Compose 配置校验或 Rust 编译检查本身不能证明热更新和数据库初始化已在容器内通过。
