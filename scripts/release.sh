#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPOSITORY="ImoLR/mmwx-custom"
TAG="${1:-}"

if [[ ! "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Usage: $0 vX.Y.Z" >&2
  exit 1
fi

if ! git -C "$ROOT_DIR" diff --quiet || ! git -C "$ROOT_DIR" diff --cached --quiet; then
  echo "Working tree must be clean before creating release assets." >&2
  exit 1
fi

if ! git -C "$ROOT_DIR" rev-parse "$TAG" >/dev/null 2>&1; then
  echo "Tag $TAG must exist locally before publishing." >&2
  exit 1
fi

if ! git -C "$ROOT_DIR" ls-remote --exit-code --tags origin "refs/tags/$TAG" >/dev/null; then
  echo "Tag $TAG must be pushed to origin before publishing." >&2
  exit 1
fi

if gh release view "$TAG" --repo "$REPOSITORY" >/dev/null 2>&1; then
  echo "Release $TAG already exists in $REPOSITORY." >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  if [ -x /usr/local/go/bin/go ]; then
    export PATH="/usr/local/go/bin:$PATH"
  fi
fi

for command in go npm tar sha256sum gh; do
  command -v "$command" >/dev/null || {
    echo "Missing required command: $command" >&2
    exit 1
  }
done

TEMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TEMP_DIR"' EXIT
RELEASE_DIR="$TEMP_DIR/release"
mkdir -p "$RELEASE_DIR"

pushd "$ROOT_DIR/frontend" >/dev/null
npm ci
npm run build
popd >/dev/null

for arch in amd64 arm64; do
  stage="$TEMP_DIR/mmwx-custom-linux-$arch"
  mkdir -p "$stage/frontend"
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$stage/mmwx-custom" "$ROOT_DIR"
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$RELEASE_DIR/mmwxc-helper-linux-$arch" "$ROOT_DIR/cmd/mmwxc-helper"
  cp -R "$ROOT_DIR/frontend/dist" "$stage/frontend/dist"
  tar -C "$stage" -czf "$RELEASE_DIR/mmwx-custom-linux-$arch.tar.gz" mmwx-custom frontend
done

pushd "$RELEASE_DIR" >/dev/null
sha256sum \
  mmwx-custom-linux-amd64.tar.gz \
  mmwx-custom-linux-arm64.tar.gz \
  mmwxc-helper-linux-amd64 \
  mmwxc-helper-linux-arm64 > checksums.txt
popd >/dev/null

gh release create "$TAG" \
  --repo "$REPOSITORY" \
  --title "$TAG" \
  --generate-notes \
  "$RELEASE_DIR/mmwx-custom-linux-amd64.tar.gz" \
  "$RELEASE_DIR/mmwx-custom-linux-arm64.tar.gz" \
  "$RELEASE_DIR/mmwxc-helper-linux-amd64" \
  "$RELEASE_DIR/mmwxc-helper-linux-arm64" \
  "$RELEASE_DIR/checksums.txt"
