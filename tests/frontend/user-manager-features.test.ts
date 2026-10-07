import assert from "node:assert/strict";
import test from "node:test";
import { credentialWriteState, expiredUnboundPackage, rebindPackageInput, eligiblePackages, matchesExpiry, parseRenewDays, parseTrafficOverride, remainingDays, renewedDate, searchUserURIs, trafficOverrideGB, validUsername } from "../../frontend/src/user-manager-logic.ts";

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

const expiredPackage = { role: "user", last_package_id: 3, last_package_name: "Last package", last_package_end_date: "2026-08-25T00:00:00", rebindable: true, reason: "" };
const expiredUser = { username: "alice", role: "user", is_active: true, package_id: null, package_end_date: null };

test("expired-unbound history is expired, never permanent or upcoming", () => {
  const now = Date.parse("2026-10-07T12:00:00Z");
  for (const filter of ["all", "expired", "permanent", "d7", "d30"] as const) {
    assert.equal(matchesExpiry(null, filter, now, expiredPackage.last_package_end_date), filter === "all" || filter === "expired");
  }
  assert.equal(matchesExpiry(null, "permanent", now), true);
  assert.equal(matchesExpiry(null, "expired", now), false);
  assert.equal(expiredUnboundPackage(expiredUser, expiredPackage), expiredPackage);
  for (const extra of [{ role: "admin" }, { package_id: 4 }, { assignment_package_ids: [4] }]) assert.equal(expiredUnboundPackage({ ...expiredUser, ...extra }, expiredPackage), undefined);
});

test("rebind dates start at Taiwan today across UTC midnight, leap day and year end", () => {
  for (const [instant, days, start, expiry] of [
    ["2026-10-07T15:59:59Z", 30, "2026-10-07", "2026-11-06"],
    ["2026-10-07T16:00:00Z", 30, "2026-10-08", "2026-11-07"],
    ["2026-12-31T15:59:59Z", 1, "2026-12-31", "2027-01-01"],
    ["2028-02-28T16:00:00Z", 1, "2028-02-29", "2028-03-01"],
    ["2026-10-07T16:00:00Z", 90, "2026-10-08", "2027-01-06"],
    ["2026-10-07T16:00:00Z", 365, "2026-10-08", "2027-10-08"],
    ["2026-10-07T16:00:00Z", 3650, "2026-10-08", "2036-10-05"],
  ] as const) {
    for (const oldDate of ["2026-08-25", "2029-01-01"]) {
      const body = rebindPackageInput(expiredUser, { ...expiredPackage, last_package_end_date: oldDate }, days, false, new Date(instant));
      assert.equal(body.start_date, start);
      assert.equal(body.expire_date, expiry);
      assert.equal(body.package_id, 3);
      assert.equal(body.permanent, false);
      assert.equal(body.inherit_expire_date, false);
      assert.equal(body.inherit_traffic, false);
      assert.equal(body.is_reset, true);
      assert.equal(body.reset_day, 1);
      assert.equal(body.traffic_limit_override_gb, null);
    }
  }
  const retained = rebindPackageInput({ ...expiredUser, is_reset: false, reset_day: 28, traffic_limit_override_gb: 12 }, expiredPackage, 30, true);
  assert.equal(retained.is_reset, false);
  assert.equal(retained.reset_day, 28);
  assert.equal(retained.traffic_limit_override_gb, 12);
  assert.equal(retained.confirm_disabled, true);
  for (const days of [0, -1, 1.5, 3651]) assert.throws(() => rebindPackageInput(expiredUser, expiredPackage, days));
  for (const last of [undefined, { ...expiredPackage, rebindable: false }, { ...expiredPackage, last_package_id: null }]) assert.throws(() => rebindPackageInput(expiredUser, last, 30));
  assert.throws(() => rebindPackageInput({ ...expiredUser, role: "admin" }, expiredPackage, 30));
});
