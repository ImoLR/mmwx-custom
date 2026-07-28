#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="$ROOT_DIR/build"

cd "$ROOT_DIR/frontend"
if [ ! -d node_modules ]; then
  npm ci
fi
npm run build

cd "$ROOT_DIR"
mkdir -p "$OUTPUT_DIR"
go build -o "$OUTPUT_DIR/mmwx-custom-api" .

echo "Built:"
echo "  frontend/dist"
echo "  build/mmwx-custom-api"
