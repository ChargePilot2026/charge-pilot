#!/bin/sh
set -eu

if [ "$SERVICE_NAME" = admin ]; then
    cargo run --locked -p admin --example seed_dev_admin
fi
exec cargo run --locked -p "$SERVICE_NAME" --bin "$SERVICE_NAME"
