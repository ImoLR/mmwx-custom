#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CORE_SOURCE_DIR="${MMWXC_CORE_SOURCE_DIR:-$ROOT_DIR/../xray-core-vision-limiter}"
REPOSITORY="ImoLR/mmwx-custom"
TAG="${1:-}"

if [[ ! "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Usage: $0 vX.Y.Z" >&2
  exit 1
fi

if [[ -n "$(git -C "$ROOT_DIR" status --porcelain)" ]]; then
  echo "Working tree must be clean before creating release assets." >&2
  exit 1
fi

if [[ ! -f "$CORE_SOURCE_DIR/go.mod" ]] || [[ ! -d "$CORE_SOURCE_DIR/main" ]]; then
  echo "Custom Core source not found: $CORE_SOURCE_DIR" >&2
  exit 1
fi
if [[ -n "$(git -C "$CORE_SOURCE_DIR" status --porcelain)" ]]; then
  echo "Custom Core source tree must be clean before creating release assets." >&2
  exit 1
fi
if [[ "$(git -C "$CORE_SOURCE_DIR" branch --show-current)" != "custom-connection-control" ]]; then
  echo "Custom Core must be built from branch custom-connection-control." >&2
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

(
  cd "$ROOT_DIR"
  go test ./...
  go test -race ./...
)
bash -n "$ROOT_DIR"/scripts/*.sh
"$ROOT_DIR/scripts/install-helper-test.sh"

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
  (
    cd "$CORE_SOURCE_DIR"
    GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$RELEASE_DIR/mmwxc-core-linux-$arch" ./main
  )
  cp -R "$ROOT_DIR/frontend/dist" "$stage/frontend/dist"
  tar -C "$stage" -czf "$RELEASE_DIR/mmwx-custom-linux-$arch.tar.gz" mmwx-custom frontend
done

install -m 0755 "$ROOT_DIR/scripts/install-helper.sh" "$RELEASE_DIR/install-helper.sh"
cat >"$RELEASE_DIR/core-build-info.txt" <<EOF
repository=https://github.com/ImoLR/Xray-core-mmwx
branch=custom-connection-control
commit=$(git -C "$CORE_SOURCE_DIR" rev-parse HEAD)
EOF

pushd "$RELEASE_DIR" >/dev/null
sha256sum \
  mmwx-custom-linux-amd64.tar.gz \
  mmwx-custom-linux-arm64.tar.gz \
  mmwxc-helper-linux-amd64 \
  mmwxc-helper-linux-arm64 \
  mmwxc-core-linux-amd64 \
  mmwxc-core-linux-arm64 \
  install-helper.sh \
  core-build-info.txt > checksums.txt
popd >/dev/null

gh release create "$TAG" \
  --repo "$REPOSITORY" \
  --title "$TAG" \
  --generate-notes \
  "$RELEASE_DIR/mmwx-custom-linux-amd64.tar.gz" \
  "$RELEASE_DIR/mmwx-custom-linux-arm64.tar.gz" \
  "$RELEASE_DIR/mmwxc-helper-linux-amd64" \
  "$RELEASE_DIR/mmwxc-helper-linux-arm64" \
  "$RELEASE_DIR/mmwxc-core-linux-amd64" \
  "$RELEASE_DIR/mmwxc-core-linux-arm64" \
  "$RELEASE_DIR/install-helper.sh" \
  "$RELEASE_DIR/core-build-info.txt" \
  "$RELEASE_DIR/checksums.txt"
