# 网络拓扑(单客户单部署)

> **配套**:`docs/技术规格.md` § 10(部署)+ `examples/docker-compose.yml` + `examples/Caddyfile`
> **网络划分原则**:**最小暴露原则**——只暴露公网必需的端口,内部服务全部走 docker network

---

## 一、Docker 网络划分

本系统部署在 3 个独立的 docker network 上(均 `bridge` driver):

| 网络 | 加入者 | 暴露端口 | 说明 |
| --- | --- | --- | --- |
| **`internal`** | mysql / redis-cache / redis-stream / user / admin / billing / worker / caddy | **无**(`expose:` 仅 docker network 内可达) | 5 服务 + 2 Redis + Caddy 互通;**无端口映射到宿主机** |
| **`device-net`** | gateway | **9100 / 1883**(公网设备入网) | 设备长连接专用;**仅 gateway 加入**,避免设备 → MySQL / Redis 直连 |
| **`public`** | caddy / gateway(9100 / 1883) | **80 / 443**(公网 HTTPS) | Caddy TLS 终止 + gateway 设备长连接端口 |

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
#    客户场景:通常是物业 / 园区内网 → 公网设备 IP 白名单
iptables -A INPUT -p tcp --dport 9100 -s <设备IP段> -m state --state NEW -j ACCEPT
iptables -A INPUT -p tcp --dport 1883 -s <设备IP段> -m state --state NEW -j ACCEPT

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
| **设备长连接 9100 / 1883** | gateway 加入 `device-net`,允许公网设备 → gateway(设备 IP 白名单) |
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
                            gateway:8083(charge_ended_stream 消费)
                              ↓
                            gateway:9100(TCP/MQTT → 设备)
                              ↓
                            Redis:6379(redis-cache,snapshot:{order_id})
                              ↓
[小程序] ← Caddy:443 ← user:8081(snapshot JSON)
```

### 4.2 设备上报遥测 → 告警推送

```
[设备] → gateway:9100(TCP)或 1883(MQTT)
       → gateway_db.telemetry
       → 越界则 XADD alert_stream → redis-stream
       → worker 消费 → admin 写 alert_event → Webhook 推送
```

### 4.3 充电结束 → 计费 / 退款

```
[设备] → gateway:9100
       → charge_state 变化 → XADD charge_ended_stream → redis-stream
       → billing 消费(charge_ended_stream.billing-cg) → 写 billing_db
       → user 消费(charge_ended_stream.user-cg) → 关轮询
       → 条件触发 → XADD refund_required_stream → redis-stream
       → admin 消费 → 调微信退款 → 通知用户
```

---

## 五、与客户端的网络协议矩阵

| 客户端 | 入口 | 出口 → | 鉴权 |
| --- | --- | --- | --- |
| 小程序 | Caddy:443 → user:8081 | MySQL / Redis / billing:8084 / gateway:8083 | JWT(openid) |
| PC 后台 | Caddy:443 → admin:8082 | MySQL / Redis / user:8081 / gateway:8083 | JWT(角色)+ 双因素(待二期) |
| 微信支付回调 | Caddy:443 → user:8081 `/api/v1/public/payment/wechat/callback` | MySQL / Redis Stream | 微信签名(RSA) |
| 充电桩 | gateway:9100 / 1883(直连,**不过 Caddy**) | gateway_db / redis-stream | device_id(无 TLS) |
| 监管平台 Webhook 推送 | admin 主动推送(出站) | 客户配置的 Webhook URL | HMAC-SHA256(`X-Signature`) |
| OTA 固件下载 | 客户 OSS / 对象存储 → 设备(由 admin 调度,gateway 中转 URL) | — | URL 签名(SHA-256 + 厂商私钥) |

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
                              设备(公网,9100/1883)

public:          caddy(80/443) + gateway(9100/1883)
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
| 等保三级 — 入侵防范 | gateway 9100/1883 鉴权(device_id) | `docs/技术规格.md` § 6.2 |
| 等保三级 — 通信完整性 | TLS 1.3 + HSTS | `docs/技术规格.md` § 9.1 |
| 个保法 — 数据本地化 | 单客户单部署,数据不出客户机房 | `docs/需求分析.md` § 1.2 |