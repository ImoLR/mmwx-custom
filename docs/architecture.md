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
  active/NEW accounting, rejection, and session-owned CLOSE_WAIT cleanup.
- `mmwxc-helper` reads Linux `/proc/net/tcp*`, aggregates inbound ports and
  online source IPs, persists the last controller settings, reapplies them
  after Core reconnects, and optionally owns one dedicated nftables table for
  inbound online-IP slots.
- The Custom API persists desired settings and stores the latest Helper
  snapshot. The Custom UI only talks to this API.

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

System TCP states and Xray user-level physical sockets are intentionally kept
separate. A Linux TIME_WAIT entry is never presented as a user-owned outbound
socket. IPv4 online identities use the exact address; IPv6 identities use a
masked `/64` so privacy addresses from one delegated client prefix do not
consume independent slots. Per-user IP enforcement is disabled for an inbound
port shared by multiple authenticated users because the firewall cannot know
the Xray-authenticated identity.

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
