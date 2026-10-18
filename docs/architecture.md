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
the formal lifecycle for more than 20 seconds queues one signed transactional
repair with bounded exponential backoff. Short reconnects preserve the last
settled UI state; explicit requests and recorded mode changes remain immediate.

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
`MMWXC_ADMIN_DB_CONFIG`. Core-mode transitions use the official server update
API through its Secure Channel, preserving the server's other fields. They use
the switch request's admin session, or an existing active admin session for
background repair; no official table is written directly. Its action allowlist is fixed in both
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

Helper v0.7.1 checks service cgroups before periodic systemd probes. A new
cgroup bypasses the inactive cache immediately; without one, a one-minute
negative cache uses `list-units`, which does not load stopped/garbage-collected
units. Inactive services skip MainPID queries. Enablement is read only for
rollback snapshots; Helper lifecycle actions invalidate every service cache.
Official units and the ownership drop-in contents are unchanged.

External handoff verifies an admin session before arming can stop Core. A
refused Embedded rollback preserves the current ownership/runtime; if an
External handoff already stopped its service, it resumes that External service.
Missing sessions surface as degraded intent with the administrator-session
error and the existing 1/5/15/30-minute retry backoff. Configuration-repair
backoff retains its error instead of reporting false recovery, so repeated
failures use the existing ten-minute log summary.

Server edits and mode changes echo the latest full official readback, normalize
expiry dates, and retrieve the hidden pull token separately. Redacted tokens and
computed traffic totals are excluded. The editor overlays only changed fields.
Mode-write errors reread the official database because official can commit the
mode before rejecting another field. A rejected transition or completed rollback
adopts the real mode instead of requeuing the previous switch.
Before either mode starts, Custom fills missing traffic-statistics fields while
preserving the rest of policy; otherwise Agent startup can replace user timeouts
and levels with its defaults. User inbounds, routes and credentials are retained.

Helper v0.7.4 validates rollback files before stopping services and restores only
the Agent mode from its historical snapshot, retaining current tokens and ports.
Embedded handoff disables both External services. Failed Embedded activation
attempts to resume the pinned External runtime, including when Agent restart
fails. Mode handoffs restart the serving runtime and can close existing sessions;
service-manager or filesystem failures can also prevent fallback recovery.

Helper and Core binaries are downloaded into staging, size-limited, checked by
SHA256, ELF architecture, and version output, then atomically renamed. An active
Custom Core is restarted and health checked after update; an inactive Core is
never started by installation. Core configuration is validated with Xray's
`run -test` before activation. At most two rollback files are retained for each
component.

Helper batch upgrades persist their selected servers and signed command IDs in
`helper-state.json`. An authenticated administrator previews and starts the job
through `/api/custom/helper-batch`. The controller dispatches at most two Helpers
at a time, waits up to five minutes per server, continues after failures, and can
cancel only the remaining queue. Restarting Custom resumes the same commands.
Single-server updates share the batch reservation and upgrade lock; overlapping
Helper/Core updates return HTTP 409. Batch transitions are saved together on the
five-second scheduler; unchanged jobs do not write. Progress posts across servers
share a five-second persistence limit, with a trailing flush for terminal posts.
A single-server update silent for 15 minutes with no unexpired update command is
marked failed by the same scheduler, which releases its lock; batch rows keep
their own five-minute timeout. Helper and Core versions remain independent; the
batch path never dispatches a Core update.

`helper-state.json` is written at once (temp file + rename) for tokens and
machine binding, queued, acknowledged or expired commands, results, mode-intent
and repair-state changes, observed status changes, batch steps, server deletion
and settings. An ordinary report changes only heartbeat fields (last seen, report
time, log samples, intent `updated_at`); these stay current in memory and one
background flusher writes them at most every 30 seconds (every five seconds while
a batch runs), on graceful shutdown, or with the next immediate write. A crash
can lose up to 30 seconds of heartbeat timestamps, which the next report restores.

An official reconnect with a healthy local Core retains the existing repair
logic and grace period. `waiting_official_since` records that presentation window
until recovery, allowing the UI to show a calm reconnect message for 90 seconds
and then the elapsed interruption with the last official heartbeat.

After an External handoff and during ownership reconciliation, missing API,
metrics, API inbound or routing sections are merged into the existing config.
Official system-config GET supplies the ports (defaults 46736 and 38889); its
POST is avoided because it replaces user policy. Custom preserves non-statistics
policy fields, rereads immediately before writing through the official config
API, then requests an official Core restart. Persisted activation state makes a
lost save/restart response retryable. Official has no conditional-write contract,
so simultaneous official edits after the final read remain a concurrency limit.
Helper then restarts only the External `mmw-agent` to reload its cached metrics
config. A persisted fingerprint protects all fields outside this control-plane
repair. If official retains a pending recovery, Custom accepts it only after a
new Agent report exactly matches the repaired config and the protected fields
still match both the live config and master snapshot. Unrelated recovery waits
for an administrator. Retry state prevents repeated Agent restarts while a
snapshot confirmation is pending.

Helper startup installs a 128M/7-day journald cap only when no SystemMaxUse is
configured, and a 20M/two-rotation mmw-agent rule only when absent. It reports
cap/rotation presence, journald usage and agent log size in `agent_status.logs`,
cached for one minute, with a startup baseline and unit line counts for the last
10 minutes. Journal metadata sampling is bounded to three seconds/50,000 rows;
incomplete samples are marked explicitly. Custom Core defaults empty access logs to none;
`MMWXC_CORE_ACCESS_LOG=console` or a path enables debugging without changing the
official config. Explicit access paths and error logging retain their behavior.

## Server deletion

Administrators preview the cascade and confirm the full server name. Plans and
step results persist in `mmwxc_server_deletions`; operations serialize per server,
reject identity/scope drift and resume failures without expanding the saved plan.
Completion rereads official servers, nodes and packages and checks active orphans.
Direct nodes are always deleted through the official API with `delete_nodes=true`.
Dependencies follow parent/chain/routed IDs, required forwarding hops, pinned
exits, tunnels, whole-inbound outbounds and the server's normalized addresses.
An address shared with another registered server is excluded from address-based
matching for nodes, outbounds and tunnels, with one attention line naming its
other owners. Direct nodes and explicit ID dependencies still cascade; direct
node subscription fingerprints retain host plus port, never shared-host-only matches.
Valid alternative paths survive with failed members removed; repeated-server
loops are not alternatives. Usable tunnel suffixes and partially usable routed
inbounds survive, with failed rules removed and attention notes retained.
Only packages explicitly referencing deleted nodes are trimmed; emptied packages
use the user lifecycle executor to delete/unbind while retaining user accounts.
Traffic groups are trimmed or deleted when empty, preserving remaining quotas.
Helper identities/tokens, reports, commands, connection state, ownership, retired
credentials, import cleanup tasks and live caches are cleared or retired safely.
Signed `helper.uninstall` stops/disables owned Core, removes its ownership drop-in,
confirms Core stopped, then acknowledges and self-uninstalls only MMWXC resources.
Offline/old Helpers retain warnings and manual cleanup tasks; official Agent
uninstall defaults on, and request acceptance alone does not prove shutdown.
Subscription selections are trimmed or deleted if emptied; stale `/x/` and `/t/`
caches containing retired nodes return 410 without revoking all user tokens.
Certificate material and billing/audit history remain; dependent certificate
automation stops. Unrelated historical records and user operation intent remain.

## Package node traffic groups

Groups live in `mmwxc_package_traffic_groups` (package FK, name, byte quota,
JSONB member node IDs and timestamps). `mmwxc_package_traffic_group_blocks`
records each over-quota assignment/group, username, package, cycle start,
usage when blocked and block time. Package/group/assignment/user deletion
cascades to the corresponding Custom rows. No official traffic, baseline or
`package_node_traffic_suspensions` rows are written by Custom.

Authenticated administrators use:

- `GET /api/custom/packages/{id}/traffic-groups`: `{groups: [...]}`; a group
  has `id`, `package_id`, `name`, `limit_bytes`, `node_ids`, `created_at`,
  `updated_at`.
- `PUT` at the same path: `{groups: [{id?, name, limit_bytes, node_ids}]}`
  fully replaces the groups; `[]` clears them. Existing IDs must belong to
  the package. Names are required, each group needs a node, each node can be
  in only one group, and its positive quota cannot exceed a finite package
  total. A package total of zero means unlimited and allows positive group quotas.
  Member per-node quotas cannot exceed the group quota. Empty package node
  selection means all nodes. Removed/deleted members are filtered on read
  and ignored by enforcement.
- `GET /api/custom/packages/{id}/traffic-groups/usage`: `{usage: [...],
  nodes: [...]}`. Each usage row contains `username`, `assignment_id`,
  `group_id`, `group_name`, `used_bytes`, `limit_bytes`, `blocked`,
  `cycle_start`, nullable `cycle_end`, and `nodes`. A node has `node_id`,
  `node_name`, `server_name`, `status`, and optional `reason`. The top-level
  nodes describe package capabilities even before a user is bound.

The editor saves the official package first, then replaces its Custom groups.
These two API writes are not a cross-service transaction; failed group saves
remain visible and can be retried using the already-created package ID.
The UI displays GB as `2^30` bytes, truncating fractional bytes on save.

The controller evaluates active assignments on startup and approximately
every 60 seconds (also after group edits and when reading usage). Reaching
the sum quota creates a desired block; new cycles, changed quotas/members,
and unbinding cause reevaluation and removal. Package total and official
per-node limits continue independently. Quota transitions are logged as
`over-quota` / `over-quota cleared`. Changes to the desired identity lists are
logged separately as `desired block` / `desired unblock`, pending Helper/Core
application; neither log is execution confirmation.
`blocked` in the usage view means an over-quota group has at least one safe,
capable node selected for blocking; it does not claim every member is enforced.

Identity resolution uses current Core configuration, existing ownership,
assignment-specific inbound credentials, and routed-node subaccounts.
Empty identities, ambiguous/shared authentication, other users sharing the
identity, and identities potentially affecting the user's group-external
nodes are skipped. Unresolved or stale ownership is conservatively reported
as `no_identity`; a shared single-secret inbound is never blocked as a group.

A legacy user credential sharing an inbound with a group-external alias only
prevents blocking when that user is entitled to the alias, or entitlement is
uncertain. Entitlements include every active package assignment, the legacy
`users.package_id` binding (empty node lists mean all nodes), node ownership,
active routed subaccounts, and forward-chain node ownership/billing bindings.
File subscriptions/authorship and same-inbound user outbound relations retain
the conservative refusal because their node scope is not reliably known.
Missing entitlement tables/columns or invalid package node lists fail closed;
another user's alias with a distinct identity does not alone prevent blocking.

Computed identities are merged into the existing
`POST /api/custom/agent/connections` desired settings response only for
Helper >= v0.6.8. Admin connection-settings PUT cannot edit this field and it
is never saved into administrator settings. Helper persists the last list,
resends it through Core `PUT /v1/config`, and reports
`snapshot.core.traffic_block_supported`. Core v7 must accept the complete
configuration before this capability is true. Older Core retries preserve
supported settings without blocks and report false. Explicit `[]` removes
blocks; missing controller data preserves the Helper's previous list. After
controller restart or a database refresh failure, unblocking waits for a
successful evaluation rather than silently clearing persisted blocks.

Node statuses are `enforced`, `embedded`, `core_outdated`, `helper_outdated`,
`external_node`, or `no_identity`. Embedded, unmanaged external nodes, old
Core/Helper and unresolved identities cannot enforce the quota and are shown
as warnings in the editor/usage view. Offline or unconfirmed capabilities
also have explanatory reasons. The official collection interval, controller
minute loop and Helper report/config application allow an overshoot window.

Core v7 closes authenticated tracked inbound connections and rejects new
admissions. Existing native UDP/untracked sessions may not terminate
immediately. Mux has no per-logical-stream termination guarantee on a shared
physical connection; tracked mux carrier connections close with their
inbound. Unblocking permits reconnection and cannot restore closed sockets.

### User access lifecycle and traffic groups

Custom persists disabled intent in `mmwxc_user_lifecycle.desired_state`.
On External servers with a fresh Helper report confirming traffic-block
support, disable records client `(inbound_tag, email)` identities in
`mmwxc_user_disable_blocks`, without changing credentials. The email comes
from Core clients or official credential records, never `users.email`.
Assignment credentials, routed subaccounts, package node entitlements and
owned nodes are included. Shared authentication, another user's ambiguous
entitlement and administrator default credentials are conflicts and are
never blocked. SOCKS/HTTP accounts and clients without a Core email cannot
use this block path.

The minute traffic-group loop also reconciles disabled users. New identities
are added after official node/package/assignment changes. The official
inactive API may remove clients from the current configuration, so recorded
official credentials and persisted disable identities remain usable for
blocking a later re-push. Persisted identities are rechecked for ownership
conflicts. A stale Helper capability report preserves existing safe blocks.
The Helper receives the union of traffic-group and user-disable sources;
enabling removes only the user's source, and a group cycle reset removes
only the group's source. Failed evaluations omit a replacement list so the
Helper retains its last persisted list.

Custom also calls the official user page's status operation
(`POST /api/v3`, op `4b18ad3836973389`) to set the official account inactive.
The prior state is saved as `official_was_active` in the Custom lifecycle
row before the first disable. Enable restores that state; an account already
inactive before Custom disabled it stays inactive. Because the official inactive
API also removes clients, Custom skips that call when any access item is
conflicting or unverified, preserving a partial state with an explicit reason
instead of risking another user’s shared credential. Background migration
respects both cached and freshly detected conflicts. No official account table
or schema is written directly. The local v0.5.5 harness still serves inactive
users' `/x/` subscriptions, which do not traverse this API proxy. Subscription
downloads are therefore not blocked by Custom.

Unsupported servers and credentials keep the previous one-time random
credential swap, with original/replacement backups in
`mmwxc_user_disabled_credentials`. There is no periodic re-swap. The user
card and disable preview identify these nodes as best effort and explain
that official renewals, package changes or new nodes can restore access.
Enable leaves never-swapped credentials unchanged, restores backed-up
credentials only after drift checks, and retires successfully restored
backups. A process-local server/inbound lock serializes configuration
read/modify/write/verify across different users. Replanning preserves
unrelated users' concurrent credential changes while rejecting target or
inbound-shape drift. The official replacement API has no demonstrated
conditional write contract, so writes by the official controller itself
remain outside this process lock.

Existing disabled rows need no destructive data migration. Supported
identities are blocked first; restoration of legacy swapped credentials
waits for a fresh Helper snapshot confirming those blocks. The background
migration uses an existing unexpired official administrator session for
required official API calls. Without one, blocks and backups remain in place
until an administrator session becomes available. Unsupported backups remain
swapped. For an inactive account whose client was removed by the official
API, a backup can be retired only if its exact original is still recorded
by the official credential relation. The prior official account state is
captured when a legacy row is first migrated.

`GET /api/custom/users/{username}/access` previews the disable scope without
persisting changes. Lifecycle responses include per-node `blocked`,
`pending`, `best_effort` and `conflict` states. A desired block is shown as
pending until the Helper snapshot confirms it; HTTP success alone does not
claim that the runtime applied it. Existing Core limitations for native UDP,
untracked sessions and multiplexed connections remain unchanged.

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

## FLVX 共享

An operator can share a server with FLVX from the **FLVX 共享** navigation
entry after **转发管理**. Sharing does not create or modify official nodes,
users, packages, subscriptions, or the official Core configuration. The service
card's **已共享给 FLVX** badge opens this page. The page groups FLVX's structured
node metadata by server or owner; protocol credentials are never returned to
the operator list. Existing pages retain their layouts.

### Persistence and API v1

The official PostgreSQL database contains three additive Custom-owned tables:
`mmwxc_flvx_integration` (one SHA-256 integration-token hash, last authenticated
API call and the latest landing-host report), `mmwxc_flvx_shares` (share config,
latest desired revision/nodes, Helper acknowledgements and latest aggregate
status), and `mmwxc_flvx_counters` (per-node persistent cursors, lifetime totals
and removed-node tombstones). A transaction advisory lock serializes revisions,
counter merges, host reports and operator edits. Official server addresses and
current inbound snapshots are read only.

`/api/custom/flvx/v1/*` requires `Authorization: Bearer <FLVX token>`; operator
sessions, Helper credentials and `MMWXC_API_TOKEN` do not authorize this API.
The token is generated on the sharing page, returned once, and can be revoked
or replaced. Responses disable caching. The v1 wire contract is plan 101 §H:

| Method and relative path | Request / response |
| --- | --- |
| `GET /shares` | `{shares:[{server_id,name,hosts,status,port_min,port_max,traffic_limit_bytes,traffic_used_bytes,reset_day,max_connections,connections,share_until,core,protocols,dual_install}]}` |
| `PUT /shares/{server_id}/desired` | `{revision,nodes:[{id,owner,name,protocol,port,enabled,max_connections,max_online_ips,expires_at,ss2022?,vless?}]}` → `{accepted_revision}` |
| `GET /shares/{server_id}/status` | `{applied_revision,core:{state,epoch},nodes:[{id,state,error?,up_bytes,down_bytes,connections}],share_traffic_used_bytes,collected_at}` |
| `PUT /landing-hosts` | `{native_landing_hosts:[],flvx_node_hosts:[]}` → `{}` |

Desired state is a complete replacement. An identical revision retry is
idempotent; stale or changed equal revisions return 409. Validation returns
`422 {errors:[{node_id,field,message}]}`; other errors use
`{error:{code,message}}`. Unknown JSON fields, raw inbound configuration,
duplicate node IDs/ports, official port collisions, invalid credentials and
unsupported protocols are rejected. Only the §D SS2022 AES-128 template and
VLESS Reality Vision are supported. The Helper validates again before applying.

The operator endpoints use the existing operator authorization:
`GET /api/custom/flvx/admin`, `POST`/`DELETE /api/custom/flvx/admin/token`, and
`PUT /api/custom/flvx/admin/shares/{server_id}` with share config and status
`active`, `paused` or `revoked`. A status-only update retains the config.
Cancellation is terminal and the UI requires typing the server name. Paused
and revoked shares reject FLVX desired pushes. Hostnames are resolved before
comparison: an FLVX ordinary node match warns, while a native landing match
refuses sharing. Unresolved addresses refuse new/resumed shares and desired
pushes, but a server-domain lookup error in a later host report leaves an active
share running and remains visible as `detection_error` in the admin list.
Missing official inbound inventory fails closed. Only a positive native-host
conflict pauses an existing active share during host reporting.

### Independent Helper instance

Both Embedded and External servers use the same separate Helper manager.
Helpers advertising `flvx.share` exchange reports and HMAC-signed desired
envelopes through the existing authenticated
`POST /api/custom/agent/connections` channel. Envelopes bind the Helper's server
ID, internal version and ten-minute delivery expiry. No inbound management port
or FLVX agent is added. Reconciliation, download and retry work run outside the
official Core management loop, with rate-limited error logging and backoff.

| Resource | FLVX-only location |
| --- | --- |
| Binary | `/opt/mmwxc/flvx-core/mmwxc-flvx-core` |
| Unit / unprivileged account | `mmwxc-flvx-core.service` / `mmwxc-flvx` |
| Base config | `/etc/mmwxc/flvx-core/config.json` |
| Control socket | `/run/mmwxc-flvx/core-control.sock` |
| Persistent desired/counter state | `/var/lib/mmwxc-helper/flvx/state.json` (0600, atomic + fsync) |
| HandlerService / StatsService | Own persisted ephemeral port on `127.0.0.1` |

The binary comes from the existing Custom release cache and the same bounded,
SHA-256-verified download path as normal Core installation. A matching installed
Custom binary can be copied read-only. FLVX never replaces the official binary.
The unit defaults to `MemoryMax=256M`, `CPUQuota=50%`, and uses
`NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, an empty capability
set and a private runtime directory. Access logs are disabled; systemd and
Helper error messages are rate limited. Resource changes use systemd properties
without restarting Core. Only a binary replacement restarts this instance.

The Helper renders the fixed base config and structured inbound templates.
Its first routing rule blocks every explicit §D private/loopback/link-local/
metadata/multicast CIDR, without geoip data. Freedom uses IP resolution and the
route checks resolved addresses. Sniffed Host/SNI cannot replace the original
destination for routing. Reality camouflage destinations are resolved, rejected
if any answer is private, and pinned to a validated public IP because that dial
does not use normal outbound routing. The pin is persisted by node tag and a
hash of its structured fields, and reused across reconciles and Helper/Core
restarts. Only changed fields or a missing pin trigger resolution. If a changed
node cannot resolve safely, an already applied public pin keeps serving, with
a rate-limited warning; a new node without a valid pin reports an error.

Node diffs use gRPC AddInbound/RemoveInbound through the fixed Core CLI.
Unchanged inbounds retain existing connections. The instance's own control
socket receives the existing v7 `connectionSettings` semantics for per-node
connection/online-IP caps and the share-wide total connection cap. Inactive or
replaced identities are blocked before removing handlers, including established
connections. These counts retain the current Core total in/out socket semantics.

Inbound up/down statistics include unauthenticated bytes. The Helper persists
raw cursors and monotonic totals across Core epochs; PostgreSQL independently
deduplicates Helper generation/sequence reports and merges resets. Final counters
are sampled before removal. Material changes (including quota crossings and
acknowledgements) are persisted immediately; counter/heartbeat-only writes occur
at most once per 30 seconds. Reports and their sequence are published only after
a successful durable write, so a crash cannot roll back published lifetime
totals. The public status allows 60 seconds before marking a running report stale.
Steady reconciliation uses one Core CLI stats query; inbound listing and extra
samples are reserved for desired changes, diffs, epoch changes or error recovery.
Acknowledged removed nodes stop enlarging Helper
uploads, while their database tombstones remain available to FLVX's 60-second
poll. Share quotas reset at Taiwan midnight on the selected monthly day,
clamped at month-end. Sampling runs about every five seconds, so traffic caps
can overshoot by traffic between samples. Exceeding the total quota, expiry or
pause removes FLVX inbounds; a reset, raised quota or resume restores eligible
nodes. Cancellation stops the instance and waits for final-counter acknowledgement
before deleting its unit, config, binary and state. The service account remains.

The v1 desired schema does **not** contain a per-node traffic limit. Custom
therefore shows the node's cumulative usage and **未提供** for that limit;
FLVX must enforce its node quota by changing `enabled`. No extra v1 field is
invented. Custom independently enforces the share's total quota and all supplied
connection caps and expiry values.

The FLVX binary name deliberately contains no `xray`, preserving
`countXrayProcesses`/SingleCore detection. Official port ownership only examines
official inbound ports, which sharing excludes. External takeover, drift repair,
mode switching, machine protection and nftables code are unchanged. Tests use
fake gRPC/control transports and the real v1.4.59 Core with loopback fixtures to
check hot updates, statistics, private-address blocks and unchanged official
process/port ownership. Systemd installation and real-server integration require
the separately approved release/canary task. Cancel and await cleanup before
downgrading Helper, so an older Helper cannot leave an unmanaged FLVX unit.
