# Architecture

`mmwx-custom` is intentionally separate from the official miaomiaowuX fork.

The Custom UI runs from this repository and uses two same-origin API areas:

- `/api/*` is proxied to the configured Fork miaomiaowuX backend.
- `/api/custom/*` is handled by this repository's Custom API.

The official fork must not import this repository, proxy to it, embed its
frontend, or expose `/api/custom/*` as part of the official UI contract.

## Custom Connection Control

The connection-control feature is split across three independently owned
layers:

- The `ImoLR/Xray-core-mmwx` branch `custom-connection-control` attributes an
  authenticated inbound user to the final physical outbound TCP dial. It owns
  exact tuple/state accounting, NEW admission, rejection, and session-owned
  CLOSE_WAIT timeout cleanup.
- `mmwxc-helper` reads Linux `/proc/net/tcp*`, aggregates inbound ports and
  online source IPs, persists the last controller settings, reapplies them
  after Core reconnects, and optionally owns one dedicated nftables table for
  inbound online-IP slots.
- The Custom API persists desired settings and stores the latest Helper
  snapshot. The Custom UI only talks to this API.

Core capability and lifecycle state are independent. A dedicated Custom
lifecycle transition is marked pending only until it succeeds; afterwards the
formal controller's `xray_mode` is authoritative. Saving metrics, traffic,
gRPC, inbounds, outbounds, or routes never changes `xray_mode`. Each fresh
Helper report compares controller mode, Agent mode, service ownership, runtime
ownership, single-Core state, and Core readiness. Ownership inconsistent with
the formal lifecycle queues one signed transactional repair with bounded
exponential backoff.

External with the Fork Core is the default lifecycle. A change of the recorded
`xray_mode` that did not come from a pending Custom transition is recorded with
its previous mode and time; a change to Embedded is respected but marked
`formal_change_unconfirmed` and surfaced on the service card until an
administrator confirms Embedded or switches back to External.

Core does not contain database, public HTTP, UI, or Linux firewall management.
Its bridge is opt-in through `MMWXC_CORE_CONTROL_SOCKET` and listens only on an
owner-only Unix socket. The Helper uses `MMWXC_HELPER_CORE_SOCKET` to reach it.

The controller endpoints are:

- `POST /api/custom/agent/connections`: authenticated Helper report and desired
  settings response.
- `GET /api/custom/servers/:id/connections`: latest detailed snapshot and
  persisted settings for an authenticated administrator.
- `PUT /api/custom/servers/:id/connections`: validate and persist administrator
  settings. Empty JSON values are represented by `null` and mean unlimited or
  inherited, depending on the field.

System TCP states and Xray-owned physical sockets are intentionally kept
separate. Core API v6 reports a TIME_WAIT or CLOSE_WAIT entry only when its
exact four-tuple is still owned by one retained Core socket record; tuple reuse
is de-duplicated and unknown machine rows are never guessed into Core totals.
IPv4 online identities use the exact address; IPv6 identities use a masked
`/64` so privacy addresses from one delegated client prefix do not consume
independent slots. Per-user IP enforcement is disabled for an inbound port
shared by multiple authenticated users because the firewall cannot know the
Xray-authenticated identity.

`MMWXC_HELPER_ENABLE_NFTABLES` defaults to `false`. When explicitly enabled,
the Helper validates a complete replacement ruleset with `nft -c` before it
touches its dedicated `inet mmwxc_connection_control` table. It does not alter
other tables or sysctls.

Mux remains outside this feature. With mux enabled, a physical outbound socket
may carry multiple logical streams, so the physical socket counters must not be
described as logical user connection counts.

## Custom Agent Management

The existing five-second Helper report is also the transport for controller
commands and results. It remains outbound-only from the managed VPS. The Helper
authenticates each report with its existing bearer token; commands and results
are additionally HMAC-SHA256 signed with a key derived from that token. Commands
have unique IDs, a ten-minute expiry, and a persisted replay window.

The operator API accepts the independent `MMWXC_API_TOKEN` for server-to-server
automation. Browser requests are authorized by checking the active admin
session directly in the local PostgreSQL database identified by
`MMWXC_ADMIN_DB_CONFIG`; the controller never forwards that session to the
official miaomiaowuX Secure Channel API. Its action allowlist is fixed in both
the controller and Helper. Lifecycle code can only touch the following owned
resources:

```text
/usr/local/bin/mmwxc-helper
/etc/mmwxc-helper.env
mmwxc-helper.service
/opt/mmwxc/core/xray
/etc/mmwxc/core/config.json
mmwxc-core.service
/var/lib/mmwxc/staging
/var/lib/mmwxc/rollback
```

Helper and Core binaries are downloaded into staging, size-limited, checked by
SHA256, ELF architecture, and version output, then atomically renamed. An active
Custom Core is restarted and health checked after update; an inactive Core is
never started by installation. Core configuration is validated with Xray's
`run -test` before activation. At most two rollback files are retained for each
component.

## Package node traffic groups

### Usage formula and official v0.5.5 evidence

The official per-node quota counter is **weighted traffic**, already adjusted
when collected. It does not apply the current node multiplier a second time.
For a user and member node, the observed official query is:

```sql
SELECT COALESCE(SUM(weighted_uplink + weighted_downlink), 0)
FROM traffic_daily_user_nodes WHERE username = $1 AND node_id = $2;
SELECT baseline FROM package_user_node_traffic_baselines
WHERE username = $1 AND package_id = $2 AND node_id = $3;
```

Usage is the nonnegative difference between this all-time sum and the stored
baseline. The query has no date predicate. Both `oneway` and `twoway` use the
same weighted sum; the per-node read does not reapply the traffic mode to
those counters. Raw `uplink`/`downlink` are not a fallback when weighted
columns are zero. `node_traffic_limits` JSON values are GiB (`2^30` bytes),
whereas group limits, package limits, traffic overrides, and baselines are
bytes. `traffic_limit_override` changes the separate package total limit; it
does not rescale a node or group counter or quota.

This was measured on 2026-10-03 with the archived v0.5.5 binary, PostgreSQL
18, and the archived Secure Channel client, using only a disposable database.
PostgreSQL statement logging exposed the executed SQL and its parameters;
a test-only trigger recorded the official suspension inserts before the
official backend removed them after the deliberately offline server rejected
the remote operation. No running Core or production service was involved.
Twenty suspension-attempt assertions passed:

| Seed / change | Official result |
| --- | --- |
| Weighted upload 0.25 GiB + download 0.5 GiB, quota 0.75 GiB | Suspension attempted (equality blocks) |
| Same counters, quota 0.751 GiB, either traffic mode | No suspension |
| Same counters, current multiplier changed to 20, quota 1 GiB | No suspension; multiplier is not reapplied |
| Baseline 0.25 GiB, quota 0.6 GiB | No suspension; remaining usage is 0.5 GiB |
| Baseline above all-time sum | No suspension |
| Raw traffic 2 GiB, weighted counters zero | No suspension |
| Rows before package start, `last_reset_at`, or monthly reset date, baseline zero | Still counted by the official per-node query |
| Missing baseline, weighted sum 0.75 GiB | Official initializes baseline to 0.75 GiB; initial usage is zero |
| User total override 0.1 GiB, node quota 1 GiB, weighted sum 0.75 GiB | No per-node suspension |

Official monthly reset advances existing node baselines to their current
all-time sums and sets `last_reset_at` to the actual reset execution time.
For example, a reset-day-1 user last reset on September 1 was checked on
October 3: the baseline advanced to 800,000,000 bytes and `last_reset_at` to
October 3 12:25:35 UTC. Binding or resetting a package with no per-node quotas
did **not** create baselines for its nodes; manual reset updates existing
baseline rows only. Per-node enforcement was exercised for legacy bindings
and migrated assignments (`legacy_source = 1`); the equivalent native
assignment path (`legacy_source = 0`) could not be confirmed with this fixture.

The Custom API reads official tables without modifying them. It uses a
baseline only when its update time belongs to the current cycle. If a member
has no current baseline, it sums weighted daily rows from the effective cycle
start instead. That start respects the binding start, its `last_reset_at`, and
its monthly reset settings; a reset day beyond the end of a month is clamped.
This fallback is isolated in `packageNodeTrafficUsage` and tested. Daily rows
cannot distinguish traffic before and after a same-day bind/manual reset;
exact parity for that case, month-end resets, and downtime catch-up is not
established by the closed-source backend probes. The fallback's calendar
interpretation is explicit rather than silently returning zero forever for
nodes without official per-node quotas.

Reproduction SQL, the 20-case assertion script, observed SQL logs, and reset
results are archived at
`/root/mmwx-workers/runs/pkg-traffic-groups/evidence/`. The harness binds the
official HTTP port and PostgreSQL only to `127.0.0.1`; its named containers,
volume, and network are disposable. The official and Custom processes should
use the same local timezone for timestamp-without-time-zone columns and daily
ledger dates (the empirical harness used UTC).
