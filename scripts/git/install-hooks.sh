#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/../.."
git config core.hooksPath .githooks
chmod +x .githooks/pre-commit
echo 'pre-commit enabled: make lint test (requires make and Go)'
