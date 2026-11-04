#!/usr/bin/env bash
set -e
echo "======================================================================="
echo "  UniCompute Desktop Control Room (Linux: Ubuntu 18 / 20 / 24 LTS)"
echo "======================================================================="

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_PATH="$SCRIPT_DIR/bin/unicompute-gui-linux"

if [ -f "$BIN_PATH" ]; then
    echo "Running static ELF binary: $BIN_PATH"
    chmod +x "$BIN_PATH"
    exec "$BIN_PATH" "$@"
else
    echo "Compiled binary not found. Running via go toolchain..."
    exec go run ./cmd/unicompute-gui "$@"
fi
