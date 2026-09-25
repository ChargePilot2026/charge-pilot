# ChargePilot · 二轮车充电运营管理系统

> **仓库地址**:[github.com/ChargePilot2026/charge-pilot](https://github.com/ChargePilot2026/charge-pilot)
> **当前文档版本**:v1.0(2026 Q3 freeze)
> **面向读者**:后端 / 前端 / 测试 / 运维 / AI 辅助开发协作者 / 客户运营方

ChargePilot 是一套**单客户单部署**的二轮车(电动自行车)充电运营管理系统:覆盖充电桩长连接接入、扫码启动 / 停止、按实走表计费、价费分离、多方分账、对账、退款、风控告警、OTA 远程升级、审计与合规。**不面向 C 端用户公网分发**,只服务一家客户的私有云部署(每客户独立一套)。

---

## 仓库结构

本仓库目前**只包含文档**(代码尚未实现,后续按文档落地)。文档分三层:

```
docs/
├── 需求分析.md                 ← 业务层:做什么、合规边界、业务规则
├── 技术规格.md                 ← 技术实现层:怎么做的全局技术决策
├── db/                         ← 数据层:5 个 schema,59 张表的字段级定义
│   ├── user.md      (16 张表)
│   ├── admin.md     (25 张表)
│   ├── gateway.md   ( 8 张表)
│   ├── billing.md   ( 5 张表)
│   └── worker.md    ( 5 张表)
├── api/                        ← 接口层:5 个服务的端点级 API 详细设计
│   ├── user.md      (29 个端点,  对小程序)
│   ├── admin.md     (112 个端点, 对 PC 后台)
│   ├── gateway.md   (17 个端点 + TCP/MQTT, 对内部)
│   ├── billing.md   ( 9 个端点 + Stream 消费约定, 对内部)
│   └── worker.md    (0 个 HTTP + 12 个定时任务 + 4 个 Stream 消费, 对内部)
└── cross-reference.md          ← 一致性对账:API 端点 ↔ DB 表 ↔ Stream 三方对照
```

辅助资料:

```
examples/                        ← 可直接复制的部署配置
├── Caddyfile                    ← 反向代理 + TLS 终止 + 静态资源服务
└── docker-compose.yml           ← 完整 Docker Compose 编排(技术规格 § 10 引用)

tools/                           ← 开发 / 维护工具
└── check-api-consistency.ts     ← API ↔ DB ↔ Stream 三方一致性检查脚本
```

---

## 推荐阅读顺序

| 读者 | 阅读顺序 | 时间预算(粗读 / 精读) |
| --- | --- | --- |
| **业务 / 产品** | `需求分析.md`(全文) | 2 h / 4 h |
| **后端开发** | `技术规格.md` → `db/`(各服务对应文件) → `api/`(各服务对应文件) → `cross-reference.md` | 8 h / 20 h |
| **前端开发**(小程序) | `需求分析.md § 5/6/11` → `技术规格.md § 6/7/12` → `api/user.md` | 4 h / 8 h |
| **前端开发**(PC 后台) | `需求分析.md § 7-10` → `技术规格.md § 3.3/7` → `api/admin.md` | 5 h / 12 h |
| **测试 / QA** | `需求分析.md` → `api/`(按场景端点对) → `技术规格.md § 13` | 4 h / 10 h |
| **运维** | `技术规格.md § 9-12` → `examples/` | 3 h / 6 h |
| **AI 协作者**(自动开发) | `技术规格.md § 1/2/3/7` → `api/对应服务.md` → `db/对应服务.md` → `cross-reference.md` | 1 h / 3 h(上下文压缩后) |

> **说明**:全套文档约 472 KB(技术规格 92 KB + DB 设计 226 KB + API 设计 154 KB)。粗读 = 通读理解整体;精读 = 交叉对账 + 标注落地细节,工作量为粗读的 2-3 倍。

---

## 核心约束(必读)

> 这些是经过多轮 grilling 收敛的**不可逆决策**,AI 协作者遇到冲突时**以这些为准**,不要自行扩展或反向提议。

1. **拆分充电订单 vs 支付订单**:`charge_order` 纯生命周期(开始/结束/电量/状态),`payment_order` 纯支付(微信支付回调),通过 `biz_type + biz_id` 关联,**不允许合并**。
2. **单客户部署**:每客户独立一套系统,数据库 / 服务 / 域名全部独立。**不引入分布式锁**(单实例)、**不做跨客户数据迁移工具**(需求 § 13.2)、**不带 `customer_id` 列**(由部署边界保证隔离)。
3. **跨服务数据访问**:完全禁止直连其他 schema。所有跨服务数据通过 HTTP 内部 API + Redis Stream 异步事件获取。
4. **一致性模型**:分布式事务不实现,用 **Saga 模式补偿**(`comp_tx_stream`)+ **幂等保证**(`event_id` 唯一)。
5. **退款 SOP**(需求文档 § 8.6 + 技术规格 § 7):微信退款 API 指数退避 4 次(1s / 5s / 30s / 2min),settled 后仅 `customer_finance` 角色可退,风控双重(5 min ≥ 3 次 + ≥ 500 元),每日 03:00 自动对账。
6. **风控 2 重 + 对账日频**:订单时间窗内频次 + 单笔金额双触发,差异自动入人工。
7. **telemetry 保留策略**:原始 1 月 + 15 分钟聚合 3 年 + 小时聚合 3 年(单表 + 双粒度设计,§ 技术规格 § 4.6)。
8. **扫码 3 端点**:拆为 `/scan/resolve`(路由分发)+ `/scan/port`(单端口详情)+ `/scan/start`(启动,基于 `port_id`),各自限流。
9. **i18n 预留**:`announcement` / `pricing_template` / `split_template` / `coupon` 加 `_i18n JSON` 字段,本期只填 `zh-CN`。
10. **Stream 名严格沿用 § 5.1 真实列表**(9 个):`device_event_stream` / `alert_stream` / `charge_started_stream` / `charge_ended_stream` / `refund_required_stream` / `invoice_required_stream` / `webhook_retry_stream` / `ota_schedule_stream` / `comp_tx_stream`。**新增 Stream 必须先在技术规格登记,不允许在文档里随意取名**。

---

## 仓库状态

- ✅ 需求分析 v1.0(15 章 + 附录 A)
- ✅ 技术规格 v1.0(15 章 + 术语表)
- ✅ 5 个 schema 的 59 张数据库表字段级设计
- ✅ 5 个服务的 API 详细设计(共 167 个 HTTP 端点 + 任务/Stream 消费约定)
- ✅ 一致性对账文档(API ↔ DB ↔ Stream)
- ✅ 部署范例(Docker Compose / Caddyfile)
- ⏳ 代码实现(尚未开始,等 AI 协作者按文档落地)

---

## 开发流程(技术规格 § 12)

- **分支策略**:GitHub Flow(`main` + 短期 feature 分支,不长期保留)
- **Commit 规范**:Conventional Commits(`feat:` / `fix:` / `refactor:` / `test:` / `docs:` / `chore:`),**强制** `feat(api):` / `feat(db):` / `fix(api):` 等 scope 命名
- **PR Review**:≥ 1 人 review,AI 生成代码仍需人工 review
- **CI**:GitHub Actions,跑 `cargo test` + `cargo clippy -D warnings` + `tools/check-api-consistency.ts`
- **禁止** `--force` push 到 `main`

---

## 反馈

- **文档错误 / 漏写**:开 Issue 标 `docs:` scope
- **架构决策质疑**:开 Issue 标 `arch:` scope(注意:§ 核心约束 10 条不可逆,质疑前请确认不冲突)
- **代码实现开工**:开 Issue 标 `feat:` scope,标注依赖哪些文档章节

---

## 许可证

TBD(单客户私有项目,本期不公开分发)