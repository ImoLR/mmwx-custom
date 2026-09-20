#!/usr/bin/env bash
set -euo pipefail

REPO="${MMWXC_HELPER_REPO:-ImoLR/mmwx-custom}"
GITHUB_ACCELERATOR="${MMWXC_GITHUB_ACCELERATOR-https://ghfast.top/}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo) REPO="${2:-}"; shift 2 ;;
    --github-accelerator) GITHUB_ACCELERATOR="${2-}"; shift 2 ;;
    -h|--help)
      echo "Usage: update-helper.sh [--repo OWNER/REPO] [--github-accelerator URL]"
      exit 0
      ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
base="https://github.com/${REPO}/releases/latest/download"

normalize_accelerator() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  if [[ -z "$value" ]]; then printf ''; return 0; fi
  [[ "$value" =~ ^https://[^/?#[:space:]]+(/[^?#[:space:]]*)?$ ]] || {
    echo "GitHub accelerator must be an HTTPS URL without query or fragment" >&2
    return 1
  }
  printf '%s/' "${value%/}"
}

GITHUB_ACCELERATOR="$(normalize_accelerator "$GITHUB_ACCELERATOR")"

fetch_release_asset() {
  local name="$1" destination="$2" official="${base}/$1" accelerated
  if command -v curl >/dev/null 2>&1; then
    if [[ -n "$GITHUB_ACCELERATOR" ]]; then
      accelerated="${GITHUB_ACCELERATOR}${official}"
      if curl --fail --show-error --silent --location --connect-timeout 10 --max-time 900 -o "$destination" "$accelerated"; then
        return 0
      fi
      rm -f "$destination"
      echo "[mmwxc] Accelerator download failed; falling back to official GitHub" >&2
    fi
    curl --fail --show-error --silent --location --connect-timeout 10 --max-time 900 -o "$destination" "$official"
  elif command -v wget >/dev/null 2>&1; then
    if [[ -n "$GITHUB_ACCELERATOR" ]]; then
      accelerated="${GITHUB_ACCELERATOR}${official}"
      if wget -q --connect-timeout=10 --read-timeout=900 -O "$destination" "$accelerated"; then
        return 0
      fi
      rm -f "$destination"
      echo "[mmwxc] Accelerator download failed; falling back to official GitHub" >&2
    fi
    wget -q --connect-timeout=10 --read-timeout=900 -O "$destination" "$official"
  else
    echo "curl or wget is required" >&2
    return 1
  fi
}

fetch_release_asset checksums.txt "$tmp/checksums.txt"
fetch_release_asset install-helper.sh "$tmp/install-helper.sh"

expected="$(awk '$2=="install-helper.sh" || $2=="*install-helper.sh" {print $1; exit}' "$tmp/checksums.txt")"
actual="$(sha256sum "$tmp/install-helper.sh" | awk '{print $1}')"
[[ "$expected" =~ ^[0-9a-fA-F]{64}$ && "${actual,,}" == "${expected,,}" ]] || {
  echo "installer checksum verification failed" >&2
  exit 1
}

chmod 0755 "$tmp/install-helper.sh"
export MMWXC_HELPER_REPO="$REPO"
export MMWXC_GITHUB_ACCELERATOR="$GITHUB_ACCELERATOR"
exec "$tmp/install-helper.sh"
