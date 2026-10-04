# Traffic accounting isolation investigation

This investigation reproduces issues 2 and 3 from `pkg-traffic-e2e-3` using
synthetic credentials and local traffic only. It does not change cycle-start
timezone handling or access production.

The baseline server is Core `6547fc9`, built from source with
`GOMAXPROCS=2 go build -p 1 ./main`. The official client is Xray v26.3.27,
commit `d2758a0`. Its release ZIP SHA256 is
`23cd9af937744d97776ee35ecad4972cf4b2109d1e0fe6be9930467608f7c8ae`,
matching both the E2E artifact record and the official `.dgst`.

`tests/audit/traffic_accounting.py` creates a local HTTP file target, a server
with SS2022/VLESS/Trojan inbounds, and a SOCKS client. All listeners bind
`127.0.0.1:28100–28122`. The SS2022 inbound uses AES-128-GCM with three synthetic
users, matching A65's multi-user shape. The script expects `bin/fork-xray` and
`bin/official-xray` in `$MMWXC_TRAFFIC_ARTIFACTS` (default
`/root/mmwx-custom-artifacts/traffic-accounting`).

```sh
python3 tests/audit/traffic_accounting.py serve
# In a second terminal, while no other collector resets counters:
python3 tests/audit/traffic_accounting.py meter --name ss-fast-client
python3 tests/audit/traffic_accounting.py meter --name ss-reset --reset --interval 0.01
python3 tests/audit/traffic_accounting.py meter --name ss-slow --rate 5242880
python3 tests/audit/traffic_accounting.py meter --name vless --protocol vless
python3 tests/audit/traffic_accounting.py meter --name trojan --protocol trojan
```

SIGTERM or Ctrl-C on `serve` stops its exact child PIDs, HTTP server, and Unix
control socket. Each measurement records client payload, gRPC counters,
non-destructive metrics, poll timestamps, and HTTP target byte counts.
Reset mode sums every atomic reset response plus the final residual. Do not
run reset measurements alongside a collector that expects cumulative values.

First baseline: three successful 50 MiB SS2022 downloads received 157,286,400
payload bytes. Core user downlink and metrics both reported 157,286,946 bytes
(546 bytes of HTTP headers); inbound downlink was 157,450,541 bytes including
encrypted transport overhead. This case reproduced no Core under-count.

All seven completed Core measurements received 157,286,400 payload bytes each:

| Case | Poll interval | User downlink | Inbound downlink |
| --- | --- | ---: | ---: |
| SS2022, fast, client closes | 200 ms, cumulative | 157,286,946 | 157,450,541 |
| SS2022, fast, target closes | 200 ms, cumulative | 157,286,931 | 157,450,594 |
| SS2022, fast | 10 ms, reset | 157,286,946 | 157,450,541 |
| SS2022, 5 MiB/s | 200 ms, reset | 157,286,946 | 157,465,433 |
| SS2022, 5 MiB/s | 50 ms, cumulative | 157,286,946 | 157,469,683 |
| VLESS, fast | 10 ms, reset | 157,286,946 | 157,278,390 |
| Trojan, fast | 10 ms, reset | 157,286,946 | 157,286,946 |

Every user counter equals the HTTP body plus response headers. Server-close
headers are five bytes shorter per request. Inbound counters measure another
layer and must not replace the user counter: encrypted SS2022 includes
overhead, and the VLESS control case also showed a small inbound discrepancy
that does not explain the SS2022 user-ledger loss. It is recorded, not fixed.
Raw samples and assertions are in `meter/*-result.json` and
`meter/matrix-summary.json` in the artifact directory.

The source-built Helper v0.6.8 `-print` diagnostic successfully read Core API
v7. It reports connections, identities and enforcement state, not user byte
counters, and does not call the destructive Stats API. The diagnostic did not
run the Helper's lifecycle/firewall reconciliation or upload loop.

Validation passed, run serially with `GOMAXPROCS=2 go test -p 1`:
Core `./app/stats/... ./app/dispatcher ./common/singbridge
./common/mmwxcustom/connection`; Custom `./cmd/mmwxc-helper`.

## Official ledger: first observation becomes the baseline

The official v0.5.5 backend reproduced permanent loss of a new identity's
first nonzero cumulative sample. It stored that sample in `last_downlink`
while recording zero usage. Later samples contributed only their increments.
The backend is closed source: its internal file and line cannot be identified
from this experiment. The responsibility is narrowed to initial cumulative
sample processing behind `POST /api/remote/traffic`, not Custom's group formula.
The executed SQL in `official/first-seen-insert-proof.txt:38–39` (original
PostgreSQL log line 11959) inserts zero raw/weighted usage and initializes
`last_downlink` from the supplied 25,952,438-byte value. This is evidence from
the backend's own SQL, rather than an inferred implementation in the harness.

A real official Agent v0.9.6 fetched the source-built Core metrics. The harness
read `GET /api/child/traffic` and forwarded its unchanged `stats` to the official
backend endpoint under two isolated test-server identities. This exercised the
real collector and accounting handler, not automatic WebSocket delivery.
The first fixture received a zero baseline; the second received its first
sample about five seconds into the same downloads:

| Layer | Zero baseline | Delayed first sample |
| --- | ---: | ---: |
| Client payload | 157,286,400 | same transfer |
| Core and Agent user downlink | 157,286,946 | 157,286,946 |
| First accepted cumulative sample | 0 | 25,952,438 |
| Official email counter (bigint) | 157,286,946 | 131,334,508 |
| Official daily raw and weighted downlink (float4 cast to float8) | 157,286,944 | 131,334,512 |

`157,286,946 - 25,952,438 = 131,334,508`. Repeated collection through 95.5 seconds
did not recover the missing first sample. The daily table adds only a few bytes
of floating-point rounding. HTTP 200 alone does not mean an immediate ledger
write; snapshots were followed until the backend had processed the samples.

Agent v0.9.6 was released before the original E2E and verified using SHA256 and
its detached Ed25519 signature. The archived E2E evidence does not identify the
deployed Agent version. `/root/projects/mmw-agent` is the older open-source
0.4.6-custom.1; `/root/projects/mmwx-agent-current` supplies the closed-source
release tooling. Neither directory establishes exact production binary parity.

Original A65 ledger updates fit approximately 30-second collection windows.
Its first small probe occurred after the previous window; the next window fell
inside the first 50 MiB download. This supports the same first-sample mechanism
as the best explanation of the original 26,929,010-byte minimum gap. Original
Core samples and Agent payloads are absent, so it is not a byte-for-byte proof
of the production transaction. The 17% gap is not a fixed correction factor.

Evidence: `official/real-collection.jsonl`, `official/daily-exact.json`,
`collection-review.md`, and `e2e-hypothesis.md`. The official backend exposes a
wildcard HTTP listener; it was confined to a Docker
`--network none` namespace containing only loopback, with no published host
ports. The Agent and PostgreSQL bound 127.0.0.1 there. A Unix socket bridge carried only the
local Core metrics into that namespace. No existing harness data was reused.

## SS2022 EOF: client-side close propagation

The fork server calls full `net.Conn.Close()` for blocked inbound identities
(`common/mmwxcustom/connection/tracker.go:595–596,1674–1675`). Packet captures
showed FIN within about 0.3–1.1 ms of the local block action and the active
inbound count became zero. This is not a server-only `CloseWrite()` path.

Official Xray v26.3.27 `proxy/shadowsocks_2022/outbound.go:126` uses
`singbridge.CopyConn`. Its `common/singbridge/pipe.go:33–35` implements `Close()`
as a no-op, and line 51 limits the still-waiting read to 300 seconds. The sing
copy task waits for both directions. The SS2022 client does not propagate the
remote close to the application's SOCKS connection until that upload read
times out.

| Control case | Request to EOF | Block/target close to EOF |
| --- | ---: | ---: |
| Fork server blocks, official SS2022 client | 300.001 s | 297.995 s |
| Fork server blocks, fork SS2022 client | 300.002 s | 297.994 s |
| Fork server blocks, relay forces RST to official SS2022 client | 300.002 s | 297.997 s |
| Fork server blocks, official VLESS client | 3.009 s | 1.002 s |
| Fork server blocks, official Trojan client | 3.005 s | 1.001 s |
| Official server, target closes, official SS2022 client | 300.002 s | 298.053 s |
| Official server, target closes, fork SS2022 client | 300.002 s | 298.058 s |
| Fork server, target closes, official SS2022 client | 300.002 s | 298.058 s |

Each received 524,288 body bytes and then stopped progressing. The RST control
removed the remote socket but left the application SOCKS connection established
until the same 300-second timeout. Adding server-side RST would not solve it.
The original E2E blocked about 50 seconds into a request, which explains about
250 seconds remaining before its 300.498-second completion.

The current fork's existing pipe-close fix also does not solve its SOCKS
client case: `proxy/socks/server.go:163–165` creates a direct link whose
`BufferToBytesWriter` has no close method, and `WrapLink` adds a
`TimeoutWrapperReader` without interruption forwarding. Therefore this
investigation does not recommend the current fork as a verified client fix.
Official-server controls use target close because upstream has no Custom
block API; they do not claim to exercise that API on upstream.

Evidence and repeatable script: `close-review.md`, `close/run_close.py`,
per-case `result.json`, `ss.jsonl`, and small `control.pcap` files. A preliminary
run with modified 600-second client policy and failed capture was explicitly
discarded; its manually terminated EOFs are excluded from these results.

## Disposition

No Custom or server-side Helper/Core product fix is warranted by these two reproductions.
No timezone code, dependencies, releases, or production state were changed.
Core's original `custom-connection-control` remains at `6547fc9`; the separate
investigation worktree has no Core changes. There is no server upgrade to ship.

Propose an official accounting fix that distinguishes a newly provisioned
identity from adoption of an existing cumulative counter. A small authenticated
warm-up followed by confirmed collection before bulk traffic can bound the
first lost sample; this remains an operational proposal, not an implemented
production change. Do not multiply usage by 1.17 or reset live counters.
For EOF latency, submit the client close-propagation reproduction upstream or
choose a client after the same test confirms the behavior. Server-only upgrades
are insufficient for a bug in the local client.

Full evidence and the final Chinese report are saved outside the repository:
`/root/mmwx-custom-artifacts/traffic-accounting/` and
`/root/mmwx-workers/runs/traffic-accounting-investigation/summary.md`.
