# FLVX 节点总连接数 on shared servers

Custom + Helper side of contract `landing-node-total-v1`
(`/root/flvx-workers/contracts/landing-node-total-v1.md`, FLVX plan 211).
A shared landing node can carry an 入站 limit (`max_connections` with
`conn_mode: inbound`) **and** a 节点总连接数 (every TCP socket of its port in
any state, TIME_WAIT / CLOSE_WAIT included) at the same time; whichever is hit
first refuses new connections, existing ones stay. Only the FLVX shared Core
(`mmwxc-flvx-core`) is involved; mmwx's own Core, users, packages and
入站 / 出站 logic never are.

Before this (Helper ≤ v0.7.19) FLVX could already send the total the old way
(`max_connections` without `conn_mode` = port `max_total_connections`) for nodes
without an 入站 limit; with both set it sent only the 入站 limit (「总上限待升级」).

## Wire changes (all optional, old peers unaffected)

| Where | Field | Meaning |
| --- | --- | --- |
| Helper capability | `flvx.node_total.v1` | applies `nodes[].max_total_connections`, reports `nodes[].tcp` (Helper v0.7.20) |
| `GET /shares` item | `node_total: {"versions": [1]}` | only while the share's Helper has the capability; FLVX sends the field only then |
| `PUT /shares/{id}/desired` | `nodes[].max_total_connections` | int, omitted when 0 (payload byte-identical), 1–10 000 000 (422 `max_total_connections`); stored with the desired state |
| `GET /shares/{id}/status` | `nodes[].tcp` | `{inbound, outbound, established, time_wait, close_wait}`; relayed only when `inbound + outbound == connections` and the states fit; dropped for a node no longer reported |
| admin share `helper` | `flvx_node_total` | the Helper's capability (operator page / checks only) |

## Helper (v0.7.20)

- `limits()`: after the node limit mapping (`conn_mode: inbound` → port
  `max_inbound_connections`, otherwise port `max_total_connections`),
  `max_total_connections > 0` sets the port `max_total_connections`, or the
  smaller one when the node limit already counts 入站+出站. Nodes without it
  give a `/v1/config` body byte-identical to v0.7.19's
  (`testdata/flvx-node-total-v0.7.19-config.json`, written by the same test on
  b2dbc84).
- `nodes[].tcp`: summed per node over its Core identities from the snapshot the
  pass already read (no new probe or polling): `inbound` / `outbound` =
  Σ `inbound_tcp` / `outbound_tcp` `tcp_total`, the three states summed over
  both directions. The Core's `current_total` is exactly
  `inbound_tcp + outbound_tcp`, so it matches `connections`; otherwise `tcp`
  is left out. Every Core the FLVX runtime accepts (snapshot v7) reports these
  state counts. Like `connections`, `tcp` is volatile: it does not count as a
  material change, so the report still goes out on a real change or every
  30 s and `state.json` is not written more often.

## Custom

- Forwards the field to Helpers with the capability. For an older Helper it is
  stripped (that Helper's strict signed-payload check would reject the unknown
  field) and, like FLVX does for such a runtime, kept where the old fields can
  carry it: with a 入站+出站 node limit the smaller one; without an 入站 limit
  as the node limit; next to an 入站 limit it waits for the upgrade. Never
  unlimited. Runs after the `conn_mode` strip.
- `nodes[].tcp` from the Helper report is stored with the node's counters and
  relayed in the status.

## Tests

- `internal/flvx`: wire shape (omitted when 0), validation bounds, `tcp`
  consistency rule.
- Helper: `TestFLVXNodeTotalMapsPortLimits` (inbound + total both set; no
  `conn_mode` → min; total only; untouched without it),
  `TestFLVXNodeTotalAbsentConfigUnchanged` (golden vs v0.7.19),
  `TestFLVXNodeTCPReport`, `TestFLVXNodeTCPNoExtraStateWrites`.
- Custom: `TestFLVXHelperNodeTotalStrip`, `TestFLVXNodeTotalPostgres`
  (capability, accept / 422, forward vs strip with signature check, status
  `tcp` relay / drop / removed node).
- Real Core, three namespaces, Helper reconcile (`TestFLVXNodeTotalNetnsE2E`,
  `MMWXC_TEST_FLVX_CORE_NODE_TOTAL=<FLVX Core>` as root): 总 8 old way → 4
  clients; TIME_WAIT after disconnect still counted and keeps clients out;
  CLOSE_WAIT counted; 入站 3 + 总 5 → total first (2 clients); 入站 2 +
  总 100 → inbound first; 入站+出站 10 + 总 4 → 2 clients.
