#!/usr/bin/env bash

set -Eeuo pipefail

readonly APP_DIR=/app
readonly WORK_DIR="${AMDL_WORK_DIR:-$APP_DIR/working}"
readonly DATA_DIR="${AMDL_DATA_DIR:-$APP_DIR/data}"
readonly DOWNLOAD_DIR="${AMDL_DOWNLOAD_DIR:-$APP_DIR/downloads}"
readonly SERVER_PORT="${AMDL_SERVER_PORT:-8080}"

mkdir -p "$WORK_DIR" "$DATA_DIR" "$DOWNLOAD_DIR"

# The Go service owns the Wrapper lifecycle.  It starts the process with an
# argv array, relays sanitized logs to stdout, probes its port and performs the
# bounded restart/2FA handling.  Keeping this shell as a direct exec prevents
# a second Wrapper supervisor from competing for the same state and port.
printf '[server] Starting downloader on port %s...\n' "$SERVER_PORT"
cd "$WORK_DIR"
exec "$APP_DIR/downloader" serve --port "$SERVER_PORT" --static-dir "$APP_DIR/src/renderer"
