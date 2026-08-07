#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
  echo "please run as root" >&2
  exit 1
fi

systemctl stop mmwxc-helper.service 2>/dev/null || true
systemctl disable mmwxc-helper.service 2>/dev/null || true
rm -f /etc/systemd/system/mmwxc-helper.service
systemctl daemon-reload
rm -f /usr/local/bin/mmwxc-helper

answer="${MMWXC_HELPER_REMOVE_CONFIG:-}"
if [[ -z "$answer" ]]; then
  read -r -p "Remove /etc/mmwxc-helper.env? [y/N] " answer
fi
case "$answer" in
  y|Y|yes|YES) rm -f /etc/mmwxc-helper.env ;;
esac

echo "MMWXC Helper uninstalled"
