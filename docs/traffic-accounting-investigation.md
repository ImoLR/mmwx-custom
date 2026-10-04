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

Full evidence and the final Chinese report are saved outside the repository:
`/root/mmwx-custom-artifacts/traffic-accounting/` and
`/root/mmwx-workers/runs/traffic-accounting-investigation/summary.md`.
