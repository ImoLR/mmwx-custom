import assert from "node:assert/strict";
import test from "node:test";
import { batchRenameTransform, chainNodePayload, chainProxyCandidates, defaultNodeIPVersion, duplicateNodeGroups, duplicateNodeKey, matchesNodeSource, moveSelectedNodes, relayGroupCandidates, relayGroupPayload, subscriptionDefaultTag } from "../../frontend/src/node-manager-logic.ts";
import type { XrayNode } from "../../frontend/src/types.ts";

const source: XrayNode = { id: 1, node_name: "落地 A", protocol: "vless", clash_config: JSON.stringify({ name: "落地 A", type: "vless", server: "example.com", port: 443, uuid: "source-user" }), parsed_config: "{}", raw_url: "vless://source", created_at: "2026-10-01T00:00:00Z" };
const target: XrayNode = { ...source, id: 2, node_name: "中转 B", protocol: "trojan", tag: "外部" };

test("duplicate keys match official sorted JSON including the node name and raw fallback", () => {
  const config = JSON.parse(source.clash_config!);
  const reversed = JSON.stringify(Object.fromEntries(Object.entries(config).reverse()));
  assert.equal(duplicateNodeKey(source), duplicateNodeKey({ ...source, clash_config: reversed }));
  assert.notEqual(duplicateNodeKey(source), duplicateNodeKey({ ...source, node_name: "另一个名称" }));
  assert.equal(duplicateNodeKey({ ...source, clash_config: "invalid" }), "invalid|落地 A");
  const nested = { ...source, clash_config: JSON.stringify({ ...config, "ws-opts": { path: "/ws", headers: { Host: "example.com" } } }) };
  assert.equal(duplicateNodeKey(nested), JSON.stringify({ ...JSON.parse(nested.clash_config), __node_name__: nested.node_name }, Object.keys({ ...JSON.parse(nested.clash_config), __node_name__: nested.node_name }).sort()));
});

test("duplicate groups preserve the earliest node and never group chain or relay copies with their source", () => {
  const duplicate = { ...source, id: 3, created_at: "2026-10-02T00:00:00Z" };
  const chain = { ...chainNodePayload(source, target), id: 4 };
  const relay = { ...relayGroupPayload(source, "中转组", [target.id]), id: 5 };
  assert.deepEqual(duplicateNodeGroups([duplicate, source, chain, relay]).map((group) => group.map((node) => node.id)), [[1, 3]]);
});

test("chain candidates exclude self, managed, routed and chain nodes while relay candidates allow managed nodes", () => {
  const nodes = [source, target, { ...target, id: 3, inbound_tag: "managed-inbound" }, { ...target, id: 4, protocol: "vless⇋trojan" }, { ...target, id: 5, node_type: "routed" }, { ...target, id: 6, tag: "中转组" }];
  assert.deepEqual(chainProxyCandidates(nodes, source.id).map((node) => node.id), [2, 6]);
  assert.deepEqual(relayGroupCandidates(nodes, source.id).map((node) => node.id), [2, 3]);
  assert.deepEqual(chainProxyCandidates(nodes, source.id, "TROJAN").map((node) => node.id), [2, 6]);
  assert.deepEqual(relayGroupCandidates(nodes, source.id, "外部").map((node) => node.id), [2, 3]);
  assert.deepEqual(chainProxyCandidates(nodes, source.id, "not-found"), []);
});

test("chain payload creates a renamed source copy with the target ID and original relay endpoint", () => {
  const payload = chainNodePayload({ ...source, relay_orig_server: "origin.example", relay_orig_port: 9443, enabled: false }, target);
  assert.equal(payload.node_name, "落地 A | 中转 B");
  assert.equal(payload.protocol, "vless⇋trojan");
  assert.equal(payload.chain_proxy_node_id, 2);
  assert.equal(payload.enabled, true);
  assert.equal(payload.tag, "链式代理");
  assert.equal(payload.raw_url, source.raw_url);
  assert.equal(payload.parsed_config, payload.clash_config);
  assert.deepEqual(JSON.parse(payload.clash_config!), { ...JSON.parse(source.clash_config!), name: payload.node_name });
  assert.equal(payload.relay_orig_server, "origin.example");
  assert.equal(payload.relay_orig_port, 9443);
  assert.equal(chainNodePayload(source, target).relay_orig_port, 0);
  assert.throws(() => chainNodePayload({ ...source, clash_config: "invalid" }, target), /源节点配置解析失败/);
});

test("relay group preserves a managed source and clears its chain when configured or removed", () => {
  const managed = { ...source, inbound_tag: "inbound", tags: ["远程:server", "自定义"], chain_proxy_node_id: target.id, enabled: false };
  const payload = relayGroupPayload(managed, "中转", [target.id]);
  assert.equal(payload.node_name, source.node_name);
  assert.equal(payload.clash_config, source.clash_config);
  assert.equal(payload.inbound_tag, managed.inbound_tag);
  assert.equal(payload.enabled, false);
  assert.deepEqual(payload.tags, managed.tags);
  assert.equal(payload.chain_proxy_node_id, null);
  assert.deepEqual(payload.relay_group_node_ids, [target.id]);
  const removed = relayGroupPayload(managed, "", []);
  assert.equal(removed.relay_group_name, "");
  assert.deepEqual(removed.relay_group_node_ids, []);
  const external = relayGroupPayload(source, "中转", [target.id]);
  assert.equal(external.node_name, "落地 A | 中转");
  assert.equal(external.tag, "中转组");
  assert.equal(external.inbound_tag, undefined);
});

test("IP version defaults to IPv4 when available and IPv6 otherwise", () => {
  assert.equal(defaultNodeIPVersion({ ip_address: "192.0.2.1" }), "v4");
  assert.equal(defaultNodeIPVersion({ ip_address: "" }), "v6");
  assert.equal(defaultNodeIPVersion({}), "v6");
});

test("subscription tag prefers explicit, suggested, hostname then fallback", () => {
  assert.equal(subscriptionDefaultTag("https://sub.example/path", " 标签 ", "建议"), "标签");
  assert.equal(subscriptionDefaultTag("https://sub.example/path", " ", "建议"), "建议");
  assert.equal(subscriptionDefaultTag("https://sub.example/path"), "sub.example");
  assert.equal(subscriptionDefaultTag("invalid"), "外部订阅");
});

test("batch rename uses literal find text and preserves blank lines when adding prefix and suffix", () => {
  assert.equal(batchRenameTransform("a.b.a.b\nA\n", { find: "a.b", replace: "X" }), "X.X\nA\n");
  assert.equal(batchRenameTransform("A\n\nB", { prefix: "[", suffix: "]" }), "[A]\n\n[B]");
  assert.equal(batchRenameTransform("A", { find: "A", replace: "" }), "");
  assert.equal(batchRenameTransform("A", { find: "", replace: "X" }), "A");
});

test("source filtering distinguishes subscription URLs, source tags and managed custom tags", () => {
  assert.equal(matchesNodeSource(source, "manual"), true);
  assert.equal(matchesNodeSource({ ...source, raw_url: "https://sub.example/nodes", tag: "自定义" }, "subscription"), true);
  assert.equal(matchesNodeSource({ ...source, tags: ["订阅导入"] }, "subscription"), true);
  assert.equal(matchesNodeSource({ ...source, tag: "自定义" }, "manual"), true);
  assert.equal(matchesNodeSource({ ...source, tag: "自定义", inbound_tag: "managed" }, "manual"), false);
  assert.equal(matchesNodeSource({ ...source, inbound_tag: "managed" }, "manual"), false);
  assert.equal(matchesNodeSource({ ...source, tag: "远程:server" }, "manual"), false);
  assert.equal(matchesNodeSource({ ...source, tag: "远程:server" }, "all"), true);
});

test("sort actions move selected nodes as a stable group and keep boundary selections unchanged", () => {
  const order = [1, 2, 3, 4, 5, 6];
  const selected = new Set([2, 4]);
  assert.deepEqual(moveSelectedNodes(order, selected, "top"), [2, 4, 1, 3, 5, 6]);
  assert.deepEqual(moveSelectedNodes(order, selected, "bottom"), [1, 3, 5, 6, 2, 4]);
  assert.deepEqual(moveSelectedNodes(order, selected, "up"), [2, 4, 1, 3, 5, 6]);
  assert.deepEqual(moveSelectedNodes(order, selected, "down"), [1, 3, 5, 2, 4, 6]);
  assert.deepEqual(moveSelectedNodes(order, new Set([1, 4]), "up"), order);
  assert.deepEqual(moveSelectedNodes(order, new Set([2, 6]), "down"), order);
  assert.deepEqual(moveSelectedNodes(order, new Set(), "top"), order);
});
