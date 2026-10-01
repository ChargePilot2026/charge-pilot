# 部署配置与维护入口

配置统一使用仓库根目录，不再维护 examples 副本。

| 文件 | 用途 |
| --- | --- |
| docker-compose.yml | 生产服务、数据库、Redis、Caddy 的唯一拓扑 |
| compose.dev.yaml | 本地开发环境 |
| .env.example | 生产环境变量模板，复制到根目录 .env 后填写 |
| docker/Dockerfile | gateway、central、worker、migrate 共用构建文件，SERVICE 指定目标 |
| docker/Caddyfile | HTTPS、后台静态资源、API 代理及内部 API 屏蔽 |
| docker/mysql/init-databases.sh | 新 MySQL 数据卷初始化三个 schema 和应用账号授权 |

MySQL 初始化脚本由开发和生产 Compose 共用，只建库和授权；所有业务表由 cmd/migrate 执行 Goose 初始化。已有数据卷不会重复执行初始化脚本。

生产后端为 gateway、central、worker。central 在 8080 承载用户、运营与财务接口，gateway 的内部 HTTP 为 8083，worker 为 8085；公网只发布 HTTPS 80/443 和设备 TCP 9100。MySQL 和两套 Redis 使用容器内网。Caddy 将 /api/v1/internal/* 拒绝为 404。

## 配置验证

使用 Node.js 24 和 Docker：

```bash
node scripts/ops/check-deploy.mjs
```

检查实际 Compose 配置、构建及挂载路径、端口、三库连接、Redis 内存策略、凭证模板值和 Caddy 语法。Caddy 校验使用生产配置声明的镜像，首次会下载镜像；临时验证容器结束即删除，不启动生产栈。准备实际部署时先构建 admin-web/dist。

仅在本地用占位环境变量验证配置结构时使用 --config-only，它跳过凭证检查，仍检查 Compose 和 Caddy。配置校验不替代 [业务与外部联调验收](migration/go-rebuild.md)。

## 开发、测试和数据维护

```powershell
./scripts/dev/start.ps1
./scripts/dev/simulator.ps1 -Number 1
./scripts/dev/simulator.ps1 -Number 2
node scripts/dev/prepare.mjs
./scripts/test/integration.ps1
./scripts/git/install-hooks.ps1
```

Linux / Git Bash 的隔离测试和 hook 安装分别使用 scripts/test/integration.sh、scripts/git/install-hooks.sh。旧单项 HTTP 探针由当前 Go 集成测试覆盖；真实 DC589 模拟器支持两个设备的交互联调。

三库备份与恢复位于 scripts/db/backup.sh、scripts/db/restore.sh。scripts/db/reset-dev.ps1 -ResetDevelopmentData 只用于明确清空并重建本地开发数据，先备份，再重建数据库并清空项目 Redis。数据库精简过程中使用的一次性迁移工具已删除，实施记录与备份保留在 [数据库精简清单](db/central.md)。
