#!/usr/bin/env bash
set -euo pipefail

echo "Go backend migration is in progress; production rollout is blocked." >&2
echo "Complete docs/migration/go-rebuild.md acceptance and external payment/device tests before enabling this script." >&2
exit 1
