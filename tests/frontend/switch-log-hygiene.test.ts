import assert from "node:assert/strict";
import test from "node:test";
import { emptyPackage, packageSavePayload, packageServers, groupPackages } from "../../frontend/src/package-manager-logic.ts";
import type { ManagedPackage, XrayNode } from "../../frontend/src/types.ts";

test("deleted package nodes remain explicit until an administrator saves", () => {
  const pkg = { ...emptyPackage(), id: 9, name: "套餐9", nodes: [37, 4], node_speed_limits: { 37: 10, 4: 20 }, node_name_overrides: { 37: "旧节点", 4: "现有节点" } } as ManagedPackage;
  const nodes = [{ id: 4, original_server: "s2" }] as XrayNode[];
  assert.deepEqual(packageServers(pkg, nodes), ["已删除节点 #37", "s2"]);
  assert.equal(groupPackages([pkg], [], nodes, "server")[0].name === "全部节点", false);
  const saved = packageSavePayload(pkg, [4]);
  assert.deepEqual(saved.nodes, [4]);
  assert.deepEqual(saved.node_speed_limits, { 4: 20 });
  assert.deepEqual(saved.node_name_overrides, { 4: "现有节点" });
  assert.deepEqual(pkg.nodes, [37, 4]);
  assert.deepEqual(packageSavePayload({ ...pkg, nodes: [37] }, [4]).nodes, []);
  assert.deepEqual(packageSavePayload({ ...pkg, nodes: [] }, [4]).nodes, []);
});
