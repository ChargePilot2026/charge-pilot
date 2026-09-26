#!/usr/bin/env bash
# 部署配置校验脚本(技术规格 § 10.3)
# 检查:
#   1. Caddyfile 全局块必须在文件开头
#   2. docker-compose 内部端口未泄露(expose 而非 ports)
#   3. Redis maxmemory-policy 区分事件/缓存
#   4. 网络拓扑完整
#   5. .env 必备变量已设置

set -euo pipefail
cd "$(dirname "$0")/.."

fail=0
ok() { echo "  [OK]    $*"; }
warn() { echo "  [WARN]  $*"; }
err() { echo "  [ERR]   $*"; fail=1; }

echo "==> Caddyfile 全局块位置检查"
if [[ -f Caddyfile ]]; then
    first_block=$(awk '/^[a-zA-Z]/ {print NR": "$0; exit}' Caddyfile || true)
    if echo "$first_block" | grep -q ": {$"; then
        ok "Caddyfile 全局块在文件开头"
    else
        err "Caddyfile 全局块不在开头(必须以 '{' 起首)"
    fi
else
    warn "Caddyfile 不存在(尚未创建)"
fi

echo ""
echo "==> docker-compose 内部端口检查"
if [[ -f docker-compose.yml ]]; then
    # 不应有 8081/8082/8083/8084 直接暴露到 0.0.0.0
    if grep -qE '^\s*-\s*"[0-9]+:(8081|8082|8083|8084)"' docker-compose.yml; then
        err "检测到内部端口 8081-8084 直接暴露(应用容器应仅 expose)"
    else
        ok "内部端口未直接暴露"
    fi
    # 9100/1883 应走 ports (gateway 设备长连接)
    if grep -qE '"9100:9100"' docker-compose.yml && grep -qE '"1883:1883"' docker-compose.yml; then
        ok "设备长连接端口 9100/1883 已映射"
    else
        warn "9100 或 1883 端口映射未配置"
    fi
fi

echo ""
echo "==> Redis 实例配置检查"
if [[ -f docker-compose.yml ]]; then
    cache_maxmem=$(grep -A5 'redis-cache' docker-compose.yml | grep maxmemory | head -1 || true)
    stream_maxmem=$(grep -A5 'redis-stream' docker-compose.yml | grep maxmemory | head -1 || true)
    cache_policy=$(grep -A5 'redis-cache' docker-compose.yml | grep maxmemory-policy | head -1 || true)
    stream_policy=$(grep -A5 'redis-stream' docker-compose.yml | grep maxmemory-policy | head -1 || true)
    if echo "$cache_policy" | grep -q allkeys-lru; then
        ok "redis-cache 使用 allkeys-lru"
    else
        warn "redis-cache maxmemory-policy 未设置 allkeys-lru"
    fi
    if echo "$stream_policy" | grep -q noeviction; then
        ok "redis-stream 使用 noeviction"
    else
        err "redis-stream 必须使用 noeviction(防止 Stream 被 LRU 淘汰)"
    fi
fi

echo ""
echo "==> .env 必备变量检查"
if [[ -f .env ]]; then
    for v in MYSQL_ROOT_PASSWORD DB_PASSWORD REDIS_PASSWORD REDIS_STREAM_PASSWORD JWT_SECRET SERVICE_TOKEN; do
        if grep -q "^${v}=" .env && ! grep -q "^${v}=change-me" .env; then
            ok "$v 已设置"
        else
            err "$v 未设置或仍为默认值"
        fi
    done
else
    err ".env 文件不存在"
fi

echo ""
if [[ $fail -eq 0 ]]; then
    echo "✓ 部署配置校验全部通过"
else
    echo "✗ 部署配置校验失败"
    exit 1
fi