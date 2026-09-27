# 网络拓扑(单客户单部署)

> **配套**:`docs/技术规格.md` § 10(部署)+ `examples/docker-compose.yml` + `examples/Caddyfile`
> **网络划分原则**:**最小暴露原则**——只暴露公网必需的端口,内部服务全部走 docker network

---

## 一、Docker 网络划分

本系统部署在 3 个独立的 docker network 上(均 `bridge` driver):

| 网络 | 加入者 | 暴露端口 | 说明 |
| --- | --- | --- | --- |
| **`internal`** | mysql / redis-cache / redis-stream / user / admin / billing / worker / caddy | **无**(`expose:` 仅 docker network 内可达) | 5 服务 + 2 Redis + Caddy 互通;**无端口映射到宿主机** |
| **`device-net`** | gateway | **9100**(设备 TCP/JSON 接入) | 设备长连接专用;**仅 gateway 加入**,避免设备 → MySQL / Redis 直连 |
| **`public`** | caddy / gateway(9100) | **80 / 443**(公网 HTTPS) | Caddy TLS 终止 + gateway 设备长连接端口 |

---

## 二、宿主机防火墙建议规则

```bash
# iptables / nftables 简化示例(生产请按客户合规要求调整)
# 默认策略:拒绝所有入站
iptables -P INPUT DROP
iptables -P FORWARD DROP
iptables -P OUTPUT ACCEPT

# 1. 允许已建立连接
iptables -A INPUT -m state --state ESTABLISHED,RELATED -j ACCEPT
iptables -A INPUT -i lo -j ACCEPT

# 2. 允许 SSH(运维入口)
iptables -A INPUT -p tcp --dport 22 -m state --state NEW -j ACCEPT

# 3. 允许 HTTP/HTTPS(Caddy)
iptables -A INPUT -p tcp --dport 80 -m state --state NEW -j ACCEPT
iptables -A INPUT -p tcp --dport 443 -m state --state NEW -j ACCEPT

# 4. 允许设备长连接端口(gateway)
#    只开放当前已实现的 9100 TCP/JSON，并限制到设备 IP 白名单
iptables -A INPUT -p tcp --dport 9100 -s <设备IP段> -m state --state NEW -j ACCEPT

# 5. 关键:**不允许**暴露 3306 / 6379 / 8081 / 8082 / 8083 / 8084
#    这些端口在 docker-compose.yml 中只 `expose:`,不 `ports:`
#    即使端口暴露在 docker 0.0.0.0 上,宿主机防火墙也应拒绝
```

---

## 三、关键安全姿态

| 项 | 实施 |
| --- | --- |
| **MySQL 3306** | 不暴露公网;docker-compose 仅 `expose: 3306`(仅 internal 网络可达) |
| **Redis 6379** | 不暴露公网;两个 Redis 容器(缓存 + Stream)均仅 `expose:` |
| **内部 HTTP**(user:8081 / admin:8082 / gateway:8083 / billing:8084) | 不暴露公网;仅 `expose:`,经 Caddy 反代 |
| **设备长连接 9100** | gateway 加入 `device-net`,允许已建档设备 → gateway(建议限制设备 IP 白名单);当前为 TCP/JSON |
| **MQTT 1883** | 未实现且未监听;防火墙与 Docker 均不得开放 |
| **Caddy 80 / 443** | 唯一对外 HTTPS 入口;`public` 网络 |
| **微信支付回调** | 走 Caddy:443 → user:8081;需在微信商户平台"支付回调 URL"配置 `https://<customer-domain>/api/v1/public/payment/wechat/callback` |

---

## 四、流量路径示例

### 4.1 小程序用户扫码充电

```
[小程序] → Caddy:443 → user:8081 → MySQL:3306(user_db)
                              ↓
                            Redis:6379(redis-stream,charge_started_stream)
                              ↓
                            gateway:9100(设备 TCP/JSON 会话)
                              ↓
                            设备 ACK(JSON Frame)
                              ↓
                            Redis:6379(redis-cache,snapshot:{order_id})
                              ↓
[小程序] ← Caddy:443 ← user:8081(snapshot JSON)
```

### 4.2 设备上报遥测 → 告警推送

```
[设备] → gateway:9100(TCP/JSON;首帧 heartbeat 身份校验)
       → gateway_db.telemetry / 聚合
设备主动上报 alert 帧 → event_outbox → alert_stream
       → admin 消费并写 alert_event
       → webhook_retry_stream 缺少目标 URL，worker 投递失败后进入 Redis DLQ
```

### 4.3 充电结束 → 计费 / 退款

```
[设备] → gateway:9100(TCP/JSON status/停止确认)
       → gateway 持久化停止确认 → charge_ended_stream
       → billing 消费 → 写费用/分账并向 user 投递 fee_delivery
       → user 消费结束事件及 fee_delivery，写实结费用
       → 满足条件时 user 事务创建退款记录并发布 refund_required_stream
       → admin 持久化退款任务并尝试执行；真实商户资金联调尚未验证
```

---

## 五、与客户端的网络协议矩阵

| 客户端 | 入口 | 出口 → | 鉴权 |
| --- | --- | --- | --- |
| 小程序 | Caddy:443 → user:8081 | MySQL / Redis / billing:8084 / gateway:8083 | JWT(openid) |
| PC 后台 | Caddy:443 → admin:8082 | MySQL / Redis / user:8081 / gateway:8083 | JWT(角色)+ 双因素(待二期) |
| 微信支付回调 | Caddy:443 → user:8081 `/api/v1/public/payment/wechat/callback` | MySQL / Redis Stream | 微信签名(RSA) |
| 充电桩 | gateway:9100(TCP/JSON,直连,**不过 Caddy**) | gateway_db / redis-stream | 已启用的 device_id 与 vendor(无 TLS) |
| 监管平台 Webhook 推送 | 当前未接通投递 | 目标为客户配置的 Webhook URL | 目标为 HMAC-SHA256(`X-Signature`) |
| OTA 固件下载 | 当前未实现；目标为客户 OSS / 对象存储与设备下载 | — | 目标为 URL 签名 |

---

## 六、Docker Compose `networks:` 拓扑速查

```
internal:        mysql ←→ redis-cache ←→ redis-stream ←→ user ←→ admin
                  ↑                              ↑             ↓
                  └────── billing ←──── worker ←─┘             ↓
                  ↑                                              ↓
                  └────── caddy (反代 user / admin) ←────────────┘

device-net:      gateway ←→ MySQL(经 internal 互通,设备不可直接访问)
                              ↓
                              设备(9100/TCP JSON;1883 未监听)

public:          caddy(80/443) + gateway(9100)
```

---

## 七、运维调试路径

```bash
# 进容器排查
docker exec -it chargepilot-mysql mysql -uroot -p"$MYSQL_ROOT_PASSWORD"
docker exec -it chargepilot-redis-cache redis-cli -a "$REDIS_PASSWORD"
docker exec -it chargepilot-redis-stream redis-cli -a "$REDIS_STREAM_PASSWORD"

# 查看网络连通性(从 user 容器内)
docker exec -it chargepilot-user sh
> wget -qO- http://chargepilot-mysql:3306 || true   # TCP 测试
> wget -qO- http://chargepilot-gateway:8083/health 200

# 查看 docker 网络
docker network inspect chargepilot_internal
docker network inspect chargepilot_device-net
docker network inspect chargepilot_public
```

---

## 八、合规追溯

| 网络安全要求 | 实现 | 备注 |
| --- | --- | --- |
| 等保三级 — 网络架构 | 3 网络隔离 + 最小暴露 | 见 `docs/checklists/equal-protection-l3.md` § 四 |
| 等保三级 — 边界防护 | 宿主机防火墙规则 | § 二 |
| 等保三级 — 入侵防范 | gateway:9100 校验已启用 device_id 与 vendor;当前无 TLS | `docs/技术规格.md` § 6.2 |
| 等保三级 — 通信完整性 | TLS 1.3 + HSTS | `docs/技术规格.md` § 9.1 |
| 个保法 — 数据本地化 | 单客户单部署,数据不出客户机房 | `docs/需求分析.md` § 1.2 |
