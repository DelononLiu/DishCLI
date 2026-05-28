#!/bin/bash
set -euo pipefail

VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS="-s -w"

mkdir -p dist

echo "Building dishcli v${VERSION}"

echo "  linux/amd64 ..."
GOOS=linux GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o dist/dishcli .

echo "  windows/amd64 ..."
GOOS=windows GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o dist/dishcli-windows-amd64.exe .

echo "Done"
ls -lh dist/
