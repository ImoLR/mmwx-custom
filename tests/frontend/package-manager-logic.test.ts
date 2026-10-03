import assert from "node:assert/strict";
import test from "node:test";
import { emptyPackage, newAutoSpeedRule, orderPackageNodes, packageMemberNodeIds, packageResetDay, packageTemplateType, packageToForm, updatePackageNodeOverride, validatePackage } from "../../frontend/src/package-manager-logic.ts";
import { retainTrafficGroupNodes, validateTrafficGroups } from "../../frontend/src/package-traffic-groups.ts";
import type { ManagedPackage, RemoteServer, XrayNode } from "../../frontend/src/types.ts";

test("editing a full package preserves writable fields, override zeroes and hidden forwarding values", () => {
  const original = {
    ...emptyPackage(), id: 42, name: "套餐", description: "  原始说明\n", traffic_limit_gb: 0.125,
    nodes: [8, 2, 99], nodes_configured: true, short_code: "code", created_at: "created", updated_at: "updated",
    node_multipliers: { 8: 2.5, 2: 100 }, node_name_overrides: { 8: " 节点 ", 2: "another" }, node_name_override_enabled: true,
    node_speed_limits: { 8: 0, 2: 12.5 }, node_device_limits: { 8: 0, 2: 400 }, node_traffic_limits: { 8: 0.125 },
    template_filename: "clash.yaml", surge_template_filename: "SURGE.CONF", loon_template_filename: "LOON.LCF",
    ip_limit: 3, ip_over_limit_action: "kick_oldest", auto_speed_rules: [newAutoSpeedRule(), { ...newAutoSpeedRule(), type: "burst", burst_count: 7 }],
    forward_rule_limit: 0, forward_port_limit: 12, forward_speed_mbps: 20, forward_conn_limit: 300, forward_chains: [9, 6],
    future_official_field: { retained: true },
  };
  const form = packageToForm(original);
  const { nodes_configured, short_code, created_at, updated_at, ...writable } = original;
  assert.deepEqual(form, writable);
  assert.notEqual(form.nodes, original.nodes);
  assert.notEqual(form.auto_speed_rules[0], original.auto_speed_rules[0]);
  assert.equal(validatePackage(form), "");
});

test("legacy optional fields receive official defaults without losing unavailable template names", () => {
  const form = packageToForm({ ...emptyPackage(), id: 1, auto_speed_rules: null, node_multipliers: null, loon_template_filename: "missing.lcf", ip_limit: undefined, ip_over_limit_action: undefined } as ManagedPackage);
  assert.deepEqual(form.auto_speed_rules, []);
  assert.deepEqual(form.node_multipliers, {});
  assert.equal(form.ip_limit, 0);
  assert.equal(form.ip_over_limit_action, "reject");
  assert.equal(form.loon_template_filename, "missing.lcf");
});

test("unlimited traffic is valid while negative traffic, invalid cycles, and invalid reset dates are rejected", () => {
  const form = { ...emptyPackage(), name: "无限", traffic_limit_gb: 0 };
  assert.equal(validatePackage(form), "");
  for (const value of [-1, Infinity, NaN]) assert.match(validatePackage({ ...form, traffic_limit_gb: value }), /流量额度/);
  for (const value of [0, -1, 0.5, NaN]) assert.match(validatePackage({ ...form, cycle_days: value }), /计量周期/);
  for (const value of [0, 32, 1.5, NaN]) assert.match(validatePackage({ ...form, reset_day: value }), /重置日期/);
  assert.equal(validatePackage({ ...form, reset_day: 31 }), "");
  assert.match(validatePackage({ ...form, traffic_limit_gb: 10, node_traffic_limits: { 1: 11 } }), /单个节点/);
  assert.equal(validatePackage({ ...form, traffic_limit_gb: 10, node_traffic_limits: { 1: 10 } }), "");
});

test("template suffix classification follows official case-insensitive Surge and Loon rules", () => {
  for (const filename of ["a.conf", "a.CONF"]) assert.equal(packageTemplateType(filename), "surge");
  for (const filename of ["a.lcf", "a.LCF"]) assert.equal(packageTemplateType(filename), "loon");
  for (const filename of ["a.yaml", "a.yml", "a.conf.yaml"]) assert.equal(packageTemplateType(filename), "clash");
});

test("new package node order follows user order while retaining unknown IDs", () => {
  const selected = [2, 99, 8, 100];
  assert.deepEqual(orderPackageNodes(selected, [8, 2]), [8, 2, 99, 100]);
  assert.deepEqual(selected, [2, 99, 8, 100]);
  assert.deepEqual(orderPackageNodes(selected, []), selected);
});

test("reset defaults follow the current first node and fall back to day one", () => {
  const nodes = [{ id: 1, original_server: "A" }, { id: 2, original_server: "B" }, { id: 3 }] as XrayNode[];
  const servers = [{ name: "A", traffic_reset_day: 15 }, { name: "B", traffic_reset_day: 31 }] as RemoteServer[];
  assert.equal(packageResetDay([1, 2], nodes, servers), 15);
  assert.equal(packageResetDay([2], nodes, servers), 31);
  assert.equal(packageResetDay([3, 1], nodes, servers), 1);
  assert.equal(packageResetDay([], nodes, servers), 1);
  assert.equal(packageResetDay([1], nodes, [{ name: "A", traffic_reset_day: 32 }] as RemoteServer[]), 1);
});

test("deselecting the final explicit node keeps traffic group members when the package returns to all nodes", () => {
  const groups = [{ name: "组", limit_gb: 20, node_ids: [2] }];
  const allowed = packageMemberNodeIds([], [1, 2, 3]);
  assert.deepEqual(retainTrafficGroupNodes(groups, allowed), groups);
  assert.deepEqual(retainTrafficGroupNodes(groups, packageMemberNodeIds([1], [1, 2, 3]))[0].node_ids, []);
  assert.equal(validateTrafficGroups(groups, 0, allowed, {}), "");
  assert.match(validateTrafficGroups(groups, 10, allowed, {}), /套餐总额度/);
});

test("overrides distinguish inherited defaults from explicit unlimited and preserve unrelated entries", () => {
  const node = { id: 1, node_name: "原名" };
  const original = { ...emptyPackage(), node_speed_limits: { 2: 30 }, node_multipliers: { 1: 3, 2: 4 }, node_name_overrides: { 1: "覆盖名", 2: "其他" } };
  assert.deepEqual(updatePackageNodeOverride(original, "node_speed_limits", node, "0").node_speed_limits, { 1: 0, 2: 30 });
  assert.deepEqual(updatePackageNodeOverride(original, "node_speed_limits", node, "").node_speed_limits, { 2: 30 });
  assert.deepEqual(updatePackageNodeOverride(original, "node_speed_limits", node, "-1").node_speed_limits, original.node_speed_limits);
  assert.deepEqual(updatePackageNodeOverride(original, "node_multipliers", node, "1").node_multipliers, { 2: 4 });
  assert.deepEqual(updatePackageNodeOverride(original, "node_multipliers", node, "").node_multipliers, { 2: 4 });
  assert.deepEqual(updatePackageNodeOverride(original, "node_multipliers", node, "0").node_multipliers, { 1: 0, 2: 4 });
  assert.deepEqual(updatePackageNodeOverride(original, "node_name_overrides", node, "原名").node_name_overrides, { 2: "其他" });
  assert.deepEqual(updatePackageNodeOverride(original, "node_name_overrides", node, "").node_name_overrides, { 2: "其他" });
  assert.deepEqual(updatePackageNodeOverride(original, "node_device_limits", node, "12.8").node_device_limits, { 1: 12 });
  assert.deepEqual(original.node_name_overrides, { 1: "覆盖名", 2: "其他" });
});
