import type { HelperUserConnectionsResponse, ManagedUser, ManagedUserExpiredPackage, ManagedUserLifecycle, ManagedUserPackageInput, RemoteServer, UserConnectionsResponse } from "./types";

export function userConnectionCoverage(formal: UserConnectionsResponse | null, helper: HelperUserConnectionsResponse | null, servers: RemoteServer[] | null) {
  if (!formal && !helper) return { incomplete: true, message: "" };
  const available = new Set(helper?.success ? helper.available_server_ids?.map(String) : []);
  const excluded = [...new Set(formal?.excluded_server_names || [])];
  const covered: string[] = [], missing: string[] = [], unknown: string[] = [];
  for (const name of excluded) {
    const matches = servers?.filter((server) => server.name === name) || [];
    if (!matches.length) unknown.push(name);
    else if (matches.every((server) => available.has(String(server.id)))) covered.push(name);
    else missing.push(name);
  }
  const notReady = (formal?.not_ready_server_ids || []).filter((id) => !available.has(String(id)));
  const notReadyNames = notReady.map((id) => servers?.find((server) => String(server.id) === String(id))?.name || `服务器 #${id}`);
  const waiting = formal?.connection_count_ready === false && !(formal.not_ready_server_ids?.length);
  const parts: string[] = [];
  if (!formal?.success) parts.push("官方连接／IP 数据读取失败，请稍后刷新");
  if (!helper?.success) parts.push("Helper 连接数据读取失败，请稍后刷新");
  if (missing.length) parts.push(`连接数待补齐：${missing.join("、")}（请检查 Helper 是否在线并有最新上报）`);
  if (unknown.length) parts.push(`连接数覆盖待核对：${unknown.join("、")}（无法匹配服务器列表，请刷新）`);
  if (notReadyNames.length) parts.push(`官方连接统计尚未就绪：${notReadyNames.join("、")}`);
  else if (waiting) parts.push("官方连接统计尚未就绪");
  if (covered.length) parts.push(`连接数已由 Helper 补齐：${covered.join("、")}`);
  if (excluded.length) parts.push(`IP／地理位置仍仅显示官方覆盖的数据；未覆盖：${excluded.join("、")}`);
  return { incomplete: !formal?.success || !helper?.success || missing.length > 0 || unknown.length > 0 || notReady.length > 0 || waiting, message: parts.join("。") };
}

export type ExpiryFilter = "all" | "expired" | "d7" | "d30" | "permanent";

export function remainingDays(expiry: string | null | undefined, now = Date.now()): number | null {
  if (!expiry?.trim()) return null;
  const end = new Date(`${expiry.slice(0, 10)}T23:59:59`).getTime();
  return Number.isNaN(end) ? null : Math.ceil((end - now) / 86400000);
}

export function matchesExpiry(expiry: string | null | undefined, filter: ExpiryFilter, now = Date.now(), lastPackageEndDate?: string | null) {
  if (filter === "all") return true;
  if (!expiry && lastPackageEndDate) return filter === "expired";
  const days = remainingDays(expiry, now);
  if (filter === "permanent") return days === null;
  if (days === null) return false;
  return filter === "expired" ? days <= 0 : days > 0 && days <= (filter === "d7" ? 7 : 30);
}

export function expiredUnboundPackage(user: ManagedUser, lastPackage?: ManagedUserExpiredPackage) {
  return user.role !== "admin" && !user.package_id && !user.assignment_package_ids?.length && lastPackage?.last_package_end_date ? lastPackage : undefined;
}

export function rebindPackageInput(user: ManagedUser, lastPackage: ManagedUserExpiredPackage | undefined, days: number, confirmDisabled = false, now = new Date()): ManagedUserPackageInput {
  parseRenewDays(String(days));
  if (!expiredUnboundPackage(user, lastPackage) || !lastPackage?.rebindable || !lastPackage.last_package_id) throw new Error(lastPackage?.reason || "上次套餐不可重新绑定，请在「套餐」中重新选择");
  // Taiwan calendar days, independent of the browser timezone or the old expiry.
  const date = new Date(now.getTime() + 8 * 60 * 60 * 1000);
  const startDate = date.toISOString().slice(0, 10);
  date.setUTCDate(date.getUTCDate() + days);
  return {
    username: user.username, package_id: lastPackage.last_package_id,
    start_date: startDate, expire_date: date.toISOString().slice(0, 10), permanent: false,
    is_reset: user.is_reset ?? true, reset_day: user.reset_day || 1,
    inherit_expire_date: false, inherit_traffic: false,
    traffic_limit_override_gb: user.traffic_limit_override_gb ?? null, confirm_disabled: confirmDisabled,
  };
}

export function validUsername(username: string) { return /^[A-Za-z0-9-]{3,20}$/.test(username); }

export function parseRenewDays(value: string) {
  const days = Number(value);
  if (!Number.isInteger(days) || days < 1 || days > 3650) throw new Error("请输入 1–3650 之间的整数天数");
  return days;
}

export function renewedDate(expiry: string | null | undefined, days: number, now = new Date()) {
  parseRenewDays(String(days));
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const current = expiry ? new Date(`${expiry.slice(0, 10)}T00:00:00`) : today;
  const date = Number.isNaN(current.getTime()) || current < today ? today : current;
  date.setDate(date.getDate() + days);
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
}

export function parseTrafficOverride(value: string): number | null {
  if (!value.trim()) return null;
  const amount = Number(value);
  if (!Number.isFinite(amount) || amount < 0 || !Number.isSafeInteger(Math.trunc(amount * 2 ** 30))) throw new Error("流量覆写必须是有效的非负数");
  return amount;
}

export function trafficOverrideGB(bytes: number | null | undefined) { return bytes == null ? "" : String(bytes / 2 ** 30); }

export function eligiblePackages<T extends { id: number }>(packages: T[], availableIds: number[], assignedIds: number[] = [], currentId?: number) {
  const available = new Set(availableIds);
  const assigned = new Set(assignedIds);
  return packages.filter((pkg) => available.has(pkg.id) && (pkg.id === currentId || !assigned.has(pkg.id)));
}

export function searchUserURIs<T extends { username: string; node_name: string; server_name?: string }>(items: T[], username: string, query: string, server = "") {
  const term = query.trim().toLowerCase();
  return items.filter((item) => item.username === username && (!server || item.server_name === server)
    && (!term || [item.node_name, item.server_name].some((value) => value?.toLowerCase().includes(term))));
}

export function credentialWriteState(state?: string): "allow" | "confirm" | "refuse" {
  if (state === "deleting" || state === "delete_partial") return "refuse";
  return state === "disabled" || state?.startsWith("partially_") || state === "enabling" || state === "disabling" ? "confirm" : "allow";
}

export type UserQuickFilter = "all" | "d7" | "expired" | "disabled";
export type UserSort = "expiry" | "username";

export function managedUserStatus(user: ManagedUser, lifecycle?: ManagedUserLifecycle) {
  const admin = user.role === "admin";
  const officialInactive = user.is_active === false;
  const deleting = lifecycle?.effective_state === "deleting" || lifecycle?.effective_state === "delete_partial";
  const pending = deleting ? lifecycle?.pending_count ?? 0 : 0;
  const accessState = lifecycle?.effective_state ?? "enabled";
  const accessDisabled = accessState === "disabled" || accessState === "partially_disabled" || accessState === "partially_enabled" || accessState === "enabling";
  const accessPending = !deleting && (lifecycle?.pending_count ?? 0) > 0;
  const enableAction = lifecycle?.desired_state === "disabled" || accessState === "disabled" || accessState === "partially_enabled" || accessState === "enabling";
  const accessOperationLabel = lifecycle?.operation === "enable" ? "启用" : "禁用";
  const statusLabel = deleting ? pending > 0 ? "删除未完成" : "正在删除"
    : accessPending ? `${accessOperationLabel}未完成`
      : accessState === "disabling" ? "正在禁用" : accessState === "enabling" ? "正在启用" : accessDisabled ? "已禁用" : "已启用";
  const displayedStatus = officialInactive ? statusLabel === "已启用" ? "官方已停用" : `${statusLabel}（官方已停用）` : statusLabel;
  return { admin, officialInactive, deleting, pending, accessState, accessDisabled, accessPending, enableAction, displayedStatus };
}

export function matchesUserQuickFilter(user: ManagedUser, filter: UserQuickFilter, lifecycle?: ManagedUserLifecycle, lastPackage?: ManagedUserExpiredPackage, ManagedUserLifecycle, now = Date.now()) {
  if (filter === "all") return true;
  if (filter === "disabled") {
    const status = managedUserStatus(user, lifecycle);
    return status.accessDisabled || status.officialInactive;
  }
  return matchesExpiry(user.package_end_date, filter, now, expiredUnboundPackage(user, lastPackage)?.last_package_end_date);
}

export function userQuickCounts(users: ManagedUser[], lifecycles: Record<string, ManagedUserLifecycle>, expiredPackages: Record<string, ManagedUserExpiredPackage>, now = Date.now()) {
  const count = (filter: UserQuickFilter) => users.filter((user) => matchesUserQuickFilter(user, filter, lifecycles[user.username], expiredPackages[user.username], now)).length;
  return { all: users.length, d7: count("d7"), expired: count("expired"), disabled: count("disabled") };
}

export function sortManagedUsers(users: ManagedUser[], sort: UserSort, expiredPackages: Record<string, ManagedUserExpiredPackage>) {
  const end = (user: ManagedUser) => {
    const value = user.package_end_date || expiredUnboundPackage(user, expiredPackages[user.username])?.last_package_end_date;
    const date = value ? Date.parse(value.slice(0, 10)) : NaN;
    return Number.isNaN(date) ? Infinity : date;
  };
  return [...users].sort((a, b) => Number(a.role === "admin") - Number(b.role === "admin")
    || (sort === "expiry" ? (end(a) - end(b) || 0) : 0) || a.username.localeCompare(b.username));
}

export function userRowTags(user: ManagedUser, lifecycle?: ManagedUserLifecycle, expiredPackage?: ManagedUserExpiredPackage, ManagedUserLifecycle, now = Date.now()) {
  const status = managedUserStatus(user, lifecycle);
  const lastPackage = expiredUnboundPackage(user, expiredPackage);
  const days = remainingDays(user.package_end_date, now);
  const expired = Boolean(lastPackage) || (days !== null && days <= 0);
  const critical = [
    status.pending > 0 ? `删除未完成 ${status.pending}` : status.accessPending ? `${lifecycle?.operation === "enable" ? "启用" : "禁用"}未完成` : "",
    lifecycle?.access?.some((item) => item.status === "conflict") ? "冲突" : "",
    lastPackage && !lastPackage.rebindable ? "无法重绑" : "",
  ].filter(Boolean);
  return { critical, expiry: expired ? "已过期" : days === null ? "" : `剩余 ${days} 天`, tone: expired ? "bad" : days !== null && days <= 7 ? "warning" : "", status: status.admin ? "管理员" : status.displayedStatus === "已启用" ? "" : status.displayedStatus };
}
