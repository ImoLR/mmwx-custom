# FLVX VLESS Encryption landings on shared servers

Custom + Helper side of landing protocol `vless_enc` (FLVX plan 253, task
`landing-vless-enc`), one more landing-protocols-v1 protocol next to AnyTLS /
Snell / Mieru. VLESS Encryption is Xray's `mlkem768x25519plus` (Xray 25.8+):
VLESS on raw TCP without TLS / Reality, the handshake ML-KEM-768 + X25519
(forward secrecy, 0-RTT tickets, anti-replay). FLVX generates the keys at
node create; the Helper only writes the inbound. Only the FLVX shared Core
(`mmwxc-flvx-core`, fork f6f5844) is involved.

## Wire changes (all optional, old peers unaffected)

| Where | Field | Meaning |
| --- | --- | --- |
| Helper capability | `flvx.vless_enc.v1` | the Helper takes `nodes[].vless_enc` (Helper v0.7.27) |
| Helper report | `flvx_protocols.vless_enc = {"udp": true, "versions": [1]}` | only after the installed Core build passed the config test below |
| `GET /shares` item | `protocol_caps.protocols.vless_enc = {"udp": true, "versions": [1]}` | only while the share's Helper has `flvx.protocols.v1` **and** `flvx.vless_enc.v1` and reports the cap |
| `PUT /shares/{id}/desired` | `nodes[].protocol: "vless_enc"` + `nodes[].vless_enc: {"uuid", "flow", "decryption"}` | see validation; 422 `protocol` without the cap |
| admin share `helper` | `flvx_vless_enc` | the Helper's capability (operator page / checks only) |

`vless_enc` is `omitempty`: the payload of a share without such nodes is
byte-identical for every Helper (`TestFLVXVLESSEncPostgres`).

## Validation (Custom on PUT, Helper again before rendering)

- `uuid`: canonical UUID; `flow`: `""` or `xtls-rprx-vision`.
- `decryption`: exactly `mlkem768x25519plus.<native|xorpub|random>.<ticket>.<key>`
  (what `xray vlessenc` prints): ticket `600s`, `0s` or `300-600s`; one key,
  canonical unpadded base64url of 32 bytes (X25519 private key) or 64 bytes
  (ML-KEM-768 seed); no padding segments, ≤ 256 bytes. The string holds the
  server key: errors never echo it, it is never logged or returned to FLVX or
  the operator page.
- The `vless_enc` object exclusively; a cap whose `versions` lack 1 refuses it.
- Ports: TCP only (UDP rides the VLESS stream), so only a busy TCP port
  blocks the node.

## Rendering

```json
{"tag": "flvxl-<id>", "listen": "0.0.0.0", "port": <port>, "protocol": "vless",
 "settings": {"decryption": "<decryption>", "clients": [{"id": "<uuid>", "email": "flvxl-<id>", "flow": "<flow, omitted when empty>"}]},
 "streamSettings": {"network": "tcp", "security": "none"},
 "sniffing": <same as every FLVX node>}
```

No `fallbacks` (the Core refuses them with `decryption`). Tag, stats, node /
user limits, conn caps, online-IP caps, scan guard and egress routing work by
the inbound tag / client email exactly as for the other node types. The Core's
built-in freedom rule refuses private and reserved targets for VLESS inbounds,
as for every SS2022 / VLESS Reality node.

## Capability detection (Helper)

Once per FLVX Core build (artifact SHA256), when `/v1/features` is read on a
new Core start: `xray run -test -config stdin:` with one rendered vless_enc
inbound (throwaway key, nothing bound, output discarded). Exit 0 = supported,
a config rejection = unsupported; both are persisted with the build hash, so a
Core or Helper restart does not test again. A run that could not finish
(timeout, signal) is retried on the next Core / Helper start. Cost: one
~15 ms process per build. A Core without `/v1/features` is never tested and
advertises nothing.

## Lab (`TestFLVXVLESSEncNetnsE2E`, real f6f5844 Core in a netns)

Xray client (same build) random + Vision, native + Vision, random without
flow, and mihomo v1.19.32 random + Vision: TCP 2 MiB byte-exact, UDP to two
targets in one association, traffic billed both ways, online-IP cap 2,
connection cap 6, disable cuts the live stream and re-enable restores, no Core
restart. Xray clients without Vision send UDP to ports 53 / 443 as one plain
VLESS UDP flow (Xray behaviour), so the lab uses 5353.
