import test from "node:test";
import assert from "node:assert/strict";
import { emptyPackage, groupPackages, packageBoundUsers, packageServers, searchPackages } from "../../frontend/src/package-manager-logic.ts";
import type { ManagedPackage, ManagedUser, XrayNode } from "../../frontend/src/types.ts";
const pkg = (id: number, nodes: number[] = []): ManagedPackage => ({ ...emptyPackage(), id, name: `套餐 ${id}`, nodes });
const nodes = [{ id: 1, node_name: "香港 VLESS", original_server: "HK" }, { id: 2, node_name: "日本 SS", original_server: "JP" }, { id: 3, node_name: "HK 备用", original_server: "HK" }] as XrayNode[];
const users: ManagedUser[] = [
  { username: "admin", role: "admin", is_active: true, package_id: 3 },
  { username: "alice", role: "user", is_active: true, package_id: 1, assignment_package_ids: [1, 2] },
  { username: "bob", role: "user", is_active: false, package_id: 1 },
  { username: "expired", role: "user", is_active: true },
];
test("user groups include legacy and assignments once, shared packages twice, exclude admin and put unbound last", () => {
  const packages = [pkg(3), pkg(1), pkg(2)];
  const groups = groupPackages(packages, users, nodes, "user");
  assert.deepEqual(groups.map((g) => [g.name, g.packages.map((p) => p.id)]), [["alice", [1, 2]], ["bob", [1]], ["未绑定", [3]]]);
  assert.deepEqual(packageBoundUsers(pkg(1), users).map((u) => u.username), ["alice", "bob"]);
  assert.equal(packages[0].id, 3);
});
test("server groups deduplicate same server, repeat multi-server and retain all-nodes packages", () => {
  const packages = [pkg(1, [1, 2, 3]), pkg(2, [1, 3]), pkg(3)];
  const groups = groupPackages(packages, users, nodes, "server");
  assert.deepEqual(groups.find((g) => g.name === "HK")?.packages.map((p) => p.id), [1, 2]);
  assert.deepEqual(groups.find((g) => g.name === "JP")?.packages.map((p) => p.id), [1]);
  assert.deepEqual(groups.find((g) => g.name === "全部节点")?.packages.map((p) => p.id), [3]);
  assert.deepEqual(packageServers(packages[0], nodes), ["HK", "JP"]);
  assert.deepEqual(groupPackages(packages, users, nodes, "none")[0].packages, packages);
  assert.deepEqual(packageServers(pkg(4, [999]), nodes), ["已删除节点 #999"]);
});
test("search matches name, short code, description and selected/all-node names without case sensitivity", () => {
  const packages = [pkg(1, [1]), { ...pkg(2, [2]), short_code: "TEST2", description: "轻量套餐" }, pkg(3)];
  for (const [query, ids] of [[" 套餐 1 ", [1]], ["test2", [2]], ["轻量", [2]], ["vless", [1, 3]], ["日本", [2, 3]], ["missing", []]] as const) assert.deepEqual(searchPackages(packages, nodes, query).map((p) => p.id), ids);
  assert.equal(searchPackages(packages, nodes, ""), packages);
});
