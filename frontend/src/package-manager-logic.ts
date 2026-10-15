import type { ManagedPackage, ManagedUser, PackagePayload, PackageAutoSpeedRule, RemoteServer, XrayNode } from "./types";

export const emptyPackage = (): PackagePayload => ({
  name: "",
  description: "",
  traffic_limit_gb: 100,
  cycle_days: 30,
  is_reset: true,
  reset_day: 1,
  nodes: [],
  node_multipliers: {},
  node_name_overrides: {},
  node_name_override_enabled: false,
  node_speed_limits: {},
  node_device_limits: {},
  node_traffic_limits: {},
  speed_limit_mbps: 0,
  device_limit: 0,
  ip_limit: 0,
  ip_over_limit_action: "reject",
  auto_speed_rules: [],
  forward_rule_limit: 0,
  forward_port_limit: 0,
  forward_speed_mbps: 0,
  forward_conn_limit: 0,
  forward_chains: [],
  traffic_mode: "oneway",
  template_filename: "",
  surge_template_filename: "",
  loon_template_filename: "",
});

// Official updates replace the whole record; preserve unknown writable fields.
const PACKAGE_READ_ONLY_FIELDS = new Set(["nodes_configured", "short_code", "created_at", "updated_at"]);

export function packageToForm(pkg: ManagedPackage): PackagePayload {
  const preserved = Object.fromEntries(Object.entries(pkg).filter(([key]) => !PACKAGE_READ_ONLY_FIELDS.has(key)));
  return {
    ...emptyPackage(),
    ...preserved,
    id: pkg.id,
    name: pkg.name,
    description: pkg.description ?? "",
    traffic_limit_gb: Number(pkg.traffic_limit_gb) || 0,
    cycle_days: Number(pkg.cycle_days ?? 30),
    is_reset: Boolean(pkg.is_reset),
    reset_day: Number(pkg.reset_day) || 1,
    nodes: (pkg.nodes ?? []).map(Number),
    node_multipliers: numericMap(pkg.node_multipliers),
    node_name_overrides: stringMap(pkg.node_name_overrides),
    node_name_override_enabled: Boolean(pkg.node_name_override_enabled),
    node_speed_limits: numericMap(pkg.node_speed_limits),
    node_device_limits: numericMap(pkg.node_device_limits),
    node_traffic_limits: numericMap(pkg.node_traffic_limits),
    speed_limit_mbps: Number(pkg.speed_limit_mbps) || 0,
    device_limit: Number(pkg.device_limit) || 0,
    ip_limit: Number(pkg.ip_limit) || 0,
    ip_over_limit_action: pkg.ip_over_limit_action || "reject",
    auto_speed_rules: (pkg.auto_speed_rules ?? []).map((rule) => ({ ...rule })),
    forward_rule_limit: Number(pkg.forward_rule_limit) || 0,
    forward_port_limit: Number(pkg.forward_port_limit) || 0,
    forward_speed_mbps: Number(pkg.forward_speed_mbps) || 0,
    forward_conn_limit: Number(pkg.forward_conn_limit) || 0,
    forward_chains: (pkg.forward_chains ?? []).map(Number),
    traffic_mode: pkg.traffic_mode || "oneway",
    template_filename: pkg.template_filename ?? "",
    surge_template_filename: pkg.surge_template_filename ?? "",
    loon_template_filename: pkg.loon_template_filename ?? "",
  };
}

function numericMap(value: Record<number, number> | null | undefined) {
  return Object.fromEntries(Object.entries(value ?? {}).map(([key, item]) => [Number(key), Number(item)]));
}

function stringMap(value: Record<number, string> | null | undefined) {
  return Object.fromEntries(Object.entries(value ?? {}).map(([key, item]) => [Number(key), String(item)]));
}

export function packageTemplateType(filename: string): "clash" | "surge" | "loon" {
  return /\.conf$/i.test(filename) ? "surge" : /\.lcf$/i.test(filename) ? "loon" : "clash";
}

export function orderPackageNodes(selected: number[], nodeOrder: number[]): number[] {
  const positions = new Map(nodeOrder.map((id, index) => [id, index]));
  return [...selected].sort((a, b) => (positions.get(a) ?? Infinity) - (positions.get(b) ?? Infinity));
}

export function packageResetDay(nodeIds: number[], nodes: XrayNode[], servers: RemoteServer[]): number {
  const first = nodes.find((node) => node.id === nodeIds[0]);
  const day = Number(servers.find((server) => server.name === first?.original_server)?.traffic_reset_day);
  return Number.isInteger(day) && day >= 1 && day <= 31 ? day : 1;
}

export function packageMemberNodeIds(selected: number[], available: number[]): number[] {
  return selected.length > 0 ? selected : available;
}

export function newAutoSpeedRule(): PackageAutoSpeedRule {
  return { type: "sustained", threshold_mbps: 1000, sustained_seconds: 30, window_seconds: 60, burst_count: 3, limit_mbps: 200, limit_duration: 300 };
}

export function validatePackage(form: PackagePayload): string {
  if (!form.name.trim()) return "请输入套餐名称";
  if (!Number.isFinite(form.traffic_limit_gb) || form.traffic_limit_gb < 0) return "流量额度不能为负数（0 = 不限）";
  if (!Number.isInteger(form.cycle_days) || form.cycle_days <= 0) return "计量周期必须大于0";
  if (form.is_reset && (!Number.isInteger(form.reset_day) || form.reset_day < 1 || form.reset_day > 31)) return "默认重置日期必须在 1 到 31 之间";
  for (const limit of Object.values(form.node_traffic_limits)) {
    if (!Number.isFinite(limit) || limit < 0) return "节点流量额度不能为负数";
    if (form.traffic_limit_gb > 0 && limit > form.traffic_limit_gb) return "单个节点的流量额度不能大于套餐流量额度";
  }
  return "";
}

export function updatePackageNodeOverride(form: PackagePayload, key: "node_multipliers" | "node_name_overrides" | "node_speed_limits" | "node_device_limits" | "node_traffic_limits", node: Pick<XrayNode, "id" | "node_name">, value: string): PackagePayload {
  const values = { ...form[key] } as Record<number, number | string>;
  if (key === "node_name_overrides") {
    // Keep "" while editing so a cleared input stays empty instead of snapping back to the original name.
    if (value === node.node_name) delete values[node.id];
    else values[node.id] = value;
  } else {
    const parsed = key === "node_device_limits" ? parseInt(value, 10) : parseFloat(value);
    if (value === "" || (key === "node_multipliers" && (!Number.isFinite(parsed) || parsed === 1))) delete values[node.id];
    else if (Number.isFinite(parsed) && parsed >= 0) values[node.id] = parsed;
  }
  return { ...form, [key]: values };
}

export function packageSavePayload(form: PackagePayload, availableIds?: number[]): PackagePayload {
  const exists = (id: number) => !availableIds || availableIds.includes(id);
  const payload = { ...form, nodes: form.nodes.filter(exists), node_name_overrides: Object.fromEntries(Object.entries(form.node_name_overrides).filter(([id, name]) => exists(Number(id)) && name.trim() !== "")) };
  for (const key of ["node_multipliers", "node_speed_limits", "node_device_limits", "node_traffic_limits"] as const) {
    payload[key] = Object.fromEntries(Object.entries(form[key]).filter(([id]) => exists(Number(id))));
  }
  return payload;
}

export type PackageGrouping ="user" | "server" | "none";
export type CompactPackageGroup = { key: string; name: string; packages: ManagedPackage[] };

export function packageBoundUsers(pkg: ManagedPackage, users: ManagedUser[]) {
  return users.filter((user) => user.role !== "admin" && (user.package_id === pkg.id || user.assignment_package_ids?.includes(pkg.id)));
}

export function packageDanglingNodeIds(pkg: Pick<ManagedPackage, "nodes" | "node_name_overrides" | "node_multipliers" | "node_speed_limits" | "node_device_limits" | "node_traffic_limits">, nodes: XrayNode[]) {
  // Official filters deleted IDs out of nodes, but keeps their per-node maps.
  const ids = [...(pkg.nodes ?? []), ...[pkg.node_name_overrides, pkg.node_multipliers, pkg.node_speed_limits, pkg.node_device_limits, pkg.node_traffic_limits].flatMap((values) => Object.keys(values ?? {}).map(Number))];
  return [...new Set(ids)].filter((id) => !nodes.some((node) => node.id === id));
}

export function packageServers(pkg: ManagedPackage, nodes: XrayNode[]) {
  const linked = [...new Set((pkg.nodes ?? []).map((id) => {
    const node = nodes.find((node) => node.id === id);
    return node ? node.original_server || "外部节点" : `已删除节点 #${id}`;
  }).concat(packageDanglingNodeIds(pkg, nodes).map((id) => `已删除节点 #${id}`)))];
  if (!pkg.nodes?.length && !pkg.nodes_configured && linked.length) linked.unshift("全部节点");
  return linked.length || !pkg.nodes_configured ? linked : ["已删除节点"];
}

export function groupPackages(packages: ManagedPackage[], users: ManagedUser[], nodes: XrayNode[], grouping: PackageGrouping): CompactPackageGroup[] {
  if (grouping === "none") return [{ key: "all", name: "全部套餐", packages }];
  const groups = new Map<string, CompactPackageGroup>();
  for (const pkg of packages) {
    const keys = grouping === "user"
      ? packageBoundUsers(pkg, users).map((user) => ({ key: `user:${user.username}`, name: user.username }))
      : packageServers(pkg, nodes).map((name) => ({ key: `server:${name}`, name }));
    if (!keys.length) keys.push(grouping === "user" ? { key: "unbound", name: "未绑定" } : { key: "all-nodes", name: "全部节点" });
    for (const { key, name } of keys) {
      const group = groups.get(key) || { key, name, packages: [] };
      group.packages.push(pkg);
      groups.set(key, group);
    }
  }
  return [...groups.values()].sort((a, b) => Number(a.key === "unbound") - Number(b.key === "unbound") || a.name.localeCompare(b.name));
}

export function searchPackages(packages: ManagedPackage[], nodes: XrayNode[], query: string) {
  const term = query.trim().toLowerCase();
  if (!term) return packages;
  return packages.filter((pkg) => [pkg.name, pkg.short_code, pkg.description, ...nodes.filter((node) => !pkg.nodes?.length || pkg.nodes.includes(node.id)).map((node) => node.node_name)].some((text) => text?.toLowerCase().includes(term)));
}

export function packageOverrideCount(pkg: ManagedPackage) {
  return new Set([pkg.node_speed_limits, pkg.node_device_limits, pkg.node_traffic_limits, pkg.node_multipliers, pkg.node_name_overrides].flatMap((values) => Object.keys(values ?? {}))).size;
}
