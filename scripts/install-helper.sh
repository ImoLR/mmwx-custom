#!/usr/bin/env bash
set -euo pipefail

REPO="${MMWXC_HELPER_REPO:-ImoLR/mmwx-custom}"
API_URL="${MMWXC_HELPER_API_URL:-https://mmwxc.imgamer.top}"
SERVER_ID="${MMWXC_HELPER_SERVER_ID:-}"
TOKEN="${MMWXC_HELPER_TOKEN:-}"
INTERVAL="${MMWXC_HELPER_INTERVAL:-5s}"
RELEASE_TAG="${MMWXC_RELEASE_TAG:-latest}"
ASSET_DIR="${MMWXC_ASSET_DIR:-}"
ROOT_PREFIX="${MMWXC_INSTALL_ROOT:-}"
SYSTEMCTL="${MMWXC_SYSTEMCTL:-systemctl}"
INSTALL_STARTED_AT="$(date +%s)"

log_step() {
  printf '[mmwxc] %s\n' "$1"
}

log_step "Custom Agent installer started"

usage() {
  cat <<'EOF'
Usage:
  install-helper.sh [--server-id ID] [--token TOKEN] [--api-url URL]

The same installer upgrades an existing Helper in place or performs a fresh
installation when registration values are supplied by a one-time install URL.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --server-id) SERVER_ID="${2:-}"; shift 2 ;;
    --token) TOKEN="${2:-}"; shift 2 ;;
    --api-url) API_URL="${2:-}"; shift 2 ;;
    --interval) INTERVAL="${2:-}"; shift 2 ;;
    --repo) REPO="${2:-}"; shift 2 ;;
    --release-tag) RELEASE_TAG="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ "$(uname -s)" != "Linux" ]]; then echo "mmwxc-helper only supports Linux" >&2; exit 1; fi
if [[ "${EUID:-$(id -u)}" -ne 0 && -z "$ROOT_PREFIX" ]]; then echo "please run as root" >&2; exit 1; fi
case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
log_step "Detected Linux/$ARCH"

HELPER_BINARY="${ROOT_PREFIX}/usr/local/bin/mmwxc-helper"
HELPER_CONFIG="${ROOT_PREFIX}/etc/mmwxc-helper.env"
HELPER_UNIT="${ROOT_PREFIX}/etc/systemd/system/mmwxc-helper.service"
HELPER_STATE="${ROOT_PREFIX}/var/lib/mmwxc-helper/state.json"
CORE_BINARY="${ROOT_PREFIX}/opt/mmwxc/core/xray"
CORE_CONFIG="${ROOT_PREFIX}/etc/mmwxc/core/config.json"
CORE_UNIT="${ROOT_PREFIX}/etc/systemd/system/mmwxc-core.service"
ROLLBACK_ROOT="${ROOT_PREFIX}/var/lib/mmwxc/rollback"
STAGING_ROOT="${ROOT_PREFIX}/var/lib/mmwxc/staging"

mkdir -p "$STAGING_ROOT"
chmod 700 "$STAGING_ROOT"
tmp="$(mktemp -d "$STAGING_ROOT/install-XXXXXX")"
cleanup() {
  local status=$?
  trap - EXIT
  rm -rf "$tmp"
  if [[ $status -ne 0 ]]; then
    printf '[mmwxc] Installation failed (exit %d). Existing services and configuration were preserved or rolled back.\n' "$status" >&2
  fi
  exit "$status"
}
trap cleanup EXIT

asset_url() {
  local name="$1"
  if [[ "$RELEASE_TAG" == "latest" ]]; then
    printf 'https://github.com/%s/releases/latest/download/%s' "$REPO" "$name"
  else
    printf 'https://github.com/%s/releases/download/%s/%s' "$REPO" "$RELEASE_TAG" "$name"
  fi
}

fetch_asset() {
  local name="$1" destination="$2"
  log_step "Downloading $name"
  if [[ -n "$ASSET_DIR" ]]; then cp "$ASSET_DIR/$name" "$destination"
  elif command -v curl >/dev/null 2>&1; then curl --fail --show-error --silent --location --retry 3 --retry-delay 2 --retry-all-errors --connect-timeout 10 --max-time 180 -o "$destination" "$(asset_url "$name")"
  elif command -v wget >/dev/null 2>&1; then wget -q --connect-timeout=10 --read-timeout=180 -O "$destination" "$(asset_url "$name")"
  else echo "curl or wget is required" >&2; exit 1
  fi
  [[ -s "$destination" ]] || { echo "downloaded empty asset: $name" >&2; exit 1; }
}

verify_asset() {
  local name="$1" file="$2" expected actual
  expected="$(awk -v name="$name" '$2==name || $2=="*"name {print $1; exit}' "$tmp/checksums.txt")"
  [[ "$expected" =~ ^[0-9a-fA-F]{64}$ ]] || { echo "missing checksum for $name" >&2; exit 1; }
  actual="$(sha256sum "$file")"
  actual="${actual%%[[:space:]]*}"
  [[ "${actual,,}" == "${expected,,}" ]] || { echo "checksum mismatch for $name" >&2; exit 1; }
}

verify_elf() {
  local file="$1" expected_arch="$2" magic machine expected_machine
  magic="$(od -An -tx1 -N4 "$file")"
  magic="${magic//[[:space:]]/}"
  [[ "$magic" == "7f454c46" ]] || { echo "artifact is not an ELF binary: $file" >&2; exit 1; }
  machine="$(od -An -tu2 -j18 -N2 "$file")"
  machine="${machine//[[:space:]]/}"
  case "$expected_arch" in amd64) expected_machine=62 ;; arm64) expected_machine=183 ;; esac
  [[ "$machine" == "$expected_machine" ]] || { echo "artifact architecture mismatch: $file" >&2; exit 1; }
}

file_sha256() {
  local output
  output="$(sha256sum "$1")"
  printf '%s\n' "${output%%[[:space:]]*}"
}

rotate_backups() {
  local directory="$1" index
  mkdir -p "$directory"
  mapfile -t backups < <(find "$directory" -maxdepth 1 -type f -printf '%T@ %p\n' | sort -nr | awk '{print $2}')
  for ((index=2; index<${#backups[@]}; index++)); do rm -f "${backups[$index]}"; done
}

backup_file() {
  local source="$1" kind="$2" directory
  [[ -f "$source" ]] || return 0
  directory="$ROLLBACK_ROOT/$kind"
  mkdir -p "$directory"
  cp -a "$source" "$directory/$(basename "$source")-$(date -u +%Y%m%dT%H%M%S)-$(date +%N)Z"
  rotate_backups "$directory"
}

atomic_install() {
  local source="$1" target="$2" mode="$3"
  mkdir -p "$(dirname "$target")"
  install -m "$mode" "$source" "$target.new"
  mv -f "$target.new" "$target"
}

fetch_asset checksums.txt "$tmp/checksums.txt"
fetch_asset "mmwxc-helper-linux-$ARCH" "$tmp/mmwxc-helper"
fetch_asset "mmwxc-core-linux-$ARCH" "$tmp/mmwxc-core"
log_step "Verifying checksums, ELF architecture, and versions"
verify_asset "mmwxc-helper-linux-$ARCH" "$tmp/mmwxc-helper"
verify_asset "mmwxc-core-linux-$ARCH" "$tmp/mmwxc-core"
chmod 755 "$tmp/mmwxc-helper" "$tmp/mmwxc-core"
verify_elf "$tmp/mmwxc-helper" "$ARCH"
verify_elf "$tmp/mmwxc-core" "$ARCH"
helper_version_output="$("$tmp/mmwxc-helper" --version)"
helper_version_line="${helper_version_output%%$'\n'*}"
[[ "$helper_version_line" =~ ^mmwxc-helper[[:space:]]v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] || { echo "invalid Helper version output" >&2; exit 1; }
core_version_output="$("$tmp/mmwxc-core" version)"
core_version_line="${core_version_output%%$'\n'*}"
[[ "$core_version_line" =~ ^Xray[[:space:]][0-9]+\.[0-9]+\.[0-9]+([[:space:]].*)?$ ]] || { echo "invalid Custom Core version output" >&2; exit 1; }

existing_config=false
if [[ -f "$HELPER_CONFIG" ]]; then
  existing_config=true
  log_step "Existing Helper identity/config detected; preserving it unchanged"
  grep -Eq '^(MMWXC_HELPER_API_URL|CUSTOM_API_URL)=' "$HELPER_CONFIG" || { echo "existing Helper config has no API URL" >&2; exit 1; }
  grep -Eq '^(MMWXC_HELPER_SERVER_ID|SERVER_ID)=' "$HELPER_CONFIG" || { echo "existing Helper config has no server ID" >&2; exit 1; }
  grep -Eq '^(MMWXC_HELPER_TOKEN|TOKEN)=' "$HELPER_CONFIG" || { echo "existing Helper config has no token" >&2; exit 1; }
elif [[ -z "$SERVER_ID" || -z "$TOKEN" ]]; then
  echo "fresh installation requires a one-time install command from mmwx-custom" >&2
  exit 1
fi

old_helper=""
had_helper=false
if [[ -x "$HELPER_BINARY" ]]; then
  had_helper=true
  old_helper_output="$($HELPER_BINARY --version 2>/dev/null || true)"
  old_helper_line="${old_helper_output%%$'\n'*}"
  if [[ "$old_helper_line" =~ ^mmwxc-helper[[:space:]]v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then old_helper="${old_helper_line#mmwxc-helper }"; fi
fi
new_helper="${helper_version_line#mmwxc-helper }"
log_step "Helper version: ${old_helper:-not-installed} -> $new_helper"
helper_changed=true
if [[ -f "$HELPER_BINARY" ]] && [[ "$(file_sha256 "$HELPER_BINARY")" == "$(file_sha256 "$tmp/mmwxc-helper")" ]]; then helper_changed=false; fi
if $helper_changed; then backup_file "$HELPER_BINARY" helper; atomic_install "$tmp/mmwxc-helper" "$HELPER_BINARY" 0755; fi

if ! $existing_config; then
  mkdir -p "$(dirname "$HELPER_CONFIG")"
  umask 077
  cat >"$HELPER_CONFIG" <<EOF
MMWXC_HELPER_API_URL=${API_URL}
MMWXC_HELPER_SERVER_ID=${SERVER_ID}
MMWXC_HELPER_TOKEN=${TOKEN}
MMWXC_HELPER_INTERVAL=${INTERVAL}
MMWXC_HELPER_CORE_SOCKET=/run/mmwxc/core-control.sock
MMWXC_HELPER_STATE_FILE=/var/lib/mmwxc-helper/state.json
MMWXC_HELPER_ENABLE_NFTABLES=false
EOF
fi
chmod 600 "$HELPER_CONFIG"

mkdir -p "$(dirname "$HELPER_UNIT")"
cat >"$HELPER_UNIT.new" <<'EOF'
[Unit]
Description=MMWXC Custom Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/mmwxc-helper.env
ExecStart=/usr/local/bin/mmwxc-helper
StateDirectory=mmwxc-helper
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
mv -f "$HELPER_UNIT.new" "$HELPER_UNIT"

core_was_active=false
if [[ -z "$ROOT_PREFIX" ]] && "$SYSTEMCTL" is-active --quiet mmwxc-core.service; then core_was_active=true; fi
had_core=false
if [[ -f "$CORE_BINARY" ]]; then had_core=true; fi
core_changed=true
if [[ -f "$CORE_BINARY" ]] && [[ "$(file_sha256 "$CORE_BINARY")" == "$(file_sha256 "$tmp/mmwxc-core")" ]]; then core_changed=false; fi
if $core_changed; then backup_file "$CORE_BINARY" core; atomic_install "$tmp/mmwxc-core" "$CORE_BINARY" 0755; fi

mkdir -p "$(dirname "$CORE_CONFIG")" "$(dirname "$CORE_UNIT")" "$ROLLBACK_ROOT/config"
chmod 700 "$(dirname "$CORE_CONFIG")" "$ROLLBACK_ROOT" "$ROLLBACK_ROOT/config"
cat >"$CORE_UNIT.new" <<'EOF'
[Unit]
Description=MMWXC Custom Xray Core
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=MMWXC_CORE_CONTROL_SOCKET=/run/mmwxc/core-control.sock
ExecStart=/opt/mmwxc/core/xray run -config /etc/mmwxc/core/config.json
RuntimeDirectory=mmwxc
RuntimeDirectoryMode=0700
RuntimeDirectoryPreserve=yes
Restart=on-failure
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF
mv -f "$CORE_UNIT.new" "$CORE_UNIT"

core_is_ready() {
  local snapshot
  "$SYSTEMCTL" is-active --quiet mmwxc-core.service || return 1
  [[ -S /run/mmwxc/core-control.sock ]] || return 1
  if command -v curl >/dev/null 2>&1; then
    snapshot="$(curl -fsS --max-time 2 --unix-socket /run/mmwxc/core-control.sock http://localhost/v1/snapshot)" || return 1
    [[ "$snapshot" =~ \"version\"[[:space:]]*:[[:space:]]*1[[:space:]]*[,\}] ]]
  fi
}

helper_is_ready() {
  local connected connected_epoch connected_matches
  grep -q "\"helper_version\"[[:space:]]*:[[:space:]]*\"$new_helper\"" "$HELPER_STATE" 2>/dev/null || return 1
  connected_matches="$(sed -n 's/.*"controller_connected_at"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$HELPER_STATE")"
  connected="${connected_matches%%$'\n'*}"
  [[ -n "$connected" ]] || return 1
  connected_epoch="$(date -u -d "$connected" +%s 2>/dev/null || true)"
  [[ "$connected_epoch" =~ ^[0-9]+$ && "$connected_epoch" -ge "$INSTALL_STARTED_AT" ]]
}

if [[ -z "$ROOT_PREFIX" ]]; then
  log_step "Reloading systemd and restarting mmwxc-helper.service"
  "$SYSTEMCTL" daemon-reload
  "$SYSTEMCTL" enable mmwxc-helper.service >/dev/null
  if ! "$SYSTEMCTL" restart mmwxc-helper.service || ! "$SYSTEMCTL" is-active --quiet mmwxc-helper.service; then
    echo "Helper start failed; restoring previous binary" >&2
    latest="$(find "$ROLLBACK_ROOT/helper" -maxdepth 1 -type f -printf '%T@ %p\n' 2>/dev/null | sort -nr | awk 'NR==1 {print $2}' || true)"
    if [[ -n "$latest" ]]; then cp -a "$latest" "$HELPER_BINARY"; "$SYSTEMCTL" restart mmwxc-helper.service || true
    elif ! $had_helper; then rm -f "$HELPER_BINARY"; "$SYSTEMCTL" disable mmwxc-helper.service >/dev/null 2>&1 || true
    fi
    if $core_changed; then
      latest="$(find "$ROLLBACK_ROOT/core" -maxdepth 1 -type f -printf '%T@ %p\n' 2>/dev/null | sort -nr | awk 'NR==1 {print $2}' || true)"
      if [[ -n "$latest" ]]; then cp -a "$latest" "$CORE_BINARY"; elif ! $had_core; then rm -f "$CORE_BINARY"; fi
    fi
    exit 1
  fi
  deadline=$((SECONDS + 30))
  log_step "Waiting for Helper to reconnect to the controller"
  while (( SECONDS < deadline )); do
    if helper_is_ready; then break; fi
    sleep 1
  done
  if ! helper_is_ready; then
    echo "Helper health check failed; restoring previous binary" >&2
    latest="$(find "$ROLLBACK_ROOT/helper" -maxdepth 1 -type f -printf '%T@ %p\n' 2>/dev/null | sort -nr | awk 'NR==1 {print $2}' || true)"
    if [[ -n "$latest" ]]; then cp -a "$latest" "$HELPER_BINARY"; "$SYSTEMCTL" restart mmwxc-helper.service || true
    elif ! $had_helper; then rm -f "$HELPER_BINARY"; "$SYSTEMCTL" disable mmwxc-helper.service >/dev/null 2>&1 || true
    fi
    if $core_changed; then
      latest="$(find "$ROLLBACK_ROOT/core" -maxdepth 1 -type f -printf '%T@ %p\n' 2>/dev/null | sort -nr | awk 'NR==1 {print $2}' || true)"
      if [[ -n "$latest" ]]; then cp -a "$latest" "$CORE_BINARY"; elif ! $had_core; then rm -f "$CORE_BINARY"; fi
    fi
    exit 1
  fi
  if $core_changed && $core_was_active; then
    core_ready=false
    if "$SYSTEMCTL" restart mmwxc-core.service; then
      deadline=$((SECONDS + 20))
      while (( SECONDS < deadline )); do
        if core_is_ready; then core_ready=true; break; fi
        sleep 1
      done
    fi
    if ! $core_ready; then
      echo "Custom Core update failed; restoring previous binary" >&2
      latest="$(find "$ROLLBACK_ROOT/core" -maxdepth 1 -type f -printf '%T@ %p\n' 2>/dev/null | sort -nr | awk 'NR==1 {print $2}' || true)"
      if [[ -n "$latest" ]]; then cp -a "$latest" "$CORE_BINARY"; "$SYSTEMCTL" restart mmwxc-core.service || true; fi
      exit 1
    fi
	fi
fi

log_step "MMWXC Custom Agent installation complete"
log_step "Helper service: mmwxc-helper.service"
log_step "Custom Core prepared at /opt/mmwxc/core/xray"
log_step "Custom Core remains stopped until an explicit config/cutover command is issued"
