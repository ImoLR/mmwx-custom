import type { NodeMutationRequest, RemoteServer, XrayNode } from "./types";

export function duplicateNodeKey(node: XrayNode) {
  try {
    const config = { ...JSON.parse(node.clash_config || ""), __node_name__: node.node_name };
    return JSON.stringify(config, Object.keys(config).sort());
  } catch {
    return `${node.clash_config}|${node.node_name}`;
  }
}

export function duplicateNodeGroups(nodes: XrayNode[]) {
  const groups = new Map<string, XrayNode[]>();
  for (const node of nodes) {
    const key = duplicateNodeKey(node);
    const group = groups.get(key) || [];
    group.push(node);
    groups.set(key, group);
  }
  return [...groups.values()].filter((group) => group.length > 1).map((group) =>
    [...group].sort((a, b) => new Date(a.created_at || "").getTime() - new Date(b.created_at || "").getTime()));
}

function matchesNodeSearch(node: XrayNode, query: string) {
  if (!query.trim()) return true;
  const keyword = query.toLowerCase();
  return [node.node_name, node.protocol, node.tag].some((value) => value?.toLowerCase().includes(keyword));
}

export function chainProxyCandidates(nodes: XrayNode[], sourceId: number, query = "") {
  return nodes.filter((node) => node.id !== sourceId && !node.protocol?.includes("⇋") && !node.inbound_tag && node.node_type !== "routed" && matchesNodeSearch(node, query));
}

export function relayGroupCandidates(nodes: XrayNode[], sourceId: number, query = "") {
  return nodes.filter((node) => node.id !== sourceId && !node.protocol?.includes("⇋") && node.tag !== "中转组" && node.node_type !== "routed" && matchesNodeSearch(node, query));
}

function sourceClash(source: XrayNode) {
  try { return JSON.parse(source.clash_config || ""); }
  catch { throw new Error("源节点配置解析失败"); }
}

export function chainNodePayload(source: XrayNode, target: XrayNode): NodeMutationRequest {
  const name = `${source.node_name} | ${target.node_name}`;
  const config = JSON.stringify({ ...sourceClash(source), name });
  return {
    raw_url: source.raw_url,
    node_name: name,
    protocol: `${source.protocol}⇋${target.protocol}`,
    parsed_config: config,
    clash_config: config,
    enabled: true,
    tag: "链式代理",
    chain_proxy_node_id: target.id,
    relay_orig_server: source.relay_orig_server || "",
    relay_orig_port: source.relay_orig_port || 0,
  };
}

export function relayGroupPayload(source: XrayNode, groupName: string, memberIds: number[]): NodeMutationRequest {
  const clash = sourceClash(source);
  if (source.inbound_tag) return {
    raw_url: source.raw_url,
    node_name: source.node_name,
    protocol: source.protocol,
    parsed_config: source.parsed_config,
    clash_config: source.clash_config,
    enabled: source.enabled,
    tag: source.tag,
    tags: source.tags || [],
    inbound_tag: source.inbound_tag,
    chain_proxy_node_id: null,
    relay_group_name: groupName,
    relay_group_node_ids: memberIds,
    relay_orig_server: source.relay_orig_server || "",
    relay_orig_port: source.relay_orig_port || 0,
  };
  const name = `${source.node_name} | ${groupName}`;
  const config = JSON.stringify({ ...clash, name });
  return {
    raw_url: source.raw_url,
    node_name: name,
    protocol: source.protocol,
    parsed_config: config,
    clash_config: config,
    enabled: true,
    tag: "中转组",
    relay_group_name: groupName,
    relay_group_node_ids: memberIds,
  };
}

export function defaultNodeIPVersion(server: Pick<RemoteServer, "ip_address">): "v4" | "v6" {
  return server.ip_address ? "v4" : "v6";
}

export function subscriptionDefaultTag(url: string, userTag = "", suggestedTag = "") {
  if (userTag.trim()) return userTag.trim();
  if (suggestedTag) return suggestedTag;
  try { return new URL(url).hostname || "外部订阅"; }
  catch { return "外部订阅"; }
}

export function batchRenameTransform(text: string, options: { find?: string; replace?: string; prefix?: string; suffix?: string }) {
  return text.split("\n").map((name) => {
    if (options.find) name = name.replace(new RegExp(options.find.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "g"), options.replace || "");
    return name ? `${options.prefix || ""}${name}${options.suffix || ""}` : name;
  }).join("\n");
}

export function matchesNodeSource(node: XrayNode, source: "all" | "manual" | "subscription") {
  if (source === "all") return true;
  const tags = node.tags?.length ? node.tags : [node.tag || ""];
  const subscription = /^https?:\/\//i.test(node.raw_url || "") || tags.includes("订阅导入") || tags.includes("外部订阅");
  return source === "subscription" ? subscription : !subscription && (tags.includes("手动输入") || !node.original_server && !node.inbound_tag && !node.tag?.startsWith("远程:"));
}

export function moveSelectedNodes(order: number[], selected: ReadonlySet<number>, direction: "top" | "up" | "down" | "bottom") {
  const moving = order.filter((id) => selected.has(id));
  const remaining = order.filter((id) => !selected.has(id));
  if (!moving.length) return [...order];
  if (direction === "top") return [...moving, ...remaining];
  if (direction === "bottom") return [...remaining, ...moving];
  if (direction === "up") {
    const first = order.findIndex((id) => selected.has(id));
    if (first <= 0) return [...order];
    const index = remaining.indexOf(order[first - 1]);
    return [...remaining.slice(0, index), ...moving, ...remaining.slice(index)];
  }
  const last = order.length - 1 - [...order].reverse().findIndex((id) => selected.has(id));
  if (last >= order.length - 1) return [...order];
  const index = remaining.indexOf(order[last + 1]);
  return [...remaining.slice(0, index + 1), ...moving, ...remaining.slice(index + 1)];
}

export type NodeOwner = {
  users: string[];
  admin_only: boolean;
  shared: boolean;
  source: "credential" | "package" | "none";
  inbound_backed: boolean;
  parent_node_id?: number;
};
export type NodeOwners = Record<string, NodeOwner>;
export type NodeGrouping = "user" | "server" | "package" | "none";
export type NodeGroup = { key: string; name: string; subtitle: string; nodes: XrayNode[] };
type Memberships = Record<string, Array<{ package_id: number; package_name: string }>>;

export function nodeOwnership(node: XrayNode, owners: NodeOwners): NodeOwner {
  return owners[String(node.id)] || { users: [], admin_only: false, shared: false, source: "none", inbound_backed: Boolean(node.original_server && node.inbound_tag) };
}

export function nodeOwnerHint(owner: NodeOwner) {
  const label = owner.admin_only ? "自用（管理员）" : owner.users.join("、") || (owner.inbound_backed ? "未归属" : "外部节点");
  return `${label} · ${owner.source === "credential" ? "依据入站凭据 / 业务关系" : owner.source === "package" ? "依据唯一非管理员套餐绑定" : "没有可确认的归属"}${owner.shared ? " · 多位用户共用" : ""}`;
}

export function groupNodes(nodes: XrayNode[], grouping: NodeGrouping, owners: NodeOwners, memberships: Memberships): NodeGroup[] {
  const groups = new Map<string, NodeGroup>();
  for (const node of nodes) {
    const owner = nodeOwnership(node, owners);
    const entries = grouping === "user"
      ? owner.admin_only ? [["admin", "自用（管理员）"]] : owner.users.length ? owner.users.map((user) => [`user:${user}`, user]) : [[owner.inbound_backed ? "unowned" : "external", owner.inbound_backed ? "未归属" : "外部节点"]]
      : grouping === "server" ? [[`server:${node.original_server || ""}`, node.original_server || "外部节点"]]
      : grouping === "package" ? memberships[node.id]?.length ? memberships[node.id].map((pkg) => [`package:${pkg.package_id}`, pkg.package_name]) : [["unpackaged", "未加入套餐"]]
      : [["none", "全部节点"]];
    for (const [key, name] of entries) {
      if (!groups.has(key)) groups.set(key, { key, name, subtitle: "", nodes: [] });
      const group = groups.get(key)!;
      if (!group.nodes.some((item) => item.id === node.id)) group.nodes.push(node);
    }
  }
  const rank = (key: string) => key === "admin" ? 1 : key === "external" || key === "unpackaged" ? 2 : key === "unowned" ? 3 : 0;
  return [...groups.values()].map((group) => {
    const servers = [...new Set(group.nodes.map((node) => node.original_server).filter(Boolean))];
    const packages = [...new Set(group.nodes.flatMap((node) => (memberships[node.id] || []).map((pkg) => pkg.package_name)))];
    return { ...group, subtitle: grouping === "user" && packages.length === 1 ? `套餐 ${packages[0]}` : servers.join(" · ") || "外部节点" };
  }).sort((a, b) => rank(a.key) - rank(b.key) || a.name.localeCompare(b.name, "en", { sensitivity: "base" }));
}

export function isNodeRelay(node: XrayNode) {
  return Boolean(node.relay_orig_server || node.inbound_tag?.endsWith("-relay"));
}

export function nodeRelayRows(nodes: XrayNode[], owners: NodeOwners) {
  const ids = new Set(nodes.map((node) => node.id));
  const children = new Map<number, XrayNode[]>();
  const childIds = new Set<number>();
  for (const node of nodes) {
    const parent = nodeOwnership(node, owners).parent_node_id;
    if (isNodeRelay(node) && parent && parent !== node.id && ids.has(parent) && !isNodeRelay(nodes.find((item) => item.id === parent)!)) {
      children.set(parent, [...children.get(parent) || [], node]);
      childIds.add(node.id);
    }
  }
  return nodes.filter((node) => !childIds.has(node.id)).map((node) => ({ node, children: children.get(node.id) || [] }));
}

export function toggleNodeSelection(selected: Set<number>, ids: number[]) {
  const next = new Set(selected);
  const remove = ids.length > 0 && ids.every((id) => next.has(id));
  for (const id of ids) remove ? next.delete(id) : next.add(id);
  return next;
}

export function matchesNodeFilters(node: XrayNode, parsed: Record<string, unknown>, owner: NodeOwner, filters: {
  query: string; protocol: string; tags: string[]; source: "all" | "manual" | "subscription"; server: string; state: string;
}) {
  const protocol = String(node.protocol || parsed.type || "").trim().toLowerCase().replace(/^shadowsocks$/, "ss").replace(/^socks$/, "socks5");
  const tags = (node.tags?.length ? node.tags : node.tag ? [node.tag] : []).map((tag) => tag.trim()).filter(Boolean);
  const text = [node.node_name, protocol, parsed.server, parsed.port, node.original_server, node.inbound_tag, node.routed_outbound_tag, node.relay_orig_server, ...tags, ...owner.users, owner.admin_only ? "自用 管理员" : ""].join(" ").toLowerCase();
  return (!filters.query.trim() || text.includes(filters.query.trim().toLowerCase()))
    && (filters.protocol === "all" || protocol === filters.protocol)
    && (!filters.tags.length || tags.some((tag) => filters.tags.includes(tag)))
    && matchesNodeSource(node, filters.source)
    && (filters.server === "all" || (node.original_server || "外部节点") === filters.server)
    && (filters.state !== "enabled" || node.enabled !== false)
    && (filters.state !== "disabled" || node.enabled === false)
    && (filters.state !== "relay" || isNodeRelay(node))
    && (filters.state !== "routed" || node.node_type === "routed");
}
