# FLVX per-user limits on shared servers

Custom + Helper side of the FLVX contract `landing-user-limits-v1`
(`/root/flvx-workers/contracts/landing-user-limits-v1.md`, FLVX plans 158 / 159).
FLVX counts a user's inbound connections and online IPs on shared servers in
the user's FLVX-wide totals, can cap the user's 入站连接数 per shared server and
can give the user one 速率 for all of their nodes on that server. Only the FLVX
shared Core (`mmwxc-flvx-core`) and the FLVX node ports are touched; mmwx's own
Core, its inbounds, users and connection settings never are.

Status: implemented on branch `feat/flvx-landing-user-limits`, not released.
No Core change: the pinned Cores already have management groups (verified with
2bf03c9 and 8f5b614).

## Wire changes (all optional, old peers unaffected)

| Where | Field | Meaning |
| --- | --- | --- |
| Helper capabilities | `flvx.user_limits.v1` | groups + 入站 node limits + usage |
| Helper capabilities | `flvx.user_rate.v1` | only while `nft` and the kernel take the rate table (probed once) |
| `GET /shares` item | `user_limits: {"versions": [1]}` / `[1, 2]` | only while the share's Helper reports the capabilities |
| `PUT /shares/{id}/desired` node | `conn_mode: "inbound"` | validated (`""`, `total`, `inbound`); stripped for Helpers without the capability |
| `PUT /shares/{id}/user-limits` | contract §4 / §7.2 body | 404 / 409 (`share_inactive`, `user_limits_unsupported`) / 422; stale sequence → 200 with the stored state |
| node status | `inbound_connections` | Σ `inbound_current` of the node's tag |
| Custom → Helper upload answer | `flvx_user_limits` envelope | signed with `mmwxc-flvx-user-limits-v1\n`, expiry = last FLVX exchange + 10 min, relay sources = IP literals of `flvx_node_hosts` + loopback; sent only while the Helper does not hold that sequence |
| Helper → Custom upload | `flvx_user_limits` usage | held / applied sequence, per-owner usage; memory only on both sides |

Budgets never enter the share row, the signed desired payload or its version.

## Helper mapping

Per owner with a budget: Core management group `flvx-user-<owner.id>`, identity
`{tag, tag}` of each of the owner's admitted nodes. Limits as FLVX's agent
(`go-gost/x/landing/user_limits.go`): connections `min(budget + live relays,
server cap)`, IPs `budget + max(relay IPs seen, reserve)`; 0 with nothing open
blocks the identities, otherwise 1. All in the pass's existing `PUT /v1/config`.

The Core binds a lease to its group when it admits it, so leases opened before
the owner's mapping existed (release day, budgets re-appearing) are missing from
the group counters. The Helper reads the owner's usage from its identities and
takes the uncounted leases / sources off the limits it gives the Core (never
blocking while anything is open). Steady state equals the contract formulas.

## 速率 (version 2)

The Core has no bandwidth limiter, so the Helper polices in the kernel (contract
§7.4 alternative): table `inet mmwxc_flvx_rate`, one named byte `limit` per owner
and direction, shared by rules matching that owner's live FLVX node ports
(upload: input by destination port, download: output by source port; TCP, and
UDP for SS2022 / Mieru). No burst is set, so the bucket holds one second of the
rate (FLVX's own limiter allows the same). The table is replaced atomically and
only when its content changes; its presence is re-checked once a minute while it
exists; it is removed when no rate is in force, on budget expiry, on share
removal and by the uninstall script.

Known limit: the total per owner is exact, but the split between one owner's
concurrent downloads is uneven (drops on locally sent packets make the Linux
TCP sender back off); one connection can get little for a while. A Core-level
group limiter (contract §7.4 recommended path) would shape fairly.

## Tests

- `internal/flvx`: wire format, validation, signatures, relay sources.
- Helper unit tests: conn_mode mapping, formulas incl. relay add-back, server
  cap, block, reserve and uncounted leases, memory-only budgets, expiry, nft
  rendering and call counts.
- Custom (PostgreSQL): capability, conn_mode strip, endpoint errors, sequence
  rule, delivery, usage round trip, report merge.
- `TestFLVXUserLimitsNetnsE2E` (root, `MMWXC_TEST_FLVX_CORE_FIXED`): the real
  Core through the Helper's reconcile in three namespaces.
- `TestFLVXUserLimitsPanelE2E` (+ root package `TestFLVXUserLimitsLabServer`):
  a real FLVX panel, this Custom and the Helper code end to end.
- `BenchmarkFLVXUserLimitsPass`, `TestFLVXUserLimitsCoreCost`: cost per pass.
