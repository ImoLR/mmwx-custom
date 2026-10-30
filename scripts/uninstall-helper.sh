#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
  echo "please run as root" >&2
  exit 1
fi

# Never start the old External Core while deleting a server. Only units whose
# runtime belongs to MMWXC may be stopped here; official Agent removal is separate.
stop_owned_unit() {
  local unit="$1"
  systemctl stop "$unit"
  systemctl disable "$unit" 2>/dev/null || true
  if systemctl is-active --quiet "$unit"; then
    echo "cannot stop $unit" >&2
    exit 1
  fi
}

if [[ "${1:-}" != "--finish" ]]; then
  if [[ -f /etc/systemd/system/mmwxc-core.service ]]; then
    stop_owned_unit mmwxc-core.service
  fi
  owned_external=false
  if [[ -f /etc/systemd/system/xray.service.d/90-mmwxc-owner.conf ]]; then
    if ! grep -q '^ExecStart=/opt/mmwxc/core/xray ' /etc/systemd/system/xray.service.d/90-mmwxc-owner.conf; then
      echo "ownership drop-in changed; manual review required" >&2
      exit 1
    fi
    owned_external=true
  fi
  if [[ -f /etc/systemd/system/xray.service ]] && grep -q '^ExecStart=/opt/mmwxc/core/xray ' /etc/systemd/system/xray.service; then
    owned_external=true
  fi
  if $owned_external; then
    stop_owned_unit xray.service
    rm -f /etc/systemd/system/xray.service.d/90-mmwxc-owner.conf /etc/systemd/system/xray.service.d/90-mmwxc-owner.conf.bak
    if [[ -f /etc/systemd/system/xray.service ]] && grep -q '^ExecStart=/opt/mmwxc/core/xray ' /etc/systemd/system/xray.service; then
      rm -f /etc/systemd/system/xray.service /etc/systemd/system/xray.service.bak
    fi
  fi
  if [[ -L /usr/local/bin/xray ]] && [[ "$(readlink /usr/local/bin/xray)" == /opt/mmwxc/core/xray ]]; then
    rm -f /usr/local/bin/xray
  fi
  # Do not remove a live binary, including an orphan outside its expected unit.
  for executable in /proc/[0-9]*/exe; do
    target="$(readlink "$executable" 2>/dev/null || true)"
    case "$target" in
      /opt/mmwxc/core/xray|'/opt/mmwxc/core/xray (deleted)'|/var/lib/mmwxc/ownership/xray|'/var/lib/mmwxc/ownership/xray (deleted)')
        echo "MMWXC Core is still running; manual review required" >&2
        exit 1 ;;
    esac
  done
  if command -v nft >/dev/null 2>&1; then
    for table in mmwxc_connection_control mmwxc_machine_protection mmwxc_flvx_rate; do
      if nft list table inet "$table" >/dev/null 2>&1; then nft delete table inet "$table"; fi
    done
  fi
  # FLVX per-user 速率 (tc): our filters first (a redirect into a missing IFB
  # drops packets), then the IFBs; a clsact only when nothing else is on it.
  if command -v tc >/dev/null 2>&1; then
    for dev in $(tc qdisc show 2>/dev/null | awk '$2 == "clsact" { print $5 }'); do
      for hook in ingress egress; do
        tc filter del dev "$dev" "$hook" pref 61870 2>/dev/null || true
        tc filter del dev "$dev" "$hook" pref 61871 2>/dev/null || true
      done
      if [[ -z "$(tc filter show dev "$dev" ingress 2>/dev/null)$(tc filter show dev "$dev" egress 2>/dev/null)" ]]; then
        tc qdisc del dev "$dev" clsact 2>/dev/null || true
      fi
    done
    ip link del mmwxcr-up 2>/dev/null || true
    ip link del mmwxcr-dn 2>/dev/null || true
  fi
  rm -f /etc/systemd/system/mmwxc-core.service /etc/systemd/system/mmwxc-core.service.bak
  systemctl daemon-reload
  [[ "${1:-}" == "--prepare" ]] && exit 0
fi

# The signed action runs this phase in a separate transient unit, after its
# stopped-Core result is acknowledged by the controller.
systemctl disable mmwxc-helper.service 2>/dev/null || true
rm -f /etc/systemd/system/mmwxc-helper.service /etc/systemd/system/mmwxc-helper.service.bak /usr/local/bin/mmwxc-helper /etc/mmwxc-helper.env
rm -f /etc/systemd/journald.conf.d/90-mmwxc-cap.conf
rm -rf /opt/mmwxc/core /etc/mmwxc /var/lib/mmwxc /var/lib/mmwxc-helper /run/mmwxc
systemctl daemon-reload
echo "MMWXC Core and Helper removed; uninstall the official Agent separately"
systemctl stop mmwxc-helper.service 2>/dev/null || true
