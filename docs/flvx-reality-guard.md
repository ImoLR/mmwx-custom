# FLVX Reality 防盗 on shared servers

Custom + Helper side of the FLVX contract `landing-reality-guard-v1`
(`/root/flvx-workers/contracts/landing-reality-guard-v1.md`, FLVX plan 127,
rendering as FLVX `go-gost/x/landing/guard.go` incl. the plan 128 `protocol:
["tls"]` fix). An unauthenticated probe with a foreign or missing SNI on a
shared VLESS Reality node is closed instead of being relayed to the decoy
`dest`, so a shared server cannot be used as a free TLS relay. Only the FLVX
shared Core (`mmwxc-flvx-core`) is touched; the official / mmwx-side Core
never is.

Status: implemented on branch `feat/flvx-reality-guard`, not released.

## Wire changes (all optional, old peers unaffected)

| Where | Field | Meaning |
| --- | --- | --- |
| Helper → Custom capabilities | `flvx.reality_guard.v1` | Helper renders the guard |
| `GET /api/custom/flvx/v1/shares` item | `reality_guard: {"versions": [1]}` | only while the share's Helper advertises the capability |
| `PUT /api/custom/flvx/v1/shares/{id}/desired` | `reality_guard: true \| false` (top level, next to `revision` / `nodes` / `egress`) | FLVX server switch; absent = off (today's behaviour) |
| `GET /api/custom/flvx/v1/shares/{id}/status` | `reality_guard: {"state": "on" \| "restart_required" \| "error", "nodes": n}` | omitted while the switch is off or the share has no running Reality node |
| Custom → Helper signed payload | `desired.reality_guard` | omitted (stripped) for Helpers without the capability, so their signature check is unchanged |
| Helper → Custom report | `reality_guard: {state, nodes, error}` | `error` (≤ 200 chars) is shown to the share operator only, never to FLVX |

`nodes` counts nodes actually pointed at a listening guard. `state` is `on`
whenever at least one node is guarded (a partial failure keeps `on` and puts
the reason in the operator-only `error`); `restart_required` / `error` only
when none is. No per-node field exists: links, keys, ports, serverNames and
shortIds stay byte-identical, and `vless.reality.dest` is validated exactly as
before (public `host:port` only) — the loopback dest is Helper-internal.

## Helper rendering

- One guard per (pinned dest, sorted lower-case serverNames) among the share's
  active VLESS Reality nodes. The dokodemo target is the dest address the
  Helper already pins against DNS rebinding.
- Guard inbound `flvxg-<port>` on `127.0.0.1`, `dokodemo-door`, TLS sniffing
  with `routeOnly`.
- Rules before the safety rule: `inboundTag + protocol tls + domain
  full:<name> → flvxg-direct`, then `inboundTag → block`.
- One deviation from FLVX's rendering, Helper-internal: the allow rule goes
  to `flvxg-direct`, a plain `freedom` outbound with `userLevel: 1` (a level
  BaseConfig never defines → the Core's built-in 1 s uplink/downlink-only
  timeouts) instead of the shared `direct` (level 0: 2 s / 5 s). REALITY
  closes its dest connection after every handshake, but the outbound never
  half-closes towards the dest, so with `direct` the guard inbound and its
  dest socket stayed 5 s per new Reality connection — counted against the
  share's connection limit (measured: 300 new connections → Core
  `current_total` 616 for 5 s). The inbound's level does not help: the
  outbound runs the copy with its own level. Added with `ado` before the
  rules, removed with the last guard.
- Reality inbound: `realitySettings.dest = 127.0.0.1:<port>` plus
  `sockopt.customSockopt` TCP_DEFER_ACCEPT (level 6, opt 9, value 5).
- Order per pass: rules (with any egress plan, atomic `adrules`) → guard
  inbounds → re-pointed nodes → unused guards removed; their rules leave the
  rule set on the next pass. `GuardRulePorts` records which guards' rules the
  Core holds, so a failed rule update never points a node at a guard without
  rules.
- Fail-open everywhere: no routing API (`restart_required`, no Core restart),
  failed rule update, guard that cannot listen (port avoided, fresh port after
  1 minute) — the node keeps its real dest and stays active.
- Ports: kernel-chosen loopback ports, skipping the share range, node ports,
  official inbound ports, the control API port and ports a guard failed on;
  persisted in the Helper state (stable across Helper restarts), moved when an
  official inbound or the share range later claims them, freed when no node
  needs or points at them.
- Steady state: no extra Core API call and no extra DNS lookup.

## Tests

- `internal/flvx/guard_test.go`: exact contract JSON, grouping keys, status
  bounds, unchanged signed payload without the switch.
- `cmd/mmwxc-helper/flvx_guard_test.go` (fake Core): absent/false switch,
  grouping and call order, byte-identical node fields, steady state, Helper
  restart, removal and port release, fail-open (rules, one / all inbounds, old
  Core), port conflict move, egress plan coexistence, paused share.
- `flvx_guard_test.go` (PostgreSQL): capability, switch storage, user dest
  validation, payload stripping, status relay, operator-only reason.
- `cmd/mmwxc-helper/flvx_guard_netns_test.go` (real Core, three netns):
  `MMWXC_TEST_FLVX_CORE_FIXED=<core> go test ./cmd/mmwxc-helper -run
  TestFLVXRealityGuardNetnsE2E` as root.
- `tests/visual/flvx-reality-guard.mjs`: share card screenshots.
