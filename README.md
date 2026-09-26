# ChargePilot · 二轮车充电运营管理系统

> **状态**: v0.1.0(代码已落地,等待部署调优)
> **架构**: 5 服务 Rust 后端 + React 19 + antd 6 PC 后台 + 微信小程序
> **部署形态**: 单客户单部署,每客户独立一套

---

## 快速开始

本地 Docker 开发（前端 HMR、Rust 自动编译重启）：

```powershell
docker compose -f compose.dev.yaml up --build
```

访问 `http://localhost:5173/admin/`，开发账号为 `admin` / `DevAdmin2026!`。
首次启动会初始化开发数据库；完整配置、调试和数据保留说明见 [Docker 本地开发指南](docs/local-development.md)。
以下原有一键启动配置用于部署，与 `compose.dev.yaml` 独立。

### 前置依赖

| 工具 | 版本 | 用途 |
| --- | --- | --- |
| Rust | 1.80+ | 后端服务 |
| Node.js | 20+ | PC 后台 + CI |
| Docker | 24+ | 一键启动 |
| MySQL | 8.4 | 数据存储 |
| Redis | 8+ | 业务缓存 + Stream 事件总线 |
| Caddy | 2.11+ | 反代 + TLS 终止 |

### 一键启动(开发机或客户服务器)

```bash
# 1. 克隆仓库
git clone https://github.com/ChargePilot2026/charge-pilot.git
cd charge-pilot

# 2. 复制 .env 模板
cp .env.example .env
# 修改 JWT_SECRET / SERVICE_TOKEN / WECHAT_* / DB_PASSWORD 等

# 3. 启动(自动构建 + 健康检查)
bash scripts/start.sh          # Linux/macOS
.\scripts\start.ps1            # Windows PowerShell
```

启动后:

| 地址 | 内容 |
| --- | --- |
| `http://<host>/admin/` | PC 后台(React SPA) |
| `http://<host>/api/v1/health` | 全栈健康检查 |
| `9100/TCP` | 设备长连接(直连,不经过 Caddy) |
| `1883/MQTT` | MQTT 设备长连接 |

---

## 仓库结构

```
charge-pilot/
├── Cargo.toml                     # Rust workspace(5 服务 + 9 公共库)
├── rust-toolchain.toml            # 锁定 Rust 版本
├── docker-compose.yml             # 一键部署编排
├── Caddyfile                      # 反代 + TLS
├── .env.example                   # 环境变量模板
├── docs/                          # 设计文档(15 章需求 + 15 章技术规格 + 5 个 API + 5 个 DB + 对账)
│
├── crates/                        # 公共库
│   ├── common-error/              # 统一错误码 + 响应包装
│   ├── common-config/             # 环境配置加载
│   ├── common-db/                 # sqlx 连接池 + 迁移 + IdGen
│   ├── common-redis/              # 业务缓存 + Stream + 端口锁
│   ├── common-auth/               # JWT + Argon2 + ServiceToken + Axum middleware
│   ├── common-stream/             # Stream Consumer 抽象 + DLQ
│   ├── common-http/               # 跨服务 HTTP 客户端 + RequestId
│   ├── common-wechat/             # 微信登录 + 支付 + 退款
│   └── common-telemetry/          # tracing + OpenTelemetry 初始化
│
├── services/                      # 5 个独立 Rust 服务(每个都是独立 cargo package)
│   ├── gateway/                   # 设备长连接(TCP 9100 / MQTT 1883)+ 17 个内部 HTTP API
│   ├── user/                      # 小程序 API(8081)+ 30 个端点 + 微信支付回调
│   ├── admin/                     # PC 后台 API(8082)+ 112 个端点 + SPA 静态托管
│   ├── billing/                   # 计费引擎 + 多方分账(8084)
│   └── worker/                    # 12 个定时任务 + Stream 消费
│
├── migrations/                    # 5 个 schema 的 61 张表 DDL
│   ├── gateway_db/0001_init.sql
│   ├── user_db/0001_init.sql
│   ├── admin_db/0001_init.sql
│   ├── billing_db/0001_init.sql
│   └── worker_db/0001_init.sql
│
├── admin-web/                     # React 19 + antd 6 + Vite 6(PC 后台)
│   ├── src/pages/                 # Dashboard / Orders / Devices / Stations / Users /
│   │                              # Alerts / Coupons / Billing / Webhooks / OTA /
│   │                              # Announcements / Settings / Login
│   ├── src/layouts/MainLayout.tsx # 侧边栏 + 顶栏 + Outlet
│   └── src/api/client.ts          # axios + envelope 解析
│
├── miniprogram/                   # 微信小程序(原生 TS)
│   ├── app.ts                     # request 封装 + login
│   ├── pages/                     # 18 个页面
│   └── README.md                  # 小程序部署指南
│
├── tools/
│   └── check-api-consistency.ts   # API ↔ DB ↔ Stream 三方一致性 CI 检查
│
├── scripts/                       # 运维脚本
│   ├── start.sh / start.ps1       # 一键启动(校验 + 构建 + 部署 + 等就绪)
│   ├── dev.sh                     # 本地 cargo run
│   └── check-deploy-config.sh     # Caddy + docker-compose + Redis 配置校验
│
├── examples/
│   ├── docker-compose.yml         # 完整 P0-4 部署范例
│   └── Caddyfile                  # 完整 P0-4 反代配置
│
└── .github/workflows/ci.yml       # GitHub Actions:fmt + clippy + test + build + docker
```

---

## 核心架构

### 5 个服务 + 5 个 schema

| 服务 | 端口 | 数据库 | 关键职责 |
| --- | --- | --- | --- |
| **gateway** | 9100/1883/8083 | gateway_db(8 表) | 设备长连接(TCP/MQTT)+ 遥测落库 + 告警发布 + 启动指令下发 |
| **user** | 8081 | user_db(18 表) | 小程序 API + 微信支付 + 退款编排 + 个人中心 + 钱包 |
| **admin** | 8082 | admin_db(25 表) | PC 后台 API(设备/订单/告警/角色/财务/Webhook/OTA/白标)+ SPA 托管 |
| **billing** | 8084 | billing_db(5 表) | 计费引擎 + 价费分离 + 多方分账 + 提现 |
| **worker** | 8085 | worker_db(5 表) | 12 个定时任务 + Stream 消费(告警/计费/对账/OTA/退款等) |

### 11 个 Redis Stream 事件总线

```
device_event_stream        gateway → admin / worker       设备状态 + 快照缓存
alert_stream               gateway → admin / worker       告警 → Webhook
charge_started_stream      user → gateway                 微信回调成功后下发启动
charge_ended_stream        gateway → billing / user       计费 + 关轮询
refund_required_stream     billing → admin                自动退款触发
invoice_required_stream    billing → admin                发票审核触发
webhook_retry_stream       admin → worker                 Webhook 重试
ota_schedule_stream        admin → worker / gateway       OTA 推送调度
comp_tx_stream             各服务                          跨服务补偿事务
coupon_grant_required_stream admin → user                  运营发券
pricing_rule_changed_stream admin → billing                计费规则变更
```

### 关键约束(不可逆决策)

1. **拆分充电订单 vs 支付订单**:`charge_order`(纯生命周期)+ `payment_order`(纯支付),**不允许合并**
2. **单客户部署**:不引入分布式锁 / 不带 `customer_id` / 客户级隔离由部署边界保证
3. **跨服务数据访问**:完全禁止直连其他 schema,所有跨服务数据通过 HTTP API + Redis Stream
4. **最终一致性**:用 Saga 模式补偿 + 幂等保证,不用分布式事务
5. **扫码 ≠ 启动**(P0-1):扫码 / 选端口只展示;启动 = 微信支付回调成功 + `charge_started_stream` → gateway
6. **端口级三层防护**:逻辑锁(5 min)+ 物理锁(30 s)+ DB 兜底(`active_port_charge` 跨月唯一性)
7. **Redis 拆 cache / stream**:cache 用 allkeys-lru;stream 必须 noeviction(防止事件丢失)
8. **退款 SOP**:微信 API 指数退避(1s/5s/30s/2min),`refund_record.status='failed'` → 客户财务后台人工补退

---

## 开发流程

### 本地开发

```bash
# 启动 5 服务(需先起 MySQL + Redis + 跑迁移)
bash scripts/dev.sh all

# 单服务调试
bash scripts/dev.sh user

# PC 后台
cd admin-web && npm install && npm run dev
# 访问 http://localhost:5173,proxy 到 admin:8082
```

### 代码规范

- **Commit**: Conventional Commits(`feat:` / `fix:` / `refactor:` / `test:` / `docs:` / `chore:`),scope 强制如 `feat(api):` / `fix(db):`
- **Rust**: clippy `-D warnings` + rustfmt 强制
- **PR**: ≥ 1 人 review,AI 生成代码仍需人工 review
- **CI**: 5 服务并行 build/test + 部署配置检查 + docker 镜像构建

### 添加新 API 的流程

1. 在 `docs/api/<service>.md` 新增端点定义
2. 同步更新 `docs/cross-reference.md § 4.x`(端点 ↔ 表)
3. 在 `services/<service>/src/<module>.rs` 实现 handler + 路由
4. 同步更新 `docs/db/<schema>.md`(若有新表)
5. CI 自动跑 `tools/check-api-consistency.ts` 校验一致性

### 添加新 Stream

**禁止**!11 个 Stream 名严格沿用技术规格 § 5.1。新增必须先在 `docs/技术规格.md` + `docs/cross-reference.md § 1` 登记。

---

## 部署清单(客户首次交付)

### 硬件最低

| 资源 | 最低 | 推荐 |
| --- | --- | --- |
| CPU | 4 核 | 8 核 |
| 内存 | 8 GB | 16 GB |
| SSD | 100 GB | 500 GB |
| 公网带宽 | 10 Mbps | 100 Mbps |

### 软件部署步骤

```bash
# 1. 安装 Docker
curl -fsSL https://get.docker.com | sh

# 2. 准备域名 A 记录(charge.example.com → 服务器公网 IP)
# 3. 开放 80/443 + 9100/1883 端口
# 4. 配置 .env(尤其 JWT_SECRET / SERVICE_TOKEN / WECHAT_* / DB_PASSWORD)
# 5. 启动
bash scripts/start.sh

# 6. 验证
curl https://charge.example.com/api/v1/health
```

### 配置微信支付(关键)

1. 微信商户平台 → API 安全 → API v3 密钥
2. 设置回调地址:`https://<domain>/api/v1/public/payment/wechat/callback`
3. 退款需要 API 证书 + 私钥:放入 `services/user/certs/`(本期占位,部署时配)

### 备份策略(技术规格 § 4.6)

- 每日 `mysqldump` 全量
- binlog 增量,异地存储
- RPO ≤ 1 小时,RTO ≤ 4 小时

---

## 文档导航

| 文档 | 内容 |
| --- | --- |
| `docs/需求分析.md` | 业务层:做什么、合规边界 |
| `docs/技术规格.md` | 技术实现层:怎么做 |
| `docs/db/*.md` | 数据层:5 schema 61 张表字段级定义 |
| `docs/api/*.md` | 接口层:5 服务 167 个端点详细设计 |
| `docs/diagrams/*.md` | 时序图 + 状态机 + 数据血缘 |
| `docs/runbook/*.md` | 故障排查手册 |
| `docs/checklists/*.md` | 客户上线 / 等保三级清单 |
| `docs/cross-reference.md` | API ↔ DB ↔ Stream 一致性对账 |
| `docs/glossary.md` | 术语表 |

---

## 商业授权

| 许可证 | 文件 | 适用 |
| --- | --- | --- |
| **AGPL-3.0**(开源) | [`LICENSE`](./LICENSE) | 学习 / 自评 / 内部部署;衍生作品必须同样开源 |
| **商业授权** | [`COMMERCIAL_LICENSE.md`](./COMMERCIAL_LICENSE.md) | 付费买断客户;闭源二开 + 集成自有系统 |

---

## 反馈

- 文档错误 / 漏写:开 Issue 标 `docs:` scope
- 架构决策质疑:开 Issue 标 `arch:` scope(注意 § 核心约束 8 条不可逆)
- 代码实现开工:开 Issue 标 `feat:` scope,标注依赖哪些文档章节
