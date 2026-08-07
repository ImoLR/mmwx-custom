#!/usr/bin/env bash
set -euo pipefail

REPO="${MMWXC_HELPER_REPO:-ImoLR/mmwx-custom}"
BINARY_PATH=""

usage() {
  cat <<'EOF'
Usage:
  update-helper.sh [--binary PATH] [--repo OWNER/REPO]
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary) BINARY_PATH="${2:-}"; shift 2 ;;
    --repo) REPO="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
  echo "please run as root" >&2
  exit 1
fi
if [[ "$(uname -s)" != "Linux" ]]; then
  echo "mmwxc-helper only supports Linux" >&2
  exit 1
fi
case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

download_helper() {
  local dest="$1"
  local url="https://github.com/${REPO}/releases/latest/download/mmwxc-helper-linux-${ARCH}"
  echo "Downloading mmwxc-helper (${ARCH})..."
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --connect-timeout 10 --max-time 180 -o "$dest" "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -q --connect-timeout=10 --read-timeout=180 -O "$dest" "$url"
  else
    echo "curl or wget is required" >&2
    exit 1
  fi
}

current_version="not-installed"
if [[ -x /usr/local/bin/mmwxc-helper ]]; then
  current_version="$(/usr/local/bin/mmwxc-helper --version 2>/dev/null | awk '{print $2}' || true)"
fi
echo "Current version: ${current_version:-unknown}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

if [[ -n "$BINARY_PATH" ]]; then
  if [[ ! -f "$BINARY_PATH" ]]; then
    echo "binary not found: $BINARY_PATH" >&2
    exit 1
  fi
  cp "$BINARY_PATH" "$tmp/mmwxc-helper"
else
  download_helper "$tmp/mmwxc-helper"
fi
chmod 755 "$tmp/mmwxc-helper"
"$tmp/mmwxc-helper" --version >/dev/null
new_version="$("$tmp/mmwxc-helper" --version | awk '{print $2}')"

ts="$(date +%Y%m%d-%H%M%S)"
backup="/usr/local/bin/mmwxc-helper.backup-${ts}"
if [[ -f /usr/local/bin/mmwxc-helper ]]; then
  cp -a /usr/local/bin/mmwxc-helper "$backup"
fi

install -m 0755 "$tmp/mmwxc-helper" /usr/local/bin/mmwxc-helper.new
mv -f /usr/local/bin/mmwxc-helper.new /usr/local/bin/mmwxc-helper

if ! systemctl restart mmwxc-helper.service || ! systemctl is-active --quiet mmwxc-helper.service; then
  echo "update failed; restoring previous binary" >&2
  if [[ -f "$backup" ]]; then
    install -m 0755 "$backup" /usr/local/bin/mmwxc-helper
    systemctl restart mmwxc-helper.service || true
  fi
  exit 1
fi

echo "MMWXC Helper updated successfully"
echo "Previous version: ${current_version:-unknown}"
echo "Current version: ${new_version}"
echo "Service: $(systemctl is-active mmwxc-helper.service)"
