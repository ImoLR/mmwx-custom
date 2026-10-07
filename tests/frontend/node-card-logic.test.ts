import test from "node:test";
import assert from "node:assert/strict";
import { externalNodeSource, nodeRoutingReadResult, nodeTunnelChain, nodeTunnels, resolveWholeOutbound, wholeOutboundRule } from "../../frontend/src/node-card-logic.ts";

test("routing failures identify the server, failed read and upstream reason", () => {
  const server = { id: 12, name: "Boil Hinet 158" };
  const rules = { status: "fulfilled", value: { success: true, routing: { rules: [] } } } as const;
  const outbounds = { status: "fulfilled", value: { success: true, outbounds: [] } } as const;
  const rejected = { status: "rejected", reason: new Error("HTTP 502: Agent timeout") } as const;
  const result = nodeRoutingReadResult(server, rejected, outbounds);
  assert.equal(result.state, undefined);
  assert.match(result.error, /Boil Hinet 158（#12）.*路由：HTTP 502: Agent timeout/);
  const both = nodeRoutingReadResult(server, rejected, { status: "fulfilled", value: { success: false, message: "Agent disconnected" } });
  assert.match(both.error, /路由：.*出站：Agent disconnected/);
  assert.equal(both.state, undefined);
  assert.deepEqual(nodeRoutingReadResult(server, rules, outbounds), { state: { rules: [], outbounds: [] }, error: "" });
});
const server = { id: 1, name: "entry", domain: "entry.example", ip_address: "192.0.2.1" };
const node = { id: 1, node_name: "source", original_server: "entry", inbound_tag: "in", clash_config: JSON.stringify({ server: "192.0.2.1", port: 443 }) };
test("whole outbound uses first unconstrained dedicated rule and hides built-ins", () => {
  const rules = [{ inboundTag: ["in"], domain: ["example.com"], outboundTag: "specific" }, { inboundTag: ["in"], outboundTag: "direct" }, { inboundTag: ["in"], outboundTag: "late" }];
  assert.equal(wholeOutboundRule(rules, "in")?.outboundTag, "direct");
  assert.equal(resolveWholeOutbound(node, { rules }, [], [], []), null);
  for (const field of ["source", "sourcePort", "network", "user", "attrs", "protocol", "ip", "port"]) assert.equal(wholeOutboundRule([{ inboundTag: ["in"], outboundTag: "target", [field]: "x" }], "in"), undefined);
});
test("whole outbound resolves landing tags and balancer prefix members", () => {
  const target = { id: 2, node_name: "landing" };
  const state = { rules: [{ inboundTag: ["in"], balancerTag: "pool" }], balancers: [{ tag: "pool", selector: ["landing-"] }], outbounds: [{ tag: "landing-node-1-target-2-a" }, { tag: "landing-node-1-target-2-b" }, { tag: "direct" }] };
  assert.deepEqual(resolveWholeOutbound(node, state, [target], [], []), { tag: "pool", label: "负载均衡:pool", members: ["landing"] });
  assert.equal(resolveWholeOutbound({ ...node, node_type: "routed" }, state, [target], [], []), null);
});
test("tunnels map by destination aliases and retain relay original target", () => {
  const tunnels = [{ kind: "inbound" as const, tag: "tunnel-a", server_id: 2, server_name: "relay", target_address: "entry.example", target_port: 443 }];
  assert.deepEqual(nodeTunnels(node, tunnels, [server]), tunnels);
  assert.deepEqual(nodeTunnels({ ...node, relay_orig_server: "192.0.2.1", relay_orig_port: 443, clash_config: JSON.stringify({ server: "relay.example", port: 8888 }) }, tunnels, [server]), tunnels);
  assert.deepEqual(nodeTunnels({ ...node, clash_config: JSON.stringify({ server: "192.0.2.1", port: 80 }) }, tunnels, [server]), []);
});
test("chain mapping disambiguates entry port and address, never picks ambiguous chains", () => {
  const relayed = { ...node, relay_orig_server: "target", relay_orig_port: 1080 };
  const chains = [1, 2].map((id) => ({ label: String(id), entry_server: id, entry_port: 443, final_target: "target:1080", hops: [] }));
  assert.equal(nodeTunnelChain(relayed, chains, [server])?.label, "1");
  assert.equal(nodeTunnelChain(relayed, chains, []), null);
});
test("external source uses official first tag and omits managed nodes", () => {
  assert.equal(externalNodeSource({ id: 3, node_name: "external", tags: ["subscription", "other"] }, []), "📥 外部:subscription");
  assert.equal(externalNodeSource({ ...node, tags: ["managed"] }, [server]), "");
  assert.equal(externalNodeSource({ id: 4, node_name: "historical", tags: ["old-source"], parsed_config: JSON.stringify({ server: "entry.example" }) }, [server]), "");
  assert.equal(externalNodeSource({ id: 5, node_name: "invalid", tags: ["subscription"], parsed_config: "invalid" }, [server]), "📥 外部:subscription");
});
