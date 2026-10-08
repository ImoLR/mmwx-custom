import assert from "node:assert/strict";
import test from "node:test";
import { groupNodes, matchesNodeFilters, nodeDisplayAddress, nodeDisplayName, nodeOwnerHint, nodeOwnership, nodeRelayRows, toggleNodeSelection } from "../../frontend/src/node-manager-logic.ts";
import type { NodeOwners } from "../../frontend/src/node-manager-logic.ts";
import type { XrayNode } from "../../frontend/src/types.ts";

const nodes: XrayNode[] = [
  { id: 1, node_name: "shadowsocks2022-10015", protocol: "ss", original_server: "Boil Hinet 158", inbound_tag: "ss", tags: ["远程:Boil Hinet 158", "台湾"] },
  { id: 2, node_name: "relay A", original_server: "Boil Hinet 158", inbound_tag: "ss-relay", relay_orig_server: "origin" },
  { id: 3, node_name: "relay B", original_server: "Boil Hinet 158", inbound_tag: "ss-relay", relay_orig_server: "origin" },
  { id: 4, node_name: "admin", original_server: "B", inbound_tag: "self" },
  { id: 5, node_name: "external", raw_url: "https://sub.example/nodes" },
  { id: 6, node_name: "unowned", original_server: "B", inbound_tag: "unowned", enabled: false },
  { id: 7, node_name: "external package" },
];
const shared = { users: ["wings", "usb"], admin_only: false, shared: true, source: "credential" as const, inbound_backed: true };
const owners: NodeOwners = {
  1: shared,
  2: { ...shared, parent_node_id: 1 },
  3: { ...shared, parent_node_id: 1 },
  4: { users: ["admin"], admin_only: true, shared: false, source: "credential", inbound_backed: true },
  7: { users: ["Riczzoe"], admin_only: false, shared: false, source: "package", inbound_backed: false },
};
const memberships = { 1: [{ package_id: 1, package_name: "套餐一" }, { package_id: 2, package_name: "套餐二" }], 7: [{ package_id: 3, package_name: "外部套餐" }] };

test("display names strip only leading region and bracket prefixes and preserve original node names for search", () => {
  const tw = { flag: "🇹🇼", label: "台湾" }, hk = { flag: "🇭🇰", label: "香港" }, us = { flag: "🇺🇸", label: "美国" };
  const cases: Array<[string, typeof tw | null, string]> = [
    ["🇹🇼 台湾 [Boil Hinet 马年] shadowsocks2022-10016", tw, "🇹🇼 shadowsocks2022-10016"],
    ["🇭🇰 香港 [Boil HKT 99u] max ss-10015", hk, "🇭🇰 max ss-10015"],
    ["[Dmit T1] vless-hk 自己用", hk, "🇭🇰 vless-hk 自己用"],
    ["Vless US 自己用", us, "🇺🇸 Vless US 自己用"],
    ["[台湾 家宽 甲] 我的节点", tw, "🇹🇼 我的节点"],
    ["[Server With Spaces] name [keep this]", us, "🇺🇸 name [keep this]"],
    ["name [Server With Spaces] suffix", hk, "🇭🇰 name [Server With Spaces] suffix"],
    ["🇭🇰 香港 [Boil HKT] max [备用]", hk, "🇭🇰 max [备用]"],
    ["台湾 [Boil Hinet] ss", tw, "🇹🇼 ss"],
    ["台湾专线 [备用]", tw, "🇹🇼 台湾专线 [备用]"],
    ["🇭🇰 Boil HKT 自己用", hk, "🇭🇰 Boil HKT 自己用"],
    ["[未知 地区] ss", null, "ss"],
    ["plain [keep] name", null, "plain [keep] name"],
    ["[only bracket]", null, "[only bracket]"],
    ["[unclosed name", null, "[unclosed name"],
    ["🇭🇰 original", null, "🇭🇰 original"],
  ];
  for (const [name, region, shown] of cases) {
    const node = { id: 90, node_name: name };
    assert.equal(nodeDisplayName(node.node_name, region), shown, name);
    assert.equal(node.node_name, name);
    assert.equal(matchesNodeFilters(node, {}, nodeOwnership(node, {}), { query: name, protocol: "all", tags: [], source: "all", server: "all", state: "all" }), true);
  }
});

test("display addresses include the full host and port and bracket IPv6 exactly once", () => {
  assert.equal(nodeDisplayAddress("boilhkt99u.imgamer.top", "10015"), "boilhkt99u.imgamer.top:10015");
  assert.equal(nodeDisplayAddress("192.0.2.1", "443"), "192.0.2.1:443");
  assert.equal(nodeDisplayAddress("2001:db8:1234:5678:90ab:cdef:1234:5678", "10016"), "[2001:db8:1234:5678:90ab:cdef:1234:5678]:10016");
  assert.equal(nodeDisplayAddress("[2001:db8::1]", "443"), "[2001:db8::1]:443");
  assert.equal(nodeDisplayAddress("a-very-long-entry-address.example.test", "10015"), "a-very-long-entry-address.example.test:10015");
});

test("owner groups sort users alphabetically before admin, external and unowned; shared and relay copies appear in every owner group", () => {
  const groups = groupNodes(nodes, "user", owners, memberships);
  assert.deepEqual(groups.map((group) => group.name), ["Riczzoe", "usb", "wings", "自用（管理员）", "外部节点", "未归属"]);
  for (const user of ["usb", "wings"]) assert.deepEqual(groups.find((group) => group.name === user)!.nodes.map((node) => node.id), [1, 2, 3]);
  assert.equal(groups[0].subtitle, "套餐 外部套餐");
  assert.match(nodeOwnerHint(nodeOwnership(nodes[0], owners)), /共用/);
  assert.match(nodeOwnerHint(nodeOwnership(nodes[6], owners)), /套餐绑定/);
  assert.equal(nodeOwnership(nodes[5], owners).source, "none");
  assert.match(nodeOwnerHint(nodeOwnership(nodes[5], owners)), /未归属/);
});

test("package membership duplicates display only; no package, server and ungrouped views preserve node membership", () => {
  const groups = groupNodes(nodes, "package", owners, memberships);
  assert.equal(groups.filter((group) => group.nodes.some((node) => node.id === 1)).length, 2);
  assert.equal(groups.at(-1)!.name, "未加入套餐");
  assert.deepEqual(groupNodes(nodes, "none", owners, memberships)[0].nodes.map((node) => node.id), nodes.map((node) => node.id));
  assert.deepEqual(groupNodes(nodes, "server", owners, memberships).find((group) => group.name === "B")!.nodes.map((node) => node.id), [4, 6]);
});

test("default-admin and shared packages preserve owner groups and explain their source", () => {
  const updated: NodeOwners = { ...owners, 1: { ...shared, source: "package" }, 4: { ...owners[4], source: "default-admin" } };
  assert.deepEqual(groupNodes(nodes, "user", updated, memberships), groupNodes(nodes, "user", owners, memberships));
  assert.equal(nodeOwnerHint(updated[4]), "自用（管理员） · 无其他用户，默认归管理员");
  assert.equal(nodeOwnerHint(updated[1]), "wings、usb · 依据非管理员套餐绑定 · 多位用户共用");
  assert.equal(nodeOwnerHint(nodeOwnership(nodes[4], updated)), "外部节点 · 没有可确认的归属");
});

test("relay rows follow source order, keep all children accessible and remain visible when a filter excludes their parent", () => {
  const rows = nodeRelayRows([nodes[2], nodes[1], nodes[0]], owners);
  assert.deepEqual(rows.map(({ node }) => node.id), [1]);
  assert.deepEqual(rows[0].children.map((node) => node.id), [3, 2]);
  assert.deepEqual(nodeRelayRows([nodes[1]], owners).map(({ node }) => node.id), [2]);
});

test("owner search combines protocol, tag, source, server and state filters without admitting unrelated nodes", () => {
  const all = { query: " USB ", protocol: "all", tags: [] as string[], source: "all" as const, server: "all", state: "all" };
  const parsed = { server: "origin.test", port: 10015 };
  assert.equal(matchesNodeFilters(nodes[0], parsed, owners[1], all), true);
  assert.equal(matchesNodeFilters(nodes[0], parsed, owners[1], { ...all, protocol: "ss", tags: ["台湾"], server: "Boil Hinet 158", state: "enabled" }), true);
  for (const filter of [{ protocol: "vless" }, { server: "B" }, { tags: ["日本"] }, { state: "disabled" }, { query: "king" }, { source: "subscription" as const }]) {
    assert.equal(matchesNodeFilters(nodes[0], parsed, owners[1], { ...all, ...filter }), false);
  }
  assert.equal(matchesNodeFilters(nodes[0], parsed, owners[1], { ...all, query: "10015" }), true);
  assert.equal(matchesNodeFilters(nodes[4], {}, nodeOwnership(nodes[4], owners), { ...all, query: "", source: "subscription" }), true);
  assert.equal(matchesNodeFilters(nodes[1], {}, owners[2], { ...all, state: "relay" }), true);
});

test("shared-group selection and batch actions dedupe by ID, with partial and full group toggles", () => {
  const selected = toggleNodeSelection(new Set([1]), [1, 1, 2, 3]);
  assert.deepEqual([...selected], [1, 2, 3]);
  const secondGroup = toggleNodeSelection(selected, [1, 2, 3, 7]);
  assert.deepEqual(nodes.filter((node) => secondGroup.has(node.id)).map((node) => node.id), [1, 2, 3, 7]);
  assert.deepEqual([...toggleNodeSelection(secondGroup, [1, 2, 3])], [7]);
  assert.deepEqual([...toggleNodeSelection(new Set([7]), [])], [7]);
});
