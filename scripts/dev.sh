#!/usr/bin/env bash
# 本地开发模式启动:不依赖 docker,直接在 host 跑
# 前提: 1) 已启动 MySQL 8.4 + Redis 8; 2) 已创建 5 个 schema
# 用法: bash scripts/dev.sh [user|admin|gateway|billing|worker|all]

set -euo pipefail

cd "$(dirname "$0")/.."

target=${1:-user}
echo ">>> starting service: $target"

case $target in
    user|admin|gateway|billing|worker)
        cargo run -p $target
        ;;
    all)
        # 并行跑 5 个服务
        for svc in gateway user admin billing worker; do
            (cargo run -p $svc > /tmp/chargepilot-$svc.log 2>&1 &)
            echo "  started $svc (log: /tmp/chargepilot-$svc.log)"
        done
        wait
        ;;
    *)
        echo "用法: $0 [user|admin|gateway|billing|worker|all]"
        exit 1
        ;;
esac