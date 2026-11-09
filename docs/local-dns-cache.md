# 本机 DNS 缓存 (Helper v0.7.25+)

User 2026-11-08: FLVX plan 239 put a local DNS cache on every machine by hand (incl. all mmwx servers) and
ended the 5 s DNS stalls; every new machine must get it as a standing service. FLVX plan 240 turned the
recipe into `flvx-dns-cache.sh` (`ensure | status | check | rollback`). The Helper ships the same script.

## The vendored script

`cmd/mmwxc-helper/flvx-dns-cache.sh` is byte-identical to FLVX `go-gost/x/dnscache/flvx-dns-cache.sh`
at commit `1455ba60` (released in FLVX 3.0.27-fork.127), sha256
`7350f279b4d27975ab0ad3feb83e16c9e1722d9e8eb8ab7602508a06c98f4919` (`dnsCacheScriptSHA256`,
`TestDNSCacheScriptIsTheFLVXCopy`). Never edit it here: copy a newer FLVX version byte for byte and update
the pin. What it does (modes dnsmasq / resolved / minimal, loopback only, backups and `rollback.sh` in
`/root/dns-backup-<date>/`) is documented in its header and in FLVX `plans/240-dns-cache-default.md`.

## When it runs

`ensure` runs **once per Helper version**; `/var/lib/mmwxc-helper/dns-cache.json` records it.

- **Install / in-place reinstall**: `install-helper.sh` runs `mmwxc-helper dns-cache ensure-once` at the end
  and prints the script's 「DNS 缓存：…」 line. A failure prints one warning; the install still succeeds.
- **「升级 Helper」** (managed update): the old binary installs the new one, so the new Helper runs it
  itself, one minute after it first reaches Custom (after the upgrade health check).
- The run is a transient unit `mmwxc-dns-cache-ensure` (outside the Helper's cgroup, so a Helper restart
  cannot cut an apt install in half; a later Helper picks up its result). No retry loop: a failed or
  interrupted run is retried at most once for that version. One log line per run.
- `--zones` is the Custom API host (`mmwxc.imgamer.top` → `imgamer.top`). The script keeps the zones
  already configured, so it never removes one (e.g. FLVX's `nexkr.cloud`).
- On a host that is already right (plan 239) it is a no-op: no file, unit or process changes.
- Host-local extras are never touched: PQS TW's `flvx-dnscache.service.d/60-local-conf-dir.conf`,
  `/etc/flvx-dnscache/local.d/` and `flvx-dnstcp.service` stay as they are and are not reported as drift.

Opt out per server with `MMWXC_HELPER_DNS_CACHE=0` in `/etc/mmwxc-helper.env` (the installer honours the
same variable). Uninstalling the Helper removes only its copy of the script; the DNS cache stays (it is a
system service that FLVX uses too).

## Status

The Helper reports `dns_cache {mode, ok, reason, checked_at, ensure}` with its normal status. The check
reads `/etc/resolv.conf`, `/etc/flvx-dnscache/state` and `/proc` at most every 10 minutes: no exec, no
network (same rules as the script's `check` and FLVX's agent). Custom saves it like other status fields;
a new `checked_at` alone is a heartbeat and does not rewrite `helper-state.json`.

服务管理: the card shows 「⚠ DNS 缓存未启用 / 未运行」 only when a fresh Helper reports the cache broken;
the Helper dialog (更多操作 → Connections Helper) shows the state and the last install / upgrade run.
Helpers older than v0.7.25 show 未知.

## 「修复 DNS 缓存」 (Custom v1.4.92 / Helper v0.7.26+)

User 2026-11-09: the same admin button as FLVX's node card (plan 240). It sits on the DNS 缓存 row of the
Connections Helper dialog (always there for admins); the card's ⚠ opens that dialog.

- Click → confirm → `POST /api/custom/servers/{id}/agent {"action":"dns.cache.ensure"}` (operator / admin
  only, no payload) → the existing command channel → the Helper claims the run and answers at once, then
  runs `ensure` in the background in the same transient unit (a run may take minutes with apt; the report
  loop never waits, so the server never looks offline meanwhile).
- The Helper reports `dns_cache.repair {command_id, started_at, finished_at, exit_code, changed, summary}`
  and re-checks the cache right after the run, so the next report (≤ 5 s) carries the new state. The
  dialog polls that record and shows 无需改动 / 已安装并启用 / 精简模式 / 失败 + reason. The record is kept in
  `/var/lib/mmwxc-helper/dns-cache-repair.json`; the once-per-version marker is not touched.
- Guards: one run per server (Custom: queued or running → 409; Helper: run lock / active unit → busy,
  nothing waits), one click a minute (Custom and Helper), Helper offline → refused, Helper without the
  `dns.cache.ensure` capability → 「需要先升级 Helper」, an upgrade in flight → refused. The command lives 2
  minutes (a Helper that is away never runs it late), a run is bounded at 260 s, and nothing is ever
  retried automatically (a run cut off by a Helper restart is recorded, not relaunched).
  `MMWXC_HELPER_DNS_CACHE=0` hosts refuse it with that reason.
- One log line per run; no probes or extra connections: the command only exists after a click.

## Shell

```
mmwxc-helper dns-cache status        # read-only
mmwxc-helper dns-cache check
mmwxc-helper dns-cache ensure        # repair now (no-op when right)
mmwxc-helper dns-cache rollback      # DNS config from before the first change
```
