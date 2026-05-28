#!/bin/bash
set -euo pipefail

VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS="-s -w"

mkdir -p dist

echo "Building dish v${VERSION}"

echo "  linux/amd64 ..."
GOOS=linux GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o dist/dish ./cmd/dish

echo "  windows/amd64 ..."
GOOS=windows GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o dist/dish-windows-amd64.exe ./cmd/dish

echo "Done"
ls -lh dist/
