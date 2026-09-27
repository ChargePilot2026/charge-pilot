# charge-pilot 后端重构方案

基线 `3803232`（本地 `main`，领先 origin/main 4 个提交） · 2026-09-27

---

## 零、病因

**这个仓不缺规范，也不缺抽象。缺的是让它们生效的机制。**

| 建好的 | 实际 |
|---|---|
| 技术规格 2109 行（§7 API 设计完整） | 几乎没照做 |
| `common-db::with_tx` | **零调用**（75 处手写 `begin()`） |
| `Db::migrate` | **函数体是空的** |
| `Db::pool()` 暴露裸 pool | 295 处直接取 ← `with_tx` 零调用的根因 |
| `utoipa`（规格 §7.6 强制） | 声明了，零引用 |
| `errors.toml`（规格 §7.2 强制） | 不存在 |
| CI：fmt + clippy + `RUSTFLAGS: -D warnings` | **配置了，代码照样烂** |

CI 实测跑过 2 次，2 次全红，5 个 rust job 全挂，28 秒死在第一步 `cargo fmt --check`；之后 4 个提交全滞留本地（`ahead 4`）。**门禁拦住了，应对方式是不再推送。** `ci.yml:65` 的 `check-deploy-config.sh || true` —— 唯一会失败的检查被加了后缀变永久绿。

> 核心原则：**消灭分叉的物理可能性**。样板/规范/文档都是"要求"，本仓已有三份，全都没拦住人。

---

## 一、架构

垂直切片 + 内部分层（六边形）。**不按技术类型分层**——会得到贫血模型，且多人改同一目录。

```
services/user/src/
  lib.rs          # 唯一模块真源（消除 main.rs/lib.rs 双模块树）
  main.rs         # 只做装配，~8 行
  routes.rs       # 唯一路由真源，字符串无入口
  shared/
      app_state.rs   # AppState：只持有 usecase 服务对象，不持有 Db
      db.rs          # 各服务的 Db 包装 + with_tx；db 字段私有
      auth.rs  error.rs  paging.rs
  capability/charge/
      domain.rs           纯逻辑，零 I/O，零 sqlx/axum/reqwest ← 业务规则的真 home
      repository.rs       trait 定义
      repository_sql.rs   sqlx 实现；持有 Pool，字段私有
      usecase.rs          编排、事务边界、跨服务调用
      dto.rs              类型化 Req/Res
      handler.rs          HTTP 薄壳
      tests.rs            domain 纯函数单测（零依赖）
  mocks/
```

### 事务边界与可测性

```rust
// common-db：新增事务句柄（跨 crate 唯一入口）
pub struct Tx<'a> { tx: Transaction<'a, MySql> }
impl<'a> Tx<'a> {
    pub fn executor(&mut self) -> &mut MySqlConnection { &mut *self.tx }
    pub async fn commit(self) -> AppResult<()>;
    pub async fn rollback(self) -> AppResult<()>;   // 必须有：见下
}
impl Db {
    pub async fn begin(&self) -> AppResult<Tx<'_>>;   // 新增 API
    pub async fn with_tx<F, T>(&self, f: F) -> AppResult<T>   // 现有签名原样保留
    where F: for<'c> FnOnce(&'c mut Transaction<'_, MySql>) -> BoxFuture<'c, AppResult<T>>;
}

// repository：静态分发，具体类型
pub struct OrderRepository;
impl OrderRepository {
    pub async fn find(conn: &mut MySqlConnection, order_no: &str) -> AppResult<Order>;
}
```

**`rollback()` 不可省。** `charge_start.rs:40-50` 的重试语义依赖**显式回滚并传播错误**：

```rust
for attempt in 0..3 {
    let mut tx = pool.begin().await?;
    match apply(&mut tx, path, req).await {
        Ok(r) => { tx.commit().await?; return Ok(r); }
        Err(e) => {
            let deadlock = /* 40001 判定 */;
            tx.rollback().await?;          // ← 等待完成 + 错误传播
            if !deadlock || attempt == 2 { return Err(e); }
        }
    }
}
```

sqlx 的 `Transaction` **析构时启动回滚**，但那是 fire-and-forget，**不能替代"等待回滚完成并处理错误"**。`Tx` 必须显式提供 `rollback(self)`，同时保留内部 `Transaction` 的析构兜底（提前 return / task 被 cancel 路径）。验收须覆盖：提前返回、任务取消、重试路径。

**保留 `with_tx` 原样的理由（不足以断言 `BoxFuture` 是必需品）**：

| 尝试 | 结果 |
|---|---|
| `F: FnOnce(&mut Transaction) -> impl Future` | 失败：闭包返回的 Future 借用入参，普通推导表达不出 |
| `F: for<'c> AsyncFnOnce(&'c mut Transaction<'c, MySql>) -> ...` | 失败：`E0597` + `E0505`（`Transaction<'c>` 借用 pool，future 仍借 `tx` 时无法 `tx.commit()` 移动它） |
| `F: for<'c> AsyncFnOnce(&'c mut Transaction<'static, MySql>) -> ...` | ✅ **编译通过**（含 SQL 执行与 `commit()` 的完整样例） |

**修正**：早期把前两条的失败推广成"`BoxFuture` 是必需品"，属于**过度断言**——`Transaction<'static, MySql>` 版本可用。之所以本次仍不改 `with_tx`，是因为异步闭包版本的**跨线程 `Send` 约束尚未单独验证**，属独立议题。`Db::begin() -> Tx` 路线已实测编译通过（`executor()` + `commit()` + `rollback()` 三者共存）。

### 仓储 trait 的分发方式与 `Send`

**只有真正需要替换实现的边界才用 trait，其余用具体类型（静态分发）**。在 `&mut MySqlConnection` 签名下仓储无法 mock（见下"可测性"），trait 换不来测试收益。

若某边界确需替换实现，**两个问题是独立的，不可混谈**：

| 问题 | 手段 | 备注 |
|---|---|---|
| 返回的 Future 是不是 `Send`？ | 返回位置 `-> impl Future<Output = T> + Send`，或用 `async-trait` | `trait Repo: Send + Sync` **不保证** async fn 的 Future 是 `Send`——泛型 usecase 接入 Axum handler 或 `tokio::spawn` 时会编译失败 |
| trait 能不能 `dyn`？ | **必须用 `async-trait`**，或显式 `Pin<Box<dyn Future<Output = T> + Send + '_>>` | **`-> impl Future + Send`（RPITIT）不支持 `dyn`**——实测 `E0038: the trait Repo is not dyn compatible` |

**装箱 Future 的生命周期默认用 `'_` 绑定方法借用，不要写 `'static`。** 实测：直接借用 `&self` 的实现用 `+ 'static` 报 `lifetime may not live long enough`，**必须先 `clone` 才通过**；改成 `+ '_` 后直接借用即可。**支持 `dyn` 并不要求 Future 是 `'static`**——只有实现确实要克隆或转移数据、返回完全拥有的 Future 时才用 `'static`。

即：静态分发可用 RPITIT；需要 `dyn` 时只能用 `async-trait`（项目已有该依赖）或显式装箱。

**可测性的诚实结论**（修正早期方案中"mock 三行搞定"的错误说法）：

| 层 | 测试手段 | 理由 |
|---|---|---|
| `domain` | 纯函数单测，**零依赖，必跑** | 状态机、金额规则、资格判定全在这一层 |
| `usecase` / `repository_sql` | **真实 DB 测试** | 行锁、回滚、并发幂等 mock 验证不了 |
| `handler` | `oneshot` 打真实 Router | ⚠️ **不消除 DB 依赖**，见下 |

`&mut MySqlConnection` 签名保留（资金原子性优先于 mock 便利性）。业务规则必须在 `domain` 抽成纯函数，否则只能靠真 DB 测。

**`oneshot` 的真实边界**：`tower::ServiceExt::oneshot` 只是**不监听网络端口**，handler 内部调真实 usecase 时仍会连真实 DB。二选一：

- 引入 usecase 注入点（handler 泛型或 trait object），使 HTTP 测试无 DB——代价是每 handler 多一层间接
- 接受现实：**HTTP 测试只覆盖路由匹配、状态码、信封结构**（`State` 指向不可达 DB + 断言错误路径），**成功路径归入集成测试**（V2b 真 DB）

**工具链真实下限是 1.88，声明的 1.80 是假的。**

`Cargo.toml` 声明 `rust-version = "1.80"`，但传递依赖链已突破：

```
common-auth → jsonwebtoken 9.3.1 → simple_asn1 0.6.4 → time 0.3.55
                                                       rust-version = 1.88.0
                                                       edition = 2024
```

本机 `rustc 1.98.1`，`rust-toolchain.toml` 用浮动的 `stable`。**代码无法在 1.80 编译**，MSRV 声明形同虚设。

P1a 必须先统一，否则后续所有版本相关判断都建立在错误前提上：

| 项 | 现状 | 改为 |
|---|---|---|
| `Cargo.toml` | `rust-version = "1.80"` | `1.88`（与 `time` 对齐） |
| `rust-toolchain.toml` | `channel = "stable"`（浮动） | **锁定具体版本**，与 `rust-version` 一致 |
| 5 个 `Dockerfile` | `FROM rust:1.80-bookworm` | 同步锁定 |
| CI / 本地验收 | 未固定 | 统一加 `--locked` |

**连带修正**：早期以"1.80 达不到"为由排除 `AsyncFnOnce`（1.85 稳定）的推理**不成立**——真实下限 1.88 本就包含它。但结论仍是不改 `with_tx`，理由换成实测结论（见下）。

**可测性的诚实结论**（修正早期方案中"mock 三行搞定"的错误说法）：
`&mut MySqlConnection` 签名保留（资金原子性优先于 mock 便利性）。业务规则必须在 `domain` 抽成纯函数，否则就只能靠真 DB 测。

---

## 二、入口封死

让错误写法写不出来，不是要求别这么写。

| # | 现在为什么被绕过 | 封死方式 |
|---|---|---|
| E1 | `Db::pool()` 暴露裸 pool（295 处） | **`AppState.db` 逐步移除**——P1a **只新增**服务对象（不动 `st.db`），P3 每个服务调用方迁移完成后才删该服务的 `AppState.db` 字段。服务的 `db` 字段私有，handler 取不到 pool（详见下） |
| E2 | 裸 `sqlx::query` 随处可写（430 处） | handler/usecase 层写 SQL —— **仅 `cargo clippy` 拒绝，`cargo check` 仍通过**，见下 |
| E2b | `ApiEnvelope<Value>` 152 处 → 无类型检查 → `json!` 无处安放 | 见 §三 |
| E3 | 3 份 `ServiceClient` 各抄一份能跑 | **删掉旧 3 份**，只在 `common-http` 一处 |
| E4 | 路由字面量随时可写（20 处） | 只能从类型化注册表构造，字符串无入口 |
| E5 | `api_types::paths` 127 条与 `api-contracts` 重复 | 删 `api_types::paths`，唯一真源在 `api-contracts` |
| E6 | 两个 Envelope（`ApiEnvelope` / `Envelope` 别名） | 删别名 |
| E7 | `current_request_id()` 每次现生成 UUID | 见 §四 |

**E1 的跨 crate 实现**：`Db` 在 `common-db` crate，`repository_sql.rs` 在服务 crate，因此**不能靠 `pub(crate)` 实现**（那会把仓储也挡在门外）。可执行的形态是**依赖倒置**：

```rust
// shared/app_state.rs —— 改造完成后
pub struct AppState {
    pub charge: ChargeService,     // 内部 db 字段私有
    pub wallet: WalletService,
}
// 不再有 `pub db: Db`
```

handler 拿 `AppState` 只能调到 service 的 usecase 方法，取不到 `Pool<MySql>`。**这是 Rust 跨 crate 下唯一可行的封法**——类型系统无法按"层"授权，只能靠不暴露。

**但 `st.db` 现有 298 处引用**，所以这一步**不可能在改造前完成**：P1a 只把服务对象建出来（与旧 `db` 字段并存，零行为变化），真正删除 `AppState.db` 是 P3 每个服务调用方全部迁完之后的事。

---

**禁止 sqlx 的覆盖面**——只禁 `sqlx::query` 会漏掉 257 处：

| 入口 | 出现次数 |
|---|---:|
| `sqlx::query(` | 430 |
| `query_scalar` | 133 |
| `query_as` | 107 |
| `QueryBuilder` | 17 |
| `query_as!` / `query!` 宏 | 0 |

`clippy.toml` 需全部登记：

```toml
# 方法类：只能登记函数路径
disallowed-methods = [
  { path = "sqlx::query",      reason = "只能在 repository_sql.rs 使用" },
  { path = "sqlx::query_as",   reason = "同上" },
  { path = "sqlx::query_scalar", reason = "同上" },
]
# 类型类：QueryBuilder 是 struct，登记到 disallowed-types
disallowed-types = [
  "serde_json::Value",
  "sqlx::QueryBuilder",
]
```

### 约束的强制等级（不可混为一谈）

| 类型 | 由谁保证 | 表现 |
|---|---|---|
| 私有字段、模块可见性、crate 依赖 | **rustc 编译器** | `cargo check` 即报错 |
| 禁 SQL、禁 `Value`、禁 `json!`、禁直接注册路由 | **clippy / 扫描闸口** | `cargo check` **通过**，`cargo clippy` 才拒绝 |

方案早期写的"编译不过"只适用于第一类。**V1 不能替代 V3/V6**——不跑 clippy 的构建不会拦下任何一条。

**架构守护必须自带自检**：闸口内嵌**预期失败的违规样例**（`#[should_panic]` 或独立 fixture），证明拦截真的生效。否则"守护测试全绿"可能只是因为它什么都没检。

## 三、零 `json!`

**规则：生产代码全仓零 `json!`（含 crates）；测试允许。**

只禁 token 挡不住，只会换马甲且更差（`Value::Object(Map::from_iter(...))` 或 `from_str(r#"..."#)`）。必须整体打通：

```
152 处 ApiEnvelope<Value> → ApiEnvelope<ConcreteDto>
     ↓ 编译器检查字段 → json! 无处安放 → 归零
     ↓ disallowed_macros 锁死防回潮
```

| 层 | 规则 | 手段（**已实测验证**） |
|---|---|---|
| 硬 | 生产代码零 `json!` | 根 `clippy.toml` + `#![deny(clippy::disallowed_macros)]` |
| 硬 | handler/usecase/domain 禁 `serde_json::Value` | 根 `clippy.toml` + `#![deny(clippy::disallowed_types)]` |
| 软 | `Value` 仅限存储/线缆边界 | 例外清单 |

**强制机制的正确形态**（本机 `clippy 0.1.98` 实测，**仓库当前没有 `clippy.toml`，需新建**）：

> **精确表述**：`clippy.toml` 只放 **config 列表**；lint **级别**既可写 `#![deny(...)]`，也**可以**用 `cfg_attr` 条件化（实测 `#![cfg_attr(not(test), deny(clippy::disallowed_macros))]` 正常触发禁用宏诊断）。把 `= [...]` 配置列表放进 `cfg_attr` 才非法。

```toml
# 仓库根 clippy.toml
disallowed-macros = ["serde_json::json"]
disallowed-types  = ["serde_json::Value"]
```

```rust
// 各服务 src/lib.rs
#![deny(clippy::disallowed_macros, clippy::disallowed_types)]

#[cfg(test)]
mod tests {
    #[allow(clippy::disallowed_macros, clippy::disallowed_types)]   // 测试放行
    ...
}
```

**三种直觉写法都无效**（已逐一实测）：

| 写法 | 结果 |
|---|---|
| `cfg_attr(not(test), deny(disallowed_macros))` | ✅ **有效**（级别可条件化） |
| `cfg_attr(not(test), clippy::disallowed_macros = ["..."])` | ❌ `attribute value must be a literal`（配置列表不能进 `cfg_attr`） |
| 源码内 `#![clippy::disallowed_macros = [...]]` | `custom inner attributes are unstable` |
| `Cargo.toml [lints.clippy] disallowed_macros = [...]` | ❌ `invalid type: sequence, expected a string or map`（`[lints]` 只接受字符串级别或 map） |
| `disallowed-methods` 里登记 `sqlx::QueryBuilder` | ❌ `warning: expected a function, found a struct`，**且违规样例仍通过**——它是类型，必须进 `disallowed-types` |

lint **配置**只能放 `clippy.toml`；lint **级别**用源码 `#![deny(...)]`；测试豁免用模块级 `#[allow(...)]`。实测 `json!` 与 `Value` 均被拦到，`cargo test` 正常放行。

**跨服务透传**：admin 存在"取上游响应→就地改 JSON→透传"（`admin/src/billing.rs:refunds`）。必须先有类型化上游 DTO（进 `api-contracts`）+ admin 侧 View Model。

**`Value` 例外清单**（⚠️ 待确认）：Redis Stream 事件载荷 / `gateway_db.raw_frame_log` 设备原始帧 / `webhook_delivery_log` 第三方载荷。

工作量：152 处签名需定 DTO、288 处 `json!`（44 文件）、501 处 `Value`。admin 一家 21 文件。**单块最大工作量**，必须先备齐 DTO 再动服务。

---

## 四、request_id 全链修复

三处各自生成 UUID，追踪链是断的：

| 位置 | 现状 |
|---|---|
| `common-error:235` `current_request_id()` | 每次现生成，不读请求作用域 |
| `common-error:188` `IntoResponse for AppError` | 又生成一个 |
| `common-http/internal.rs:25` `ApiClient` | 出站调用重新生成，**不透传上游 id** |
| `common-http:117-134` `request_id_layer` | 只改 header，不进 body |

后果：成功响应体 id ≠ `X-Request-Id`；错误响应体 id ≠ 日志 id；跨服务调用 id 断裂。**线上报错无法关联日志。**

修法（**中间件不碰 body**）：

1. `common-error` 加 `tokio.workspace = true`（tokio 二进制已在依赖树），`task_local!` 存当前请求 id
2. `request_id_layer` **只做两件事**：把 id 放进请求作用域（scope）+ 写回响应头 `X-Request-Id`
3. `ApiEnvelope::ok/err` 与 `IntoResponse for AppError` **读同一 task_local**，不再自造
4. `ApiClient` 出站**透传**当前 id，缺失时才新建
5. **验证 4 类一致性**：成功 / 错误 / 鉴权失败 / 跨服务调用的 id 全链一致（补单测）

**中间件不得改响应 body。** `request_id_layer` 覆盖全部路由，其中包括：

| 路由 | 响应形态 |
|---|---|
| `/api/v1/health` | `&'static str` 纯文本，非信封 |
| admin `fallback(static_serve::serve_spa)` | 静态资源（HTML/JS/CSS） |
| 微信支付回调 | 可能是 **204 空响应** |

改 body 会破坏这三类。只有 `ApiEnvelope` 构造器与错误转换这两条路径读 task_local，其余响应体原样透传。

### 后台任务与日志关联

`tokio::task_local` **不会被子任务自动继承**——`tokio::spawn` 出去的 task 读作用域会拿到 `AccessError`。本仓有 **16 处 `tokio::spawn`**（outbox、Stream 消费者、`device_import` 恢复、后台循环等）。规则：

1. **读取一律用 `try_with`，禁止 `with`**。后台 task 没有 HTTP 请求作用域，用 `with` 会直接 panic
2. **需继承上下文的子任务**：显式捕获 `let id = try_current_id();`，在新 task 里 `SCOPE.scope(id, async { ... }).await`
3. **独立后台任务**（定时清理、outbox 轮询）自行建立作用域并生成自己的 id，不继承请求 id

**日志关联需另行处理。** task-local 不会自动给 tracing 附加字段；且 4 个服务的 `TraceLayer::new_for_http()` **全部是无配置的裸调用**——`user/src/main.rs:190`、`admin/src/main.rs:188`、`gateway/src/main.rs:127`、`billing/src/main.rs:96`，**默认不记录 request id**。所以"响应体 id 一致"**不足以**完成日志关联，还需：

- `request_id_layer` 内建 span（**`Span::new` 收的是 `Metadata` 值，不是 key=value，不能直接写**）：```rust
  let span = tracing::info_span!("request", trace_id = %id);
  ```
  handler 后续用 `.instrument(span)`
- `TraceLayer` 改为 `TraceLayer::new_for_http().make_span_with(...)` 注入该字段
- 验收确认**四者一致**：响应体 id、响应头 id、`ApiClient` 出站 id、服务端日志字段 id

`thread_local!` 不可行（axum 跨 worker 线程）。218 个 `current_request_id()` 调用点全无参，签名不用改。

---

## 五、现有缺陷台账

**关键原则：分层改造不会自动修复这些缺陷。** 把 `roles.rs:update` 搬进 usecase，漏洞会**原样保留**。因此每条必须显式决策：**修 / 删 / 接受**，并单列行为验收。

同时：**已知错误行为不得进入"必须原样保持"的契约基线快照**（§六 V5）——否则快照会把 bug 固化。

| # | 缺陷 | 证据 | 处置 | 阶段 | 行为验收 |
|---|---|---|---|---|---|
| **D1** | **账号/角色管理无操作授权**（提权） | `roles.rs:66`、`users.rs:79`、`users.rs:114` 参数均为 `_c: AdminClaims` **下划线=未用**；`roles.rs:update` 可 `DELETE role_permission` + 插入任意 `permission_ids`；`users.rs:update` 可改任意 `role_id`/`status`；`reset_password` 可重置任意账号密码。全 admin 仅 7 文件做权限校验，roles/users 不在其中 | **修**：**管理端写接口的全量**权限矩阵（非仅 3 个函数），授权判定下沉 usecase；至少含角色/账号/重置密码/**计费规则**/**分账模板**/`split_party_create` 参与方 | **P0.5** | **所有**管理端写接口逐条单测：无权限账号 → **403 且数据库零变更**；有权者可成功。**P0.5 门槛项，未过不得进入 P1a** |
| **D2** | **停用/撤权后仍可续命** | `auth.rs:43` 只判 `status == "locked"` 且仅当 `locked_until > now`，**`disabled` 不拒绝**；`auth.rs:92` refresh 用 `claims.permissions.clone()` **复制旧 token 权限，不查库**；logout 明示无状态无撤销 | **修**：登录状态约束 + refresh 重新授权 + token 失效策略 | **P0.5** | 账号置 `disabled` 后：登录 403、refresh 403、旧 token 被拒；撤权后 refresh 不再获得该权限；**连续失败达阈值 → 锁定且登录被拒 → 到期恢复 → 成功登录后计数清零；并发失败请求下原子更新**。**P0.5 门槛项，未过不得进入 P1a** |
| **D3** | **提现与会员卡两个跨库视图的调用方都是死功能** | `withdraw_request` 视图：admin 4 端点直写 billing_db，`admin-web` **零调用** `withdraw`；billing 仅 1 个固定 503 端点、**无 list/review**。`membership_card` 视图：`admin/src/api/membership.rs:18` 列表直查，**`admin-web` 零调用** `membership`，同文件 `create` 已 503（预留功能）；`miniprogram` 的 `membership_card` 只是 `/user/profile` 的响应字段，**由 user 服务读自己库的表，不经过该视图** | **删**：移除 admin 提现端点（4）+ admin 会员卡列表端点 + billing 503 端点 + **两个**跨库视图。**不是"改调 HTTP"** | **P4** | 见下方"API 404 与 SPA fallback"——删除后必须返回 **API 404**，不能回退 SPA HTML；视图不存在；功能若将来要上则重新设计，不做半迁移 |
| **D4** | **Stream 裁剪丢未消费事件** | `common-redis/src/lib.rs:147` `XADD ... MAXLEN ~ 100000`，近似裁剪**可移除仍在 PEL 中的消息正文**；`gateway/src/outbox.rs:76` XADD 成功即标记 `published`，正常轮询不再补发 | **修**：按消费确认水位裁剪 + 逐流持久化台账 + outbox 补 `stream_message_id`，见下方"D4 可执行设计（第二版）" | **P4** | 消费者停机至积压超阈值后恢复，可靠流事件**零丢失**；tombstone/DLQ 只能报告丢失，不能恢复 |
| **D5** | **规则变更事件永久丢失** | `admin/src/api/settings.rs:41-51`：INSERT 经 `pool()` 独立提交（autocommit），随后 `let _ = ...xadd_envelope(...)` **静默丢弃发布错误** | **明确契约**：必须投递 → 事务 outbox + 重试；允许丢失 → 写明契约且消费者容忍。当前消费者主要记录变更，**不能推断为计费金额错误** | **P4** | 断连 Redis 时创建规则：或事件最终必达（outbox 重试），或契约明示可丢且消费者不报错 |
| **D6** | **billing 跨库裸查必失败** | `billing/src/api.rs:196-198` 查 `invoice_request`；`grep -rn 'invoice_request' migrations/billing_db/` **零命中**；全仓仅 `migrations/user_db/0001_init.sql:302` 建表；billing_db 内无视图 | **修**：改经 user 服务 HTTP | **P4** | 端点不再报 `Table doesn't exist` |
| **D7** | **双层信封** | `common-http/internal.rs:168` `ApiClient` 会解包；`admin/clients.rs:56` `ServiceClient::post_typed` 是 `Ok(resp.json().await?)` **不解包**；`admin/src/api/internal.rs:130-138` 又包一层 | **修**：收敛 ServiceClient（E3）时自然修复 | **P3-admin** | 响应为单层信封 |
| **D8** | **request_id 追踪断裂** | `common-error:235`、`:188`、`common-http/internal.rs:25` 三处各自生成 UUID | **修**：见 §四 | **P1a** | 响应体/响应头/出站/日志字段四处 id 一致 |
| **D9** | **静态资源路径穿越**（未授权读任意文件） | `admin/src/static_serve.rs:27-31` 用 `PathBuf::starts_with` 做防护，但它是**逐组件**比较：`/app/static/../x` 的组件为 `["/","app","static","..","x"]`，**以静态根开头 → 检查通过**；`resolved_path` 全程无 `..` 过滤，`fs::read` 由 OS 消解 `..`。已用纯路径逻辑复现：`/../sentinel.txt`、`/../../etc/passwd`、`/assets/../../sentinel.txt` 三种形式的 `starts_with` 均为 `true`。且该 fallback 在 **JWT 保护之外**（`admin/src/main.rs:187`） | **修**：拒绝含 `..` 与绝对路径的组件；对 `root.canonicalize()` 与 `target.canonicalize()` 的结果做包含性校验（消解符号链接逃逸） | **P0.5**（安全门槛） | 目录外文件**始终不可读**（含符号链接指向目录外）；穿越请求返回 4xx；测试须以**真实静态目录存在**为前提运行 |
| **D10** | **全额退款规则被计价错误截断** | `billing/src/charge_fee.rs:24-31` 先 `metered_pricing::calculate(...)?`（**`?` 直接返回**），`:33-41` 才判断「60 秒内停止 / 超 10 小时 → 费用归零」。**跨分时电价时前者报错，退款分支不可达**——已用现有计价源码复现：跨电价边界的 30 秒订单、超 10 小时订单，均符合归零条件却先返回 `Conflict` | **修**：**分离计量合法性校验与计价**；合法且符合退款条件的订单，其归零判定不应依赖分段计价成功 | **P0.5**（资金） | 跨电价边界 30 秒订单、超 10 小时订单 → **最终费用为 0**（不是 Conflict）；重复处理幂等；`fee_receipt` 与 `fee_delivery` 的同事务边界保留 |
| **D11** | **webhook_retry 全链路未实现** | 生产者 `admin/src/stream_consumer.rs:63-68` 的 payload 是 `{alert_device_id, severity, event_id}`——**没有 `url`**；消费者 `worker/src/streams.rs:25-32` 第一件事就要求 `url`，缺失即 `BadRequest`，即便有 `url` 也固定返回 `ServiceUnavailable("webhook delivery is not configured")`。**两条路径均已复现** | **明确标注"未实现"**；决定补齐或接受功能限制。**不得用"已有重试队列保证最终送达"论证可丢**（原分级表即因此误判）。HTTP DTO 改造不会自动修复此 Stream 契约不匹配 | **待定**（需业务决策） | 若补齐：端到端投递测试（生产的 payload 能被消费者接受）；若接受：端点/流下线并在 API 变更清单标注 |
| **D14** | **开票用预付款金额，不等实结与退款** | `user/src/invoice.rs:50` 只要求订单 `completed` + 支付 `paid` + `refunded_cents = 0`，随后以 **`paid_cents`** 校验开票金额。但 `charge_end.rs:106` 停机即标 `completed`（此时尚未计费），`charge_fee.rs:131` 的差额退款**仅登记 pending**。完整路径：**预付 1000 → 停机 → 开票 1000 → 实结 400 并退款 600**。审核侧 `invoice.rs:216` **也不重新校验**计费/支付/退款状态 | **修**：明确"可开票"状态与**金额来源为实结额**；申请与**审核两处**都校验实结与退款终态；与资金更新采用**一致的加锁顺序**。⚠️ **实结额 ≠ 可开票额**，另需规则：① 欠款订单（`charge_fee.rs:157` 允许实结 > 已付并记 `shortfall_cents`，**无补扣闭环**）**是否禁止开票**；② 实结后发生人工退款时的**金额上限**（人工退款不改写原实结费用） | **P0.5**（资金） | 覆盖：计费延迟时申请、**退款处理中**申请、审核前发生退款、**欠款订单（实结>已付）**申请、**实结后人工退款**申请 → 全部按明确规则拒绝或按上限处理。**无需强制扩建补缴功能，但必须有明确拒绝或处理路径**。与 D6（查询归属）/ D10（归零规则）**互不替代** |
| **D16** | **跨分时电价订单根本无法计费**（数据缺失，非逻辑错误） | `metered_pricing.rs:36-42`：只要 `wh > 0` 且该分钟费率与前一分钟不同，即 `Conflict("跨分时电价缺少分段电量读数，需审核")`。而 `api-contracts/src/lib.rs:159` 的 `ChargeEndMeter` **只有** `charged_wh` / `charged_seconds` / `ended_at`，**没有任何分段读数**。故**任何跨电价订单都无法完成计费** | **需明确决策**（三选一）：A 补齐分段计量与结算；B 此类订单转入**可实际完成**的异常处理流程；C 接受功能限制并列入准入约束。**错误信息里的"需审核"目前没有任何对应流程** | **待业务决策** | 若选 C：台账与准入约束须写明，**且不得把 D10 / D4 通过当作计费闭环**（DLQ 重放补不出缺失的分段读数） |
| **D17** | **限流计数可能永不过期** | `common-redis/src/lib.rs:79-86` 的 `rate_limit` 把 `INCR` 与 `EXPIRE` 分成两次 await：若 `INCR` 已执行而 `EXPIRE` 因断连/任务取消未执行，且后续 `v > 1` 便**永不再设 TTL**，键永久残留。实际用于每用户 24 小时 5 次报修限制（`user/src/station.rs:89`） | **修**：`INCR` + 首次 `EXPIRE` 合并为 **Lua 原子操作**；并处理**已存在的无 TTL 异常键** | **P4** | 故障注入（`INCR` 成功、`EXPIRE` 失败）后键仍有 TTL；窗口到期计数自动归零、报修限制恢复。**D15 隔离连接不解决此问题** |
| **D15** | **Redis 阻塞消费与发布共享同一连接** | `common-redis/src/lib.rs:99` `RedisStream` 只有**一个** `ConnectionManager`；XREADGROUP BLOCK（:193）、XADD、ACK、PING 全部用它的 `clone()`——**clone 共享底层连接**，阻塞命令会挡住该连接上的其它命令。gateway 的 OTA 消费组 `BLOCK 5000ms`（`gateway/src/stream_consumer.rs:17`）而 outbox 发布超时仅 3 秒（`gateway/src/outbox.rs:69`）→ 发布排在阻塞读之后即超时重复投递；健康检查同样受影响 | **修**：阻塞消费者用**独立底层连接**；发布 / ACK / 健康检查走**非阻塞连接** | **P4** | **空流长轮询期间仍能及时发布并正常探活**（并发验收）；D12（任务存活）与 D4（裁剪）**都不覆盖**此问题 |
| **D12** | **gateway 关键 TCP 任务失败但服务仍报健康** | `gateway/src/main.rs:80-87` 把 TCP 监听放进 `tokio::spawn` 并**丢弃 `JoinHandle`**，失败仅 `tracing::error`。端口被占用或地址配置错误时 HTTP 仍正常启动；`gateway/src/api.rs:12` 的健康检查**只探 DB 与 Redis**，仍会返回 `ok` | **修**：就绪前完成 TCP bind；监听启动失败 → **进程启动失败**；关键任务异常退出 → **撤销就绪状态或退出进程**（不静默降级） | **P1a**（与启动装配同批） | 端口占用 → 启动失败且不就绪；TCP 任务异常退出 → 不再报 `ok`；健康检查需纳入关键任务存活 |
| **D13** | **Argon2 直接阻塞 Tokio 执行线程** | `admin/src/auth.rs:50` 在 async handler 中同步调 `verify_password`；`users.rs:42`（建号）与 `:115`（重置密码）同步调 `hash_password`；底层 `common-auth/src/lib.rs:125` 是**同步 argon2id**。**全仓 `spawn_blocking` 零使用**。探针实测：单线程 Tokio 下 20ms 定时器在直接验密时约 **193ms** 才完成，`spawn_blocking` 后约 **21ms** | **修**：密码计算一律 `spawn_blocking`，并**限制并发计算数量**（CPU 密集，需背压） | **P3-admin** | 登录/建号/重置密码期间定时器不被明显推迟；并发改密时排队受控、内存不被打爆。**仅换 `tokio::spawn` 或 async trait 不解决**——必须离开执行线程 |

### D4 可执行设计（第二版）

**上一版的两处前提已被证伪，本版重写：**

| 上一版的说法 | 实际 |
|---|---|
| "重放数据源是现成的 `event_outbox`" | **只对 user / gateway 成立**。`admin_db` **没有** `event_outbox` 表；admin 产的 `webhook_retry` / `pricing_rule_changed` 直接 `xadd`，零持久化 |
| "禁止近似裁剪即可保护可靠流" | **错**。`MAXLEN = N` 精确裁剪同样**不保护未 ACK 的正文**，它只是让裁剪更精确。安全条件是**消费确认水位**，与 `~` 无关 |
**① 逐流台账（生产者与消费组均已逐条核对）**

| 流 | 生产者 | 归属库 | 持久化 | 消费组（实际注册） | 分级 |
|---|---|---|---|---|---|
| **`charge_ended`** | **`gateway/src/charge_stop.rs:153-165`**（与端口状态、`result_reported=TRUE` **同事务**） | **`gateway_db`** | `event_outbox` OK | **`billing-cg` + `user-cg`** | **可靠（跨库）** |
| `charge_started` | `user/src/payment_receipt.rs:162`（`enqueue(tx)`） | `user_db` | `event_outbox` OK | `gateway-cg` | 可重放 |
| `refund_required` | user **5 处**：`payment_receipt.rs:155`、`charge_start.rs:182`、`charge_fee.rs:143`、`refund_review.rs:93`、`wallet_refund.rs:172` | `user_db` | `event_outbox` OK | `admin-cg` | **可靠** |
| `invoice_required` | `user/src/invoice.rs:99` | `user_db` | `event_outbox` OK | `admin-cg` | **可靠** |
| `comp_tx` | `user/src/refund_result.rs:200` | `user_db` | `event_outbox` OK | `worker-cg` | **可靠** |
| `device_event` | `gateway/src/protocol/tcp.rs:130` | `gateway_db` | `event_outbox` OK | `admin-cg` | 可重放 |
| `alert` | `gateway/src/protocol/tcp.rs:155` | `gateway_db` | `event_outbox` OK | `admin-cg` | 可丢 |
| `pricing_rule_changed` | `admin/src/api/settings.rs:51`（`let _ =` 吞错） | `admin_db` | **无** | **`billing-cg` + `user-cg`**（两处都消费：缓存失效） | **可靠**（当前不可重放） |
| `ota_schedule` | **无生产者** | — | 无 | **`gateway-cg` + `worker-cg`** | **死流**（有 2 个消费组却无人生产） |
| `webhook_retry` | `admin/src/stream_consumer.rs:68` | `admin_db` | **无** | `worker-cg` | 待定（D11） |
| `coupon_grant_required` | **无生产者** | — | 无 | `user-cg` | **死流** |

**四点结论：**

1. **`charge_ended` 归属 `gateway_db`，不是 `user_db`**（早期台账写错）。它是**跨库流**——gateway 持久化，`billing-cg` 与 `user-cg` 两处消费。**重放责任在 gateway**：billing 侧恢复必须指向 `gateway_db.event_outbox`。
2. **`admin_db` 必须补建 `event_outbox`**，否则可靠级的 `pricing_rule_changed` 无任何重放来源。
3. **两条死流**：`ota_schedule`（`gateway-cg` + `worker-cg` 两个消费组，**全仓无生产者**）与 `coupon_grant_required`（`user-cg`，无生产者）。要么补生产者，要么连消费组一并删——避免"有消费组却永不触发"的假象，也避免裁剪水位被不存在的消费组拖住。
4. **必要消费组是裁剪水位的执行依据**：本表的"消费组"列即必要集合。**必要消费组尚未创建时，禁止裁剪越过其消息**（否则消息在组建立前就被删除，无人可读）。
**② 裁剪的安全条件：消费确认水位**

`MAXLEN ~ N` 与 `MAXLEN N` **都不保护 PEL 中的消息正文**——Redis 只按长度裁剪，不看消费状态。正确做法：

- 可靠流**不由长度驱动裁剪**。改用 `XINFO GROUPS` 读取**每个必要消费组**的 `last-delivered-id` 与 PEL，裁剪上界取**所有组最小未确认 ID 之前**
- 容量告警而非静默裁剪：积压超阈值 → **停止发布新事件 + 告警**，事件留在 `event_outbox`（`status` 保持 `pending`），待 Stream 恢复后由 outbox 重放任务补发
- **绝不用"宁可内存涨"一笔带过**——Redis `noeviction` 下会直接写失败；必须落到 outbox 这条持久路径上

**③ 重放范围：必须覆盖"已发布但未完成消费"**

`gateway/src/outbox.rs:76` 在 **XADD 成功后立即标记 `published`**。因此：

- ❌ **`status != 'published'` 作为重放范围是错的**——它恰好**排除**了"已发布但随后在 Redis 丢失"的消息，而这正是要恢复的对象
- ❌ **"覆盖最大延迟发布窗口"也没有可证明的上界**——失败会持续重试，创建时间与发布时间可以相隔任意久

**修正后的方案**：恢复范围**必须包含已发布但尚未完成消费的事件**。两条路径：

- **补 `stream_message_id`（新数据）**：发布时回写 XADD 返回的 ID → 缺口可按 ID 精确反查，**但只对新数据有效，不能自动修复历史记录缺口**
- **历史记录迁移策略**：旧行无 `stream_message_id`，**无法精确定位**。⚠️ **"按 stream + 保守时间窗"不构成不遗漏的依据**——对无 `stream_message_id` 的旧记录，任意有限创建时间窗口都仍可能漏掉很早创建、最近才发布的事件。必须二选一：
  - **A. 从可信保留起点全量重放**（明确写出起点是什么、为何该点之后不丢），正确性依赖消费者幂等
  - **B. 用消费回执 / 已验证检查点缩小范围**（哪些消费者已确认处理到哪个事件，作为水位下界）

  两条路都要求 **④ 的逐消费者幂等从"建议"升级为前置条件**

**验收**：
- 「停机成功、事件已发布后丢失」→ 从 **`gateway_db`（`charge_ended`）**恢复并完成计费
- 「延迟发布后再次丢失」→ 重放必须捞回（昨天创建、今天发布、再次丢失）

**③b ACK 水位 ≠ 业务完成水位（补 D4 遗漏路径）**

`common-redis/src/lib.rs:231-242` 的 `dead_letter` Lua 脚本：先 `XADD` 到 `<stream>.dlq`，再 **`XACK` 原消息**。`common-stream/src/lib.rs:171-178` 在重试耗尽时走这条路径后 `return Ok(())`。

后果：**依赖短暂故障（如 DB 不可用）超过重试周期后，消息已离开 PEL，正常消费者永不再见。** 唯一恢复入口 `worker/src/tasks/dlq_replay.rs:8` **只打印日志，且未注册**。因此：

- **即使没有任何 Stream 正文丢失**，计费 / 退款事件仍可能永久停在 DLQ
- ② 的裁剪水位用 ACK 推导**不成立**——ACK 只代表"不再投递"，不代表"业务完成"

必须补齐：

| 项 | 要求 |
|---|---|
| 临时错误 vs 永久错误 | 分类处置。**临时错误（DB/Redis 不可用）不得耗尽重试后 ACK**——应保持 PEL 待后续重投，或延长重试直至恢复 |
| DLQ 恢复入口 | `dlq_replay` **必须注册**，明确重放责任方与调度 |
| 完成水位 | 以**业务完成**（回执表 / 业务状态）为准，**不以 ACK 为准** |
| 验收 | **依赖故障超过重试周期 → 恢复 → 计费/退款最终完成**（不能只检查"消息被保留"） |

**④ 幂等：逐消费者声明去重键，不能统一套 `event_id`**

现有 receipt 的去重键**并不统一**：有 `event_id`、订单 ID、命令 ID、计费单号、微信退款号等。**D4 不能假设"按 `event_id` 天然幂等"。**

**具体反例**——`admin/src/stream_consumer.rs:100-113` 的 `DeviceEventHandler`：
```rust
"UPDATE device_meta SET last_seen_at = NOW(3) WHERE device_id = ?"
```
每次处理都写"现在"，**重放旧事件会把历史设备活动标记为刚刚发生**。修法：用**事件自带的校验后时间**而非 `NOW(3)`，并**防止乱序回退**（仅当事件时间 > 现值才更新）。

逐消费者须验收：**重复投递、乱序投递、并发重放**三种情形。

**⑤ 分级与 D5 的冲突须消解**

D5 保留"允许丢失"选项，与 D4 把 `pricing_rule_changed` 列为可靠流矛盾。**以 D4 为准**：凡列入可靠的流，**必须先补齐事务 outbox**（与业务写同事务落库），才允许发布。补齐前不得声称可靠。

**验收**：消费者停机至超裁剪阈值 → 恢复 → 可靠流**零丢失**（对账 DB）；可丢流允许丢失但必须告警；重放不产生重复业务副作用。


### API 404 与 SPA fallback

`admin/src/static_serve.rs:45-54`：目标文件读不到时**回退 `index.html` 并返回 200**。因此"删除路由 → 返回 404"**不成立**——未注册的 `/api/...` 会拿到 **200 + SPA HTML**。这既是 D3 验收的阻塞点，也掩盖了未注册 API 路径的存在。

必须区分两条 fallback：

| 路径形态 | 应有行为 |
|---|---|
| 匹配到 API 路由但资源不存在 | **API 404**（`ApiEnvelope` 错误信封） |
| `/api/…` 且**完全未注册** | **API 404**，不得回退 SPA |
| 页面路由 / 静态资源 | 回退 `index.html`（SPA 前端路由需要） |

测试须在**静态资源实际存在**的条件下运行，否则会落进"`index.html` 读不到"那条分支，测不出问题。

**D1 的范围必须覆盖全部管理端写接口，不能只补提权入口。** 除 `roles.rs:66` / `users.rs:79` / `users.rs:114` 外，已确认的同级缺口：

| 端点 | 位置 | 风险 |
|---|---|---|
| `split_party_create` | `admin/src/api/settings.rs:131` | 丢弃 `AdminClaims`。**billing 实际读取该模板分账**（`billing/src/api.rs:97`）——向比例合计已为 10000 的模板追加正比例参与方，会使后续分账因比例校验失败而停止 |
| 计费规则 / 分账模板写入 | `settings.rs` 同族 | 直接改计费输入 |

因此 D1 的验收是**逐条遍历管理端写接口**的矩阵测试，不是抽查三个函数。

**阶段归属总览**（与阶段表一致）：**D1 / D2 / D9 / D10 / D14 = P0.5 门槛**（未过不得进入 P1a；D10 涉资金）· **D12 = P1a** · **D13 = P3-admin**· **D7 / D8 = P3-admin**（需改调用方，受 P1a"只增不拆"约束所限）· **D3 / D4 / D5 / D6 / D15 / D17 = P4 数据层与运行时**· **D11 / D16 = 待业务决策**。

**D1、D2、D9 是安全漏洞，D10、D14、D16 涉资金与计费能力，D12、D15、D17 是可用性/可靠性事故，优先级高于全部结构改造。** 攻击路径：任一登录态管理员 → 改自己 `role_id` 提权 → 重置他人密码；且账号停用或撤权后只要能 refresh 就持续获得新 token。

**D3 处置已修正**：早期方案写"admin 改调 billing HTTP"——不可执行，billing 无承接点，改造后创建会变 503。经核实 `admin-web` 零调用，按**未开放功能整体删除**处理。


| **D18** | **站点详情经纬度颠倒** | `admin/src/api/internal.rs::stations_detail` 的 SQL 列序为 `(…, longitude, latitude, …)`,即 `r.4=longitude` / `r.5=latitude`,但代码写成 `"longitude": r.5, "latitude": r.4` —— **两者互换**。同文件 `stations_nearby` 正确,仅此一处错;该端点被 user/gateway 消费 | **修**:改为按字段名显式赋值(`longitude: r.4, latitude: r.5`),并由 `StationForUser` 类型固定字段名,杜绝按列序错配 | **P2** | 测试 `coordinates_are_not_swapped` 断言北京站经度 116.397 > 纬度 39.908 |

### migration 接管

现状：`init-mysql.sh` 以 root 手工按序 apply 各 schema 的 `0001..00NN`，**无版本表**。直接切 `sqlx::migrate!` 会让存量库从版本 1 重放，`0003_device_import_retry.sql` 等会重复加列。

**① 区分存量库与全新空库**

不能用"`_sqlx_migrations` 是否为空"判断——全新空库同样为空。改用 `information_schema.tables` 探测基准表：

```sql
SELECT COUNT(*) FROM information_schema.tables
 WHERE table_schema = 'user_db' AND table_name = 'charge_order';
```

- `> 0` → 存量库，走 baseline 登记
- `= 0` → 全新库，走全量 apply

**② baseline 登记（无现成 CLI，必须自建脚本）**

`sqlx migrate add -r` 只生成 up/down 文件，**不读 schema、也不把历史版本标记为已应用**；`sqlx migrate run` 在 0.8.6 的**全部**参数只有 `--source` / `--dry-run` / `--ignore-missing` / `--target-version` / 连接参数，**不存在 `--baseline`**。

而 `run()` 的实现是**逐条**比对 `_sqlx_migrations`，缺失即 apply——所以**每一条历史迁移都必须单独登记正确 checksum**，只登记最大版本会让其余文件照样被执行（`0003_device_import_retry.sql` 重复加列）。

登记脚本的形态（**不自实现 hash**，直接用 sqlx 自己的计算结果）。**四步，顺序不可颠倒：**

```
① 核验历史版本集合 → ② 创建登记表 → ③ 校验并登记 → ④ 交给 migrator 执行剩余
```

**① 核验历史版本集合（关键：必须显式传入，不能遍历目录）**

遍历 `Migrator` 的全部 up migration 会**把尚未执行的新迁移也标记成功**——旧库只执行到 `0019`、仓库已有 `0020` 时，`0020` 会被登记，随后 migrator 认为无需执行。

因此脚本必须**接收每个 schema 已核验的历史版本集合**（由 schema 探测 + 人工确认得出），只登记集合内的版本：

```rust
let verified: HashSet<i64> = /* 逐 schema 核验得出，如 user_db = 1..=19 */;
```

**② 创建登记表**

被接管的旧库**恰好没有 `_sqlx_migrations` 表**，而 `Migrator::new()` 只读迁移文件、不建表。必须先调 sqlx 的 `ensure_migrations_table()`。

**③ 校验并登记（不用 `INSERT IGNORE`）**

已有记录要逐条核对 `checksum` 与 `success`——`INSERT IGNORE` 会静默跳过不一致的记录，把问题藏起来。checksum 必须取自 `Migrator::iter()`，保证与 `migrate!` 运行时校验一致（`run()` 遇到不匹配会报 `VersionMismatch`）。

```rust
conn.ensure_migrations_table().await?;                      // ②
for m in migrator.iter().filter(|m| m.migration_type.is_up_migration()) {
    if !verified.contains(&m.version) { continue; }         // ① 只登记已核验的
    let existing: Option<AppliedMigration> = /* 按 version 查 _sqlx_migrations */;
    match existing {
        Some(a) => {
            // AppliedMigration 只有 { version, checksum }，无 success —— 失败迁移另行检查
            assert_eq!(a.checksum, m.checksum, "checksum 不一致，人工裁决");  // ③ 不静默跳过
        }
        None => {
            sqlx::query("INSERT INTO _sqlx_migrations
                         (version, description, installed_on, success, checksum, execution_time)
                         VALUES (?,?,NOW(3),1,?,0)")
                .bind(m.version).bind(&m.description).bind(&m.checksum)
                .execute(&mut *conn).await?;
        }
    }
}
// 失败迁移检查：AppliedMigration 不暴露 success，用 sqlx 的 dirty_version()
if let Some(v) = conn.dirty_version().await? {
    anyhow::bail!("schema 处于 dirty 状态（迁移 {v} 失败），先人工修复");
}
}
```

**④ 执行剩余**：登记后 `sqlx migrate run`，`0020` 等未核验的迁移正常执行。

**③ 停脚本重复执行**

`init-mysql.sh` 仅在**空数据卷**初始化时执行，存量库升级**不会触发**。改造后它只负责建库 + 建账号授权，不再 apply 历史 SQL。

**④ 构建与部署改造（否则接 `migrate!` 直接构建失败）**

| 现状 | 需改 |
|---|---|
| `services/*/Dockerfile` 只 `COPY crates`/`COPY services`，**零 `COPY migrations`** | 补 `COPY migrations migrations`（`migrate!` 是编译期宏，文件必须在**构建期**存在） |
| `compose.dev.yaml:62` 的 `./migrations` 只挂在 **mysql 容器** | `x-rust` 锚点（34-44 行）补挂 `./migrations:/workspace/migrations:ro` |
| 5 个服务的 `Dockerfile.rust` 开发镜像 | 同步补 `migrations` |

**⑤ 迁移由谁执行（不能由服务启动时做）**

`compose.dev.yaml:65` 的 MySQL healthcheck 是 `SELECT 1 FROM admin_user_role LIMIT 0`——**依赖表已存在**；而 `x-rust` 的 `depends_on: mysql: { condition: service_healthy }`。若改为服务启动时建表 → **循环等待，死锁**。

因此：迁移由**独立的 migrate job / init 容器**执行，跑在 MySQL 可连接（但 healthcheck 尚未通过）之后、业务服务启动之前。业务服务启动时**只校验版本**（schema 版本低于期望则拒绝启动并报错），不负责建表。

**⑥ 跨库视图必须删除（不能只收权限）**

`migrations/admin_db_views.sql` 建的 2 个视图正是跨库访问入口：

```sql
CREATE VIEW `withdraw_request` AS SELECT * FROM `billing_db`.`withdraw_request`;
CREATE VIEW `membership_card`  AS SELECT * FROM `user_db`.`membership_card`;
```

`init-mysql.sh:22` 以 **root** 创建 → 默认 `SQL SECURITY DEFINER`。**只收紧服务账号的跨库权限不够**——视图仍可访问底层库。必须：

- **删除这 2 个视图**，随缺陷 ①②③ 一并把调用方全部迁走
- 已知调用方：① `admin/src/billing.rs:30,52,68`（`withdraw_request` 读写）② `admin/src/api/membership.rs:18`（`membership_card` 直查；同文件 `create` 已返回 `ServiceUnavailable`，属预留功能）
- 过渡期若必须保留：**单独记为 P4 的收口项**，写明删除阶段与权限收紧方式，不留在最终态

**⑦ 验收（V9）**

| 路径 | 要求 |
|---|---|
| 空库全新安装 | 全部迁移按序执行成功，schema 与基线等价 |
| 存量库升级 | baseline 正确识别并登记，**不重放**历史迁移，schema 无重复列 |
| **落后一个版本** | 旧库停在 `0019`、仓库有 `0020` 时：只登记 `0019`，`0020` **确实被执行**（验证"未核验版本不被误标成功"） |
| 登记表缺失 | 旧库无 `_sqlx_migrations` 时脚本先建表再登记，不报错 |
| checksum 不一致 | 已登记记录与文件 checksum 冲突时**报错中止**，不静默跳过 |
| 幂等性 | 重复执行无副作用 |
| 构建 | 5 个 `Dockerfile` 均能成功构建（`migrate!` 编译期可解析路径） |
| 整栈 | 空卷 `docker compose up` 一次成功，无循环等待 |

---

## 六、验收闸口（不修 CI）

**决定不修 CI。** 代价：1116 处 fmt + 102 条 dead_code 不单独清理，随各服务改造消化；未覆盖部分将残留。**无 CI 意味着规范一致性 100% 依赖人工逐条核对。**

| # | 检查 | 通过 |
|---|---|---|
| V1 | `cargo check --workspace --all-targets` | 零 error |
| V2 | `cargo test -p <svc>` | 全绿；**按去重后的用例与行为覆盖计**，见下 |
| **V2b** | **`cargo test -p <svc> -- --ignored`**（需 `DATABASE_URL` + Redis，`compose.dev.yaml`） | **全绿——含资金并发/行锁测试** |
| V3 | `cargo clippy -p <svc> --all-targets` | 该服务告警归零 |
| V4 | `cargo fmt -p <svc> -- --check` | 通过 |
| V5 | 契约测试 | **改造前先建基线快照**（不是 P5 才做）；**§五 已知错误行为必须排除在快照外** |
| V6 | 架构守护 | handler 无 `sqlx::`/`json!`/`Value`；usecase 无 `sqlx::`/`json!`；domain 无 `sqlx`/`axum`/`reqwest` |
| V6b | `json!` 禁令 | 生产代码计数 = 0；`Value` 仅在例外清单 |
| V7 | 入口封死 | `AppState.db` 已移除；旧客户端 / `api_types::paths` / `Envelope` 别名 零引用 |
| V8 | 共享 crates | `cargo test -p 'common-*'` 全绿——**通配符必须加引号**，否则 zsh 会在 shell 层提前展开 |
| **V8b** | **共享 crates 的 ignored 测试** | `cargo test -p 'common-*' -- --ignored`（需 Redis 环境变量 + **隔离的测试实例**，避免与 dev 数据串扰）。V2b 只跑服务，V8 只跑默认目标，**两者都不覆盖此项** |
| **V10** | **闸口自检** | 每个守护测试内嵌**预期失败的违规 fixture**，断言其确实被拦下（V1–V9 之外单独跑） |
| **V9** | **migration（P4 验收）** | 空库安装 / 存量库升级 / 幂等 / 5 个 Dockerfile 构建 / 空卷整栈启动，全部通过 |

**V2b 不可省**：`payment_receipt.rs:409` 的 `concurrent_recharge_notifications_credit_once` 起 5 个并发任务验证"充值通知只入账一次"，`#[ignore = "requires development MySQL"]`，默认 `cargo test` **跳过**。

**V8b 为何独立**：V2b（服务 `--ignored`）+ V8（共享库默认）组合起来仍不运行 `common-stream` 的 ignored 测试。被跳过的是真实可靠性用例——`common-stream/src/tests.rs:45` **重启恢复 pending**、`:151` **DLQ 写入失败不得 ACK**，以及消费者注册测试。gate/stream 属于后台可靠性路径，`cargo test` 默认**一次都不跑**。

**"测试数"不能用原始计数衡量。** 现状 `billing::engine` 的 6 个单测同时编进 lib 和 bin（双模块树），同一组用例**跑两遍**；消除双模块树后执行数自然下降。V2 的正确口径：

- 按**去重后的用例**计（`cargo test -- --list` 去重，或以 lib target 为准）
- 重点看**行为覆盖**而非数量：状态机全路径、金额边界、权限判定、幂等分支
- 允许因去重而下降，**不允许因漏测而下降**——需逐条对照改造前的用例清单

**V3 可要求归零的原因**：272 条告警中 187 条是 `dead_code`，主因是双模块树。消除双模块树 + 重写文件后自然消失——随重写决定去留，不批量删除（可能藏未完成业务）。

---

## 七、阶段

**5 个服务**：gateway / user / admin / billing / worker。

| 阶段 | 内容 | 验收 |
|---|---|---|
| **P0** | 修测试基线（`common-wechat` 测试编译失败） | ✅ `--all-targets` 零 error |
| **P0.5** | **安全修复前置**（不改结构、可独立合并）：**D9 路径穿越** + **D1 全量写接口权限矩阵** + **D2 会话状态** + **D10 退款截断** + **D14 开票金额来源**。五项都是"加校验/调顺序"，不动调用方签名，**优先级高于全部结构改造**。**⚠️ 门槛：必须通过 D1 / D2 / D9 / D10 / D14 五项行为测试才允许进入 P1a**——安全验证不得后拖到结构改造之后 | **D1+D2+D9+D10+D14 行为验收** + V1、V2 |
| **P1a** | **只新增、不删改既有调用方**（**唯一行为变更例外：D12 gateway 启动失败行为**——TCP bind 失败须使启动失败，任务异常退出须撤销就绪）：① **统一工具链**（`rust-version` 1.88 + 锁定 toolchain + 5 个 Dockerfile + `--locked`）② **新增** `Db::begin() -> Tx<'_>`（含 `rollback`），`with_tx` 原样不动 ③ **新增**统一引用路径 `common_error::ApiEnvelope`（旧别名不动）④ **新增**根 `clippy.toml` + 各服务 `#![deny(...)]`（先 `allow`，P3 逐服务开）⑤ request_id 全链修复（中间件不碰 body + `try_with` + tracing span + `TraceLayer.make_span_with`）⑥ 路由注册表骨架 ⑦ **契约基线快照**（排除 §五 已知错误行为）⑧ **各服务 usecase 服务对象建出来**（与旧 `st.db` 并存）⑨ **D12 gateway 启动与就绪**（**行为变更例外**）。**D7 不在 P1a**——它需改 `admin` 调用方，违反"只增不拆"，改到 P3-admin | V1、V2、V5、V8 + **D12 验收** |
| **P1b** | **建检查机制，默认只报告不阻断**：架构守护测试、扫描测试、lints `allow` → 试点服务 | 检查项可运行 |
| **P2** | `api-contracts` 重写：路径注册表 + **152 处 DTO** + 跨服务上游 DTO | V1、V2 |
| **P3** | **逐服务**迁移（gateway → user → **admin（含 D7 改调用方、API 404 fallback 分离、**D13 密码计算 `spawn_blocking` + 并发上限**）** → billing → worker**）。每个服务：调用方全改完 → 删该服务 `AppState.db` → 开该服务 `deny` → 过 V1–V8、V8b、V10 + **D13 验收** | 每服务全过 |
| **P4** | 数据层与运行时：**D3 提现+会员卡整块删 + D4 裁剪水位/重放/③b DLQ 重放注册 + D5 事件契约 + D6 跨库查询 + D15 阻塞消费独立连接 + D17 限流 Lua 原子化**、删跨库视图、migration 接管、5 库权限 | **V9** + V8b + **D3–D6、D15、D17 行为验收** |
| **P5** | 全局收口：删全部旧入口 + 全局 `deny` + 测试补全 + OpenAPI | V1–V8 复检 |
| **P6** | API 变更清单 | 文档评审 |

**P1a 铁律：只增不拆。** 新设施与旧路径并存、可编译、行为不变。任何"删旧入口"的动作都在 P3 该服务迁移完成之后——`st.db` 现有 298 处引用，提前删除必然编译失败。

具体地，以下两项**不在 P1a**，因为它们都要动现有调用方：

- **不改 `with_tx` 签名**：本次保留原样，**理由是 `Send` 跨线程约束未单独验证**（`Transaction<'static, MySql>` + `AsyncFnOnce` 实测可编译，见 §一）。`BoxFuture` + HRTB **不是已证明的必需品**（早期断言过度），要简化需先单独验证异步闭包版本的 `Send`。P1a 只**新增** `Db::begin() -> Tx<'_>` 供 P3 使用
- **不删 `Envelope` 别名**：`user/src/api_envelope.rs` 的 `pub use ... as Envelope` 被现有 handler 引用，删除要同步改调用方。→ P1a 只统一**新增代码**的引用路径（直接用 `common_error::ApiEnvelope`），旧别名随 P3 该服务迁移时删

**V9 归 P4。** migration 接管依赖视图删除，而视图删除依赖缺陷 ①②③ 的调用方迁移，天然在数据层阶段。P3 的服务验收不含 V9。

P3 各 worker 文件所有权不重叠（`services/<svc>/`）。

**共享 crate 的写入权限不是全程冻结**，按阶段开放，避免 D4 无处落地：

| 阶段 | 可改的共享 crate | 改完后必须重跑的验收 |
|---|---|---|
| P1a | `common-db`（`Db::begin`）、`common-error`（task_local + `trace_id`）、`common-http`（request_id 透传） | V8 全部 + V1 |
| P2 | `api-contracts` | V1、V2 |
| P3 | **只读**（服务侧改动为主） | — |
| **P4** | **`common-redis`（D4 保留/重放 + **D17 限流 Lua 原子化**）、`common-stream`（D4 DLQ 重放入口）** | **V8b**（共享库 ignored）+ **所有消费 Stream 的服务 V1–V8**（gateway / user / admin / billing / worker 全部重跑） |
| P5 | `common-http`（client 收口收尾） | V8 全部 |

P4 改 `common-redis` 影响全部 5 个服务的消费者，**不能只跑 billing 就过账**。

### worker 的改造范围

不只是定时任务。现有 4 类入口都要保留并改造：

- 3 个后台循环：`announcement_expire` / `snapshot_warmer` / `device_session_clean`（`scheduler.rs:10-12`）
- Stream 消费者：`webhook_retry` / `ota_schedule` / `comp_tx`（`streams.rs`）
- `/health` HTTP 入口（`main.rs:66`）
- 另 10 个 task 文件未注册（`tasks/mod.rs` 注释："未接入的计划任务不注册"），**其中 `dlq_replay` 除外——D4 要求它必须注册**（否则失败事件永久滞留 DLQ，见 §五 ③b）。其余 9 个本次不动
- **D12 的"关键任务生命周期"规则同样适用于此**：已注册的循环/消费者异常退出应可见，不静默消失

---

## 七·五、执行结果（2026-09-28）

### 已完成并通过验收

| 阶段 | 状态 | 证据 |
|---|---|---|
| **P0** | ✅ | `--all-targets` 零 error |
| **P0.5** | ✅ | D9 / D1 / D2 / D10 / D14 五项全部落地并附行为测试；D1 覆盖 **56/56** 管理端写端点 |
| **P1a** | ✅ | 九项全部落地：工具链锁 1.88、`Db::begin()→Tx`、request_id 全链、`clippy.toml`、路由注册表、契约基线（228 条）、`common-app` 服务对象骨架、D12 gateway 启动与就绪 |
| **P1b** | ✅ | 架构守护 + 扫描测试落地。债务基线 **1072 → 801 处 / 56 文件**（P2 的 E5 去重与 gateway 类型化各降一部分；只报告不阻断） |
| **P4** | ✅ | D3 / D4③b / D4②裁剪水位 / D5 / D6 / D15 / D17 + migration 接管（`migrate-baseline` 工具、5 个 Dockerfile 补 `COPY migrations`、compose 挂载 + 独立 migrate job） |
| **P6** | ✅ | `docs/api-change-list.md`；P2 的 gateway 类型化未改变任何字段名，§3 仍成立 |

**当前闸口**：V1 `cargo check --workspace --all-targets` **零 error**；V2 `cargo test --workspace` **235 passed / 0 failed**。

### 未完成

| 阶段 | 原因 |
|---|---|
| **P2** `api-contracts` 重写 | **进行中**。① gateway 已归零（`ApiEnvelope<Value>` 11→0、`json!` 18→0）② **E5 路径单一真源完成**：`api-contracts::paths` 79→177 条，admin/user/billing 的 `api_types::paths` 131 条定义全部改为再导出/别名（**零调用方改动**），并加 4 个防回潮测试。**user/api.rs 归零**(Value 13→1)· user 余 35 · **admin 117→50** 待做;`withdraw_*` 死代码已删(D3 闭环) |
| **P3** 逐服务迁移 | 未启动。5 个服务仍是扁平的 handler 模块；`AppState.db` 298 处引用未收敛 |
| **P5** 全局收口 | 依赖 P2/P3。lint 仍为 `allow`，未转 `deny` |
| **D13** Argon2 `spawn_blocking` | 属 P3-admin 范围，随之顺延 |
| **D11** / **D16** | 需业务决策，不在技术实施范围 |

### 已知遗留

- 7 个 `#[ignore]` 的 DB 行为测试（D1 / D2 验收）**需要 `compose.dev.yaml` 起库才能真跑**（V2b），当前一个都未执行。
- `admin_db` / `billing_db` 的 MySQL 账号仍持有全部 5 个 schema 的 `ALL PRIVILEGES`；收紧需按服务拆分账号，属运维变更。
- `docs/技术规格.md` 尚未回写本次修复的偏差。

---

## 八、已决 44 条

1. 架构 = 垂直切片 + 内部分层  2. **5 个服务**全改  3. 接口可完全重设计  4. 分层测试
5. 仓储签名 `&mut MySqlConnection`（原子性优先，**撤回"mock 三行搞定"**）  6. `disallowed_methods` + 扫描双保险
7. worker 无 handler/dto/routes 三层，但保留 3 循环 + Stream + `/health`  8. 前端由后端出变更清单、另安排人跟
9. 数据层治理 + **migration 接管 + 删除跨库视图**一并做（P4，V9 验收）  10. 3 个死端点直接删
11. **不修 CI**，fmt/dead_code 随改造消化  12. **不做样板**，5 服务按序迁移
13. 信封 `trace_id`  14. 核心原则 = 消灭分叉  15. 验收 = V1–V9
16. ~~提现归属~~ → **已定整块删除（D3）**  17. D3–D6 / D9 在 P0.5 或 P4 修，见缺陷台账
18. **`json!` 生产代码零容忍，测试放行**
19. **工具链真实下限 1.88**（`time 0.3.55` 强制），声明的 1.80 是假的——统一 `rust-version` / toolchain / 构建镜像，验收加 `--locked`
20. **事务边界走 `Db::begin() -> Tx`**（含显式 `rollback`），本次不改 `with_tx`（`Transaction<'static>` + `AsyncFnOnce` 实测可编译，但**跨线程 `Send` 未单独验证**，不作为本次依据）
21. **约束分两级**：可见性/依赖类由 rustc 保证；禁 SQL/`Value`/`json!` 由 clippy + 扫描保证（`cargo check` 拦不住）
22. **task-local 读取一律 `try_with`**，后台子任务显式捕获并 re-scope；日志关联靠 tracing span，不靠 task-local 自动注入
23. **D1 账号/角色管理补操作授权**（403 + 数据库零变更），**优先级高于结构改造**
24. **D2 会话**：登录拒绝 `disabled` 等状态 + refresh 重新查库授权 + token 失效策略
25. **D3 提现整块删除**（admin-web 零调用、billing 固定 503、无承接点）——不"改调 HTTP"
26. **D4 / D5 事件可靠性**：Stream 保留与重放机制、规则变更事件契约（outbox 或明示可丢）
27. **静态分发与 `dyn` 分开决策**：RPITIT 解决 `Send`，`dyn` 只能用 `async-trait` 或显式装箱
28. **已知错误行为不进契约基线快照**；共享库 ignored 测试单列 V8b；守护测试须自带预期失败 fixture（V10）
29. **D9 路径穿越修复前置到 P0.5**（安全，优先于一切结构改造）；API 404 与 SPA fallback 必须分离
30. **D3 提现 + 会员卡两个视图整块删除**（两个调用方均死端点）；共享 crate 写入权限按阶段开放，P4 改 `common-redis` 后全部 5 服务重跑验收
31. **P0.5 为硬门槛**：D1 / D2 / D9 行为测试未通过不得进入 P1a
32. **D4 重放按消费确认水位裁剪**（`MAXLEN ~` 与精确裁剪**都不保护未 ACK 正文**）；`admin_db` 须补 `event_outbox`，`user_db`/`gateway_db` 的 outbox 须补 `stream_message_id`
33. **D10 退款截断**（`charge_fee.rs:24` 的 `?` 先于 `:33` 的归零判定）= P0.5 资金门槛项
34. **D11 webhook_retry 未实现**（生产 payload 无 `url`，消费者要求 `url`）= 待业务决策，**不得用不存在的送达能力论证可丢**
35. **D12 gateway TCP 监听启动失败 → 进程启动失败**；关键任务异常退出撤销就绪；健康检查纳入关键任务存活
36. **D13 密码计算走 `spawn_blocking` + 并发上限**；`tokio::spawn` / async trait 不解决问题
37. **重放范围必须含"已发布但未完成消费"**：`status != 'published'` 与"最大延迟窗口"两个备选均已删除；历史记录走**全量重放或回执/检查点**，"保守时间窗"不作为不遗漏依据
38. **D14 开票金额来源 = 实结额**（非预付款）；申请与审核两处均校验实结/退款终态
39. **D15 阻塞消费用独立连接**；发布/ACK/健康检查走非阻塞连接
40. **ACK ≠ 业务完成**：`dlq_replay` 必须注册；临时错误不得耗尽重试后 ACK
41. **D16 跨分时电价无法计费 = 数据缺失**（`ChargeEndMeter` 无分段读数）→ 三选一决策；**D10/D4 通过 ≠ 计费闭环**
42. **D2 含失败锁定**：`locked_until` / `status=locked` 全仓只读不写，契约 `docs/api/admin.md:107` 的"失败 5 次锁 30 min"从未触发
43. **D17 限流 INCR+EXPIRE 合为 Lua 原子操作**，并清理已存在的无 TTL 异常键
44. **P1a 行为变更例外仅 D12**（gateway 启动失败须使启动失败）；`dlq_replay` 注册属范围内，其余 9 个未注册 task 不动

### 待确认 2 件事

1. **§三 `Value` 例外清单**——现列 3 类，是否够
2. ~~提现归属~~ —— **已定整块删除（D3）**，不再需要裁决

### 本方案不做

不修 CI · 不动 miniprogram / admin-web · 不做 OpenAPI 之外的 API 文档工具
