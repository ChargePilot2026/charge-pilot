# Go 三服务网络拓扑

当前开发与生产配置分别以仓库根目录的 `compose.dev.yaml`、`docker-compose.yml` 为准；本目录的 Compose 和 Caddyfile 是供阅读的快照，部署时不要直接在 `examples/` 下运行。后端尚在重建，生产启动脚本保持关闭。

| 入口 | 目标 | 网络范围 |
| --- | --- | --- |
| 80/443 HTTPS | Caddy → `central:8080` | 公网，仅生产 |
| 9100/TCP | `gateway` 的 `dc589` 协议适配器 | 厂商设备网络 |
| 8083/HTTP | `gateway` 内部接口与健康检查 | 容器内网 |
| 8085/HTTP | `worker` 健康检查 | 容器内网 |
| MySQL 3306 | 五个独立 schema | 容器内网 |
| Redis Cache / Stream 6379 | 会话缓存、事件流 | 容器内网 |

`central` 同进程承载 user、admin、billing 三个逻辑域；这三个模块不再独立监听 8081/8082/8084。新增设备协议可在 `gateway` 使用新 TCP 端口，实际开放端口要同时更新 Compose 与防火墙。MQTT 未接入，不开放 1883。

当前代码只覆盖部分登录、设备上行与 Outbox 路径，支付、订单、运营和 OTA 实机能力未验收。详见 [Go 重建清单](../docs/migration/go-rebuild.md)。
