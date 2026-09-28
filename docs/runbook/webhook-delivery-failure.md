# Webhook 投递失败排查

> **触发条件**:客户反馈「Webhook 没收到告警通知」;或后台「投递记录」页面
> 显示大量失败;或 `webhook_retry_stream.dlq` 持续增长。
>
> **前置**:D11 已于 2026-09-28 实现全链路。此前该能力**从未工作过**
> (生产 payload 无 `url`、消费者固定 503、投递日志表无人写入),
> 故升级到该版本后**历史「无记录」是正常的**。

---

## 0. 先分清是哪一段坏了

链路共 5 段,逐段排除,**不要从头猜**:

```
告警产生
  → admin 消费 alert_stream,落 alert_event          ①
  → 按 alert_subscription 展开订阅,写 event_outbox    ②
  → OutboxService 扫 pending,XADD 到 webhook_retry    ③
  → worker 消费并真实 HTTP 投递                       ④
  → 结果经内部端点回写 webhook_delivery_log          ⑤
```

| 现象 | 断在哪 | 查法 |
|---|---|---|
| `alert_event` 无记录 | ① | 查 `admin` 日志有无 `AlertHandler` 报错 |
| `alert_event` 有、`event_outbox` 无 | ② | 查 `alert_subscription.webhook_subscription_id` 是否有值 |
| `event_outbox` 全是 `pending` | ③ | **最常见**:发布器没跑或持续失败,见 §1 |
| `event_outbox` 是 `published` 但订阅方没收到 | ④ | 查 `webhook_delivery_log`,见 §2 |
| 投递有记录但后台页面空白 | ⑤ | 查 admin 内部端点是否被调用 |

---

## 1. 事件卡在 outbox(最常见)

`event_outbox` 表:`status` ∈ `pending` / `published` / `failed`。

```sql
SELECT status, COUNT(*) FROM admin_db.event_outbox GROUP BY status;
SELECT event_id, stream, retry_count, last_error, scheduled_at
  FROM admin_db.event_outbox
 WHERE status <> 'published' ORDER BY id DESC LIMIT 20;
```

| `last_error` | 含义 | 处理 |
|---|---|---|
| 含 `XADD` / `redis` | Redis 不可达 | 先查 Redis:`redis-cli ping` |
| 含 `JSON` / `envelope` | 载荷损坏 | 查该行 `envelope_json` 是否合法 JSON |
| `NULL` 且 `scheduled_at` 在未来 | **正常退避中**,不是故障 | 退避上限 3600 秒;`retry_count` 累积到 10 会转 `failed` |

**确认发布器在跑**:

```bash
docker compose logs worker admin 2>&1 | grep -E "DLQ 重放|outbox" | tail -20
# admin 侧发布循环每轮失败会打 error,正常时无输出
```

> **注意**:`status='failed'` 表示重试 10 次仍失败,**不会自动恢复**。
> 修好 Redis/订阅配置后需手工重置:
> ```sql
> UPDATE admin_db.event_outbox
>    SET status='pending', retry_count=0, scheduled_at=NOW(3)
>  WHERE status='failed';
> ```

---

## 2. 投递失败:查投递日志

```sql
SELECT id, subscription_id, event_id, response_status,
       LEFT(error_msg,120) AS err, attempt_count, duration_ms, delivered_at
  FROM admin_db.webhook_delivery_log
 WHERE subscription_id = <你的订阅 id>
 ORDER BY id DESC LIMIT 50;
```

### 按 `response_status` 定位

| 状态码 | 含义 | 系统行为 | 处理 |
|---|---|---|---|
| `NULL` | 未取得响应(超时/连接失败) | 重试 4 次后进 DLQ | 订阅方不可达;检查防火墙/DNS |
| `2xx` | 成功 | 记幂等标记 | — |
| `408` / `429` / `5xx` | 订阅方临时错误 | **重试** 4 次后进 DLQ | 让订阅方查自己的限流/容量 |
| `400` / `401` / `403` / `404` | 订阅方明确拒绝 | **不重试** | 见下表 |
| `3xx` | 订阅方返回重定向 | **不重试** | 系统**刻意不跟随重定向**(见 §3) |

### 4xx 逐项对照

| 码 | 典型原因 |
|---|---|
| `400` | 请求体格式不符订阅方预期 |
| `401` / `403` | **验签失败** —— 订阅方用错了算法或编码 |
| `404` | URL 路径写错 |
| `413` | 请求体过大 |

> **验签口径**:HMAC-SHA256,密钥取 `webhook_subscription.secret`,
> **签名对象是请求体的原始字节**,小写 hex,请求头
> `X-ChargePilot-Signature: sha256=<64位hex>`。另带
> `X-ChargePilot-Event-Id` 与 `X-ChargePilot-Event-Type`。
> 订阅方若重新序列化 JSON 再算签名,**必然不一致**。

---

## 3. 投递被系统拒绝(`webhook_delivery_log` 无记录)

SSRF 防护会在**任何网络动作之前**拒绝下列 URL,此时**不会有投递日志**:

| 规则 | 被拒示例 |
|---|---|
| 仅 https | `http://…` |
| 禁 userinfo | `https://user:pass@host/…` |
| 禁 IP 字面量 | `169.254.169.254`、`127.0.0.1`、`10.0.0.5` |
| 禁本地/元数据名 | `localhost`、`*.local`、`metadata.google.internal` |
| 端口白名单 | 仅 `443` / `8443` |
| 长度 ≤ 512 | 超长 URL |

**禁重定向是刻意的**:校验通过的是首跳 URL,跟随重定向等于让订阅方把我们
导向任意地址,SSRF 校验会被整体绕过。故订阅方**不能**用 302 跳转到真实地址。

**已知残余风险**:域名不做 DNS 解析后校验,存在 DNS rebinding 理论风险。

---

## 4. 事件进了 DLQ

重试耗尽后事件写入 `<stream>.dlq`,由 `dlq_replay` 每日 04:00 重放。

```bash
redis-cli XLEN webhook_retry_stream.dlq
redis-cli XRANGE webhook_retry_stream.dlq - + COUNT 5
```

**D21 修复说明**:重放改用**增量游标**(表 `worker_db.dlq_replay_cursor`),
不再每轮从队首取最早 200 条 —— 旧实现会让新积压饿死。
若怀疑游标卡住:

```sql
SELECT stream, last_id, scanned_total, replayed_total, updated_at
  FROM worker_db.dlq_replay_cursor;
```

`last_id` 应**随时间前进**。若长期不动,说明该流没有新条目进入 DLQ。

> **已知约束**:重放用 `XADD` 带**原 entry id**。若该 id 已小于流当前
> top id(历史消息被新消息超越),Redis 会拒绝,此时退回自动生成 `*`,
> 消费者按 `event_id` 仍可去重。

---

## 5. 幂等:同一事件为何只收到一次

幂等键是 **`(subscription_id, payload.event_id)`**,记在
`worker_db.retry_queue`(`queue_name='webhook_delivered'`, `status='done'`)。

```sql
SELECT payload_json, created_at FROM worker_db.retry_queue
 WHERE queue_name='webhook_delivered' ORDER BY id DESC LIMIT 20;
```

**注意幂等键不是 envelope 的 `event_id`** —— 后者是随机 UUID,每次重投都不同。
告警的业务 event id 在 `payload.event_id` 里。

若确认需要重投同一事件(比如订阅方修好了配置想再收一次):

```sql
DELETE FROM worker_db.retry_queue
 WHERE queue_name='webhook_delivered'
   AND JSON_UNQUOTE(JSON_EXTRACT(payload_json,'$.event_id')) = '<event_id>';
```

---

## 6. 快速自检清单

- [ ] `webhook_subscription.enabled=1` 且 `deleted_at IS NULL`
- [ ] `alert_subscription.webhook_subscription_id` 有值且 `enabled=1`
- [ ] `event_types` 包含 `"alert_recorded"`(空数组 = 订阅全部)
- [ ] URL 是 https、端口 443/8443、非 IP、非 localhost
- [ ] 订阅方能访问本服务的**出网**地址(不是容器内网)
- [ ] 订阅方验签用的是**原始 body 字节**,不是重新序列化的 JSON
- [ ] 订阅方**不要求**跟随 302 重定向
