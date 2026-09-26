#!/bin/sh
set -eu

case "${SERVICE_NAME:-}" in
    gateway|user|admin|billing|worker) ;;
    *) echo "Invalid SERVICE_NAME" >&2; exit 1 ;;
esac

# Keep Linux build artifacts off the Windows bind mount, with no build lock
# shared between services. Registry downloads are cached in a separate volume.
export CARGO_TARGET_DIR="/cargo-target/$SERVICE_NAME"
exec watchexec --restart --poll=1s --debounce=500ms \
    --stop-signal=SIGTERM --stop-timeout=5s --shell=none \
    --watch "services/$SERVICE_NAME" --watch services/api-contracts \
    --watch crates --watch Cargo.toml --watch Cargo.lock \
    --watch rust-toolchain.toml --watch docker/dev/run-rust.sh \
    -- sh /workspace/docker/dev/run-rust.sh
