# FLVX Reality 最快 IP 钉选 + 伪装域名测速 + 共享域名池

Contract `reality-pick-v1` (FLVX plan 227), user decision 2026-11-06. Only the
FLVX shared Core on shared servers is involved; the official mmwx inbounds keep
their own Reality dest behaviour (official xray resolves the dest per
connection), and client links never change.

## Fastest-IP pin (`reality_pin`, Helper v0.7.22)

REALITY dials its dest for every new connection and waits for the dest's
ServerHello before answering the client, so the dest RTT is added to every
handshake. Up to v0.7.21 the Helper pinned the first sorted DNS answer
(`sort.Strings(ips)[0]`); on 「Boil Hinet 马年」 that was a far CloudFront edge
(54.230.70.30, 54–85 ms) instead of 65.9.180.x (3–5 ms): +100–150 ms per new
connection.

- **Candidates** per dest (`host:port`): the IPs nodes are pinned to, the
  current DNS answer and IPs DNS returned in the last 7 days, at most 8, all
  public. 1.1.1.1 flips between edge sets within seconds on Hinet, so
  remembered IPs keep the near set in play. A DNS answer with any private
  address is ignored (DNS rebinding protection).
- **Full check**: one DNS lookup and one TCP connect per candidate
  (1 s timeout) on Helper start, on an FLVX Core restart, every 6 h and after a
  failed liveness check. **Liveness**: one connect per pinned IP every 10 min.
  Cost per dest per 6 h: 1 lookup + ≤ 8 connects + 35 liveness connects.
  Nothing per connection.
- **Quiet sockets** (Helper v0.7.23, user 2026-11-06): pin, liveness and probe
  connects share one Helper-wide cap of **2 open sockets**, and every connected
  socket is closed with `SO_LINGER 0` (an RST), so neither end keeps a
  TIME_WAIT entry. The 1 s connect timeout starts once a slot is free.
- **Margin** (`reality_pin` v2, FLVX plan 234, Helper v0.7.24): a node moves
  only if its IP failed or the best IP is faster by at least the margin:
  `Desired.reality_pin_margin_ms` (FLVX 「Reality 目标 IP：新 IP 快 ≥ X ms 才换」,
  0–200; 0 = any gain, a tie keeps the pin), else 20 ms. A failed IP (or a
  fallback pin) moves whatever the margin. The Helper decides on the last
  full check's numbers every pass, so a new margin applies from the next pass
  without any extra connect. Custom forwards the field only to Helpers with
  `flvx.reality_pin.v2`; older Helpers get the same payload as before and
  keep 20 ms.
- **No dropped connections**: a guarded node (Reality 防盗 on) moves with its
  whole guard group — only the loopback guard inbound `flvxg-<port>` is
  re-added with the new target on the same port; the node inbound and its
  connections stay (measured on the real Core: one pass, `rmi`/`adi` of the
  guard only, 75 ms). An unguarded node moves while it has no connections or
  when its inbound is re-added anyway (Core restart, node change), unless its
  IP failed.
- **Fallback**: no candidate answers → a new node gets the first sorted DNS
  answer (as before); a pinned node keeps its pin.
- **Upgrade / downgrade**: a v0.7.21 pin (node digest, no host) is adopted, not
  re-resolved. Pins keep the node digest current, so a downgraded Helper reuses
  them. Pins of deleted nodes and unused dests are dropped.
- **Status**: `GET /api/custom/flvx/v1/shares/{id}/status` → `reality_pins`:
  `[{host, pinned_ip, pinned_ms, best_ip, best_ms, checked_at, nodes, fallback?, move_reason?}]`
  per (dest, pinned IP) of the active nodes; ms are TCP connect times of the
  last full check, −1 = no answer; `checked_at` unix ms. `move_reason`
  (v2): `faster` / `failover` while the dest's last full check moved a node of
  the entry here from another IP.
- Capability in `GET /shares`: `"reality_pin": {"versions": [1]}` (Helper
  capability `flvx.reality_pin.v1`), `{"versions": [1, 2]}` once the Helper
  also has `flvx.reality_pin.v2`.

## Probe (`reality_probe`, Helper v0.7.22)

`POST /api/custom/flvx/v1/shares/{id}/reality-probe` with
`{"targets": ["host[:port]", …], "timeout_ms": 3000}` (1–64 domain targets,
port 443 by default, timeout 500–5000 ms) → `{probe_id, state: "queued",
targets (normalised), timeout_ms, …}`. One probe per share at a time (409
`probe_busy`); 409 `helper_outdated` without the capability.

`GET /api/custom/flvx/v1/shares/{id}/reality-probe/{probe_id}` →
`{probe_id, state: queued|running|done|error, targets, timeout_ms, created_at,
started_at, finished_at, results, error?}`; jobs stay pollable 30 min after
they end; never picked up in 2 min / not done in 3 min → `error`.

Custom keeps the job in memory and hands it (HMAC-signed, domain
`mmwxc-flvx-reality-probe-v1`) to the Helper with its next upload answer; the
Helper runs it in the background (the upload loop never waits) and its
report rides on the next uploads. Per target, 3x-ui's REALITY scan criteria:
resolve (any private address → not feasible, nothing dialled), TCP connect to
each public IPv4 (≤ 8, 1 s, one at a time, each closed with an RST at once),
one TLS handshake with SNI = host to the fastest — on the last IP's
connection when that one is the fastest, otherwise on a new connection (so
≤ 9 connects per target).
`feasible` = TLS 1.3 + X25519 + h2 + certificate valid for the SNI (system
roots). Result per target: `{target, feasible, reason, latency_ms (TCP connect +
TLS handshake to best_ip), best_ip, ips: [{ip, tcp_ms}], tls13, tls_version,
x25519, curve_id, h2, cert_valid, server_names (non-wildcard SANs),
cert_chain_bytes}`. At most 2 targets at once (4 up to Helper v0.7.22), no
retries; a job stops after 150 s (Custom's run limit is 3 min). Only X25519 is
offered (Go 1.24 does not expose the negotiated group), so `curve_id` is
always `X25519` when the handshake succeeds.

Capability in `GET /shares`: `"reality_probe": {"versions": [1],
"max_targets": 64, "concurrency": 2}`.

## Pool (`GET /api/custom/flvx/v1/reality-pool`)

Official v0.5.5 only exposes the pool through its admin API: the share status
(`/api/admin/remote/reality-domains/share`: licensed, enabled, pool_size) and
the Reality domain list (`/api/admin/remote/reality-domains?server_id=N`),
whose `domain_sources` mark pool domains `shared_pool` — merged only while
official's 「共享」 setting (`reality_domain_share_enabled`) is on. The list makes
server N's official agent test every candidate once (official's own picker does
the same); N = `MMWXC_REALITY_POOL_SERVER_ID` or the lowest server whose Helper
reported in the last 2 minutes. Custom borrows the newest official admin
session (as for Core handoff) and asks at most every 12 h (30 min after a
failure), detached from the request.

Response, never an error: `{domains: [{domain, official_ok,
official_latency_ms?, official_reality_usable?, official_probe_server_id}],
pool_size, fetched_at (unix ms, 0 = never), source: "official", refreshing,
error?}`. Official's admin API has no per-domain tls_version / curve_id /
contributors; FLVX probes the domains itself.
