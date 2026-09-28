import assert from "node:assert/strict";
import test from "node:test";
import { fetchUserManagementData, writeAndVerifyManagedUserStatus } from "../../frontend/src/user-management-state.ts";

const alice = { username: "alice", role: "user", is_active: true };

test("successful status state survives package refresh failure", async () => {
  const result = await fetchUserManagementData(
    async () => ({ users: [alice] }),
    async () => { throw new Error("package unavailable"); },
    async () => ({ nodes: [] }),
  );
  assert.equal(result.users?.[0]?.is_active, true);
  assert.equal(result.failures.length, 1);
  assert.match(result.failures[0], /套餐列表/);
});

test("successful status state survives node refresh failure", async () => {
  const result = await fetchUserManagementData(
    async () => ({ users: [{ ...alice, is_active: false }] }),
    async () => ({ packages: [] }),
    async () => { throw new Error("node unavailable"); },
  );
  assert.equal(result.users?.[0]?.is_active, false);
  assert.equal(result.failures.length, 1);
  assert.match(result.failures[0], /节点列表/);
});

test("verify failure reports unknown instead of preserving the old value", async () => {
  const result = await writeAndVerifyManagedUserStatus(
    "alice",
    false,
    async () => undefined,
    async () => { throw new Error("verify unavailable"); },
  );
  assert.deepEqual(result, { kind: "unknown", reason: "verify unavailable" });
});

test("disable and enable remain authoritative after reload", async () => {
  let databaseState = true;
  const mutate = async (expected: boolean) => {
    const outcome = await writeAndVerifyManagedUserStatus(
      "alice",
      expected,
      async () => { databaseState = expected; },
      async () => ({ success: true, user: { username: "alice", exists: true, is_active: databaseState } }),
    );
    assert.deepEqual(outcome, { kind: "confirmed", isActive: expected });
    assert.equal(databaseState, expected);
  };
  await mutate(false);
  await mutate(true);
});
