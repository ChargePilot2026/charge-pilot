# 本地监控

三服务暴露 `/metrics`。本地 Prometheus 每 15 秒从 Compose 内网抓取，保留 7 天。告警只显示在 Prometheus 的 Alerts 页面；通知接收方待部署时配置。

```bash
docker compose -f compose.dev.yaml -f compose.monitoring.yaml up -d prometheus
docker compose -f compose.dev.yaml -f compose.monitoring.yaml exec prometheus \
  promtool check config /etc/prometheus/prometheus.yml
docker compose -f compose.dev.yaml -f compose.monitoring.yaml exec prometheus \
  promtool check rules /etc/prometheus/alerts.yml
docker run --rm --entrypoint /bin/promtool -v "$PWD/monitoring:/etc/prometheus:ro" \
  prom/prometheus:v3.15.0 test rules /etc/prometheus/alerts.test.yml
```

浏览器打开 `http://127.0.0.1:9090/targets`，确认 `gateway`、`central`、`worker` 为 UP；`/alerts` 可看规则与状态。容器内可用 `wget -qO- http://central:8080/metrics` 核对抓取来源。

本地基准阈值：抓取失败持续 1 分钟；五分钟内至少 20 个请求且 5xx 比例超过 5% 持续 5 分钟；五分钟内至少 20 个业务请求且 p95 超过 2 秒持续 5 分钟。监控自身抓取 `/metrics` 不计入延迟规则。上线前根据实际流量调阈值，并接入客户选择的 Alertmanager 通知渠道。

Prometheus 端口仅绑定本机回环地址；生产环境不能把 `/metrics` 和 9090 直接暴露给公网。本配置只用于本地开发栈，不修改生产切换门禁。
