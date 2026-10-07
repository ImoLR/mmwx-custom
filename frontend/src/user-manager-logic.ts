import type { ManagedUser, ManagedUserExpiredPackage, ManagedUserPackageInput } from "./types";

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
