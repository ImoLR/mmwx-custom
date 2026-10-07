import type { RemoteServer, XrayNode, XrayObject } from "./types";

const visionFlow = "xtls-rprx-vision";

function object(value: unknown): XrayObject {
  return value && typeof value === "object" && !Array.isArray(value) ? value as XrayObject : {};
}

function clients(item: XrayObject): XrayObject[] {
  const values = object(item.settings).clients;
  return Array.isArray(values) ? values.map(object) : [];
}

export function inboundFlowWarning(item: XrayObject) {
  if (!["vless", "trojan"].includes(String(item.protocol).toLowerCase())) return "";
  const flows = new Set(clients(item).map((client) => String(client.flow || "")));
  if (flows.size < 2) return "";
  return flows.has(visionFlow) ? "流控不一致：部分用户带 Vision" : "流控不一致：用户的 flow 不同";
}

export function inboundSecurityMode(item: XrayObject) {
  if (item._wizard_security) return String(item._wizard_security);
  if (inboundFlowWarning(item)) return "inconsistent";
  const settings = object(item.settings);
  if (settings.decryption && settings.decryption !== "none") return "Encryption";
  const security = String(object(item.streamSettings).security || "").toLowerCase();
  const values = clients(item);
  const vision = ["vless", "trojan"].includes(String(item.protocol).toLowerCase()) && values.length > 0 && values.every((client) => client.flow === visionFlow);
  if (security === "reality") return vision ? "XTLS-Vision-REALITY" : "REALITY";
  if (security === "tls") return vision ? "XTLS-Vision" : "TLS";
  return "None";
}

export function normalizeInboundFlow(item: XrayObject, mode = inboundSecurityMode(item)) {
  if (!["vless", "trojan"].includes(String(item.protocol).toLowerCase())) return item;
  if (mode === "inconsistent") throw new Error("流控不一致，请先选择要保留的安全方式（Vision 或无 Vision）");
  const settings = object(item.settings);
  if (!Array.isArray(settings.clients)) return item;
  return { ...item, settings: { ...settings, clients: clients(item).map((client) => {
    const next = { ...client };
    if (mode.includes("Vision")) next.flow = visionFlow;
    else delete next.flow;
    return next;
  }) } };
}

export function nodeInboundFlow(node: XrayNode, inbounds: XrayObject[]) {
  if (!node.inbound_tag || node.node_type === "routed") return null;
  const inbound = inbounds.find((item) => item.tag === node.inbound_tag);
  if (!inbound || !["vless", "trojan"].includes(String(inbound.protocol).toLowerCase())) return null;
  const protocol = String(inbound.protocol).toLowerCase();
  const credentialKey = protocol === "vless" ? "uuid" : "password";
  let config: XrayObject = {};
  for (const raw of [node.clash_config, node.parsed_config]) {
    try {
      const parsed = object(JSON.parse(raw || "{}"));
      if (parsed[credentialKey] && String(parsed.type || node.protocol).toLowerCase() === protocol) { config = parsed; break; }
    } catch { /* Try the other stored link. */ }
  }
  const identity = config[credentialKey];
  const matching = identity ? clients(inbound).filter((client) => protocol === "vless" ? String(client.id || "").toLowerCase() === String(identity).toLowerCase() : client.password === identity) : [];
  const nodeFlow = String(config.flow || "");
  const mismatch = matching.some((client) => String(client.flow || "") !== nodeFlow);
  const mixed = inboundFlowWarning(inbound);
  return { inbound, nodeFlow, matched: matching.length > 0, mismatch, mixed, warning: mismatch ? `流控不一致：节点链接为 ${nodeFlow || "无 Vision"}，同一凭据的入站用户流控不同` : mixed };
}

export function relayPortChangeWarning(original: XrayObject, next: XrayObject, nodes: XrayNode[], server: Pick<RemoteServer, "name">) {
  if (!Number(original.port) || Number(original.port) === Number(next.port)) return "";
  const relays = nodes.filter((node) => node.relay_orig_server && node.inbound_tag === original.tag && (node.original_server || (node.tag?.startsWith("远程:") ? node.tag.slice(3) : "")) === server.name);
  if (!relays.length) return "";
  return `入站端口将从 ${original.port} 改为 ${next.port}；关联中转节点 ${relays.map((node) => node.node_name).join("、")} 的外部中转转发规则必须手动更新，否则仍会转发到旧端口。`;
}
