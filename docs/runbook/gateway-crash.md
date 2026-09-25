# Runbook:gateway 进程崩溃 / 设备全离线

> **引用**:`技术规格.md` § 3.1(TCP / MQTT 接入层) + § 10.2(资源基线)
> **触发场景**:gateway 容器崩溃 / OOM / 端口冲突 → 全部设备 TCP/MQTT 离线 → 充电业务暂停

---

## 一、症状

- admin PC 后台"设备列表"全显示 **离线**(红色)
- alert_stream 收到 `gateway_disconnected`(若失败模式有探针)
- 小程序扫码 → POST `/scan/resolve` 返回 200 但 `port_status` 卡在旧值
- 当前正在充电的订单:用户端轮询仍正常缓存值,但新订单无法启动(gateway 调不通)

---

## 二、立刻止血(5 min 内)

```bash
# 1. 看容器状态
docker ps | grep gateway

# 2. 看最近日志找崩溃原因
docker logs --tail=300 gateway 2>&1 | tee /tmp/gateway-crash.log

# 3. 重启(单客户单部署,kill 后自动起)
docker compose restart gateway

# 4. 等待 health 通过
timeout 30 bash -c 'until docker inspect --format="{{.State.Health.Status}}" gateway | grep -q healthy; do sleep 2; done'

# 5. 看设备重连进度(gateway /metrics 暴露 `mqtt_clients_active` / `tcp_connections_active`)
curl -s http://localhost:9100/metrics | grep -E 'tcp_connections_active|mqtt_clients_active'
# 期望:30-60 秒内回到崩溃前水平
```

---

## 三、根因排查

| 现象 | 根因 | 处理 |
| --- | --- | --- |
| 日志报 `connection count exceeded` | TCP 连接数超 `tcp.max_connections` 默认 10000 | 调大配置 / 排查是否有设备端重连风暴 |
| 日志报 `out of memory` | OOM,通常是消息洪峰或内存泄漏 | 短期重启;长期需要复现 + 加限流 |
| 日志报 `mysql: connection refused` | MySQL 健康但 gateway 无法连 | 检查 `DATABASE_URL` / MySQL 容器状态 |
| 日志无错误但容器反复重启 | `healthcheck` 配置不当 | 看 docker-compose healthcheck 配置 |
| 端口冲突(9100 / 1883 被别的进程占) | `netstat -tnlp \| grep 9100` 看占用 PID | 杀掉占用进程 |

---

## 四、设备侧影响范围评估

```bash
# 看过去 10 分钟内有多少设备重连成功
curl -s http://localhost:9100/metrics | grep gateway_device_reconnect_total

# 查充电中订单数(避免误操作中断用户)
docker exec mysql mysql -uroot -p$MYSQL_ROOT_PASSWORD \
  -e "SELECT COUNT(*) FROM user_db.charge_order WHERE status='charging';"

# 若有用户正在充电,告知客服 + 启动应急通知模板
```

---

## 五、应急通知模板(给客服团队微信用)

```
[gateway 故障应急] 2026-09-25 23:00-23:10
- 影响:扫码启动新充电 10 分钟内不可用
- 在充电用户:受影响设备数 X 台,Y 单订单
- 现状态:已恢复
- 客户如还在充电:不影响,继续按实时单价计费
- 退款:无需手动退款,billing 自动判 + 推自动退款
```

---

## 六、永久预防

- **资源监控**:`docker stats gateway` 看内存 / CPU 趋势,设 OOM 告警(memory > 80% 持续 5 min)
- **TCP 连接数上限**:根据客户设备规模 → admin PC 后台"系统设置"配 `tcp.max_connections`(默认 10000 ≈ 5000 设备预留 2 倍余量)
- **客户端断线重连**:协议层(`docs/技术规格.md` § 6.2)已经强制设备每 90s 心跳 + 自动重连
- **服务降级**:若 gateway 重启时间 > 30s,`workder` 自动 fallback:暂停非关键告警处理,只保留充电核心
- **客户报告机制**:PC 后台首页有"网关健康度"卡 → 客户客服自查
