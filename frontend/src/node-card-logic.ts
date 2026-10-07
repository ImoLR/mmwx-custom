import type { NodeTunnel, NodeTunnelChain, RemoteServer, XrayNode, XrayObject } from "./types";

export type NodeRoutingState = { rules?: XrayObject[]; balancers?: XrayObject[]; outbounds?: XrayObject[] };

type NodeRoutingResponse = { success?: boolean; message?: string; error?: string; routing?: NodeRoutingState; outbounds?: XrayObject[] };

export function nodeRoutingReadResult(server: Pick<RemoteServer, "id" | "name">, rules: PromiseSettledResult<NodeRoutingResponse>, outbounds: PromiseSettledResult<NodeRoutingResponse>) {
  const failures: string[] = [];
  for (const [label, result] of [["路由", rules], ["出站", outbounds]] as const) {
    if (result.status === "rejected") failures.push(`${label}：${result.reason instanceof Error ? result.reason.message : "请求失败"}`);
    else if (result.value.success === false) failures.push(`${label}：${result.value.error || result.value.message || "读取失败"}`);
  }
  if (failures.length || rules.status !== "fulfilled" || outbounds.status !== "fulfilled") {
    return { state: undefined, error: `${server.name}（#${server.id}）：${failures.join("；")}` };
  }
  return { state: { ...rules.value.routing, outbounds: outbounds.value.outbounds || [] }, error: "" };
}

export function nodeCardConfig(node: XrayNode): XrayObject {
  for (const raw of [node.clash_config, node.parsed_config]) {
    try { const value = JSON.parse(raw || "{}"); if (value && typeof value === "object" && value.server) return value; } catch { /* Historical configs may be invalid. */ }
  }
  return {};
}

export function nodeManagedServer(node: XrayNode, servers: RemoteServer[]) {
  const name = node.original_server || (node.tag?.startsWith("远程:") ? node.tag.slice(3) : "");
  return servers.find((server) => server.name === name);
}

export function tunnelEntryHost(tunnel: NodeTunnel, servers: RemoteServer[]) {
  const server = servers.find((item) => item.id === tunnel.server_id);
  return server?.domain?.trim() || server?.ip_address?.trim() || server?.pull_address?.trim() || tunnel.server_name || "";
}

export function nodeTunnels(node: XrayNode, tunnels: NodeTunnel[], servers: RemoteServer[]) {
  const parsed = nodeCardConfig(node), server = nodeManagedServer(node, servers);
  const port = Number(node.relay_orig_port || parsed.port);
  const hosts = new Set([node.relay_orig_server || parsed.server, server?.ip_address, server?.domain, server?.pull_address].filter(Boolean));
  if (!port || !hosts.size) return [];
  return tunnels.filter((tunnel) => Number(tunnel.target_port) === port && hosts.has(tunnel.target_address));
}

export function nodeTunnelChain(node: XrayNode, chains: NodeTunnelChain[], servers: RemoteServer[]) {
  if (!node.relay_orig_server || !node.relay_orig_port) return null;
  const target = `${node.relay_orig_server.trim()}:${node.relay_orig_port}`;
  const matches = chains.filter((chain) => chain.final_target === target);
  if (matches.length <= 1) return matches[0] || null;
  const parsed = nodeCardConfig(node), byPort = matches.filter((chain) => Number(chain.entry_port) === Number(parsed.port));
  if (byPort.length <= 1) return byPort[0] || null;
  const byHost = byPort.filter((chain) => {
    const server = servers.find((item) => item.id === chain.entry_server);
    return [server?.ip_address, server?.domain, server?.pull_address, server?.ip_address_v6, server?.domain_v6].some((host) => host?.trim() === String(parsed.server || "").trim());
  });
  return byHost.length === 1 ? byHost[0] : null;
}

export function externalNodeSource(node: XrayNode, servers: RemoteServer[]) {
  if (nodeManagedServer(node, servers)) return "";
  try {
    const address = JSON.parse(node.parsed_config || "{}").server;
    if (address && servers.some((server) => [server.ip_address, server.ip_address_v6, server.domain, server.domain_v6, server.pull_address].includes(String(address).trim()))) return "";
  } catch { /* Historical nodes may only have their source tag. */ }
  const source = node.tags?.[0]?.trim();
  return source ? `📥 外部:${source}` : "";
}

const matchFields = ["domain", "ip", "port", "sourcePort", "network", "source", "user", "protocol", "attrs"];
export function wholeOutboundRule(rules: XrayObject[], inboundTag: string) {
  if (!inboundTag) return undefined;
  return rules.find((rule) => Array.isArray(rule.inboundTag) && rule.inboundTag.includes(inboundTag) && (rule.outboundTag || rule.balancerTag) && !matchFields.some((field) => {
    const value = rule[field];
    return Array.isArray(value) ? value.length > 0 : typeof value === "string" ? value.trim() !== "" : value != null;
  }));
}

function outboundName(outbound: XrayObject, nodes: XrayNode[], servers: RemoteServer[], tunnels: NodeTunnel[]) {
  const tag = String(outbound.tag || ""), target = /^landing-node-\d+-target-(\d+)-/.exec(tag);
  if (target) { const node = nodes.find((item) => item.id === Number(target[1])); if (node) return node.node_name; }
  const settings = outbound.settings as { vnext?: Array<{ address: string; port: number }>; servers?: Array<{ address: string; port: number; users?: Array<{ user?: string; pass?: string }> }>; address?: string; port?: number } | undefined;
  const endpoint = settings?.vnext?.[0] || settings?.servers?.[0] || settings;
  const socks = outbound.protocol === "socks" ? settings?.servers?.[0]?.users?.[0] : undefined;
  const resolve = (address?: string, port?: number, depth = 0): string => {
    if (!address || depth > 3) return "";
    const node = nodes.find((item) => {
      const config = nodeCardConfig(item);
      return config.server === address && Number(config.port) === Number(port) && (!socks || (config.type === "socks5" && String(config.username || "") === String(socks.user || "") && String(config.password || "") === String(socks.pass || "")));
    });
    if (node) return socks ? node.node_name : nodeManagedServer(node, servers)?.name || node.node_name;
    const server = servers.find((item) => [item.ip_address, item.domain, item.pull_address].includes(address));
    const candidates = tunnels.filter((tunnel) => Number(tunnel.listen_port) === Number(port));
    const tunnel = server ? candidates.find((item) => item.server_id === server.id) : candidates.length === 1 ? candidates[0] : undefined;
    return (tunnel && resolve(tunnel.target_address, tunnel.target_port, depth + 1)) || server?.name || tunnel?.server_name || "";
  };
  return resolve(endpoint?.address, endpoint?.port) || tag;
}

export function resolveWholeOutbound(node: XrayNode, state: NodeRoutingState | undefined, nodes: XrayNode[], servers: RemoteServer[], tunnels: NodeTunnel[]) {
  if (node.node_type === "routed" || !node.inbound_tag || !state) return null;
  const rule = wholeOutboundRule(state.rules || [], node.inbound_tag);
  if (!rule) return null;
  const tag = String(rule.balancerTag || rule.outboundTag || ""), outbounds = state.outbounds || [];
  if (!rule.balancerTag) {
    if (["direct", "freedom", "block", "blackhole", "dns-out", "dns", "api"].includes(tag.toLowerCase())) return null;
    const outbound = outbounds.find((item) => item.tag === tag);
    return { tag, label: outbound ? outboundName(outbound, nodes, servers, tunnels) : tag, members: [] as string[] };
  }
  const balancer = state.balancers?.find((item) => item.tag === tag), selectors = Array.isArray(balancer?.selector) ? balancer.selector.map(String) : [];
  return { tag, label: `负载均衡:${tag}`, members: [...new Set(outbounds.filter((item) => selectors.some((prefix) => String(item.tag || "").startsWith(prefix))).map((item) => outboundName(item, nodes, servers, tunnels)))] };
}
