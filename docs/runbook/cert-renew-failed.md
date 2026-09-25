# Runbook:证书续期失败

> **引用**:`技术规格.md` § 9.1 / § 10.3
> **触发场景**:Caddy 自动续期 Let's Encrypt 失败;客户访问 `https://<customer-domain>` 提示证书过期

---

## 一、症状

- 浏览器访问 `https://<domain>` 报 `NET::ERR_CERT_DATE_INVALID`
- Caddy 日志报 `acme: error: ...`
- admin 后台 `/health` 返回 200 但 PC 后台 SPA 加载失败
- 小程序 API 域名在"微信公众平台 → 开发管理 → 开发设置"显示不安全

---

## 二、立刻止血(15 min 内)

```bash
# 1. SSH 登录客户服务器
ssh user@<server>

# 2. 验证 Caddy 容器仍在跑
docker ps | grep caddy

# 3. 看 Caddy 错误日志(过滤 ACME 相关)
docker logs caddy 2>&1 | tail -200 | grep -i 'acme\|certificate\|expired'

# 4. 查看当前证书状态
docker exec caddy caddy tls list
# 输出示例:
# 2026-09-25T... - example.com (expires in -2 days)   ← 负数 = 已过期
```

---

## 三、根因定位

| 根因 | 排查命令 | 占比 |
| --- | --- | --- |
| **ACME HTTP-01 挑战被拦截** | `docker logs caddy 2>&1 \| grep 'challenge'` + 确认 80 端口开放:`curl -I http://<domain>` | 高 |
| **Let's Encrypt 配额** | `https://crt.sh/?q=<domain>` 查证书签发节奏 | 低 |
| **DNS A 记录指向错误** | `dig +short <domain>` 与服务器公网 IP 比对 | 中 |
| **Caddy 自身 bug** | `docker logs caddy 2>&1 \| grep -i 'panic\|fatal'` | 极低 |

---

## 四、修复流程

### 4.1 单域名续期失败 → 手动触发

```bash
# 进 Caddy 容器手动触发续期
docker exec -it caddy caddy adapt --config /etc/caddy/Caddyfile \
  | docker exec -i caddy caddy reload --config /etc/caddy/Caddyfile.json
```

### 4.2 80 端口被拦截(常见于客户网关防火墙)

1. 告诉客户运维:**申请开 80 出口到服务器公网 IP**(Let's Encrypt 必须用 HTTP-01)
2. 临时方案:改用 DNS-01 挑战(需要客户域名 NS 切到 Cloudflare 或加 CNAME 别名)
3. 若客户合规禁止 80 出口:暂改用 ZeroSSL 备 CA(Caddyfile `acme ca https://acme.zerossl.com/v2/DV90`)

### 4.3 Let's Encrypt 配额用尽

- 单域名每周 50 张证书上限(罕见,只在反复签废时触发)
- 等下周一重置 + 改用 staging 环境测试

---

## 五、永久预防(P1-10:标注代码未实现)

> **P1-10 状态说明**:仓库代码尚未实现,以下项目为**计划提供**;代码动工后按文档落地。

- 在 admin PC 后台"Webhook 订阅"加 `cert_renew_failed` 告警 → 写入 `alert_event` 表(表结构见 `db/admin.md`,**本期未接入 Caddy ACME 失败回调**,代码动工后通过 `alert.dlq` 写入)
- T-7 天通过 Webhook + 邮件双通道告警(详见 `docs/技术规格.md` § 9.1,本期未启用邮件通道,仅 Webhook)
- 客户运维每月例行检查:`docker exec chargepilot-caddy caddy tls list`(容器名以 P0-4 修正版为准),确认全部证书 ≥ 60 天有效期
- 演练:每季度一次模拟证书过期 → 验证 Runbook 路径

---

## 六、不可恢复情况

**证书完全过期** = 整个对外服务中断,无 ZeroSSL 备 CA 兜底 → 立即执行 4.1 + 4.2,**15 min 内必须恢复**。恢复后:

1. 写事故报告:`docs/incidents/cert-renew-YYYY-MM-DD.md`
2. 通知所有小程序用户:虽证书失效时 API 仍可用(Caddy 仍转发),但 PC 后台不可用
3. 等保测评需要的"证书管理"留痕:本次续期失败 + 修复记录归档
