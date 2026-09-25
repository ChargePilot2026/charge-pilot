#!/usr/bin/env bash
# =============================================================================
# ChargePilot 部署配置校验脚本
# =============================================================================
# 触发:CI / 首次部署前 / docker-compose / Caddyfile 变更后
# 退出码:0=全通过 / 1=有错误
# =============================================================================

set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
COMPOSE="$ROOT/examples/docker-compose.yml"
CADDYFILE="$ROOT/examples/Caddyfile"

FAILED=0
WARNINGS=0

ok()      { printf "  \033[32m✓\033[0m %s\n" "$1"; }
err()     { printf "  \033[31m✗\033[0m %s\n" "$1"; FAILED=$((FAILED+1)); }
warn()    { printf "  \033[33m!\033[0m %s\n" "$1"; WARNINGS=$((WARNINGS+1)); }
section() { printf "\n\033[1m[%s]\033[0m\n" "$1"; }

# ---------- 1. 文件存在 ----------
section "1. 配置文件存在"
[ -f "$COMPOSE" ]   && ok "docker-compose.yml 存在"   || { err "docker-compose.yml 不存在: $COMPOSE"; exit 1; }
[ -f "$CADDYFILE" ] && ok "Caddyfile 存在"            || err "Caddyfile 不存在: $CADDYFILE"

# ---------- 2. Caddyfile 全局块位置 ----------
section "2. Caddyfile 全局块必须在文件开头"
# Caddy 要求全局块 `{ ... }` 在文件开头(在任何 site block 之前)
FIRST_SITE_LINE=$(grep -nE '^[a-zA-Z0-9._:-]+\s*\{' "$CADDYFILE" | head -n1 | cut -d: -f1)
FIRST_GLOBAL_OPEN=$(grep -n '^[[:space:]]*{' "$CADDYFILE" | head -n1 | cut -d: -f1)
if [ -z "$FIRST_SITE_LINE" ]; then
  warn "未找到 site block"
elif [ -z "$FIRST_GLOBAL_OPEN" ]; then
  err "Caddyfile 缺少全局块(必须以 `{ ... }` 开头)"
elif [ "$FIRST_GLOBAL_OPEN" -gt "$FIRST_SITE_LINE" ]; then
  err "Caddyfile 全局块(line $FIRST_GLOBAL_OPEN)位置错误:必须在 site block(line $FIRST_SITE_LINE)之前"
else
  ok "Caddyfile 全局块在第 $FIRST_GLOBAL_OPEN 行(在 site block 之前)"
fi

# ---------- 3. Caddyfile 校验(若 caddy 可用)----------
section "3. Caddy adapt 校验"
if command -v caddy >/dev/null 2>&1; then
  caddy adapt --config "$CADDYFILE" --validate >/dev/null 2>&1 \
    && ok "caddy adapt --validate 通过" \
    || err "caddy adapt 失败(运行 'caddy adapt --config $CADDYFILE' 查看细节)"
else
  warn "未检测到 caddy CLI,跳过(运行 'docker run --rm -v $CADDYFILE:/etc/caddy/Caddyfile:ro caddy:2.11 caddy adapt --config /etc/caddy/Caddyfile --validate' 自检)"
fi

# ---------- 4. docker-compose 内部端口未泄露 ----------
section "4. docker-compose 内部端口不暴露"
INTERNAL_PORTS=(8081 8082 8083 8084)
for port in "${INTERNAL_PORTS[@]}"; do
  # 扫描 `ports:` 段是否包含宿主机映射的内部端口
  # 提取 ports 块(注意:expose 段不暴露,合法)
  PORT_LINES=$(awk '/^[[:space:]]*ports:/{p=1; next} p && /^[[:space:]]*-/{print} p && /^[[:space:]]*[a-z]/{p=0}' "$COMPOSE")
  if echo "$PORT_LINES" | grep -qE "\"[0-9]+:${port}\"|\"${port}:${port}\""; then
    err "内部端口 ${port} 被错误暴露到宿主机(docker-compose.yml 的 ports 段)"
  else
    ok "内部端口 ${port} 仅 expose,不暴露公网"
  fi
done

# ---------- 5. 端口映射白名单(9100/1883/80/443 允许)----------
section "5. 端口映射白名单"
WHITELIST_PORTS=(80 443 9100 1883)
ALLOWED=$(awk '/^[[:space:]]*ports:/{p=1; next} p && /^[[:space:]]*-/{print} p && /^[[:space:]]*[a-z]/{p=0}' "$COMPOSE" \
  | grep -oE '"[0-9]+:[0-9]+"' | sort -u)
UNEXPECTED=""
for entry in $ALLOWED; do
  host_port=$(echo "$entry" | sed -E 's/"([0-9]+):[0-9]+"/\1/')
  if [[ " ${WHITELIST_PORTS[*]} " != *" ${host_port} "* ]]; then
    UNEXPECTED="$UNEXPECTED $host_port"
  fi
done
if [ -z "$UNEXPECTED" ]; then
  ok "所有暴露端口在白名单内(80/443/9100/1883)"
else
  err "未在白名单的暴露端口:$UNEXPECTED"
fi

# ---------- 6. Redis maxmemory-policy 区分 ----------
section "6. Redis 缓存 / Stream 内存策略分离"
CACHE_POLICY=$(awk '/chargepilot-redis-cache/,/^  [a-z]/' "$COMPOSE" | grep -E 'maxmemory-policy' || true)
STREAM_POLICY=$(awk '/chargepilot-redis-stream/,/^  [a-z]/' "$COMPOSE" | grep -E 'maxmemory-policy' || true)
if echo "$CACHE_POLICY" | grep -q 'allkeys-lru'; then
  ok "redis-cache 内存策略 = allkeys-lru(可淘汰)"
else
  err "redis-cache 内存策略应为 allkeys-lru,实际: $CACHE_POLICY"
fi
if echo "$STREAM_POLICY" | grep -q 'noeviction'; then
  ok "redis-stream 内存策略 = noeviction(避免事件被 LRU 淘汰)"
else
  err "redis-stream 内存策略应为 noeviction,实际: $STREAM_POLICY"
fi

# ---------- 7. MySQL binlog 启用 ----------
section "7. MySQL binlog 启用(RPO ≤ 1h 依赖)"
if grep -A 30 '^  chargepilot-mysql:' "$COMPOSE" | grep -qE '\-\-log-bin='; then
  ok "MySQL 已启用 binlog(RPO 依赖)"
else
  err "MySQL 未配置 --log-bin,RPO > 24h(违反 P0-5 备份承诺)"
fi

# ---------- 8. 容器命名规范 ----------
section "8. 容器命名统一 chargepilot-* 前缀"
# 提取 `services:` 块下所有顶级服务名(2 空格缩进)
SERVICES=$(awk '
  /^services:$/ { in_services=1; next }
  in_services && /^  [a-z]/ && !/^    / {
    line=$0; sub(/^  /,"",line); sub(/:$/,"",line);
    if (line !~ /^x-/) print line
  }
  in_services && /^[^ ]/ { in_services=0 }
' "$COMPOSE")
for svc in $SERVICES; do
  CN=$(awk "/^  ${svc}:/{flag=1; next} flag && /^    container_name:/{print \$2; exit} flag && /^  [a-z]/{flag=0}" "$COMPOSE")
  if [ -z "$CN" ]; then
    err "服务 '$svc' 缺少 container_name"
  elif [[ "$CN" != chargepilot-* ]]; then
    err "服务 '$svc' 容器名 '$CN' 不符合 chargepilot-* 前缀"
  else
    ok "服务 '$svc' 容器名 = $CN"
  fi
done

# ---------- 9. docker-compose config 校验 ----------
section "9. docker compose config --quiet 校验"
if command -v docker >/dev/null 2>&1; then
  docker compose -f "$COMPOSE" config --quiet >/dev/null 2>&1 \
    && ok "docker compose config 语法通过" \
    || err "docker compose config 失败(运行 'docker compose -f $COMPOSE config' 查看细节)"
else
  warn "未检测到 docker CLI,跳过"
fi

# ---------- 总结 ----------
echo ""
echo "=========================================="
if [ $FAILED -eq 0 ]; then
  printf "\033[32m✓ 全部通过(%d 警告)\033[0m\n" "$WARNINGS"
  exit 0
else
  printf "\033[31m✗ %d 处错误,%d 处警告\033[0m\n" "$FAILED" "$WARNINGS"
  exit 1
fi