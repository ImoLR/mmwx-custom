import type { ManagedPackage, PackagePayload, PackageAutoSpeedRule, RemoteServer, XrayNode } from "./types";

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
    if (value === "" || value === node.node_name) delete values[node.id];
    else values[node.id] = value;
  } else {
    const parsed = key === "node_device_limits" ? parseInt(value, 10) : parseFloat(value);
    if (value === "" || (key === "node_multipliers" && (!Number.isFinite(parsed) || parsed === 1))) delete values[node.id];
    else if (Number.isFinite(parsed) && parsed >= 0) values[node.id] = parsed;
  }
  return { ...form, [key]: values };
}
