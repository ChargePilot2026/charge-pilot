# ChargePilot 文档总入口

> **目的**:新人 / AI 协作 5 分钟定位"先读哪 3 份、再翻哪 5 份"。
>
> **维护规则**:本文档索引指向的所有文件路径变更,必须同步更新本文件。

---

## 文档分层地图

```
docs/
├── README.md                    ← 你在这里(读路径入口)
├── 需求分析.md                   ← "做什么 / 不做什么"(产品决策)
├── 技术规格.md                   ← "怎么做"(技术选型 + 架构 + 协议 + 部署)
├── cross-reference.md           ← 跨服务 / 跨表 / 跨 Stream 对账 + CI 维护规则
│
├── api/                         ← 5 个服务的 HTTP / Stream 端点细节
│   ├── user.md  admin.md  gateway.md  billing.md  worker.md
│
├── db/                          ← 5 个 schema 的表结构 + 字段 + 索引
│   ├── user.md  admin.md  gateway.md  billing.md  worker.md
│
├── diagrams/                    ← 状态机图 / 流程图 / 数据血缘
│   ├── charge-order.fsm.md
│   ├── refund.fsm.md
│   ├── payment.fsm.md
│   └── data-lineage.md
│
├── runbook/                     ← 运维 SOP / 应急手册 / 故障恢复
│   ├── README.md                ← 索引 + 触发条件
│   ├── cert-renew-failed.md
│   ├── backup-restore.md
│   ├── gateway-crash.md
│   └── ota-mass-failure.md
│
└── checklists/                  ← 上线 / 部署 / 合规 Checklist
    ├── customer-onboarding.md
    ├── first-deploy.md
    └── equal-protection-l3.md
```

---

## 按角色阅读路径(5 分钟起步)

| 角色 | 第一天读这三份(每个最多 15 min 浏览) | 第三天读完这份 | 后续按需深入 |
| --- | --- | --- | --- |
| **产品经理** | `需求分析.md` § 一 + § 三 + § 十五 | `cross-reference.md` § 1 + § 4 | `db/user.md` 表清单 |
| **后端** | `技术规格.md` § 一 + § 二 + § 五 | `技术规格.md` § 三 + § 四 + § 七 | `services/{role}/` 对应 `api/*.md` + `db/*.md` |
| **前端(小程序)** | `需求分析.md` § 六 | `技术规格.md` § 三.二 + § 七 | `api/user.md` 全量 + `cross-reference.md` |
| **前端(PC 后台)** | `需求分析.md` § 四 + § 十一 | `技术规格.md` § 三.三 + § 七 | `api/admin.md` 全量 + `db/admin.md` |
| **测试** | `需求分析.md` § 八 + § 九 | `技术规格.md` § 五 + § 十三 | 各 `api/*.md` 错误码段 |
| **运维** | `技术规格.md` § 十 + § 十四 | `cross-reference.md` 全量 | `runbook/README.md` + `checklists/first-deploy.md` |
| **AI 协作** | `cross-reference.md` 全量(优先!) + 本 README | `技术规格.md` § 五(异步事件) | `api/*.md` 端点清单 + `db/*.md` 表清单 |

---

## 关键对账文件(任何变更前必查)

| 改什么 | 必改文档 | 必查规则 |
| --- | --- | --- |
| 新增 / 修改 / 删端点 | `api/{service}.md` + `cross-reference.md` § 2 + § 4 | 跨引用加文档名,见 `cross-reference.md` § 6.5 |
| 新增 / 修改 / 删表 | `db/{schema}.md` + 引用此表的所有 `api/*.md` + `cross-reference.md` § 2 | 同上,表总数同步 |
| 新增 / 修改 Stream | `技术规格.md` § 5.1(权威源) + `cross-reference.md` § 1 + 所有 `api/*.md` | Stream 总数 = 9,新增必须先在技术规格登记 |
| 跨服务调用 | 被调方 `api/{service}.md`(路径落地)+ `cross-reference.md` § 3 | 引用而非重新声明 |
| 错误码 | `errors.toml` + `技术规格.md` § 7.2(权威源) | 段位固定:0=成功 / 1xxx 通用 / 2xxx 业务 / 3xxx 第三方 / 4xxx 限流 / 5xxx 服务器 |

---

## 文档依赖图

```
需求分析.md ──────────┐
                     ├──→ 决定 ──→ 技术规格.md ───→ 决定 ──→ api/*.md
                     │                                  │
                     │                                  ├──→ 决定 ──→ db/*.md
                     │                                  │
                     │                                  └──→ 决定 ──→ cross-reference.md
   法规 / 合规 ──────┘                                          │
                                                                  └──→ 守护 ──→ tools/check-api-consistency.ts

runbook/  ← 技术规格 § 9.1 § 14.4 引用
diagrams/ ← 跨多文档缝合(状态机 / 数据血缘)
checklists/ ← 需求分析 § 13 + 技术规格 § 10 引用
```

---

## 立即可用的关键事实

- **5 个服务**:`gateway`(设备) / `user`(小程序) / `admin`(PC 后台) / `billing`(计费) / `worker`(后台任务)
- **5 个 schema**:`gateway_db` 8 张 / `user_db` 16 张 / `admin_db` 25 张 / `billing_db` 5 张 / `worker_db` 5 张 = **59 张**
- **9 个 Stream**:`device_event_stream` / `alert_stream` / `charge_started_stream` / `charge_ended_stream` / `refund_required_stream` / `invoice_required_stream` / `webhook_retry_stream` / `ota_schedule_stream` / `comp_tx_stream`
- **角色载体**:终端用户→仅小程序;运营 / 财务 / 巡检 / 管理员→同一 PC 后台;**客服坐席→微信原生客服会话**
- **客户模式**:单客户单部署 = 一套系统 = 一个付费客户,**不内置合伙人 / 区域代理**
- **首版国标底线**:GB 47371—2026 七重防护;**支付底线**:价费分离;**安全底线**:等保三级
