#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
case "${1:-}" in
  gateway|central|worker)
    exec go run "./cmd/$1"
    ;;
  *)
    echo "usage: $0 gateway|central|worker" >&2
    exit 2
    ;;
esac
