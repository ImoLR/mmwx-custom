import assert from "node:assert/strict";
import test from "node:test";
import { credentialWriteState, eligiblePackages, matchesExpiry, parseRenewDays, parseTrafficOverride, remainingDays, renewedDate, searchUserURIs, trafficOverrideGB, validUsername } from "../../frontend/src/user-manager-logic.ts";

test("expiry buckets include today, exclude expired from upcoming, and preserve permanent", () => {
  const now = new Date("2026-10-04T12:00:00").getTime();
  assert.equal(remainingDays("2026-10-04", now), 1);
  assert.equal(matchesExpiry("2026-10-03", "expired", now), true);
  assert.equal(matchesExpiry("2026-10-10", "d7", now), true);
  assert.equal(matchesExpiry("2026-10-11", "d7", now), false);
  assert.equal(matchesExpiry("2026-11-02", "d30", now), true);
  assert.equal(matchesExpiry("2026-11-03", "d30", now), false);
  assert.equal(matchesExpiry("2026-10-03", "d30", now), false);
  for (const expiry of [null, "", "invalid"]) {
    assert.equal(matchesExpiry(expiry, "permanent", now), true);
    assert.equal(matchesExpiry(expiry, "d7", now), false);
  }
});

test("username matches official 3–20 letters, digits and hyphen only", () => {
  for (const value of ["abc", "A-1", "a".repeat(20)]) assert.equal(validUsername(value), true);
  for (const value of ["ab", "a".repeat(21), "a_b", " 用户 ", " abc", "abc\n"]) assert.equal(validUsername(value), false);
});

test("renewal validates bounds and adds calendar days from max(expiry, today)", () => {
  for (const value of ["", "0", "-1", "1.1", "3651", "Infinity"]) assert.throws(() => parseRenewDays(value));
  assert.equal(parseRenewDays("3650"), 3650);
  const now = new Date("2026-10-04T12:00:00");
  for (const expiry of [null, "", "2026-10-01", "invalid"]) assert.equal(renewedDate(expiry, 30, now), "2026-11-03");
  assert.equal(renewedDate("2026-12-31", 1, now), "2027-01-01");
  assert.equal(renewedDate("2028-02-28", 1, now), "2028-02-29");
});

test("traffic overrides distinguish inherit, unlimited and GiB", () => {
  assert.equal(parseTrafficOverride(" "), null);
  assert.equal(parseTrafficOverride("0"), 0);
  assert.equal(parseTrafficOverride("1.5"), 1.5);
  assert.equal(trafficOverrideGB(1610612736), "1.5");
  assert.equal(trafficOverrideGB(null), "");
  assert.equal(trafficOverrideGB(0), "0");
  for (const value of ["-1", "Infinity", "NaN", "no", "1e50"]) assert.throws(() => parseTrafficOverride(value));
});

test("package eligibility keeps same-user edits and excludes other owners and duplicate assignments", () => {
  const packages = [{ id: 1 }, { id: 2 }, { id: 3 }, { id: 4 }];
  assert.deepEqual(eligiblePackages(packages, [1, 2, 4], [1, 2]), [{ id: 4 }]);
  assert.deepEqual(eligiblePackages(packages, [1, 2, 4], [1, 2], 1), [{ id: 1 }, { id: 4 }]);
  assert.deepEqual(eligiblePackages(packages, [], [], 3), []);
});

test("URI search remains user-scoped and matches node or server case-insensitively", () => {
  const items = [{ username: "alice", node_name: "Tokyo", server_name: "JP1" }, { username: "alice", node_name: "Taiwan", server_name: "TW1" }, { username: "bob", node_name: "Tokyo", server_name: "JP1" }];
  assert.deepEqual(searchUserURIs(items, "alice", " jp1 "), [items[0]]);
  assert.deepEqual(searchUserURIs(items, "alice", "tokYO"), [items[0]]);
  assert.deepEqual(searchUserURIs(items, "alice", "", "TW1"), [items[1]]);
});

test("credential writes refuse deletion and require disabled lifecycle confirmation", () => {
  for (const state of ["deleting", "delete_partial"]) assert.equal(credentialWriteState(state), "refuse");
  for (const state of ["disabled", "partially_disabled", "partially_enabled", "enabling", "disabling"]) assert.equal(credentialWriteState(state), "confirm");
  assert.equal(credentialWriteState("enabled"), "allow");
});
