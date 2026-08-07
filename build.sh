#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="$ROOT_DIR/build"
BINARY_NAME="mmwx-custom"
HELPER_BINARY_NAME="mmwxc-helper"
GO_BIN="${GO_BIN:-$(command -v go || true)}"
if [ -z "$GO_BIN" ] && [ -x /usr/local/go/bin/go ]; then
  GO_BIN=/usr/local/go/bin/go
fi
if [ -z "$GO_BIN" ]; then
  echo "go binary not found" >&2
  exit 1
fi

cd "$ROOT_DIR/frontend"
if [ ! -d node_modules ]; then
  npm ci
fi
npm run build

cd "$ROOT_DIR"
mkdir -p "$OUTPUT_DIR"
"$GO_BIN" build -trimpath -o "$OUTPUT_DIR/$BINARY_NAME" .
"$GO_BIN" build -trimpath -o "$OUTPUT_DIR/$HELPER_BINARY_NAME" ./cmd/mmwxc-helper
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 "$GO_BIN" build -trimpath -o "$OUTPUT_DIR/$HELPER_BINARY_NAME-linux-amd64" ./cmd/mmwxc-helper
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 "$GO_BIN" build -trimpath -o "$OUTPUT_DIR/$HELPER_BINARY_NAME-linux-arm64" ./cmd/mmwxc-helper

echo "Built:"
echo "  frontend/dist"
echo "  build/$BINARY_NAME"
echo "  build/$HELPER_BINARY_NAME"
echo "  build/$HELPER_BINARY_NAME-linux-amd64"
echo "  build/$HELPER_BINARY_NAME-linux-arm64"
