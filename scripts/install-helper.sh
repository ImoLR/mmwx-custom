#!/usr/bin/env bash
set -euo pipefail

REPO="${MMWXC_HELPER_REPO:-ImoLR/mmwx-custom}"
API_URL="${MMWXC_HELPER_API_URL:-https://mmwxc.imgamer.top}"
SERVER_ID="${MMWXC_HELPER_SERVER_ID:-}"
TOKEN="${MMWXC_HELPER_TOKEN:-}"
INTERVAL="${MMWXC_HELPER_INTERVAL:-5s}"
BINARY_PATH=""

usage() {
  cat <<'EOF'
Usage:
  install-helper.sh [--server-id ID] [--token TOKEN] [--api-url URL] [--interval 5s]

Optional:
  --binary PATH        install a local mmwxc-helper binary instead of downloading from GitHub Release
  --repo OWNER/REPO    GitHub repo that publishes mmwxc-helper release assets
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --server-id) SERVER_ID="${2:-}"; shift 2 ;;
    --token) TOKEN="${2:-}"; shift 2 ;;
    --api-url) API_URL="${2:-}"; shift 2 ;;
    --interval) INTERVAL="${2:-}"; shift 2 ;;
    --binary) BINARY_PATH="${2:-}"; shift 2 ;;
    --repo) REPO="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "mmwxc-helper only supports Linux" >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

need_root() {
  if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
    echo "please run as root" >&2
    exit 1
  fi
}

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

read_config_value() {
  local key="$1"
  [[ -f /etc/mmwxc-helper.env ]] || return 1
  awk -F= -v k="$key" '$1==k {print substr($0, index($0,$2)); exit}' /etc/mmwxc-helper.env
}

need_root

if [[ -f /etc/mmwxc-helper.env ]]; then
  echo "Existing /etc/mmwxc-helper.env detected; preserving current server_id/token configuration."
else
  if [[ -z "$SERVER_ID" ]]; then
    read -r -p "Server ID: " SERVER_ID
  fi
  if [[ -z "$TOKEN" ]]; then
    read -r -s -p "Helper token: " TOKEN
    echo
  fi
  if [[ -z "$SERVER_ID" || -z "$TOKEN" ]]; then
    echo "server-id and token are required" >&2
    exit 1
  fi
fi

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

install -m 0755 "$tmp/mmwxc-helper" /usr/local/bin/mmwxc-helper

if [[ ! -f /etc/mmwxc-helper.env ]]; then
  umask 077
  cat >/etc/mmwxc-helper.env <<EOF
MMWXC_HELPER_API_URL=${API_URL}
MMWXC_HELPER_SERVER_ID=${SERVER_ID}
MMWXC_HELPER_TOKEN=${TOKEN}
MMWXC_HELPER_INTERVAL=${INTERVAL}
EOF
fi
chmod 600 /etc/mmwxc-helper.env

cat >/etc/systemd/system/mmwxc-helper.service <<'EOF'
[Unit]
Description=MMWXC Helper
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/mmwxc-helper.env
ExecStart=/usr/local/bin/mmwxc-helper
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now mmwxc-helper.service
systemctl is-active --quiet mmwxc-helper.service

echo "MMWXC Helper installed successfully"
echo "Service: $(systemctl is-active mmwxc-helper.service)"
echo "Server ID: $(read_config_value MMWXC_HELPER_SERVER_ID || true)"
echo "API: $(read_config_value MMWXC_HELPER_API_URL || true)"
echo "Version: $(/usr/local/bin/mmwxc-helper --version | awk '{print $2}')"
