#!/usr/bin/env bash
# ChargePilot 一键启动脚本
# 用法: bash scripts/start.sh
# 行为: 校验 .env → docker compose up -d → 等服务就绪

set -euo pipefail

cd "$(dirname "$0")/.."

# 1) .env 检查
if [[ ! -f .env ]]; then
  echo "[ERR] .env not found. 请先 cp .env.example .env 并填写真实值"
  exit 1
fi

# 2) JWT / SERVICE_TOKEN 检查
source .env
if [[ -z "${JWT_SECRET:-}" || -z "${SERVICE_TOKEN:-}" ]]; then
  echo "[ERR] JWT_SECRET 或 SERVICE_TOKEN 未设置"
  exit 1
fi

if [[ ${#JWT_SECRET} -lt 32 ]]; then
  echo "[ERR] JWT_SECRET 太短(应 ≥ 32 字符),请用: openssl rand -hex 32"
  exit 1
fi

# 3) 校验 docker compose 配置
echo "[1/5] 校验 docker-compose.yml ..."
docker compose config --quiet

# 4) 拉镜像
echo "[2/5] 拉镜像 ..."
docker compose pull mysql:8.4 redis:8-alpine caddy:2.11-alpine || true

# 5) 构建自定义镜像
echo "[3/5] 构建服务镜像 (gateway/user/admin/billing/worker) ..."
docker compose build --no-cache

# 6) 启动
echo "[4/5] 启动 ..."
docker compose up -d

# 7) 等健康检查
echo "[5/5] 等待服务就绪 ..."
for svc in chargepilot-mysql chargepilot-redis-cache chargepilot-redis-stream; do
  echo "  waiting for $svc ..."
  until docker compose ps "$svc" 2>/dev/null | grep -q "(healthy)"; do
    sleep 2
  done
done

echo ""
echo "==================================================="
echo " ChargePilot 启动成功"
echo " PC 后台:  http://localhost/admin/"
echo " API 文档: http://localhost/api/v1/health"
echo " (首次部署需要客户域名 + Let's Encrypt 证书)"
echo "==================================================="
echo ""
echo "查看日志:   docker compose logs -f"
echo "查看状态:   docker compose ps"
echo "停止:       docker compose down"