FROM rust:1.80-bookworm AS builder
WORKDIR /build

COPY Cargo.toml Cargo.lock ./
COPY crates crates
COPY services services
COPY rust-toolchain.toml ./

RUN apt-get update && apt-get install -y --no-install-recommends \
    pkg-config libssl-dev ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN cargo build --release -p gateway

# ---- runtime ----
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates libssl3 tini \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/target/release/gateway /app/gateway

ENV SERVICE_NAME=gateway \
    RUST_LOG=info,sqlx=warn \
    TOKIO_WORKER_THREADS=2

EXPOSE 8083 9100 1883

HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=5 \
    CMD curl -fsS http://localhost:8083/api/v1/health || exit 1

ENTRYPOINT ["/usr/bin/tini", "--", "/app/gateway"]