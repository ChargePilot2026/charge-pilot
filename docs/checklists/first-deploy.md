# 首次部署 Checklist

> **引用**:`技术规格.md` § 10.3 / `docs/runbook/`(应急路径)
> **使用**:客户服务器首次部署,本表逐项勾选。

---

## 一、服务器准备

- [ ] 服务器合规:最低 4 核 8GB 100GB SSD(见 `技术规格.md` § 10.2)
- [ ] 公网 IP + 域名解析到位(`<customer-domain>`)
- [ ] 80 / 443 端口可对外(Let's Encrypt ACME HTTP-01 需要)
- [ ] 9100 / 1883 端口仅在内网 / 设备网段开放(不进公网!)

## 二、环境安装

- [ ] Docker ≥ 24 + Docker Compose ≥ 2.20 安装
- [ ] 创建工作目录 `/opt/chargepilot/`(或客户自选)
- [ ] 拷贝交付物:`docker-compose.yml` + `Caddyfile` + `.env.example`
- [ ] 复制 `.env.example` 为 `.env`,填数据库密码、`REDIS_PASSWORD`、**不同值的** `REDIS_STREAM_PASSWORD`、`JWT_SECRET`、服务令牌与微信支付凭证
- [ ] `chmod 600 .env`(权限隔离,不入 Git)
- [ ] 运行 `bash tools/check-deploy-config.sh`;退出码 `0` 才表示全部验证,`2` 表示缺少 Docker/Caddy 校验能力,需在部署机补跑

## 三、启动基础设施

- [ ] `docker compose up -d chargepilot-mysql chargepilot-redis-cache chargepilot-redis-stream`
- [ ] 等待 MySQL `healthy`(`docker ps` 看 STATUS)
- [ ] 检查 schema 列表:`docker exec chargepilot-mysql mysql -uroot -e "SHOW DATABASES;"` 应该看到 5 个业务 schema(需使用受限密钥方式提供密码)
- [ ] 自动 migration 验证:看 `user` 容器日志是否有 `applied migration` 提示

## 四、启动 5 个应用服务

- [ ] `docker compose up -d`(全部启动)
- [ ] `docker compose ps` 确认服务健康;8081/8082 仅容器内网开放,不要从宿主机直连
- [ ] 通过 Caddy HTTPS 路由验证应用 readiness,必要时在容器内网访问 `/health` / `/ready`
- [ ] Caddy 自动申请证书:日志看 `obtained certificate`
- [ ] 验证 HTTPS:`curl -v https://<customer-domain>/api/v1/public/auth/login -X POST` 应该 200 而非 SSL 错误

## 五、PC 后台首次配置

- [ ] 打开 `https://<customer-domain>/admin/login`
- [ ] 用交付时安全渠道提供的初始管理员凭证登录
- [ ] **立即修改密码** + 配置双因素(本期不支持,只改密码)
- [ ] 上传 Logo / 主题色 / 应用名称
- [ ] 配置首条公告
- [ ] 创建第一个客服坐席(微信 ID + 客服电话)
- [ ] 配置首条计费规则(电费 + 服务费;价费分离强制)

## 六、Smoke Test 冒烟用例

> 每条用真实数据跑通,失败必须排查修复(参见 `runbook/`)

- [ ] **Test 1:扫码 → 启动 → 结束 → 计费**
  - 用 test_appid + 真桩(模拟器 / 一台真桩)扫码
  - 看到 order_id 创建 + 微信支付回调 → 设备启动 → 用户结束 → 计费快照写入
- [ ] **Test 2:退款触发**
  - 模拟支付成功但设备启动失败 → billing 发布 `refund_required_stream` → admin 经 user 内部接口领取退款记录并执行退款;未支付的 60 秒内取消只关单,不退款
- [ ] **Test 3:Webhook 推送**
  - 在 admin 后台"Webhook 订阅"创建一条 → 触发任意告警 → 看接收方日志收到 HMAC 签名请求
- [ ] **Test 4:分账计算**
  - 配置 1 个分账模板(2-3 个参与方)→ 完成一单 → 看 settlement + settlement_party_amount 写入正确金额
- [ ] **Test 5:PC 后台权限**
  - 创建 2 个角色账号 → 用低权账号登录 → 应看不到"提现审核"菜单
- [ ] **Test 6:OTA 流程**(可选,如客户已有 1 台设备)
  - 上传测试固件包 → 选 1 台设备推 OTA → 设备收到 → 重启 → 上报 `ota_apply_result`

## 七、监控 / 告警连通

- [ ] `/metrics` 端点 200 OK:`curl http://<server>:8081/metrics`
- [ ] Webhook 订阅 cert_renew_failed:`docker exec caddy caddy tls list` 触发 T-7 告警(可改时间模拟)
- [ ] `/ready` 在 Redis / MySQL down 时返回 503(可 `docker stop redis` 测试)

## 八、备份验证

- [ ] 02:00 mysqldump cron 跑通 → `/var/backup/mysql/` 有文件
- [ ] binlog 在:`docker exec mysql ls /var/lib/mysql/mysql-bin.*`
- [ ] 异地同步跑通:任一 ali-OSS / scp 备用机验证
- [ ] 执行一次完整恢复演练 → 见 `runbook/backup-restore.md`

## 九、合规 / 安全

- [ ] 等保三级:`docs/checklists/equal-protection-l3.md` 跑通
- [ ] TLS 1.3 + 强加密套件:`nmap --script ssl-enum-ciphers <customer-domain>` 看输出
- [ ] HSTS:`curl -I https://<customer-domain>` 看 `Strict-Transport-Security` header
- [ ] JWT 密钥轮转演练:`技术规格.md` § 9.2 — 模拟改 JWT_SECRET → 重启 → 旧 token 失效

## 十、上线报告

- [ ] 在 admin 后台"系统日志"截图保留(部署完成时间)
- [ ] 通知客户运维:登录账号 / 密码 / 应急联系方式
- [ ] 客户运维回执签字 → 触发运维订阅合同起算

---

**部署日期**: ____________

**部署工程师**: ____________

**客户运维回执**: ____________
