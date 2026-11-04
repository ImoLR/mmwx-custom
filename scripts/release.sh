#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CORE_SOURCE_DIR="${MMWXC_CORE_SOURCE_DIR:-$ROOT_DIR/../xray-core-vision-limiter}"
REPOSITORY="ImoLR/mmwx-custom"
TAG="${1:-}"
# MMWXC_EXTERNAL_CORE_FROM=vX.Y.Z keeps the Core offered to External servers
# byte-identical to that release (assets re-published after their checksums
# are verified) and ships the Core built from CORE_SOURCE_DIR as the separate
# FLVX shared Core channel (mmwxc-flvx-core-linux-*, flvx_core_version).
# Unset: one Core for both, as before.
EXTERNAL_CORE_FROM="${MMWXC_EXTERNAL_CORE_FROM:-}"
if [[ -n "$EXTERNAL_CORE_FROM" && ! "$EXTERNAL_CORE_FROM" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "MMWXC_EXTERNAL_CORE_FROM must be a release tag vX.Y.Z" >&2
  exit 1
fi

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

if [[ -n "$EXTERNAL_CORE_FROM" ]]; then
  HELD_DIR="$TEMP_DIR/held-core"
  mkdir -p "$HELD_DIR"
  gh release download "$EXTERNAL_CORE_FROM" --repo "$REPOSITORY" --dir "$HELD_DIR" \
    --pattern 'mmwxc-core-linux-*' --pattern checksums.txt --pattern component-versions.json --pattern core-build-info.txt
  (
    cd "$HELD_DIR"
    grep -E '  (mmwxc-core-linux-amd64|mmwxc-core-linux-arm64)$' checksums.txt >held.sha256
    [[ "$(wc -l <held.sha256)" == 2 ]] || { echo "Held Core checksums missing in $EXTERNAL_CORE_FROM" >&2; exit 1; }
    sha256sum -c held.sha256
  )
  EXTERNAL_CORE_VERSION="$(sed -n 's/.*"core_version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$HELD_DIR/component-versions.json")"
  EXTERNAL_CORE_COMMIT="$(sed -n 's/^commit=//p' "$HELD_DIR/core-build-info.txt")"
  if [[ ! "$EXTERNAL_CORE_VERSION" =~ ^[0-9a-f]{7}$ ]] || [[ ! "$EXTERNAL_CORE_COMMIT" =~ ^[0-9a-f]{40}$ ]]; then
    echo "Cannot read the held Core version from $EXTERNAL_CORE_FROM" >&2
    exit 1
  fi
fi

pushd "$ROOT_DIR/frontend" >/dev/null
npm ci
npm run build
popd >/dev/null

for arch in amd64 arm64; do
  stage="$TEMP_DIR/mmwx-custom-linux-$arch"
  mkdir -p "$stage/frontend"
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.customVersion=$TAG" -o "$stage/mmwx-custom" "$ROOT_DIR"
  GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$RELEASE_DIR/mmwxc-helper-linux-$arch" "$ROOT_DIR/cmd/mmwxc-helper"
  core_name="mmwxc-core-linux-$arch"
  if [[ -n "$EXTERNAL_CORE_FROM" ]]; then
    core_name="mmwxc-flvx-core-linux-$arch"
    install -m 0755 "$HELD_DIR/mmwxc-core-linux-$arch" "$RELEASE_DIR/mmwxc-core-linux-$arch"
  fi
  (
    cd "$CORE_SOURCE_DIR"
    GOOS=linux GOARCH="$arch" CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$RELEASE_DIR/$core_name" ./main
  )
  cp -R "$ROOT_DIR/frontend/dist" "$stage/frontend/dist"
  tar -C "$stage" -czf "$RELEASE_DIR/mmwx-custom-linux-$arch.tar.gz" mmwx-custom frontend
done

install -m 0755 "$ROOT_DIR/scripts/install-helper.sh" "$RELEASE_DIR/install-helper.sh"
HELPER_VERSION="$(sed -n 's/^[[:space:]]*helperVersion[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$ROOT_DIR/cmd/mmwxc-helper/main.go")"
if [[ ! "$HELPER_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Unable to read Helper version from cmd/mmwxc-helper/main.go." >&2
  exit 1
fi
FLVX_ASSETS=()
if [[ -n "$EXTERNAL_CORE_FROM" ]]; then
  cat >"$RELEASE_DIR/core-build-info.txt" <<EOF
repository=https://github.com/ImoLR/Xray-core-mmwx
branch=custom-connection-control
commit=$EXTERNAL_CORE_COMMIT
held_from=$EXTERNAL_CORE_FROM
flvx_commit=$(git -C "$CORE_SOURCE_DIR" rev-parse HEAD)
EOF
  cat >"$RELEASE_DIR/component-versions.json" <<EOF
{
  "custom_version": "$TAG",
  "helper_version": "$HELPER_VERSION",
  "core_version": "$EXTERNAL_CORE_VERSION",
  "flvx_core_version": "$(git -C "$CORE_SOURCE_DIR" rev-parse --short=7 HEAD)"
}
EOF
  FLVX_ASSETS=(mmwxc-flvx-core-linux-amd64 mmwxc-flvx-core-linux-arm64)
else
  cat >"$RELEASE_DIR/core-build-info.txt" <<EOF
repository=https://github.com/ImoLR/Xray-core-mmwx
branch=custom-connection-control
commit=$(git -C "$CORE_SOURCE_DIR" rev-parse HEAD)
EOF
  cat >"$RELEASE_DIR/component-versions.json" <<EOF
{
  "custom_version": "$TAG",
  "helper_version": "$HELPER_VERSION",
  "core_version": "$(git -C "$CORE_SOURCE_DIR" rev-parse --short=7 HEAD)"
}
EOF
fi

pushd "$RELEASE_DIR" >/dev/null
sha256sum \
  mmwx-custom-linux-amd64.tar.gz \
  mmwx-custom-linux-arm64.tar.gz \
  mmwxc-helper-linux-amd64 \
  mmwxc-helper-linux-arm64 \
  mmwxc-core-linux-amd64 \
  mmwxc-core-linux-arm64 \
  "${FLVX_ASSETS[@]}" \
  install-helper.sh \
  core-build-info.txt \
  component-versions.json > checksums.txt
popd >/dev/null

# MMWXC_RELEASE_DRAFT=1 publishes a draft: it is not "latest" (which every
# Custom's release cache follows) until `gh release edit "$TAG" --draft=false`.
DRAFT_FLAG=()
if [[ "${MMWXC_RELEASE_DRAFT:-}" == "1" ]]; then
  DRAFT_FLAG=(--draft)
fi

gh release create "$TAG" \
  --repo "$REPOSITORY" \
  --title "$TAG" \
  --generate-notes \
  "${DRAFT_FLAG[@]}" \
  "$RELEASE_DIR/mmwx-custom-linux-amd64.tar.gz" \
  "$RELEASE_DIR/mmwx-custom-linux-arm64.tar.gz" \
  "$RELEASE_DIR/mmwxc-helper-linux-amd64" \
  "$RELEASE_DIR/mmwxc-helper-linux-arm64" \
  "$RELEASE_DIR/mmwxc-core-linux-amd64" \
  "$RELEASE_DIR/mmwxc-core-linux-arm64" \
  "${FLVX_ASSETS[@]/#/$RELEASE_DIR/}" \
  "$RELEASE_DIR/install-helper.sh" \
  "$RELEASE_DIR/core-build-info.txt" \
  "$RELEASE_DIR/component-versions.json" \
  "$RELEASE_DIR/checksums.txt"
