# FLVX 禁止端口扫描 on shared servers

Custom + Helper side of contract `landing-scan-guard-v1` §9
(`/root/flvx-workers/contracts/landing-scan-guard-v1.md`). The Core part
(`scan_guard.v1`: counting, self-block, `GET /v1/scan`) is §8, Core
`f6f5844` (Custom v1.4.83). FLVX drives a shared server exactly like its own
landing agent: the same `scan_guard` object in the desired state, the same
`scan_guard` report in the status. Only the FLVX shared Core
(`mmwxc-flvx-core`) is involved; mmwx's own Core, users and settings never are.

## Wire changes (all optional, old peers unaffected)

| Where | Field | Meaning |
| --- | --- | --- |
| Helper capability | `flvx.scan_guard.v1` | applies `desired.scan_guard`, reports `status.scan_guard` |
| `GET /shares` item | `scan_guard: {"versions": [1]}` | only while the share's Helper has the capability **and** its FLVX Core build lists `scan_guard.v1` |
| `PUT /shares/{id}/desired` | `scan_guard` | FLVX's native object (enabled, identities = node tags, thresholds, web_ports, windows, seen); validated (422 `scan_guard.*`); stored with the desired state, so a change needs a new revision; stripped for Helpers without the capability |
| `GET /shares/{id}/status` | `scan_guard` | `{state, error?, hits, blocked}` as FLVX's agent reports it; hit `id` is the FLVX node id (string) |
| Helper → Custom report | `status.scan_guard.core_ready` | the installed FLVX Core build lists `scan_guard.v1` (Custom-only) |
| release metadata | `flvx_core_version`, assets `mmwxc-flvx-core-linux-*` | optional FLVX shared Core channel (「升级 FLVX Core」, new shares); absent = the release's Core |

## Helper

Per pass of a running FLVX Core (every 5 s), mirroring FLVX's
`go-gost/x/landing/scan_guard.go`:

- Only a Core build whose `/v1/features` lists `scan_guard.v1` gets
  `scan_guard` / `scan_unblock` in its `PUT /v1/config` (an older Core rejects
  the whole config). Without them the body is byte-identical to v0.7.17's. A
  Core that refuses them after all gets today's body for the rest of its
  epoch and the share reports `core_update`.
- `scan_guard.identities` = FLVX's identities that are rendered right now.
- `GET /v1/scan?after=` only while the guard is on or a Core block is still to
  be lifted; `after` lives in memory per Core epoch.
- Hits FLVX has not recorded (`seq > seen`) are kept in `state.json`
  (`scan_pending`, ≤ 256) and their tags go into `blocked_identities`, so a Core
  or Helper restart cannot lift the block before FLVX saw the hit.
- A Core block is lifted (`scan_unblock {identity, seq}`) once FLVX recorded
  the hit and the node is enabled again (解除禁用), when the node left the
  desired state, or when FLVX stops sending `scan_guard` at all.

## Release channel

`MMWXC_EXTERNAL_CORE_FROM=<tag> scripts/release.sh <new tag>` re-publishes
the Core of `<tag>` (checksums verified) as the Core External servers are
offered and ships the Core built from the Core source as
`mmwxc-flvx-core-linux-*` (`flvx_core_version`). Unset: one Core, as before.

## Tests

- `internal/flvx`: wire shape, storage round trip, validation, report bounds.
- Helper unit tests (`TestFLVXScanGuard*`): guard off byte-identical and no
  reads, full cycle incl. Helper / Core restart, unblock rules, old Core,
  refused fields, invalid guard, read failure.
- `TestFLVXScanGuardNetnsE2E` (root, `MMWXC_TEST_FLVX_CORE_SCAN`): the real
  Core through the Helper's reconcile in three namespaces.
- Custom (PostgreSQL) `TestFLVXScanGuardPostgres`, `TestReleaseFetchFLVXCoreChannel`.
