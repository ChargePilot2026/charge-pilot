# API 变更清单(P6)

> 基线 `3803232` → 当前工作区。契约基线快照见
> `crates/common-http/src/contract_baseline_data.rs`(228 条路由,改造前实际注册)。
>
> **⚠️ 已知错误行为不在变更清单的"保持"一侧** —— 它们是被**修复**的对象,见 §5。

## 1. 已删除的端点(前端必须同步下线)

| 方法 | 路径 | 原 handler | 删除理由 |
|---|---|---|---|
| GET | `/api/v1/admin/billing/withdraw` | `billing::withdraw_list` | **D3**:经 `withdraw_request` 跨库视图直写 billing 域,违反技术规格 §2.4;且 `admin-web` 零调用 |
| POST | `/api/v1/admin/billing/withdraw` | `billing::withdraw_create` | 同上 |
| POST | `/api/v1/admin/billing/withdraw/:id/review` | `billing::withdraw_review` | 同上 |
| GET | `/api/v1/admin/membership` | `api::membership::list` | **D3**:经 `membership_card` 跨库视图读 user 域;`admin-web` 零调用;`create` 早已 503(预留功能) |
| POST | `/api/v1/admin/membership` | `api::membership::create` | 同上(本就固定返回"功能尚未开放") |
| POST | `/api/v1/internal/withdraw-requests` | `billing::withdraw_create` | 同上(本就固定 503,无 list/review 承接点) |

**连带移除**:`migrations/admin_db_views.sql` 整体删除,`docker/dev/init-mysql.sh` 不再创建
`withdraw_request` / `membership_card` 两个跨库视图。

> 提现与会员卡功能如将来要上,应重新设计后实现,**不是恢复旧实现**。

## 2. 响应形态变化

| 端点 | 变化 | 说明 |
|---|---|---|
| `POST /api/v1/internal/devices/:id/reboot` | 双层信封 → 单层信封 | **D7**,已修。原返回 `{code:0,data:{code:0,data:…}}`,现为单层。**注意**:该端点本身仍是未接入的桩(恒 503),信封修复只是前置条件 |
| `GET /api/v1/internal/invoices/:invoice_id/settle-detail` | 行为变化 | **D6**。原先恒报 `Table 'billing_db.invoice_request' doesn't exist`;现改经 user 服务内部端点。**404 仍返回 `found:false`** |
| `GET /api/v1/internal/invoices/:invoice_id` | `json!` → 类型化 DTO | 新增 `api_contracts::InvoiceDetailResponse`。字段为**子集**(去掉了 `title`/`tax_no`/`email` 等),跨服务 DTO 只保留消费方需要的字段 |
| `GET /api/v1/internal/invoices/:invoice_id` | **新增 4 个字段** | **D19**。补 `reviewed_by`(数字)/ `reviewed_at` / `reject_reason` / `invoice_url`。admin 端靠这三项判定发票双签的崩溃恢复,原先恒不成立。只增不改,向前兼容 |
| `GET /api/v1/admin/finance/invoices` | 键顺序变化 | 17 个字段的**集合与取值完全一致**,仅序列化顺序按契约声明重排 |
| `GET /api/v1/admin/settings/whitelabel` | 未配置时响应变化 | 历史返回空对象 `{}`;现返回 12 个键齐全的默认视图(`id:0`、`miniprogram_name:""`、其余 null)。前端按固定路径读,取值等价 |
| `GET /api/v1/admin/alert/rules/:id` | `threshold` 类型澄清 | 一直是 JSON 值(数字或 `between` 的数组),**未变**;只是类型声明由 `String` 纠正为 JSON 值 |
| 全部 admin 写接口 | 新增 **403 拒绝** | **D1**。此前任一登录态管理员可调用;现在按操作权限矩阵校验,无权限返回 403 且**数据库零变更** |
| `POST /api/v1/public/auth/login` | 新增拒绝路径 | **D2**。`disabled` 账号此前可登录,现返回 403 |
| `POST /api/v1/admin/auth/refresh` | 权限来源改变 | **D2**。原先复制旧 token 的角色/权限,现**重新查库**;停用/撤权后 refresh 返回 403 |
| 全部 admin JWT 端点 | 停用后**立即失效** | **D2**。新增 `ActiveAdmin` 提取器,每请求复核账号状态(此前须等 JWT 过期) |
| `POST /api/v1/user/invoices`(申请开票) | 金额上限改变 | **D14**。金额来源由**预付额** `paid_cents` 改为**实结额** `charge_order.total_cents`;新增拒绝:计费未完成 / 存在欠款 / 退款处理中 / 可开票额 ≤ 0 |
| `POST /api/v1/internal/invoices/:invoice_id/review` | 新增复核 | **D14**。审核时重新校验计费/欠款/退款终态,金额超当前可开票额则拒绝 |

### D1 新增的操作权限码(`migrations/admin_db/0021`)

以下权限码原先**不存在**,已补建并按角色授权。高权限域(账号/角色/定价/分账/提现)
仅授予 `customer_admin` 与 `dev_admin`:

```
admin_user.create  admin_user.update  admin_user.delete  admin_user.reset_password
role.create  role.update  role.delete
pricing.rule.create  pricing.template.create
finance.split_template.create  finance.split_party_create
finance.withdraw.create  finance.withdraw.review
```

常规配置域授予 `customer_admin` / `customer_ops` / `customer_cs` / `dev_admin`:

```
alert.ack  alert.rule.create  alert.rule.update  alert.rule.delete
alert.subscription.create  alert.risk_config.update
announcement.create  announcement.update  announcement.delete
customer_service.create  customer_service.update  customer_service.delete
fault.resolve  webhook.create  webhook.update  webhook.delete
ota.package.create  ota.package.delete  ota.schedule.create  ota.schedule.trigger
settings.ota.update  membership.create  export.create
```

> **前端注意**:PC 后台若出现"无权限"提示,原因是当前登录账号的角色未被授予上述权限。
> 需在后台"角色管理"中给对应角色勾选新权限码。

## 3. 未变更的对外契约

以下保持不变,可继续按原方式调用:

- 全部 `/api/v1/user/**` 读接口(计费历史、详情、曲线、钱包、优惠券、站点、故障上报)
- 全部 `/api/v1/public/**`(登录、登出、刷新、支付/退款回调)
- 全部 `GET` 类 admin 端点(权限码沿用既有)
- 内部服务间调用的路径常量(`api_contracts::paths`)
- `ApiEnvelope` 信封结构与 `trace_id` 字段

## 4. 错误码与状态码变化

| 场景 | 原 | 现 |
|---|---|---|
| admin 写接口无权限 | **200 + 正常执行** | `403` + `ApiError::Forbidden` |
| `disabled` 账号登录 | **200 + 签发 token** | `403` |
| refresh 时账号已停用/删除 | **200 + 新 token** | `403` |
| 登录失败达 5 次 | **仅计数,永不锁定** | 账号锁定 30 分钟(契约 §`docs/api/admin.md:107` 原有约定) |
| 报修限流 | 可能**永久锁死**(键无 TTL) | 窗口到期自动恢复 |
| 开票超可开票额 | 按预付额放行 | `400` / `409` |

## 5. 被修复的缺陷(行为变化,非兼容性破坏)

| 编号 | 现象 | 现状 |
|---|---|---|
| D1 | 任一管理员可改角色权限 / 改他人 `role_id` / 重置他人密码 | 按操作权限矩阵校验 |
| D2 | 停用账号可登录;撤权后旧 token 可续命;失败 5 次从不锁定 | 全部修复 |
| D3 | admin 经跨库视图直写 billing 域 | 整块删除 |
| D5 | 计费规则创建后事件可能永久丢失(`let _ =` 吞错) | 事件与业务写同事务落 outbox |
| D6 | `settle-detail` 端点恒报错 | 改经 user 服务 |
| D7 | `reboot` 返回双层信封 | 单层信封 |
| D9 | 可通过 `/../` 读取静态目录**外**任意文件 | 拒绝 `..`/绝对路径 + `canonicalize` 后包含性校验 |
| D10 | 跨电价边界的 30 秒/超 10 小时订单返回 `Conflict` 而非全额退款 | 先校验计量、再判退款资格,归零不依赖计价成功 |
| D12 | gateway TCP 监听失败仍启动且健康检查返回 `ok` | bind 失败即启动失败;监听退出即进程退出 |
| D13 | Argon2 阻塞 Tokio 执行线程(实测 20ms 定时器被推迟到 193ms) | 已修:`spawn_blocking` + 信号量背压,见 `services/admin/src/password.rs` |
| D14 | 预付 1000 可开票 1000(实结 400 + 退款 600) | 按实结额,且审核时复核 |
| D15 | 阻塞消费挡住同连接的发布/探活 | 阻塞读走独立底层连接 |
| D16 | 跨分时电价订单**无法计费**(`ChargeEndMeter` 无分段读数) | **未修复**,见 §6 |
| D17 | 限流键可能永不过期 | `INCR`+`EXPIRE` 合并为 Lua 原子操作 |
| D18 | 站点详情经纬度颠倒 | 修正 |
| D19 | 发票双签崩溃后重试一律报「用户发票申请已处理」,审核员被永久卡死 | 补齐 `InvoiceDetailResponse` 的审核人/发票链接/拒因字段,恢复分支生效 |
| D20 | 告警规则详情的 `threshold` 被声明为字符串(实际是 JSON 列,`between` 存数组) | 改为原始 JSON 值;并补上缺失的 `enabled` 布尔字段 |
| D21 | DLQ 重放每轮只取最早 200 条,积压增长时新数据永远排不上 | **未修复**,见 §6 |
| D22 | 4 个 worker 定时循环未注册进 `scheduler::start_all`,运行期不执行 | **未修复**,见 §6 |
| D23 | DLQ 重放 `XADD *` 生成新 entry id;`orig_group` 解析后从未使用 | **未修复**,见 §6 |

## 6. 遗留:需业务决策

| 编号 | 状态 | 需要什么 |
|---|---|---|
| **D11** `webhook_retry` | **未实现**。生产 payload 是 `{alert_device_id, severity, event_id}`,**没有 `url`**,而消费者要求 `url` → 永远 `BadRequest` | 补齐投递实现,或接受"webhook 推送不可用"并在 UI 标注 |
| **D16** 跨分时电价计费 | **未修复**。`api-contracts` 的 `ChargeEndMeter` 只有 `charged_wh`/`charged_seconds`/`ended_at`,**无分段读数**,因此任何跨电价订单都无法计费 | 补齐分段计量与结算,或转入人工异常处理流程 |
| **D4 ③b** | DLQ 重放已实现并注册,但**未在真实 Redis 上端到端验证** | 需起 `compose.dev.yaml` 跑 V8b |
| **D21** DLQ 重放追不上积压 | 每轮只取最早 200 条,无滑动游标 | 确认是改成增量扫描,还是接受"只追最早的"语义 |
| **D22** 4 个 worker 循环未注册 | `export_run` / `reconcile_daily` / `billing_cycle_daily` / `alert_scan` 不执行 | 确认这 4 个是**该接线**还是**该删掉** |
| **D23** DLQ 重放可能重复投递 | `XADD *` 新 entry id;`orig_group` 未使用 | 确认消费者是按 `event_id` 还是 entry id 去重 |

## 7. 尚未执行的阶段

| 阶段 | 状态 | 说明 |
|---|---|---|
| P2 `api-contracts` 重写 | **已完成** | 全仓 `ApiEnvelope<Value>` 归零;契约新增回归测试 40 余条 |
| P3 逐服务迁移 | **大部分完成** | gateway / admin / billing / worker 已删 `AppState.db`。**admin 只换了连接来源**,SQL 仍在原模块、`DomainService::pool()` 仍是 `pub` —— 见下 |
| P5 全局收口 | **未做** | lint 仍为 `allow`,未转 `deny` |
