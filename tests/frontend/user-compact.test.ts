import test from "node:test";
import assert from "node:assert/strict";
import { managedUserStatus, matchesUserQuickFilter, sortManagedUsers, userQuickCounts, userRowTags } from "../../frontend/src/user-manager-logic.ts";
import type { ManagedUser, ManagedUserExpiredPackage, ManagedUserLifecycle } from "../../frontend/src/types.ts";

const now = new Date("2026-10-14T12:00:00").getTime();
const user = (username: string, changes: Partial<ManagedUser> = {}): ManagedUser => ({ username, role: "user", is_active: true, ...changes });
const lifecycle = (state: string, changes: Partial<ManagedUserLifecycle> = {}): ManagedUserLifecycle => ({ username: "disabled", desired_state: "disabled", effective_state: state, pending_count: 0, updated_at: "", ...changes });
const expired: ManagedUserExpiredPackage = { role: "user", last_package_id: 1, last_package_name: "上次", last_package_end_date: "2026-10-01", rebindable: false, reason: "已被占用" };

test("quick counts reuse expiry and access states, including expired-unbound and official inactive", () => {
  const users = [user("soon", { package_end_date: "2026-10-19" }), user("expired", { package_end_date: "2026-10-13" }), user("unbound"), user("disabled"), user("official", { is_active: false }), user("admin", { role: "admin" })];
  assert.deepEqual(userQuickCounts(users, { disabled: lifecycle("partially_disabled") }, { unbound: expired }, now), { all: 6, d7: 1, expired: 2, disabled: 2 });
  assert.equal(matchesUserQuickFilter(user("unbound", { assignment_package_ids: [2] }), "expired", undefined, expired, now), false);
  assert.equal(matchesUserQuickFilter(user("today", { package_end_date: "2026-10-14" }), "d7", undefined, undefined, now), true);
  assert.equal(matchesUserQuickFilter(user("later", { package_end_date: "2026-10-22" }), "d7", undefined, undefined, now), false);
  assert.equal(matchesUserQuickFilter(user("invalid", { package_end_date: "invalid" }), "expired", undefined, undefined, now), false);
});

test("sort is non-mutating, expiry includes last package, permanent last, administrators trailing", () => {
  const users = [user("admin", { role: "admin", package_end_date: "2026-01-01" }), user("zoe", { package_end_date: "2026-10-19" }), user("bob"), user("amy")];
  assert.deepEqual(sortManagedUsers(users, "expiry", { bob: expired }).map((u) => u.username), ["bob", "zoe", "amy", "admin"]);
  assert.deepEqual(sortManagedUsers(users, "username", {}).map((u) => u.username), ["amy", "bob", "zoe", "admin"]);
  assert.equal(users[0].username, "admin");
});

test("rows preserve pending delete, disable, conflict, rebind and expiry warnings", () => {
  assert.deepEqual(userRowTags(user("unbound"), undefined, expired, now), { critical: ["无法重绑"], expiry: "已过期", tone: "bad", status: "" });
  assert.equal(userRowTags(user("soon", { package_end_date: "2026-10-19" }), undefined, undefined, now).tone, "warning");
  assert.equal(userRowTags(user("expired", { package_end_date: "2026-10-13" }), undefined, undefined, now).tone, "bad");
  assert.deepEqual(userRowTags(user("deleting"), lifecycle("delete_partial", { pending_count: 3 }), undefined, now).critical, ["删除未完成 3"]);
  assert.deepEqual(userRowTags(user("disabled"), lifecycle("partially_disabled", { pending_count: 2, access: [{ status: "conflict", server_id: 1, inbound_tag: "fixture" }] }), undefined, now).critical, ["禁用未完成", "冲突"]);
  assert.equal(managedUserStatus(user("official", { is_active: false })).displayedStatus, "官方已停用");
  assert.equal(userRowTags(user("admin", { role: "admin" })).status, "管理员");
  assert.equal(managedUserStatus(user("disabled"), lifecycle("enabling")).enableAction, true);
});
