# FLVX 共享 CPU / 内存 「不限制」

User decision 2026-11-06 (after the FLVX ab-bench P0: the shared FLVX Core,
`mmwxc-flvx-core.service` at MemoryMax=128M, was OOM-killed on 「Boil Hinet 158」
and every FLVX user there dropped for 9 s): shared servers get no CPU / memory
limit for the FLVX Core, and new shares start without one. Only the FLVX shared
Core is involved; mmwx's own Core, the official xray and the official agent
never are.

## Meaning

Share config `memory_mb` / `cpu_percent`:

| Value | Unit file | Running Core (`set-property --runtime`) |
| --- | --- | --- |
| `0` (or omitted) = 不限制 | no `MemoryMax=` / `CPUQuota=` line | `MemoryMax=infinity` / `CPUQuota=` (removed) |
| `64–4096` MB / `1–400` % | `MemoryMax=<n>M` / `CPUQuota=<n>%` (byte-identical to before) | same values |

Other values are 422 `invalid_config`. Each field is independent (e.g. memory
512 MB, CPU 不限制). Before Custom v1.4.87, 0 was silently turned into 256 MB / 50 %.
A status-only update (no `port_min`) keeps the stored limits.

Changing the limits never restarts the running Core: the Helper rewrites the
unit, runs `daemon-reload` and then `set-property --runtime` (systemd keeps
earlier runtime drop-ins, including an operator's, across `daemon-reload`, so the
runtime call is what lifts them). After a reboot only the unit applies, which
has no limit lines.

## Old Helpers

| Helper | Capability | Gets for 0 |
| --- | --- | --- |
| v0.7.21+ | `flvx.core_unlimited.v1` | 0 (no limit) |
| ≤ v0.7.20 | – | 4096 MB / 400 % (`flvx.LegacyCoreLimits`), the largest values they accept |

An old Helper rejects the whole signed payload for 0 (its check is 64–4096 /
1–400) and, if it applied it, would write `MemoryMax=0M`. So Custom never
sends 0 to it. The admin share `helper` object carries `flvx_core_unlimited`;
the share dialog says when an old Helper still runs 不限制 as 4096 / 400.

## FLVX

FLVX does not send `core_memory_mb` / `core_cpu_percent` to Custom for shared
servers. `landing_runtime.go` sends them to its own native agents only, and the
shared push (`landingcustom.Desired`) has no such field. The limits are set only
in Custom's share settings. FLVX's own settings page shows 不限制 for shared servers
(FLVX task shared-core-unlimited). Contract note:
`/root/flvx-workers/contracts/share-core-unlimited-v1.md`.

## Helper (v0.7.21)

- `flvxUnitLimits` / `flvxRuntimeLimits` in `flvx_instance.go`.
- `egressMemoryCheck` already skips the per-Core cap when the limit is 0 and
  keeps the host `MemAvailable` check.
