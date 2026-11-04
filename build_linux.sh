#!/usr/bin/env bash
set -e

echo -e "\033[36mCompiling static CGO-free UniCompute binaries for Linux (Ubuntu 18.04, 20.04, 24.04 LTS)...\033[0m"

mkdir -p bin

# Compile static binaries bypassing host glibc version locks (runs on Glibc 2.27, 2.31, 2.39+)
CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/unicompute-node ./cmd/unicompute-node
CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/unicompute-coordinator ./cmd/unicompute-coordinator
CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/unicompute-relay ./cmd/unicompute-relay
CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/verify-obj3 ./cmd/verify-obj3
CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/unicompute-gui ./cmd/unicompute-gui

chmod +x bin/*

echo -e "\033[32m[SUCCESS] Static Linux binaries compiled successfully in ./bin/\033[0m"
echo -e "\033[33mTo run Desktop GUI on Linux with a display: ./bin/unicompute-gui\033[0m"
echo -e "\033[33mTo run Desktop GUI in headless server mode: ./bin/unicompute-gui -headless -listen 0.0.0.0\033[0m"
