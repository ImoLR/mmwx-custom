#!/usr/bin/env bash
set -euo pipefail

REPO="${MMWXC_HELPER_REPO:-ImoLR/mmwx-custom}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo) REPO="${2:-}"; shift 2 ;;
    -h|--help)
      echo "Usage: update-helper.sh [--repo OWNER/REPO]"
      exit 0
      ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
base="https://github.com/${REPO}/releases/latest/download"

if command -v curl >/dev/null 2>&1; then
  curl -fsSL --connect-timeout 10 --max-time 180 -o "$tmp/checksums.txt" "$base/checksums.txt"
  curl -fsSL --connect-timeout 10 --max-time 180 -o "$tmp/install-helper.sh" "$base/install-helper.sh"
elif command -v wget >/dev/null 2>&1; then
  wget -q --connect-timeout=10 --read-timeout=180 -O "$tmp/checksums.txt" "$base/checksums.txt"
  wget -q --connect-timeout=10 --read-timeout=180 -O "$tmp/install-helper.sh" "$base/install-helper.sh"
else
  echo "curl or wget is required" >&2
  exit 1
fi

expected="$(awk '$2=="install-helper.sh" || $2=="*install-helper.sh" {print $1; exit}' "$tmp/checksums.txt")"
actual="$(sha256sum "$tmp/install-helper.sh" | awk '{print $1}')"
[[ "$expected" =~ ^[0-9a-fA-F]{64}$ && "${actual,,}" == "${expected,,}" ]] || {
  echo "installer checksum verification failed" >&2
  exit 1
}

chmod 0755 "$tmp/install-helper.sh"
MMWXC_HELPER_REPO="$REPO" exec "$tmp/install-helper.sh"
