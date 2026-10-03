import assert from "node:assert/strict";
import test from "node:test";
import { TRAFFIC_GB, retainTrafficGroupNodes, trafficGroupDraft, trafficGroupNodeWarning, trafficGroupPayload, validateTrafficGroups } from "../../frontend/src/package-traffic-groups.ts";

const group = () => ({ name: "亚洲", limit_gb: 50, node_ids: [1, 2] });

test("shared quotas allow package and node quota equality", () => {
  assert.equal(validateTrafficGroups([group()], 50, [1, 2, 3], { 1: 50, 2: 0 }), "");
  assert.equal(validateTrafficGroups([{ ...group(), node_ids: [1] }, { name: "欧洲", limit_gb: 50, node_ids: [2] }], 100, [1, 2], {}), "");
});

test("shared groups require names, positive quotas, and members", () => {
  assert.match(validateTrafficGroups([{ ...group(), name: " " }], 100, [1, 2], {}), /名称/);
  assert.match(validateTrafficGroups([{ ...group(), name: "组".repeat(101) }], 100, [1, 2], {}), /100 个字符/);
  for (const limit of [0, -1, NaN, Infinity, 0.00000000001]) {
    assert.match(validateTrafficGroups([{ ...group(), limit_gb: limit }], 100, [1, 2], {}), /大于 0/);
  }
  assert.match(validateTrafficGroups([{ ...group(), limit_gb: 101 }], 100, [1, 2], {}), /套餐总额度/);
  assert.match(validateTrafficGroups([{ ...group(), node_ids: [] }], 100, [1, 2], {}), /至少需要一个节点/);
});

test("nodes must belong to the package and cannot share multiple groups", () => {
  assert.match(validateTrafficGroups([group()], 100, [1], {}), /未关联到套餐/);
  assert.match(validateTrafficGroups([group(), { name: "其他", limit_gb: 40, node_ids: [2] }], 100, [1, 2], {}), /只能属于一个共享组/);
  assert.match(validateTrafficGroups([{ ...group(), node_ids: [1, 1] }], 100, [1], {}), /只能属于一个共享组/);
  assert.match(validateTrafficGroups([group()], 100, [1, 2], { 1: 51 }), /单节点流量额度不能超过/);
});

test("removing package nodes prunes memberships and preserves empty groups for validation", () => {
  const original = [group(), { name: "欧洲", limit_gb: 20, node_ids: [3] }];
  const next = retainTrafficGroupNodes(original, [2]);
  assert.deepEqual(next.map((item) => item.node_ids), [[2], []]);
  assert.deepEqual(original[0].node_ids, [1, 2]);
  assert.match(validateTrafficGroups(next, 100, [2], {}), /至少需要一个节点/);
});

test("traffic group draft converts GB to integral bytes and retains stable IDs", () => {
  const original = { id: 7, name: " 亚洲 ", limit_bytes: 20 * TRAFFIC_GB + 1, node_ids: [1] };
  assert.deepEqual(trafficGroupPayload(trafficGroupDraft(original)), { ...original, name: "亚洲" });
  assert.equal(trafficGroupPayload({ ...group(), limit_gb: 0.01 }).limit_bytes, 10737418);
  assert.equal(trafficGroupPayload({ ...group(), limit_gb: 0.03 }).limit_bytes, 32212254);
  assert.equal(trafficGroupPayload({ ...group(), limit_gb: 1.9 / TRAFFIC_GB }).limit_bytes, 1);
});

test("fractional GB quotas can reopen and save with equal node and package limits", () => {
  const original = { id: 1, name: "亚洲", limit_bytes: 10737418, node_ids: [1] };
  const reopened = trafficGroupDraft(original);
  assert.equal(validateTrafficGroups([reopened], 0.03, [1], { 1: 0.01 }), "");
  assert.equal(validateTrafficGroups([reopened], 0.01, [1], { 1: 0.01 }), "");
  assert.deepEqual(trafficGroupPayload(reopened), original);
  const fullQuota = { ...reopened, limit_gb: 0.03 };
  assert.equal(validateTrafficGroups([fullQuota], 0.03, [1], { 1: 0.03 }), "");
  assert.equal(validateTrafficGroups([trafficGroupDraft(trafficGroupPayload(fullQuota))], 0.03, [1], { 1: 0.03 }), "");
  assert.match(validateTrafficGroups([{ ...reopened, limit_gb: 32212255 / TRAFFIC_GB }], 0.03, [1], {}), /套餐总额度/);
  assert.match(validateTrafficGroups([reopened], 0.03, [1], { 1: 10737419 / TRAFFIC_GB }), /单节点流量额度不能超过/);
});

test("all unsupported execution modes show Chinese warnings", () => {
  const node = { node_id: 1, node_name: "香港", server_name: "hk" };
  assert.equal(trafficGroupNodeWarning({ ...node, status: "enforced" }), "");
  for (const status of ["embedded", "core_outdated", "helper_outdated", "external_node", "no_identity"] as const) {
    assert.match(trafficGroupNodeWarning({ ...node, status }), /无法强制执行/);
  }
  assert.match(trafficGroupNodeWarning({ ...node, status: "no_identity", reason: "多个节点共用身份" }), /多个节点共用身份/);
});
