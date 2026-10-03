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
