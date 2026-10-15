#!/usr/bin/env bash
# Disposable container only. Uses the real released Core and a scoped systemctl
# fixture; no host units, host paths or public test ports are touched.
set -euo pipefail
root=$(cd "$(dirname "$0")/../../.." && pwd)
: "${MMWXC_TEST_CORE_BINARY:?provide the released Core binary}"
name="mmwxc-uninstall-test-$$"
trap 'docker rm -fv "$name" >/dev/null 2>&1 || true' EXIT

docker run -i --name "$name" --memory 192m --network none \
  -e GOMAXPROCS=2 -v "$root/scripts/uninstall-helper.sh:/fixture/uninstall.sh:ro" \
  -v "$MMWXC_TEST_CORE_BINARY:/fixture/core:ro" debian:trixie-slim bash -s <<'CONTAINER'
set -euo pipefail
mkdir -p /opt/mmwxc/core /etc/mmwxc/core /var/lib/mmwxc /etc/systemd/system/xray.service.d /fixture-bin /other-project
cp /fixture/core /opt/mmwxc/core/xray
printf 'foreign-data\n' >/other-project/keep
printf '[Service]\nExecStart=/usr/local/bin/xray run\n' >/etc/systemd/system/xray.service
printf '[Service]\nExecStart=/opt/mmwxc/core/xray run -config /etc/mmwxc/core/config.json\n' >/etc/systemd/system/xray.service.d/90-mmwxc-owner.conf
touch /etc/systemd/system/mmwxc-core.service /etc/systemd/system/mmwxc-helper.service /usr/local/bin/mmwxc-helper /etc/mmwxc-helper.env
cat >/etc/mmwxc/core/config.json <<'CONFIG'
{"inbounds":[{"listen":"127.0.0.1","port":12105,"protocol":"socks","settings":{"auth":"noauth"}}],"outbounds":[{"protocol":"freedom"}]}
CONFIG
/opt/mmwxc/core/xray run -c /etc/mmwxc/core/config.json >/tmp/core.log 2>&1 &
echo $! >/tmp/owned.pid
sleep 120 &
other_pid=$!
cat >/fixture-bin/systemctl <<'SYSTEMCTL'
#!/bin/bash
printf '%s\n' "$*" >>/tmp/unit-actions
case "$1" in
  stop) case "$2" in mmwxc-core.service|xray.service) kill "$(cat /tmp/owned.pid)" 2>/dev/null || true;; esac;;
  is-active) exit 1;;
esac
SYSTEMCTL
chmod +x /fixture-bin/systemctl
export PATH=/fixture-bin:$PATH
for attempt in {1..30}; do if (echo >/dev/tcp/127.0.0.1/12105) 2>/dev/null; then break; fi; sleep .1; done
(echo >/dev/tcp/127.0.0.1/12105) 2>/dev/null
bash /fixture/uninstall.sh --prepare
if (echo >/dev/tcp/127.0.0.1/12105) 2>/dev/null; then echo 'Core still serving' >&2; exit 1; fi
! test -e /etc/systemd/system/xray.service.d/90-mmwxc-owner.conf
test -e /etc/systemd/system/xray.service
test -e /usr/local/bin/mmwxc-helper
bash /fixture/uninstall.sh --finish
! test -e /usr/local/bin/mmwxc-helper
! test -e /var/lib/mmwxc
! test -e /opt/mmwxc/core
kill -0 "$other_pid"
test "$(cat /other-project/keep)" = foreign-data
! grep -E 'forwardx|flvx|cloudflared|vps-stock-watch' /tmp/unit-actions
printf 'PASS: real Core port closed; MMWXC files removed; official unit and foreign process/data preserved\n'
CONTAINER
