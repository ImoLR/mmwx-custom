# FLVX landing egress on shared servers (outbounds + routing)

Custom + Helper side of the FLVX contract `landing-egress-v1`
(`/root/flvx-workers/contracts/landing-egress-v1.md`, FLVX plan 107, branch
`feature/3.0.27-landing-egress`). FLVX owns outbound templates and routing
profiles and compiles them per shared server into one flat plan; Custom
validates it again, the Helper renders it into the FLVX Core
(`mmwxc-flvx-core`) and applies it hot through the Core API.

Status: implemented on branch `feat/flvx-landing-egress`, not released.
Release together with the FLVX side after the screenshots are approved.

## Checklist

- [x] `internal/flvx`: plan types, §4 validation (field paths, all limits,
  private-upstream ban incl. IPv4-mapped forms, forced family vs. literal,
  WireGuard/Reality/SS2022 key formats, `ext:` refusal, unknown geo
  categories), source-IP exemption arithmetic, renderer, pinned geo table.
- [x] Rendering byte-identical to FLVX's `egress_golden.json` (test compares
  against the copied file).
- [x] Capability: Helper advertises `flvx.egress.v1` and reports
  `egress{versions,geoip,geosite,state,...}`; `GET /shares` adds
  `egress{versions:[1],allowed,geoip,geosite}` only after the share's Helper
  reported support.
- [x] Share policy 「允许自定义出站与路由」 (default on), Custom-only field
  `egress_denied`; off → 422 for payloads with `egress`, delivered plans fall
  back to the default direct outbound.
- [x] Helper hot apply: `lso` → `ado` → `adrules` (atomic replace) → `rmo`
  stale; outbounds and rules before inbounds; one-time restart only for a Core
  started without RoutingService; fail closed for newly covered nodes.
- [x] Geo data: pinned files only when referenced, disk check, SHA-256 by
  stat stamp, backoff 1 → 30 min, rate-limited logs.
- [x] Resource guards: steady state makes no Core API call; render/DNS/local
  address at most every 5 min; memory check before large geo rules.
- [x] Tests: unit, Postgres, fake-Core, real Core, netns data plane, resource
  profile; UI screenshots (desktop + H5).

## Wire changes (all optional, old peers unaffected)

| Where | Change |
| --- | --- |
| `GET /shares` item | `egress: {versions:[1], allowed, geoip, geosite}` — omitted until the share's Helper reports plan v1 |
| `PUT /shares/{id}/desired` | optional `egress` plan; unknown keys of the egress object (e.g. `allow_core_restart`) ignored, outbound/rule objects strict; 422 field paths like `egress.outbounds[1].server` |
| `GET /shares/{id}/status` | `egress: {state, error, applied_revision, warnings}` when the Helper reports it |
| Helper report (`flvx.Status`) | `egress` capability + state; Custom rejects >10 warnings / >200 chars |
| Helper payload (`flvx.Desired`) | `egress,omitempty`: payloads without a plan stay byte-identical, so the HMAC of old Helpers still verifies |
| Operator API | `PUT /api/custom/flvx/admin/shares/{id}` accepts `allow_egress` (absent = keep); list adds `allow_egress` and `egress_plan` summary (types/counts only, no upstream address or credential) |
| Helper capability | `flvx.egress.v1` |

## Helper behaviour

- Base config: `RoutingService` added, safety rule tagged `flvx-safety`,
  explicit policy level 0 (handshake 4, connIdle 300, uplinkOnly 2,
  downlinkOnly 5). The file is rewritten on the next pass, but a running Core
  only reads it on start.
- Default plan (no `egress` or inactive) with nothing applied: no Core API
  call at all. A running pre-egress Core keeps running untouched.
- First active plan on a Core without RoutingService (CLI reports gRPC
  `Unimplemented`): counters are collected and persisted, the Core is
  restarted once (`routing_restart_at` in state), the status carries a warning
  for 24 h. Never repeated; any other CLI error is treated as transient.
- Apply order per change: add missing outbounds, replace the whole rule set
  without `-append` (atomic; old rules stay if building fails), remove stale
  `flvxo-*` outbounds. A failed removal is retried after 2 minutes.
- Core tags are `<plan tag>-<sha256[:4]>` of the rendered JSON, so a changed
  outbound swaps through a new tag.
- The Helper validates the signed plan again once per plan hash. An invalid
  plan (e.g. from a newer Custom) fails closed for the nodes it covers only;
  node updates of the share keep applying.
- Fail closed: a plan that cannot be applied keeps nodes it newly covers
  offline (reported per node: 「出站与路由未生效，节点暂停：…」); nodes covered
  by the applied plan keep it. After a Core restart nothing is covered yet, so
  all covered nodes wait for their plan.
- Upstream hostnames (proxy servers, WireGuard `endpoint`) are resolved by the
  Helper (3 s) and **pinned** to a validated address of the right family (see
  Deviations). Private answers → blackhole + warning; unresolvable → blackhole
  + warning, retried after 1 minute.
- Forced IP versions render exactly as FLVX (`Force*` + `sendThrough` local
  address + `IP_BIND_ADDRESS_NO_PORT`; WireGuard keeps only that family's
  interface address); no local address of that family → blackhole + warning.
- Geo data: `/var/lib/mmwxc-helper/flvx/geo/{geoip,geosite}.dat`
  (Loyalsoldier `202610182205`), downloaded only when referenced, through the
  GitHub accelerator path of the Core binary (`releaseurl` allows exactly
  these two URLs), size + SHA-256 verified, temp files removed, re-hashed only
  when the stat stamp changes. Disk check: file + 64 MiB free. Failures back
  off 1, 2, 4, 8, 16, 30 min per file. Capability `geoip/geosite` = verified or
  enough disk. The CLI reads the files (`XRAY_LOCATION_ASSET`), the Core never
  loads them. Revoking a share deletes them with the rest of the state dir.
- Memory guard before adding geo rules: estimated growth = 8 × geo payload +
  4 MiB must fit under 90 % of the share's MemoryMax given the unit's current
  `memory.current`, and the host needs growth + 64 MiB `MemAvailable`;
  otherwise the plan is refused with a clear message (nodes it newly covers
  stay offline). While nodes are withheld (state error) the Helper keeps its
  existing per-tick inbound recheck (one extra `lsi` CLI call per 5 s); a
  failed plan is retried at most once per minute.
- Limits: per-node / share connection caps and traffic accounting are keyed by
  inbound tag, so they apply unchanged to traffic leaving through WARP or
  proxy outbounds (verified with real traffic, below).

## Deviations from the contract

1. **Upstream hostnames are pinned** (contract §4.4 says resolve and check at
   render, the Core resolves again at dial time). The Helper renders the
   validated address instead of the name, as it already does for Reality
   camouflage destinations, so a DNS rebinding answer between render and dial
   cannot reach a private address. Choice: forced family → that family only;
   prefer IPv6 / IPv6-only server → AAAA first; otherwise A first; sticky while
   the answer still contains the pinned address. An unresolvable name renders
   a blackhole with a warning and is retried after 1 minute (instead of
   letting the Core retry at dial time). FLVX needs no change; its native
   agents may adopt the same.
2. **Helper downgraded after a plan was accepted**: the payload is sent
   without `egress` (an unknown field would break the old Helper's HMAC) and
   the nodes the plan covers are sent with `enabled:false` (fail closed).
   Not covered by the contract; FLVX needs no change.
3. **Policy switched off while a plan is stored**: Custom keeps FLVX's stored
   desired unchanged but delivers it without the plan, so nodes leave through
   the default direct outbound (the share owner's choice). FLVX sees
   `allowed:false`, stops sending plans and shows 「妙妙屋共享方未允许自定义出站与路由」.
4. Items count uses port **segments** (`443,8000-9000` = 2) as the contract
   text says; FLVX's agent counts commas (1). Custom is at most one item per
   port field stricter; FLVX's panel uses segments too.
5. Implementation note (no wire impact): `GET /shares` advertises `egress` from the Helper's FLVX report (it carries
   geo availability), not from the management capability list; the management
   capability `flvx.egress.v1` gates what the Helper receives.

## Measurements (local throwaway setup, real Core v1.4.59 `8f5b614`)

`TestFLVXEgressResourceProfile`: one network namespace, the Helper's own
reconcile driving the Core, 256 MiB / 50 % unit limits not applied (bare
process). Evidence: `runs/landing-egress-mmwx/evidence/resource-profile.json`.

| State | RSS | Go heap | fds | goroutines |
| --- | --- | --- | --- | --- |
| 1 node, default plan | 30.1 MiB | 3.5 MiB | 11 | 15 |
| 50 nodes, default plan | 34.2 MiB | 4.9 MiB | 108 | 161 |
| + forced IPv4 outbound + BT block | 34.4 MiB | 5.0 MiB | 108 | 161 |
| + idle WARP outbound + AI geosite (3 lists) | 33.9 MiB | 5.1 MiB | 108 | 161 |
| + 「屏蔽中国大陆」 (geoip:cn, geosite:cn, source geoip:cn − 30 exempt = 10 094 CIDRs) | 44.1 MiB | 14.0 MiB | 108 | 161 |
| 1000 connections open through the forced-IPv4 outbound | 154.7 MiB | 275.9 MiB | 2108 | 7161 |
| 15 s after closing them | 161.0 MiB | 277.9 MiB | 108 | 161 |
| 2 min after closing them | 112.5 MiB | 111.3 MiB | 108 | 159 |

- fds and goroutines return exactly to the baseline (no leak); freed heap is
  collected within ~2 min and handed back to the OS afterwards.
- Applying 「屏蔽中国大陆」: 0.70 s; geo payload 2.25 MB; peak RSS of the
  `api adrules` CLI child (Helper side, transient) 51 MiB.
- Idle Core CPU with 50 nodes and the full plan: 0 % over 30 s.
- Helper reconcile tick in steady state: 9.7 ms Helper CPU + 24 ms CLI child
  CPU (the pre-existing single `statsquery`; the plan adds no call), 35 ms wall.
- Disk/logs over the whole test: Core log 327 bytes, Helper log lines 0,
  Helper state 49 KB, geo data 27.8 MB (only when referenced).
- Fake-Core unit test: 70 ticks (≈6 min) with an applied plan → 0 routing
  calls, 1 DNS lookup and 1 local-address probe (the 5-minute refresh).

## Tests

| Test | What |
| --- | --- |
| `internal/flvx` `TestEgressRenderMatchesFLVXGolden` | every outbound type × FLVX's golden inputs, byte-identical |
| `internal/flvx` `TestEgressValidationTable` | 67 cases: every §4 rule incl. private/mapped/CGNAT/metadata upstreams, `ext:`, foreign inbound tags, geo capability, limits |
| `internal/flvx` `TestEgressForcedFamilies`, `TestEgressRulesSafetyFirstAndExemption`, `TestCIDRSetBruteForce`, `TestReadGeoIP`, `TestEgressDecoding` | forced-family render, exemption + drop of fully exempt rules, source memo, exact CIDR cover, geo reader on the pinned file, tolerant/strict decoding and stable round trip |
| `TestFLVXEgressContractPostgres` | capability gating (old Helper → no `egress`, 422), allowed:false → 422, 422 field paths, `allow_core_restart` ignored, idempotent retry, signed delivery, old-Helper withholding, status, report bounds, admin summary without secrets |
| Helper fake-Core `TestFLVXEgress*` | apply order, steady state (0 calls), swap order, default restore, fail closed + older plan kept, DNS pin/rebinding/unresolved retry, restart once, geo backoff/disk/memory refusal, stale cleanup cadence, re-validation (an invalid plan fails closed for its nodes only) |
| `TestFLVXEgressRealCore` | `run -test` on 42 rendered outbounds (8 types × 5 IP versions) + geo rules; legacy Core restarted once; 3 plan changes + default restore hot (epoch unchanged, a live stream kept) |
| `TestFLVXEgressNetnsDataPlane` | forced IPv4/IPv6 (literals, AAAA/A-only names, UDP in-session), mainland source blocked / exempt passes and still hits the next rule / non-CN passes, SOCKS upstream via pinned hostname, node caps through the proxy = direct, traffic counted |
| `TestFLVXEgressE2EWithFLVXClient` + `TestFLVXEgressE2EEnvelopeRealCore` | end to end with FLVX's own code: a driver built from FLVX `go-backend` (all eight presets, `NormalizeProfile`, `Compile`, `ExemptSources`, the `landingcustom` HTTP client) against Custom's real v1 handlers over HTTP: no capability → 422; capability → 13-rule / 4-outbound plan accepted; status parsed by FLVX; private upstream → 422; policy off → `allowed:false` + 422. Custom's signed envelope then passes the Helper's signature check and applies hot on the real Core (0.68 s, every preset geo list) |
| `TestFLVXEgressResourceProfile` | numbers above |
| `tests/visual/flvx-landing-egress.mjs` | 32 screenshots, overview pixel-identical, 13 px floor, toggle save/status-only edits |

The FLVX driver is a throwaway program placed in a copy of FLVX's
`go-backend` (its `internal` packages are module-private); source:
`runs/landing-egress-mmwx/evidence/flvx-e2e-driver.go`
(`MMWXC_TEST_FLVX_E2E_BIN`, then `MMWXC_TEST_FLVX_ENVELOPE=<evidence>/e2e-envelope.json`).

FLVX-side follow-up seen in this test: FLVX's client maps unknown 422 fields
to 「配置不符合共享约束」, so an `egress.*` refusal shows no hint that the
routing plan was the problem. Mapping fields starting with `egress` to
「出站与路由」 would help.

Run the opt-in suites as root:
`MMWXC_TEST_FLVX_CORE=<v1.4.59 core> MMWXC_TEST_GEO_DIR=<dir with the pinned files>
MMWXC_TEST_FLVX_NETNS=1 MMWXC_TEST_FLVX_RESOURCE=1 go test ./cmd/mmwxc-helper -run FLVXEgress -v`.

## Release notes for the release task

- Custom: next version after the in-flight v1.4.64 (contains the API, Helper
  v0.7.6 artifacts and the UI).
- Helper v0.7.6 only needs to reach the shared server(s) (today server 7
  「Breadcloud US 年付」). Upgrading the Helper does not restart the FLVX Core;
  the first non-default plan restarts it once (FLVX users there drop for
  1–3 s), later plan changes are hot.
- Core: unchanged (v1.4.59 `8f5b614` behaviour verified).
- Geo data: downloaded by the Helper on first use (~27 MB, both files) when a
  plan references `geoip:`/`geosite:`.
