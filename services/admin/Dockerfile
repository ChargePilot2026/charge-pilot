FROM rust:1.98-bookworm AS builder
WORKDIR /build
COPY Cargo.toml Cargo.lock ./
COPY crates crates
COPY services services
# Cargo.toml 的 members 含 tools/migrate-baseline,cargo 解析 workspace 时
# 会加载每一个 member 的 manifest —— 不 COPY tools，构建直接失败于
# "failed to load manifest for workspace member tools/migrate-baseline"。
COPY tools tools
# P4:sqlx::migrate! 是编译期宏,migrations/ 必须在构建期就位
COPY migrations migrations
COPY rust-toolchain.toml ./

RUN apt-get update && apt-get install -y --no-install-recommends pkg-config libssl-dev ca-certificates && rm -rf /var/lib/apt/lists/*

RUN cargo build --release -p admin

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl libssl3 tini && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/target/release/admin /app/admin

ENV SERVICE_NAME=admin RUST_LOG=info,sqlx=warn

EXPOSE 8082

# runtime 镜像必须装 curl:HEALTHCHECK 直接调它,debian:bookworm-slim 不自带,
# 缺失时探针恒为 exit 127(`curl: not found`),容器永远 unhealthy。
HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=5 \
    CMD curl -fsS http://localhost:8082/api/v1/health || exit 1

ENTRYPOINT ["/usr/bin/tini", "--", "/app/admin"]