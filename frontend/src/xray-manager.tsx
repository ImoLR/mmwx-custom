import { useCallback, useEffect, useMemo, useState } from "react";
import {
  ArrowDown,
  ArrowUp,
  Braces,
  Check,
  Cloud,
  Edit3,
  Eye,
  KeyRound,
  LoaderCircle,
  Play,
  Plus,
  RefreshCw,
  RotateCw,
  Save,
  Search,
  ShieldCheck,
  Square,
  Trash2,
  X,
} from "lucide-react";
import {
  controlRemoteService,
  fetchXrayConfig,
  fetchXrayInbounds,
  fetchNginxServerDomains,
  fetchXrayNodes,
  fetchRealityDomains,
  fetchValidCertificates,
  fetchXrayUsers,
  fetchXrayOutbounds,
  fetchXrayServerNICs,
  fetchXrayRouting,
  fetchXrayServiceStatus,
  fetchXraySystemConfig,
  mutateXrayInbound,
  mutateXrayOutbound,
  mutateXrayRouting,
  fetchXrayWarpStatus,
  generateXrayProtocolKeys,
  generateXrayX25519,
  probeCustomRealityDomain,
  saveXrayConfig,
  saveXraySystemConfig,
  installXrayWarp,
  removeXrayWarp,
  testXrayConfig,
  setupRemoteSSL,
  updateXrayUserEmail,
  updateXrayWarpLicense,
  updateRemoteServerDomain,
} from "./api";
import type { RemoteServer, XrayNode, XrayObject, XrayServerNIC, XraySystemConfig, XrayWarpStatus } from "./types";

type Tab = "config" | "inbounds" | "outbounds" | "routing";
type Notice = { kind: "success" | "error"; text: string } | null;

const defaultSystemConfig: XraySystemConfig = {
  metrics_enabled: false,
  metrics_listen: "127.0.0.1:38889",
  stats_enabled: false,
  grpc_enabled: false,
  grpc_port: 46736,
};

const inboundProtocols = [
  { value: "vless", label: "VLESS" },
  { value: "shadowsocks", label: "Shadowsocks" },
  { value: "shadowsocks2022", label: "Shadowsocks 2022" },
  { value: "socks", label: "Socks5" },
  { value: "trojan", label: "Trojan" },
  { value: "vmess", label: "VMess" },
  { value: "hysteria", label: "Hysteria2" },
  { value: "anytls", label: "AnyTLS" },
  { value: "http", label: "HTTP" },
  { value: "tunnel", label: "Tunnel" },
  { value: "snell", label: "Snell" },
  { value: "mieru", label: "Mieru" },
];
const outboundProtocols = ["freedom", "blackhole", "dns", "http", "loopback"];
const outboundProtocolLabels: Record<string, string> = {
  freedom: "直连",
  blackhole: "阻断",
  dns: "DNS",
  http: "HTTP",
  loopback: "回环",
  vmess: "VMess",
  vless: "VLESS",
  trojan: "Trojan",
  shadowsocks: "Shadowsocks",
  socks: "Socks",
  wireguard: "WireGuard",
  hysteria: "Hysteria2",
  snell: "Snell",
  anytls: "AnyTLS",
};
const domainStrategyLabels: Record<string, string> = {
  AsIs: "保持原样（AsIs）",
  IPIfNonMatch: "未匹配时解析 IP",
  IPOnDemand: "按需解析 IP",
  UseIP: "优先使用 IP",
  UseIPv4: "优先使用 IPv4",
  UseIPv6: "优先使用 IPv6",
  ForceIP: "强制使用 IP",
  ForceIPv4: "强制使用 IPv4",
  ForceIPv6: "强制使用 IPv6",
};
const routingFieldLabels: Record<string, string> = {
  domain: "域名",
  ip: "IP",
  port: "目标端口",
  protocol: "协议",
  network: "网络",
  inboundTag: "入站标识",
  user: "用户",
  source: "来源 IP",
  sourcePort: "来源端口",
  attrs: "属性",
};
const markTagLabels: Record<string, string> = {
  "ban-bt": "禁止 BT",
  "ban-cn-ip": "禁止访问大陆 IP",
  "ban-private": "禁止内网访问",
  "openai-direct": "OpenAI 直连",
  "anti-cn-warp": "防止送中（走 WARP）",
  "speedtest-warp": "测速分流（走 WARP）",
  "home-bypass-warp": "家宽常用（走 WARP）",
  tiktok: "抖音解锁",
  emby: "RFC EMBY",
};
const inboundCombinations: Record<string, Array<{ transport: string; securities: string[] }>> = {
  shadowsocks: [{ transport: "None", securities: ["None"] }],
  shadowsocks2022: [{ transport: "2022", securities: ["None"] }],
  socks: [{ transport: "TLS", securities: ["None"] }],
  trojan: [{ transport: "GRPC", securities: ["REALITY"] }, { transport: "TCP", securities: ["REALITY", "TLS"] }],
  vless: [
    { transport: "GRPC", securities: ["REALITY"] },
    { transport: "TCP", securities: ["REALITY", "TLS", "TLS-WS", "XTLS-Vision", "XTLS-Vision-REALITY", "Encryption"] },
    { transport: "WSS", securities: ["None"] },
    { transport: "XHTTP", securities: ["REALITY"] },
  ],
  vmess: [{ transport: "TCP", securities: ["None", "TLS"] }, { transport: "Websocket", securities: ["None", "TLS"] }],
  hysteria: [{ transport: "hysteria", securities: ["TLS"] }],
  anytls: [{ transport: "TCP", securities: ["TLS", "REALITY"] }],
  http: [{ transport: "None", securities: ["None"] }],
  tunnel: [{ transport: "None", securities: ["None"] }],
  snell: [{ transport: "None", securities: ["None"] }],
  mieru: [{ transport: "None", securities: ["None"] }],
};

function asString(value: unknown) {
  return typeof value === "string" ? value : value == null ? "" : String(value);
}

function asNumber(value: unknown) {
  return typeof value === "number" ? value : Number(value) || 0;
}

function asObject(value: unknown): XrayObject {
  return value && typeof value === "object" && !Array.isArray(value) ? value as XrayObject : {};
}

function displayMarkTag(value: unknown, index: number) {
  const tag = asString(value);
  return markTagLabels[tag] || tag || `规则 ${index + 1}`;
}

function parseObject(text: string, label = "JSON") {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch (error) {
    throw new Error(`${label} 格式错误：${error instanceof Error ? error.message : "无法解析"}`);
  }
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`${label} 必须是对象`);
  return value as XrayObject;
}

function getError(error: unknown, fallback: string) {
  return error instanceof Error ? error.message : fallback;
}

function describeOutbound(outbound: XrayObject) {
  const settings = asObject(outbound.settings);
  const vnext = Array.isArray(settings.vnext) ? asObject(settings.vnext[0]) : {};
  const servers = Array.isArray(settings.servers) ? asObject(settings.servers[0]) : {};
  return {
    address: asString(vnext.address || servers.address) || "--",
    port: asString(vnext.port || servers.port) || "--",
    users: Array.isArray(vnext.users) ? vnext.users.length : Array.isArray(servers.users) ? servers.users.length : 0,
  };
}

function inboundUsers(inbound: XrayObject) {
  const settings = asObject(inbound.settings);
  if (Array.isArray(settings.clients)) return settings.clients.length;
  if (Array.isArray(settings.accounts)) return settings.accounts.length;
  return 0;
}

function transportName(item: XrayObject) {
  const stream = asObject(item.streamSettings);
  return asString(stream.network) || "tcp";
}

function securityName(item: XrayObject) {
  const stream = asObject(item.streamSettings);
  return asString(stream.security) || "none";
}

function inboundSecurityMode(item: XrayObject) {
  const wizardMode = asString(item._wizard_security);
  if (wizardMode) return wizardMode;
  const settings = asObject(item.settings);
  if (asString(settings.decryption) && asString(settings.decryption) !== "none") return "Encryption";
  const security = asString(asObject(item.streamSettings).security).toLowerCase();
  const clients = Array.isArray(settings.clients) ? settings.clients as XrayObject[] : [];
  const vision = clients.some((client) => asString(client.flow) === "xtls-rprx-vision");
  if (security === "reality") return vision ? "XTLS-Vision-REALITY" : "REALITY";
  if (security === "tls") return vision ? "XTLS-Vision" : "TLS";
  return "None";
}

function inboundProtocolMode(item: XrayObject) {
  const wizardMode = asString(item._wizard_protocol);
  if (wizardMode) return wizardMode;
  const protocol = asString(item.protocol).toLowerCase();
  if (protocol === "shadowsocks" && asString(asObject(item.settings).method).startsWith("2022-")) return "shadowsocks2022";
  return protocol;
}

function displayInboundTransport(value: string) {
  return ({ None: "无", Websocket: "WebSocket", GRPC: "gRPC", hysteria: "Hysteria2" } as Record<string, string>)[value] || value;
}

function displayInboundSecurity(value: string) {
  return ({ None: "无", Encryption: "加密" } as Record<string, string>)[value] || value;
}

function inboundTransportMode(item: XrayObject) {
  const wizardMode = asString(item._wizard_transport);
  if (wizardMode) return wizardMode;
  const protocol = asString(item.protocol).toLowerCase();
  const stream = asObject(item.streamSettings);
  if (protocol === "shadowsocks") return asString(asObject(item.settings).method).startsWith("2022-") ? "2022" : "None";
  if (protocol === "socks") return "TLS";
  if (["http", "tunnel", "snell"].includes(protocol)) return "None";
  if (protocol === "hysteria" || asString(stream.network) === "hysteria") return "hysteria";
  if (asString(stream.network) === "grpc") return "GRPC";
  if (asString(stream.network) === "xhttp") return "XHTTP";
  if (asString(stream.network) === "ws") return protocol === "vless" && asString(stream.security) === "none" ? "WSS" : "Websocket";
  return "TCP";
}

function inboundCredentialKey(protocol: string) {
  if (protocol === "socks" || protocol === "http") return "accounts";
  if (protocol === "anytls" || protocol === "snell" || protocol === "mieru") return "users";
  return "clients";
}

function defaultInbound(protocol = "vless"): XrayObject {
  const actualProtocol = protocol === "shadowsocks2022" ? "shadowsocks" : protocol;
  const base: XrayObject = { tag: `${protocol}-443`, protocol: actualProtocol, _wizard_protocol: protocol, listen: "0.0.0.0", port: 443, sniffing: { enabled: true, destOverride: ["http", "tls"], routeOnly: true } };
  if (protocol === "shadowsocks") return { ...base, tag: "shadowsocks-8388", port: 8388, _wizard_transport: "None", _wizard_security: "None", settings: { method: "aes-256-gcm", network: "tcp,udp", clients: [] }, streamSettings: { network: "tcp" } };
  if (protocol === "shadowsocks2022") return { ...base, tag: "shadowsocks2022-8388", port: 8388, _wizard_transport: "2022", _wizard_security: "None", settings: { method: "2022-blake3-aes-128-gcm", password: randomBase64(16), network: "tcp,udp", clients: [] }, streamSettings: { network: "tcp" } };
  if (protocol === "socks") return { ...base, tag: "socks5-443", _wizard_transport: "TLS", _wizard_security: "None", settings: { auth: "password", udp: true, accounts: [] }, streamSettings: { network: "tcp", security: "none" } };
  if (protocol === "trojan") return { ...base, tag: "trojan-tcp-reality-443", _wizard_transport: "TCP", _wizard_security: "REALITY", settings: { clients: [] }, streamSettings: { network: "tcp", security: "reality", realitySettings: { dest: "www.lovelive-anime.jp:443", serverNames: ["www.lovelive-anime.jp"], shortIds: [""] } } };
  if (protocol === "vmess") return { ...base, tag: "vmess-tcp-443", _wizard_transport: "TCP", _wizard_security: "None", settings: { clients: [] }, streamSettings: { network: "tcp", security: "none" } };
  if (protocol === "hysteria") return { ...base, tag: "hysteria2-443", _wizard_transport: "hysteria", _wizard_security: "TLS", settings: { version: 2, clients: [] }, streamSettings: { network: "hysteria", security: "tls", tlsSettings: { alpn: ["h3"] }, hysteriaSettings: { version: 2 } } };
  if (protocol === "anytls") return { ...base, tag: "anytls-tcp-tls-443", _wizard_transport: "TCP", _wizard_security: "TLS", settings: { users: [], paddingScheme: ["stop=8", "0=30-30", "1=100-400", "2=400-500,c,500-1000,c,500-1000,c,500-1000,c,500-1000", "3=9-9,500-1000", "4=500-1000", "5=500-1000", "6=500-1000", "7=500-1000"] }, streamSettings: { network: "tcp", security: "tls", tlsSettings: { minVersion: "1.2" } } };
  if (protocol === "http") return { ...base, tag: "http-443", _wizard_transport: "None", _wizard_security: "None", settings: { auth: "noauth", udp: true, allowTransparent: false, accounts: [] } };
  if (protocol === "tunnel") return { ...base, tag: "tunnel-443", _wizard_transport: "None", _wizard_security: "None", settings: { address: "127.0.0.1", port: 443, network: "tcp", followRedirect: false, userLevel: 0 } };
  if (protocol === "snell") return { ...base, tag: "snell-443", _wizard_transport: "None", _wizard_security: "None", _wizard_snell_version: 4, _wizard_snell_obfs_mode: "none", settings: { users: [] } };
  if (protocol === "mieru") return { ...base, tag: "mieru-443", _wizard_transport: "None", _wizard_security: "None", settings: { transport: "tcp", users: [] } };
  return { ...base, tag: "vless-tcp-xtls-vision-reality-443", _wizard_transport: "TCP", _wizard_security: "XTLS-Vision-REALITY", settings: { decryption: "none", clients: [] }, streamSettings: { network: "tcp", security: "reality", realitySettings: { dest: "www.lovelive-anime.jp:443", serverNames: ["www.lovelive-anime.jp"], shortIds: [""] } } };
}

function defaultInboundForUser(protocol: string, username: string) {
  const inbound = defaultInbound(protocol);
  if (!username || protocol === "tunnel") return inbound;
  const settings = { ...asObject(inbound.settings) };
  const email = username;
  if (protocol === "socks" || protocol === "http") return { ...inbound, settings: { ...settings, auth: "password", accounts: [{ user: username, pass: randomPassword(), email, level: 0 }] } };
  if (protocol === "shadowsocks") return { ...inbound, settings: { ...settings, clients: [{ method: asString(settings.method) || "aes-256-gcm", password: randomPassword(), email, level: 0 }] } };
  if (protocol === "shadowsocks2022") return { ...inbound, settings: { ...settings, clients: [{ password: randomBase64(asString(settings.method).includes("128") ? 16 : 32), email, level: 0 }] } };
  if (protocol === "trojan") return { ...inbound, settings: { ...settings, clients: [{ password: randomPassword(), email, level: 0 }] } };
  if (protocol === "hysteria") return { ...inbound, settings: { ...settings, clients: [{ auth: randomPassword(), email, level: 0 }] } };
  if (protocol === "anytls") return { ...inbound, settings: { ...settings, users: [{ password: randomPassword(), email, level: 0 }] } };
  if (protocol === "snell") return { ...inbound, settings: { ...settings, users: [{ psk: randomPassword(), email, level: 0 }] } };
  if (protocol === "mieru") return { ...inbound, settings: { ...settings, users: [{ username, password: randomPassword(), email, level: 0 }] } };
  return { ...inbound, settings: { ...settings, clients: [{ id: crypto.randomUUID(), email, level: 0, ...(protocol === "vless" ? { flow: "xtls-rprx-vision" } : {}) }] } };
}

function sanitizeInbound(item: XrayObject) {
  const inbound = { ...item };
  delete inbound._runtime_status;
  delete inbound._source;
  delete inbound._wizard_security;
  delete inbound._wizard_transport;
  delete inbound._wizard_protocol;
  delete inbound._wizard_node_name;
  delete inbound._wizard_mode;
  if (["socks", "http"].includes(asString(inbound.protocol))) {
    const settings = { ...asObject(inbound.settings) };
    if (Array.isArray(settings.accounts)) settings.accounts = (settings.accounts as XrayObject[]).map((account) => ({ user: account.user, pass: account.pass }));
    inbound.settings = settings;
  }
  if (asString(inbound.protocol) === "snell") {
    const settings = { ...asObject(inbound.settings) };
    const version = asNumber(inbound._wizard_snell_version) || 4;
    const users = Array.isArray(settings.users) ? settings.users as XrayObject[] : [];
    settings.users = users.map((user) => {
      const next: XrayObject = { ...user, version };
      if (version === 6) {
        next.v6Mode = asString(inbound._wizard_snell_mode) || "default";
        delete next.obfsMode;
        delete next.obfsHost;
      } else {
        const mode = asString(inbound._wizard_snell_obfs_mode) || "none";
        delete next.clientId;
        if (mode !== "none") next.obfsMode = mode; else delete next.obfsMode;
        if (mode !== "none" && asString(inbound._wizard_snell_obfs_host)) next.obfsHost = asString(inbound._wizard_snell_obfs_host); else delete next.obfsHost;
        delete next.v6Mode;
      }
      return next;
    });
    inbound.settings = settings;
  }
  if (asString(inbound.protocol) === "shadowsocks" && !asString(asObject(inbound.settings).method).startsWith("2022-")) {
    const settings = { ...asObject(inbound.settings) };
    if (Array.isArray(settings.clients)) settings.clients = (settings.clients as XrayObject[]).map((client) => ({ ...client, method: asString(client.method) || asString(settings.method) || "aes-256-gcm" }));
    delete settings.password;
    inbound.settings = settings;
  }
  delete inbound._wizard_snell_version;
  delete inbound._wizard_snell_obfs_mode;
  delete inbound._wizard_snell_obfs_host;
  delete inbound._wizard_snell_mode;
  return inbound;
}

function validateInbound(item: XrayObject) {
  const protocol = asString(item.protocol);
  const settings = asObject(item.settings);
  const stream = asObject(item.streamSettings);
  const security = inboundSecurityMode(item);
  if (!asString(item.tag).trim()) throw new Error("入站标识不能为空");
  if (!protocol) throw new Error("请选择入站协议");
  if (asNumber(item.port) < 1 || asNumber(item.port) > 65535) throw new Error("端口必须在 1-65535 之间");
  const key = inboundCredentialKey(protocol);
  const noAuth = (protocol === "socks" || protocol === "http") && asString(settings.auth) === "noauth";
  if (protocol !== "tunnel" && !noAuth && (!Array.isArray(settings[key]) || (settings[key] as unknown[]).length === 0)) throw new Error("请至少添加一个客户端或账户");
  if (protocol === "tunnel" && (!asString(settings.address) || asNumber(settings.port) < 1)) throw new Error("隧道必须填写转发地址和端口");
  if (protocol === "mieru" && Array.isArray(settings.users) && (settings.users as XrayObject[]).some((user) => !asString(user.username) || !asString(user.password))) throw new Error("Mieru 用户必须填写用户名和密码");
  if (inboundTransportMode(item) === "GRPC" && !asString(asObject(stream.grpcSettings).serviceName)) throw new Error("gRPC 服务名称不能为空");
  if (inboundTransportMode(item) === "WSS" && (!item.cert_id || !asString(asObject(stream.tlsSettings).serverName))) throw new Error("WSS 必须选择托管证书并设置对外域名");
  if (security.includes("REALITY")) {
    const reality = asObject(stream.realitySettings);
    if (!asString(reality.dest) || !asString(reality.privateKey) || !(Array.isArray(reality.serverNames) && reality.serverNames.length)) throw new Error("Reality 必须填写目标地址、服务器名称并生成密钥");
  }
  if (asString(stream.security) === "tls" && !item.cert_id) {
    const certificates = asObject(stream.tlsSettings).certificates;
    const certificate = Array.isArray(certificates) ? asObject(certificates[0]) : {};
    if (!asString(certificate.certificateFile) || !asString(certificate.keyFile)) throw new Error("TLS 必须选择托管证书或填写证书和私钥路径");
  }
}

function defaultOutbound(protocol: string, outbounds: XrayObject[]): XrayObject {
  const base = ({ freedom: "direct", blackhole: "block", dns: "dns-out", http: "http-out", loopback: "loopback-out" } as Record<string, string>)[protocol] || protocol;
  const used = new Set(outbounds.map((value) => asString(value.tag)));
  let tag = base;
  let suffix = 2;
  while (used.has(tag)) tag = `${base}-${suffix++}`;
  return { tag, protocol, settings: protocol === "http" ? { servers: [{ address: "", port: 0 }] } : {} };
}

function validateSendThrough(value: string) {
  const normalized = value.trim();
  if (!normalized || normalized === "origin" || normalized === "srcip") return "";
  const parts = normalized.split("/");
  if (parts.length > 2) return "出站源 IP 格式无效";
  if (parts.length === 2) {
    const prefix = Number(parts[1]);
    if (!Number.isInteger(prefix) || prefix < 0 || prefix > 128) return "出站源 IP 的前缀长度无效";
  }
  const address = parts[0];
  const ipv4 = /^(\d{1,3}\.){3}\d{1,3}$/.test(address) && address.split(".").every((part) => Number(part) <= 255);
  const ipv6 = /^[0-9a-fA-F:]+$/.test(address) && address.includes(":");
  return ipv4 || ipv6 ? "" : "出站源地址必须是 IP、IP/CIDR、origin 或 srcip";
}

function validateOutbound(item: XrayObject) {
  const tag = asString(item.tag).trim();
  const protocol = asString(item.protocol).toLowerCase();
  const settings = asObject(item.settings);
  if (!tag) throw new Error("出站标识不能为空");
  if (!protocol) throw new Error("请选择出站协议");
  const sourceError = validateSendThrough(asString(item.sendThrough));
  if (sourceError) throw new Error(sourceError);
  if (protocol === "dns") {
    const port = asNumber(settings.port);
    const level = asNumber(settings.userLevel);
    if (settings.port !== undefined && (port < 1 || port > 65535)) throw new Error("DNS 端口必须在 1-65535 之间");
    if (settings.userLevel !== undefined && (level < 0 || level > 255)) throw new Error("用户等级必须在 0-255 之间");
    if (Array.isArray(settings.blockTypes) && (settings.blockTypes as unknown[]).some((value) => !Number.isFinite(Number(value)))) throw new Error("阻断查询类型必须是逗号分隔的数字");
  }
  if (protocol === "http") {
    const server = Array.isArray(settings.servers) ? asObject(settings.servers[0]) : {};
    if (!asString(server.address).trim()) throw new Error("HTTP 代理服务器地址不能为空");
    if (asNumber(server.port) < 1 || asNumber(server.port) > 65535) throw new Error("HTTP 代理服务器端口必须在 1-65535 之间");
  }
  if (protocol === "loopback" && !asString(settings.inboundTag).trim()) throw new Error("回环入站标识不能为空");
}

function nodeOutboundStream(clash: XrayObject) {
  const rawNetwork = asString(clash.network).toLowerCase() || "tcp";
  const network = ({ h2: "http", splithttp: "xhttp" } as Record<string, string>)[rawNetwork] || rawNetwork;
  const realityOptions = asObject(clash["reality-opts"]);
  const reality = clash.reality === true || Object.keys(realityOptions).length > 0;
  const tls = reality || clash.tls === true || clash.tls === "true";
  const stream: XrayObject = { network, security: reality ? "reality" : tls ? "tls" : "none" };
  if (rawNetwork === "ws") {
    const options = asObject(clash["ws-opts"]);
    stream.wsSettings = { path: asString(options.path) || "/", headers: asObject(options.headers) };
  } else if (rawNetwork === "grpc") {
    const options = asObject(clash["grpc-opts"]);
    stream.grpcSettings = { serviceName: asString(options["grpc-service-name"]) };
  } else if (rawNetwork === "h2") {
    const options = asObject(clash["h2-opts"]);
    stream.httpSettings = { path: asString(options.path) || "/", host: Array.isArray(options.host) ? options.host : [] };
  } else if (rawNetwork === "httpupgrade") {
    stream.httpupgradeSettings = { path: asString(clash.path) || "/", host: asString(clash.host) };
  } else if (rawNetwork === "splithttp") {
    stream.xhttpSettings = { path: asString(clash.path) || "/xhttp", host: asString(clash.host) };
  }
  const serverName = asString(realityOptions["server-name"] || clash.sni || clash.servername || clash.server);
  const fingerprint = asString(clash["client-fingerprint"] || clash.fingerprint) || (reality ? "chrome" : "");
  if (reality) stream.realitySettings = { serverName, publicKey: asString(realityOptions["public-key"]), shortId: asString(realityOptions["short-id"]), fingerprint: fingerprint === "randomized" ? "chrome" : fingerprint };
  if (!reality && tls) stream.tlsSettings = { serverName, allowInsecure: Boolean(clash["skip-cert-verify"]), ...(clash.alpn ? { alpn: Array.isArray(clash.alpn) ? clash.alpn : [asString(clash.alpn)] } : {}), ...(fingerprint ? { fingerprint } : {}) };
  return stream;
}

function nodeToOutbound(node: XrayNode): XrayObject {
  const clash = parseObject(node.clash_config || "{}", "节点配置");
  const rawProtocol = asString(clash.type || node.protocol).toLowerCase();
  const protocol = ({ ss: "shadowsocks", socks5: "socks", hy2: "hysteria", hysteria2: "hysteria" } as Record<string, string>)[rawProtocol] || rawProtocol;
  const address = asString(clash.server);
  const port = asNumber(clash.port);
  const tag = asString(clash.name || node.node_name).trim();
  if (!rawProtocol || !address || !port || !tag) throw new Error("节点缺少 protocol、server、port 或 name");
  if (!["vless", "vmess", "trojan", "shadowsocks", "socks", "http", "snell", "hysteria", "anytls"].includes(protocol)) throw new Error(`正式版暂不支持从 ${rawProtocol} 节点生成出站`);
  if (protocol === "hysteria") {
    const serverName = asString(clash.sni || clash.servername || clash.server);
    return { tag, protocol: "hysteria", settings: { version: 2, address, port }, streamSettings: { network: "hysteria", security: "tls", tlsSettings: { serverName, ...(clash.alpn ? { alpn: Array.isArray(clash.alpn) ? clash.alpn : [asString(clash.alpn)] } : {}) }, hysteriaSettings: { version: 2, auth: asString(clash.password || clash.auth) } } };
  }
  if (protocol === "snell") {
    const obfs = asObject(clash["obfs-opts"]);
    return { tag, protocol, settings: { address, port, psk: asString(clash.psk), ...(clash.version ? { version: asNumber(clash.version) } : {}), ...(obfs.mode ? { obfsMode: obfs.mode, ...(obfs.host ? { obfsHost: obfs.host } : {}) } : {}), ...(clash.mode ? { v6Mode: clash.mode } : {}) } };
  }
  const streamSettings = nodeOutboundStream(clash);
  if (protocol === "vless" || protocol === "vmess") {
    const user: XrayObject = { id: asString(clash.uuid) };
    if (protocol === "vless") { user.encryption = asString(clash.encryption) || "none"; if (clash.flow) user.flow = clash.flow; }
    if (protocol === "vmess") { user.alterId = asNumber(clash.alterId); user.security = asString(clash.cipher) || "auto"; }
    return { tag, protocol, settings: { vnext: [{ address, port, users: [user] }] }, streamSettings };
  }
  if (protocol === "trojan") return { tag, protocol, settings: { servers: [{ address, port, password: asString(clash.password) }] }, streamSettings };
  if (protocol === "shadowsocks") return { tag, protocol, settings: { servers: [{ address, port, password: asString(clash.password), method: asString(clash.cipher || clash.method) || "aes-256-gcm" }] }, streamSettings };
  if (protocol === "anytls") return { tag, protocol, settings: { servers: [{ address, port, password: asString(clash.password) }] }, streamSettings };
  const users = clash.username || clash.password ? [{ user: asString(clash.username), pass: asString(clash.password), level: 0 }] : [];
  return { tag, protocol, settings: { servers: [{ address, port, ...(users.length ? { users } : {}) }] }, streamSettings };
}

export function XrayManager({ server, token, username }: { server: RemoteServer; token: string; username: string }) {
  const [tab, setTab] = useState<Tab>("config");
  const [notice, setNotice] = useState<Notice>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [running, setRunning] = useState(Boolean(server.xray_running));
  const [version, setVersion] = useState(server.xray_version || "");
  const [configPath, setConfigPath] = useState("");
  const [configText, setConfigText] = useState("");
  const [savedConfigText, setSavedConfigText] = useState("");
  const [systemConfig, setSystemConfig] = useState<XraySystemConfig>(defaultSystemConfig);
  const [inbounds, setInbounds] = useState<XrayObject[]>([]);
  const [outbounds, setOutbounds] = useState<XrayObject[]>([]);
  const [routing, setRouting] = useState<XrayObject>({ domainStrategy: "AsIs", rules: [], balancers: [] });
  const [nodes, setNodes] = useState<XrayNode[]>([]);
  const [selectedNodeId, setSelectedNodeId] = useState("");
  const [editor, setEditor] = useState<{ kind: "inbound" | "outbound" | "rule" | "balancer" | "view"; item: XrayObject; index?: number; originalTag?: string } | null>(null);
  const [hideDefaults, setHideDefaults] = useState(false);
  const [warpOpen, setWarpOpen] = useState(false);

  const configError = useMemo(() => {
    try {
      parseObject(configText, "Xray 配置");
      return "";
    } catch (error) {
      return getError(error, "JSON 格式错误");
    }
  }, [configText]);
  const dirty = configText !== savedConfigText;

  const refreshConfig = useCallback(async () => {
    const [status, config, system] = await Promise.all([
      fetchXrayServiceStatus(token, server.id),
      fetchXrayConfig(token, server.id),
      fetchXraySystemConfig(token, server.id),
    ]);
    setRunning(Boolean(status.xray?.running));
    setVersion(status.xray?.version || "");
    setConfigPath(config.path || "");
    setConfigText(config.config || "{}");
    setSavedConfigText(config.config || "{}");
    const source = system.config && typeof system.config === "object" ? system.config : system;
    setSystemConfig({
      metrics_enabled: Boolean(source.metrics_enabled),
      metrics_listen: asString(source.metrics_listen) || defaultSystemConfig.metrics_listen,
      stats_enabled: Boolean(source.stats_enabled),
      grpc_enabled: Boolean(source.grpc_enabled),
      grpc_port: asNumber(source.grpc_port) || defaultSystemConfig.grpc_port,
    });
  }, [server.id, token]);

  const refreshInbounds = useCallback(async () => {
    const response = await fetchXrayInbounds(token, server.id);
    setInbounds((response.inbounds || []).filter((item) => asString(item.tag) !== "api"));
  }, [server.id, token]);

  const refreshOutbounds = useCallback(async () => {
    const response = await fetchXrayOutbounds(token, server.id);
    setOutbounds(response.outbounds || []);
  }, [server.id, token]);

  const refreshRouting = useCallback(async () => {
    const response = await fetchXrayRouting(token, server.id);
    setRouting(response.routing || { domainStrategy: "AsIs", rules: [], balancers: [] });
  }, [server.id, token]);

  const refreshTab = useCallback(async (target = tab) => {
    setLoading(true);
    setNotice(null);
    try {
      if (target === "config") await refreshConfig();
      if (target === "inbounds") {
        const [, nodeResponse] = await Promise.all([refreshInbounds(), fetchXrayNodes(token)]);
        setNodes(nodeResponse.nodes || []);
      }
      if (target === "outbounds") {
        const [, nodeResponse] = await Promise.all([refreshOutbounds(), fetchXrayNodes(token)]);
        setNodes(nodeResponse.nodes || []);
      }
      if (target === "routing") {
        const [, , , nodeResponse] = await Promise.all([refreshRouting(), refreshOutbounds(), refreshInbounds(), fetchXrayNodes(token)]);
        setNodes(nodeResponse.nodes || []);
      }
    } catch (error) {
      setNotice({ kind: "error", text: getError(error, "读取 Xray 数据失败") });
    } finally {
      setLoading(false);
    }
  }, [refreshConfig, refreshInbounds, refreshOutbounds, refreshRouting, server.id, tab, token]);

  useEffect(() => {
    void refreshTab(tab);
  }, [tab, refreshTab]);

  async function runService(action: "start" | "stop" | "restart") {
    const label = action === "start" ? "启动" : action === "stop" ? "停止" : "重启";
    if (!window.confirm(`确认${label} ${server.name} 的 Xray？`)) return;
    setBusy(`service-${action}`);
    setNotice(null);
    try {
      await controlRemoteService(token, server.id, "xray", action);
      const expected = action !== "stop";
      let finalStatus = await fetchXrayServiceStatus(token, server.id);
      for (let attempt = 0; attempt < 12 && Boolean(finalStatus.xray?.running) !== expected; attempt += 1) {
        await new Promise((resolve) => window.setTimeout(resolve, 1000));
        finalStatus = await fetchXrayServiceStatus(token, server.id);
      }
      setRunning(Boolean(finalStatus.xray?.running));
      setVersion(finalStatus.xray?.version || "");
      if (Boolean(finalStatus.xray?.running) !== expected) throw new Error(`${label}请求已返回，但服务状态尚未达到预期`);
      setNotice({ kind: "success", text: `${label}成功，远端状态已确认` });
    } catch (error) {
      setNotice({ kind: "error", text: getError(error, `${label} Xray 失败`) });
      await refreshConfig().catch(() => undefined);
    } finally {
      setBusy("");
    }
  }

  async function saveConfig() {
    if (configError) {
      setNotice({ kind: "error", text: configError });
      return;
    }
    if (!window.confirm(`确认覆盖 ${server.name} 当前完整 Xray 配置？保存系统开关时 Agent 会重启 Xray。`)) return;
    setBusy("save-config");
    setNotice(null);
    try {
      const normalized = JSON.stringify(parseObject(configText, "Xray 配置"), null, 2);
      const test = await testXrayConfig(token, server.id, normalized);
      if (!test.ok) throw new Error([test.error || test.message || "Xray 配置校验失败", test.method, test.output].filter(Boolean).join(" · "));
      await saveXrayConfig(token, server.id, normalized);
      await saveXraySystemConfig(token, server.id, systemConfig);
      await refreshConfig();
      setNotice({ kind: "success", text: "配置已保存，并已从服务器重新读取确认" });
    } catch (error) {
      setNotice({ kind: "error", text: getError(error, "保存配置失败") });
      await refreshConfig().catch(() => undefined);
    } finally {
      setBusy("");
    }
  }

  async function restartAfterRouting() {
    await controlRemoteService(token, server.id, "xray", "restart");
    const status = await fetchXrayServiceStatus(token, server.id);
    setRunning(Boolean(status.xray?.running));
    setVersion(status.xray?.version || "");
  }

  async function saveEditor(item: XrayObject) {
    if (!editor) return;
    setBusy(`save-${editor.kind}`);
    setNotice(null);
    try {
      if (editor.kind === "inbound") {
        const inbound = sanitizeInbound(item);
        const tag = asString(item.tag).trim();
        const nodeName = asString(item._wizard_node_name).trim();
        if (!tag || !asString(item.protocol)) throw new Error("入站标识和协议不能为空");
        if (editor.originalTag) {
          await mutateXrayInbound(token, server.id, { action: "update", tag: editor.originalTag, inbound, ...(nodeName ? { node_name: nodeName } : {}) });
        } else {
          await mutateXrayInbound(token, server.id, { action: "add", inbound, ...(nodeName ? { node_name: nodeName } : {}) });
        }
        await refreshInbounds();
      } else if (editor.kind === "outbound") {
        const tag = asString(item.tag).trim();
        if (!tag || !asString(item.protocol)) throw new Error("出站标识和协议不能为空");
        if (editor.originalTag) {
          try {
            await mutateXrayOutbound(token, server.id, { action: "update", tag: editor.originalTag, outbound: item });
          } catch {
            await mutateXrayOutbound(token, server.id, { action: "remove", tag: editor.originalTag });
            try {
              await mutateXrayOutbound(token, server.id, { action: "add", outbound: item });
            } catch (error) {
              await mutateXrayOutbound(token, server.id, { action: "add", outbound: editor.item }).catch(() => undefined);
              throw error;
            }
          }
        } else {
          await mutateXrayOutbound(token, server.id, { action: "add", outbound: item });
        }
        await refreshOutbounds();
      } else if (editor.kind === "rule") {
        if (!item.outboundTag && !item.balancerTag) throw new Error("路由规则必须选择出站或负载均衡");
        if (editor.index == null) {
          await mutateXrayRouting(token, server.id, { action: "add_rule", rule: item });
        } else {
          const rules = [...(Array.isArray(routing.rules) ? routing.rules as XrayObject[] : [])];
          rules[editor.index] = item;
          await mutateXrayRouting(token, server.id, { action: "set", routing: { ...routing, rules } });
        }
        await restartAfterRouting();
        await refreshRouting();
      } else if (editor.kind === "balancer") {
        const balancers = [...(Array.isArray(routing.balancers) ? routing.balancers as XrayObject[] : [])];
        if (editor.index == null) balancers.push(item); else balancers[editor.index] = item;
        await mutateXrayRouting(token, server.id, { action: "set", routing: { ...routing, balancers } });
        await restartAfterRouting();
        await refreshRouting();
      }
      setEditor(null);
      setNotice({ kind: "success", text: "保存成功，远端数据已重新读取" });
    } catch (error) {
      setNotice({ kind: "error", text: getError(error, "保存失败") });
    } finally {
      setBusy("");
    }
  }

  async function removeItem(kind: "inbound" | "outbound" | "rule" | "balancer", index: number, item: XrayObject) {
    const name = asString(item.tag || item.marktag) || `#${index + 1}`;
    if (!window.confirm(`确认删除 ${name}？该操作会修改 ${server.name} 的 Xray 配置。`)) return;
    setBusy(`remove-${kind}-${index}`);
    setNotice(null);
    try {
      if (kind === "inbound") await mutateXrayInbound(token, server.id, { action: "remove", tag: asString(item.tag) });
      if (kind === "outbound") await mutateXrayOutbound(token, server.id, { action: "remove", tag: asString(item.tag) });
      if (kind === "rule") {
        await mutateXrayRouting(token, server.id, { action: "remove_rule", index });
        await restartAfterRouting();
      }
      if (kind === "balancer") {
        const balancers = (Array.isArray(routing.balancers) ? routing.balancers as XrayObject[] : []).filter((_, position) => position !== index);
        await mutateXrayRouting(token, server.id, { action: "set", routing: { ...routing, balancers } });
        await restartAfterRouting();
      }
      await refreshTab(kind === "inbound" ? "inbounds" : kind === "outbound" ? "outbounds" : "routing");
      setNotice({ kind: "success", text: "删除成功，远端数据已重新读取" });
    } catch (error) {
      setNotice({ kind: "error", text: getError(error, "删除失败") });
    } finally {
      setBusy("");
    }
  }

  async function moveItem(kind: "outbound" | "rule", index: number, delta: -1 | 1) {
    setBusy(`move-${kind}-${index}`);
    setNotice(null);
    try {
      if (kind === "outbound") {
        const reordered = [...outbounds];
        [reordered[index], reordered[index + delta]] = [reordered[index + delta], reordered[index]];
        await mutateXrayOutbound(token, server.id, { action: "reorder", tags: reordered.map((item) => asString(item.tag)) });
        await refreshOutbounds();
      } else {
        const visibleRules = [...rules];
        [visibleRules[index], visibleRules[index + delta]] = [visibleRules[index + delta], visibleRules[index]];
        await mutateXrayRouting(token, server.id, { action: "set", routing: { ...routing, rules: [...protectedRules, ...visibleRules] } });
        await restartAfterRouting();
        await refreshRouting();
      }
      setNotice({ kind: "success", text: "顺序已保存" });
    } catch (error) {
      setNotice({ kind: "error", text: getError(error, kind === "outbound" ? "当前 Agent 不支持出站排序" : "排序失败") });
      await refreshTab(kind === "outbound" ? "outbounds" : "routing");
    } finally {
      setBusy("");
    }
  }

  const allRules = Array.isArray(routing.rules) ? routing.rules as XrayObject[] : [];
  const isProtectedRule = (rule: XrayObject) => asString(rule.outboundTag) === "api" || (Array.isArray(rule.inboundTag) && (rule.inboundTag as unknown[]).some((value) => asString(value) === "api" || asString(value) === "tunnel-in"));
  const protectedRules = allRules.filter(isProtectedRule);
  const rules = allRules.filter((rule) => !isProtectedRule(rule));
  const balancers = Array.isArray(routing.balancers) ? routing.balancers as XrayObject[] : [];
  const visibleOutbounds = hideDefaults ? outbounds.filter((item) => !["direct", "block"].includes(asString(item.tag).toLowerCase())) : outbounds;
  const managedTags = new Set(nodes.filter((node) => node.node_type === "routed" && node.routed_outbound_tag).map((node) => asString(node.routed_outbound_tag)));

  return (
    <div className="xray-manager">
      <div className="xray-server-line"><span>{server.name}</span><button type="button" onClick={() => void refreshTab()} aria-label="刷新当前页面" title="刷新"><RefreshCw /></button></div>
      <div className="service-tabs xray-tabs" role="tablist">
        {(["config", "inbounds", "outbounds", "routing"] as Tab[]).map((value) => <button key={value} type="button" className={tab === value ? "active" : ""} onClick={() => setTab(value)}>{({ config: "配置", inbounds: "入站", outbounds: "出站", routing: "路由" })[value]}</button>)}
      </div>
      {notice && <div className={`xray-notice ${notice.kind}`} role="status">{notice.kind === "success" ? <Check /> : <X />}<span>{notice.text}</span></div>}
      {loading ? <div className="xray-loading"><LoaderCircle /> 正在读取服务器数据...</div> : null}

      {!loading && tab === "config" && <>
        <section className="xray-panel xray-status-panel">
          <div><span className={`xray-status-dot ${running ? "running" : ""}`} /><div><strong>{running ? "运行中" : "已停止"}</strong><p>{version || "未返回版本信息"}</p></div></div>
          <div className="xray-control-row">
            <button type="button" disabled={Boolean(busy)} onClick={() => void runService("start")}><Play />启动</button>
            <button type="button" disabled={Boolean(busy)} onClick={() => void runService("stop")} className="danger"><Square />停止</button>
            <button type="button" disabled={Boolean(busy)} onClick={() => void runService("restart")}><RotateCw />重启</button>
          </div>
        </section>
        <section className="xray-panel">
          <div className="xray-panel-title"><div><h4>运行能力</h4><p>开关会随保存配置一起下发，成功后由服务器重新读取。</p></div></div>
          <div className="xray-switch-list">
            <Switch label="指标统计" checked={systemConfig.metrics_enabled} onChange={(checked) => setSystemConfig((value) => ({ ...value, metrics_enabled: checked }))} />
            <Switch label="流量统计" checked={systemConfig.stats_enabled} onChange={(checked) => setSystemConfig((value) => ({ ...value, stats_enabled: checked }))} />
            <Switch label="gRPC" checked={systemConfig.grpc_enabled} onChange={(checked) => setSystemConfig((value) => ({ ...value, grpc_enabled: checked }))} />
          </div>
        </section>
        <section className="xray-panel">
          <div className="xray-panel-title"><div><h4>完整配置</h4><p className="xray-break">{configPath || "Xray config"}{dirty ? " · 有未保存修改" : ""}</p></div><span className={configError ? "invalid" : "valid"}>{configError ? "JSON 错误" : "JSON 正确"}</span></div>
          <textarea className="xray-json-editor" value={configText} spellCheck={false} onChange={(event) => setConfigText(event.target.value)} aria-label="完整 Xray JSON" />
          {configError && <p className="xray-field-error">{configError}</p>}
          <div className="xray-editor-actions">
            <button type="button" disabled={Boolean(configError) || Boolean(busy)} onClick={() => setConfigText(JSON.stringify(parseObject(configText), null, 2))}><Braces />格式化</button>
            <button type="button" disabled={Boolean(busy)} onClick={() => void refreshConfig()}><RefreshCw />重新读取</button>
            <button type="button" className="primary" disabled={Boolean(configError) || Boolean(busy)} onClick={() => void saveConfig()}><Save />{busy === "save-config" ? "保存中..." : "保存配置"}</button>
          </div>
        </section>
      </>}

      {!loading && tab === "inbounds" && <section className="xray-list-section">
        <ListHeader title={`入站 (${inbounds.length})`} action="添加入站" onAdd={() => setEditor({ kind: "inbound", item: defaultInboundForUser("vless", username) })} />
        {inbounds.length === 0 ? <Empty text="当前服务器没有可管理的入站" /> : inbounds.map((item, index) => <article className="xray-item" key={`${asString(item.tag)}-${index}`}>
          <div className="xray-item-head"><div><strong>{asString(item.tag) || `入站 ${index + 1}`}</strong><p>{asString(item.protocol)} · {asString(item.listen) || "0.0.0.0"}:{asString(item.port) || "--"}</p></div><span>{inboundUsers(item)} 用户</span></div>
          <div className="xray-chip-row"><span>{transportName(item)}</span><span>{securityName(item)}</span>{item._runtime_status != null && <span>{asString(item._runtime_status)}</span>}</div>
          <ItemActions onView={() => setEditor({ kind: "view", item })} onEdit={() => setEditor({ kind: "inbound", item, originalTag: asString(item.tag) })} onDelete={() => void removeItem("inbound", index, item)} busy={Boolean(busy)} />
        </article>)}
      </section>}

      {!loading && tab === "outbounds" && <section className="xray-list-section">
        <ListHeader title={`出站 (${visibleOutbounds.length})`} action="添加出站" onAdd={() => setEditor({ kind: "outbound", item: defaultOutbound("freedom", outbounds) })} extra={<label className="xray-check"><input type="checkbox" checked={hideDefaults} onChange={(event) => setHideDefaults(event.target.checked)} />隐藏默认</label>} />
        <div className="xray-node-import"><select value={selectedNodeId} onChange={(event) => setSelectedNodeId(event.target.value)}><option value="">从节点创建出站...</option>{nodes.map((node) => <option key={node.id} value={node.id}>{node.node_name} ({node.protocol})</option>)}</select><button type="button" disabled={!selectedNodeId} onClick={() => { try { const node = nodes.find((value) => String(value.id) === selectedNodeId); if (!node) return; setEditor({ kind: "outbound", item: nodeToOutbound(node) }); setNotice(null); } catch (error) { setNotice({ kind: "error", text: getError(error, "节点转换失败") }); } }}><Plus />导入</button></div>
        <div className="xray-quick-row">{["freedom", "blackhole", "dns", "http", "loopback"].map((protocol) => <button type="button" key={protocol} onClick={() => setEditor({ kind: "outbound", item: defaultOutbound(protocol, outbounds) })}>{outboundProtocolLabels[protocol]}</button>)}{server.xray_mode !== "external" && <button type="button" onClick={() => setWarpOpen(true)}><Cloud />Cloudflare WARP</button>}</div>
        {visibleOutbounds.length === 0 ? <Empty text="当前服务器没有可显示的出站" /> : visibleOutbounds.map((item) => {
          const index = outbounds.indexOf(item); const detail = describeOutbound(item); const managed = managedTags.has(asString(item.tag)); const portForward = asString(item.tag).startsWith("tunnel-"); const protectedDelete = ["freedom", "blackhole"].includes(asString(item.protocol)) || managed || portForward;
          return <article className="xray-item" key={`${asString(item.tag)}-${index}`}>
            <div className="xray-item-head"><div><strong>{asString(item.tag) || `出站 ${index + 1}`}</strong><p>{outboundProtocolLabels[asString(item.protocol)] || asString(item.protocol)} · {detail.address}:{detail.port}</p></div>{index === 0 && <span>默认</span>}</div>
            <div className="xray-chip-row"><span>{detail.users} 用户</span>{managed && <span>路由出站</span>}{portForward && <span>端口转发</span>}</div>
            <ItemActions onView={() => setEditor({ kind: "view", item })} onEdit={portForward ? undefined : () => setEditor({ kind: "outbound", item, originalTag: asString(item.tag) })} onDelete={protectedDelete ? undefined : () => void removeItem("outbound", index, item)} busy={Boolean(busy)} moveUp={index > 0 ? () => void moveItem("outbound", index, -1) : undefined} moveDown={index < outbounds.length - 1 ? () => void moveItem("outbound", index, 1) : undefined} />
          </article>;
        })}
      </section>}

      {!loading && tab === "routing" && <section className="xray-list-section">
        <div className="xray-routing-base"><label><span>域名策略</span><select value={asString(routing.domainStrategy) || "AsIs"} onChange={(event) => setRouting((value) => ({ ...value, domainStrategy: event.target.value }))}>{["AsIs", "IPIfNonMatch", "IPOnDemand"].map((value) => <option key={value} value={value}>{domainStrategyLabels[value]}</option>)}</select></label><button type="button" disabled={Boolean(busy)} onClick={() => void (async () => { if (!window.confirm("确认保存路由基础配置并重启 Xray？")) return; setBusy("routing-base"); try { await mutateXrayRouting(token, server.id, { action: "set", routing }); await restartAfterRouting(); await refreshRouting(); setNotice({ kind: "success", text: "路由配置已保存并重启" }); } catch (error) { setNotice({ kind: "error", text: getError(error, "保存失败") }); } finally { setBusy(""); } })()}><Save />保存</button></div>
        <div className="xray-quick-row">
          <button type="button" onClick={() => setEditor({ kind: "rule", item: { type: "field", protocol: ["bittorrent"], outboundTag: "block", marktag: "ban-bt" } })}>禁止 BT</button>
          <button type="button" onClick={() => setEditor({ kind: "rule", item: { type: "field", ip: ["geoip:cn"], outboundTag: "block", marktag: "ban-cn-ip" } })}>禁止访问大陆 IP</button>
          <button type="button" onClick={() => setEditor({ kind: "rule", item: { type: "field", ip: ["geoip:private"], outboundTag: "block", marktag: "ban-private" } })}>禁止内网访问</button>
          <button type="button" onClick={() => setEditor({ kind: "rule", item: { type: "field", domain: ["geosite:openai"], outboundTag: "direct", marktag: "openai-direct" } })}>OpenAI 直连</button>
          <button type="button" onClick={() => setEditor({ kind: "rule", item: { type: "field", domain: ["geosite:cn"], outboundTag: "warp-v4", marktag: "anti-cn-warp" } })}>防止送中（走 WARP）</button>
          <button type="button" onClick={() => setEditor({ kind: "rule", item: { type: "field", domain: ["geosite:speedtest"], outboundTag: "warp-v4", marktag: "speedtest-warp" } })}>测速分流（走 WARP）</button>
          <button type="button" onClick={() => setEditor({ kind: "rule", item: { type: "field", domain: ["geosite:category-pt"], outboundTag: "warp-v4", marktag: "home-bypass-warp" } })}>家宽常用（走 WARP）</button>
          <button type="button" onClick={() => setEditor({ kind: "rule", item: { type: "field", domain: ["geosite:tiktok"], outboundTag: asString(outbounds[0]?.tag) || "direct", marktag: "tiktok" } })}>抖音解锁</button>
          <button type="button" onClick={() => setEditor({ kind: "rule", item: { type: "field", domain: ["domain:emby.media"], outboundTag: asString(outbounds[0]?.tag) || "direct", marktag: "emby" } })}>RFC EMBY</button>
        </div>
        <ListHeader title={`路由规则 (${rules.length})`} action="自定义规则" onAdd={() => setEditor({ kind: "rule", item: { type: "field", outboundTag: outbounds[0]?.tag || "direct" } })} />
        {rules.length === 0 ? <Empty text="当前没有路由规则" /> : rules.map((item, index) => <article className="xray-item" key={`rule-${index}`}>
          <div className="xray-item-head"><div><strong>{displayMarkTag(item.marktag, index)}</strong><p>{item.balancerTag ? `负载均衡：${asString(item.balancerTag)}` : `出站：${asString(item.outboundTag) || "--"}`}</p></div><span>#{index + 1}</span></div>
          <div className="xray-chip-row">{["domain", "ip", "port", "protocol", "network", "inboundTag", "user", "source"].filter((key) => item[key] != null).map((key) => <span key={key}>{routingFieldLabels[key]}</span>)}</div>
          <ItemActions onView={() => setEditor({ kind: "view", item })} onEdit={managedTags.has(asString(item.outboundTag)) || asString(item.outboundTag).startsWith("tunnel-") ? undefined : () => setEditor({ kind: "rule", item, index: allRules.indexOf(item) })} onDelete={managedTags.has(asString(item.outboundTag)) || asString(item.outboundTag).startsWith("tunnel-") ? undefined : () => void removeItem("rule", allRules.indexOf(item), item)} busy={Boolean(busy)} moveUp={index > 0 ? () => void moveItem("rule", index, -1) : undefined} moveDown={index < rules.length - 1 ? () => void moveItem("rule", index, 1) : undefined} />
        </article>)}
        <ListHeader title={`负载均衡 (${balancers.length})`} action="添加负载均衡" onAdd={() => setEditor({ kind: "balancer", item: { tag: "", selector: [], strategy: { type: "random" } } })} />
        {balancers.map((item, index) => <article className="xray-item" key={`balancer-${index}`}><div className="xray-item-head"><div><strong>{asString(item.tag) || `负载均衡 ${index + 1}`}</strong><p>{Array.isArray(item.selector) ? item.selector.join(", ") : "--"}</p></div><span>{({ random: "随机", roundRobin: "轮询", leastPing: "最低延迟", leastLoad: "最低负载" } as Record<string, string>)[asString(asObject(item.strategy).type)] || asString(asObject(item.strategy).type) || "随机"}</span></div><ItemActions onView={() => setEditor({ kind: "view", item })} onEdit={() => setEditor({ kind: "balancer", item, index })} onDelete={() => void removeItem("balancer", index, item)} busy={Boolean(busy)} /></article>)}
      </section>}

      {editor && <ObjectEditor editor={editor} server={server} token={token} username={username} nodes={nodes} outbounds={outbounds} balancers={balancers} usedPorts={inbounds.map((item) => asNumber(item.port)).filter(Boolean)} pending={busy.startsWith("save-")} onCancel={() => setEditor(null)} onSave={(item) => void saveEditor(item)} />}
      {warpOpen && <WarpManager server={server} token={token} onClose={() => setWarpOpen(false)} onChanged={() => void refreshOutbounds()} />}
    </div>
  );
}

function Switch({ label, checked, onChange }: { label: string; checked: boolean; onChange: (checked: boolean) => void }) {
  return <label className="xray-switch"><span>{label}</span><input type="checkbox" checked={checked} onChange={(event) => onChange(event.target.checked)} /><i /></label>;
}

function ListHeader({ title, action, onAdd, extra }: { title: string; action: string; onAdd: () => void; extra?: React.ReactNode }) {
  return <div className="xray-list-head"><div><h4>{title}</h4>{extra}</div><button type="button" onClick={onAdd}><Plus />{action}</button></div>;
}

function Empty({ text }: { text: string }) {
  return <div className="xray-empty">{text}</div>;
}

function ItemActions({ onView, onEdit, onDelete, moveUp, moveDown, busy }: { onView: () => void; onEdit?: () => void; onDelete?: () => void; moveUp?: () => void; moveDown?: () => void; busy: boolean }) {
  return <div className="xray-item-actions"><button type="button" disabled={busy} onClick={onView} title="查看 JSON" aria-label="查看 JSON"><Eye /><span>查看</span></button>{onEdit && <button type="button" disabled={busy} onClick={onEdit} title="编辑" aria-label="编辑"><Edit3 /><span>编辑</span></button>}{moveUp && <button type="button" disabled={busy} onClick={moveUp} title="上移" aria-label="上移"><ArrowUp /></button>}{moveDown && <button type="button" disabled={busy} onClick={moveDown} title="下移" aria-label="下移"><ArrowDown /></button>}{onDelete && <button type="button" disabled={busy} onClick={onDelete} className="danger" title="删除" aria-label="删除"><Trash2 /><span>删除</span></button>}</div>;
}

function ObjectEditor({ editor, server, token, username, nodes, outbounds, balancers, usedPorts, pending, onCancel, onSave }: { editor: { kind: "inbound" | "outbound" | "rule" | "balancer" | "view"; item: XrayObject; originalTag?: string }; server: RemoteServer; token: string; username: string; nodes: XrayNode[]; outbounds: XrayObject[]; balancers: XrayObject[]; usedPorts: number[]; pending: boolean; onCancel: () => void; onSave: (item: XrayObject) => void }) {
  const [item, setItem] = useState<XrayObject>(() => JSON.parse(JSON.stringify(editor.item)) as XrayObject);
  const [advanced, setAdvanced] = useState(false);
  const [json, setJson] = useState(() => JSON.stringify(editor.item, null, 2));
  const [error, setError] = useState("");
  const [preview, setPreview] = useState(false);
  const readOnly = editor.kind === "view";
  const set = (key: string, value: unknown) => setItem((current) => ({ ...current, [key]: value }));
  function submit() {
    try {
      const next = advanced ? parseObject(json) : item;
      if (editor.kind === "inbound") {
        validateInbound(next);
        if (asNumber(next.port) !== (editor.originalTag ? asNumber(editor.item.port) : 0) && usedPorts.includes(asNumber(next.port))) throw new Error(`端口 ${asNumber(next.port)} 已被其他入站占用`);
      }
      if (editor.kind === "outbound") validateOutbound(next);
      onSave(next);
    } catch (parseError) {
      setError(getError(parseError, "JSON 格式错误"));
    }
  }
  function toggleAdvanced() {
    if (advanced) {
      try { const parsed = parseObject(json); setItem(parsed); setAdvanced(false); setError(""); } catch (parseError) { setError(getError(parseError, "JSON 格式错误")); }
    } else { setJson(JSON.stringify(item, null, 2)); setAdvanced(true); setError(""); }
  }
  const stream = asObject(item.streamSettings);
  const settings = asObject(item.settings);
  const strategy = asObject(item.strategy);
  return <div className="xray-editor-layer" role="presentation" onClick={onCancel}><section className="xray-object-editor" role="dialog" aria-label="Xray 配置编辑" onClick={(event) => event.stopPropagation()}>
    <header><div><h4>{readOnly ? "查看 JSON" : editor.kind === "inbound" ? "入站配置" : editor.kind === "outbound" ? "出站配置" : editor.kind === "rule" ? "路由规则" : "负载均衡"}</h4><p>{readOnly ? "服务器返回的完整对象" : "结构化字段与高级 JSON 会保存为同一个对象"}</p></div><button type="button" onClick={onCancel} aria-label="关闭"><X /></button></header>
    <div className="xray-object-body">
      {readOnly || advanced ? <textarea className="xray-json-editor object" readOnly={readOnly} value={readOnly ? JSON.stringify(item, null, 2) : json} onChange={(event) => setJson(event.target.value)} spellCheck={false} /> : <div className="xray-fields">
        {(editor.kind === "inbound" || editor.kind === "outbound" || editor.kind === "balancer") && <label><span>标识（Tag）*</span><input value={asString(item.tag)} onChange={(event) => set("tag", event.target.value)} /></label>}
        {editor.kind === "inbound" && <InboundStructuredEditor server={server} token={token} username={username} nodes={nodes} item={item} onChange={setItem} onError={setError} usedPorts={usedPorts} originalPort={editor.originalTag ? asNumber(editor.item.port) : 0} />}
        {editor.kind === "outbound" && <>
          <label><span>协议 *</span><select value={asString(item.protocol)} disabled={Boolean(editor.originalTag) || !outboundProtocols.includes(asString(item.protocol))} onChange={(event) => { const protocol = event.target.value; const defaults = defaultOutbound(protocol, outbounds); setItem((current) => ({ ...current, tag: defaults.tag, protocol, settings: defaults.settings })); setError(""); }}>{!outboundProtocols.includes(asString(item.protocol)) && <option value={asString(item.protocol)}>{outboundProtocolLabels[asString(item.protocol)] || asString(item.protocol)}</option>}{outboundProtocols.map((protocol) => <option key={protocol} value={protocol}>{outboundProtocolLabels[protocol]}</option>)}</select><small>{editor.originalTag ? "编辑时协议保持不变；可通过高级 JSON 处理特殊配置。" : "选择协议后显示对应的正式版结构化字段。"}</small></label>
          <OutboundStructuredEditor serverId={server.id} token={token} item={item} onChange={setItem} onError={setError} />
        </>}
        {editor.kind === "rule" && <>
          <label><span>规则名称（Mark Tag）</span><input value={asString(item.marktag)} onChange={(event) => set("marktag", event.target.value)} placeholder="例如：入一" /></label>
          <label><span>网络</span><select value={asString(item.network)} onChange={(event) => set("network", event.target.value || undefined)}><option value="">不限</option><option value="tcp">TCP</option><option value="udp">UDP</option><option value="tcp,udp">TCP + UDP</option></select></label>
          {["domain", "ip", "protocol", "inboundTag", "user", "source"].map((key) => <label className="wide" key={key}><span>{routingFieldLabels[key]}（每行一项）</span><textarea value={Array.isArray(item[key]) ? (item[key] as unknown[]).join("\n") : asString(item[key])} onChange={(event) => set(key, event.target.value.split(/\n|,/).map((value) => value.trim()).filter(Boolean))} /></label>)}
          {["port", "sourcePort", "attrs"].map((key) => <label key={key}><span>{routingFieldLabels[key]}</span><input value={asString(item[key])} onChange={(event) => set(key, event.target.value || undefined)} /></label>)}
          <label><span>目标类型</span><select value={item.balancerTag ? "balancer" : "outbound"} onChange={(event) => setItem((current) => event.target.value === "balancer" ? { ...current, outboundTag: undefined, balancerTag: asString(balancers[0]?.tag) } : { ...current, balancerTag: undefined, outboundTag: asString(outbounds[0]?.tag) })}><option value="outbound">出站</option><option value="balancer">负载均衡</option></select></label>
          <label><span>目标 *</span><select value={asString(item.balancerTag || item.outboundTag)} onChange={(event) => item.balancerTag != null ? set("balancerTag", event.target.value) : set("outboundTag", event.target.value)}>{(item.balancerTag != null ? balancers : outbounds).map((value) => <option key={asString(value.tag)} value={asString(value.tag)}>{asString(value.tag)}</option>)}</select></label>
        </>}
        {editor.kind === "balancer" && <>
          <label><span>策略</span><select value={asString(strategy.type) || "random"} onChange={(event) => set("strategy", { ...strategy, type: event.target.value })}><option value="random">随机</option><option value="roundRobin">轮询</option><option value="leastPing">最低延迟</option><option value="leastLoad">最低负载</option></select></label>
          <label className="wide"><span>出站选择器（每行一项）</span><textarea value={Array.isArray(item.selector) ? (item.selector as unknown[]).join("\n") : ""} onChange={(event) => set("selector", event.target.value.split(/\n|,/).map((value) => value.trim()).filter(Boolean))} /></label>
          <label><span>回退标识（Fallback Tag）</span><input value={asString(item.fallbackTag)} onChange={(event) => set("fallbackTag", event.target.value || undefined)} /></label>
        </>}
      </div>}
      {error && <p className="xray-field-error">{error}</p>}
      {preview && <div className="xray-live-preview"><div><strong>{editor.kind === "outbound" ? "实时出站 JSON" : "实时入站 JSON"}</strong><button type="button" onClick={() => setPreview(false)} aria-label="关闭预览"><X /></button></div><pre>{JSON.stringify(advanced ? (() => { try { const parsed = parseObject(json); return editor.kind === "inbound" ? sanitizeInbound(parsed) : parsed; } catch { return editor.kind === "inbound" ? sanitizeInbound(item) : item; } })() : editor.kind === "inbound" ? sanitizeInbound(item) : item, null, 2)}</pre></div>}
    </div>
    <footer>{!readOnly && (editor.kind === "inbound" || editor.kind === "outbound") && <button type="button" onClick={() => setPreview((value) => !value)}><Eye />预览</button>}{!readOnly && <button type="button" onClick={toggleAdvanced}><Braces />{advanced ? "返回表单" : "高级 JSON"}</button>}<span /><button type="button" onClick={onCancel}>取消</button>{!readOnly && <button type="button" className="primary" disabled={pending || Boolean(error)} onClick={submit}><Save />{pending ? "保存中..." : "保存"}</button>}</footer>
  </section></div>;
}

function OutboundStructuredEditor({ serverId, token, item, onChange, onError }: { serverId: number; token: string; item: XrayObject; onChange: React.Dispatch<React.SetStateAction<XrayObject>>; onError: (message: string) => void }) {
  const protocol = asString(item.protocol).toLowerCase();
  const supported = outboundProtocols.includes(protocol);
  const settings = asObject(item.settings);
  const [nics, setNics] = useState<XrayServerNIC[]>([]);
  const [nicsMessage, setNicsMessage] = useState("");
  const [manualSource, setManualSource] = useState(false);
  const [blockTypesText, setBlockTypesText] = useState(() => Array.isArray(settings.blockTypes) ? (settings.blockTypes as unknown[]).join(",") : "");
  const [blockTypesError, setBlockTypesError] = useState("");
  const needsSource = protocol === "freedom" || protocol === "http" || !supported;

  useEffect(() => {
    setBlockTypesText(Array.isArray(asObject(item.settings).blockTypes) ? (asObject(item.settings).blockTypes as unknown[]).join(",") : "");
    setBlockTypesError("");
  }, [protocol]);

  useEffect(() => {
    if (!needsSource) return;
    let current = true;
    setNicsMessage("正在读取远端网卡...");
    void fetchXrayServerNICs(token, serverId).then((response) => {
      if (!current) return;
      setNics(response.nics || []);
      setNicsMessage(response.success === false ? response.message || "当前 Agent 不支持读取网卡，可手动填写" : "");
    }).catch((error) => {
      if (current) setNicsMessage(getError(error, "读取网卡失败，可手动填写"));
    });
    return () => { current = false; };
  }, [needsSource, serverId, token]);

  function update(updater: (current: XrayObject) => XrayObject) {
    onChange((current) => updater(current));
  }

  function replaceSettings(next: XrayObject) {
    update((current) => ({ ...current, settings: next }));
  }

  function setOptional(key: string, value: unknown, empty = value === "" || value == null) {
    const next = { ...settings };
    if (empty) delete next[key]; else next[key] = value;
    replaceSettings(next);
  }

  const sourceValue = asString(item.sendThrough);
  const nicAddresses = nics.flatMap((nic) => nic.addrs.map((address) => ({ ...address, nic: nic.name })));
  const predefinedSources = new Set(["", "origin", "srcip", ...nicAddresses.map((value) => value.ip)]);
  const customSource = Boolean(sourceValue) && !predefinedSources.has(sourceValue);
  const sourceError = validateSendThrough(sourceValue);
  const fragment = asObject(settings.fragment);
  const noises = Array.isArray(settings.noises) ? settings.noises as XrayObject[] : [];
  const httpServers = Array.isArray(settings.servers) ? settings.servers as XrayObject[] : [];
  const httpServer = asObject(httpServers[0]);
  const httpUsers = Array.isArray(httpServer.users) ? httpServer.users as XrayObject[] : [];
  const httpUser = asObject(httpUsers[0]);

  useEffect(() => {
    onError(sourceError || blockTypesError);
  }, [blockTypesError, onError, sourceError]);

  function updateFragment(key: string, value: string) {
    const nextFragment = { ...fragment };
    if (value.trim()) nextFragment[key] = value; else delete nextFragment[key];
    const next = { ...settings };
    if (Object.keys(nextFragment).length) next.fragment = nextFragment; else delete next.fragment;
    replaceSettings(next);
  }

  function updateNoise(index: number, values: XrayObject) {
    const next = noises.map((noise, position) => position === index ? { ...noise, ...values } : noise);
    replaceSettings({ ...settings, noises: next });
  }

  function updateHttpServer(values: XrayObject) {
    const nextFirst = { ...httpServer, ...values };
    replaceSettings({ ...settings, servers: [nextFirst, ...httpServers.slice(1)] });
  }

  function updateHttpUser(values: XrayObject) {
    const nextUser = { ...httpUser, ...values };
    const hasCredentials = Boolean(asString(nextUser.user).trim() || asString(nextUser.pass).trim());
    const nextUsers = hasCredentials ? [nextUser, ...httpUsers.slice(1)] : httpUsers.slice(1);
    const nextFirst = { ...httpServer };
    if (nextUsers.length) nextFirst.users = nextUsers; else delete nextFirst.users;
    replaceSettings({ ...settings, servers: [nextFirst, ...httpServers.slice(1)] });
  }

  return <div className="xray-outbound-form">
    {needsSource && <section className="xray-wizard-section"><div className="xray-wizard-title"><div><strong>出站源 IP</strong><small>多 IP 服务器可指定连接使用的本机地址，留空由系统路由决定。</small></div></div><div className="xray-wizard-grid">
      <label><span>来源地址（sendThrough）</span><select value={manualSource || customSource ? "__custom__" : sourceValue} onChange={(event) => { const value = event.target.value; setManualSource(value === "__custom__"); update((current) => { const next = { ...current }; if (!value || value === "__custom__") delete next.sendThrough; else next.sendThrough = value; return next; }); }}><option value="">不设置（默认）</option>{nics.map((nic) => <optgroup key={nic.name} label={nic.name}>{nic.addrs.map((address) => <option key={`${nic.name}-${address.ip}`} value={address.ip}>{address.ip} · {address.family === "v6" ? "IPv6" : "IPv4"}{address.scope && !["global", "public"].includes(address.scope) ? ` · ${address.scope}` : ""}</option>)}</optgroup>)}<optgroup label="特殊值"><option value="origin">origin · 跟随入站本机地址</option><option value="srcip">srcip · 跟随客户端源 IP</option></optgroup><option value="__custom__">自定义 IP/CIDR...</option></select></label>
      {manualSource || customSource || (!sourceValue && nicsMessage.includes("手动")) ? <label><span>自定义来源地址</span><input value={sourceValue} onChange={(event) => update((current) => ({ ...current, sendThrough: event.target.value }))} placeholder="203.0.113.10 / 2001:db8::1 / IP/CIDR" /><small>仅支持 IP、IP/CIDR、origin 或 srcip，不支持域名。</small></label> : null}
      {nicsMessage && <p className="xray-form-note wide">{nicsMessage}</p>}{sourceError && <p className="xray-inline-warning wide">{sourceError}</p>}
    </div></section>}

    {!supported && <p className="xray-inline-warning">该节点协议没有正式版结构化编辑器。生成配置已完整保留，请使用“高级 JSON”检查或修改。</p>}

    {protocol === "freedom" && <>
      <section className="xray-wizard-section"><div className="xray-wizard-title"><div><strong>直连设置</strong><small>对应 Xray Freedom outbound settings。</small></div></div><div className="xray-wizard-grid">
        <label><span>域名策略</span><select value={asString(settings.domainStrategy) || "AsIs"} onChange={(event) => setOptional("domainStrategy", event.target.value, event.target.value === "AsIs")}>{["AsIs", "UseIP", "UseIPv4", "UseIPv6", "UseIPv6v4", "UseIPv4v6", "ForceIP", "ForceIPv4", "ForceIPv6", "ForceIPv6v4", "ForceIPv4v6"].map((value) => <option key={value} value={value}>{domainStrategyLabels[value] || value}</option>)}</select></label>
        <label><span>重定向目标</span><input value={asString(settings.redirect)} onChange={(event) => setOptional("redirect", event.target.value.trim())} placeholder="127.0.0.1:80" /><small>留空时不设置 redirect。</small></label>
        <label><span>PROXY Protocol</span><select value={asString(settings.proxyProtocol) || "0"} onChange={(event) => setOptional("proxyProtocol", Number(event.target.value), event.target.value === "0")}><option value="0">0（禁用）</option><option value="1">1</option><option value="2">2</option></select></label>
      </div></section>
      <section className="xray-wizard-section"><div className="xray-wizard-title"><div><strong>数据分片</strong><small>任一字段有值时生成 fragment 对象。</small></div></div><div className="xray-wizard-grid"><label><span>分片包</span><input value={asString(fragment.packets)} onChange={(event) => updateFragment("packets", event.target.value)} placeholder="tlshello / 1-3" /></label><label><span>分片长度</span><input value={asString(fragment.length)} onChange={(event) => updateFragment("length", event.target.value)} placeholder="10-20" /></label><label><span>分片间隔</span><input value={asString(fragment.interval)} onChange={(event) => updateFragment("interval", event.target.value)} placeholder="10-20" /></label></div></section>
      <section className="xray-wizard-section"><div className="xray-wizard-title"><div><strong>噪声数据</strong><small>支持 rand、str、base64、hex。</small></div><button type="button" onClick={() => replaceSettings({ ...settings, noises: [...noises, { type: "rand", packet: "", delay: "" }] })}><Plus />添加</button></div>{noises.length === 0 ? <p className="xray-form-note">尚未添加噪声项。</p> : <div className="xray-outbound-array">{noises.map((noise, index) => <article key={`noise-${index}`}><div className="xray-wizard-grid"><label><span>类型</span><select value={asString(noise.type) || "rand"} onChange={(event) => updateNoise(index, { type: event.target.value })}>{["rand", "str", "base64", "hex"].map((value) => <option key={value}>{value}</option>)}</select></label><label><span>数据包</span><input value={asString(noise.packet)} onChange={(event) => updateNoise(index, { packet: event.target.value })} placeholder="rand: 10-20 / str: text" /></label><label><span>延迟</span><input value={asString(noise.delay)} onChange={(event) => updateNoise(index, { delay: event.target.value })} placeholder="10-16" /></label></div><button type="button" className="danger" onClick={() => { const next = noises.filter((_, position) => position !== index); const nextSettings = { ...settings }; if (next.length) nextSettings.noises = next; else delete nextSettings.noises; replaceSettings(nextSettings); }}><Trash2 />删除噪声</button></article>)}</div>}</section>
    </>}

    {protocol === "blackhole" && <section className="xray-wizard-section"><div className="xray-wizard-title"><div><strong>阻断响应</strong><small>选择是否返回 HTTP 空响应。</small></div></div><div className="xray-wizard-grid"><label><span>响应类型</span><select value={asString(asObject(settings.response).type) || "none"} onChange={(event) => setOptional("response", { type: event.target.value }, event.target.value === "none")}><option value="none">不返回响应</option><option value="http">HTTP</option></select></label></div></section>}

    {protocol === "dns" && <section className="xray-wizard-section"><div className="xray-wizard-title"><div><strong>DNS 接管</strong><small>结构化生成 DNS outbound settings。</small></div></div><div className="xray-wizard-grid">
      <label><span>网络</span><select value={asString(settings.network) || "passthrough"} onChange={(event) => setOptional("network", event.target.value, event.target.value === "passthrough")}><option value="passthrough">透传（passthrough）</option><option value="tcp">TCP</option><option value="udp">UDP</option></select><small>透传时不写入 network 字段。</small></label>
      <label><span>DNS 地址</span><input value={asString(settings.address)} onChange={(event) => setOptional("address", event.target.value.trim())} placeholder="1.1.1.1" /></label>
      <label><span>DNS 端口</span><input type="number" min="1" max="65535" value={settings.port == null ? "" : asString(settings.port)} onChange={(event) => setOptional("port", Number(event.target.value), !event.target.value)} placeholder="53" /></label>
      <label><span>用户等级</span><input type="number" min="0" max="255" value={settings.userLevel == null ? "0" : asString(settings.userLevel)} onChange={(event) => setOptional("userLevel", Number(event.target.value), !event.target.value || Number(event.target.value) === 0)} /></label>
      <label><span>非 IP 查询</span><select value={asString(settings.nonIPQuery) || "drop"} onChange={(event) => setOptional("nonIPQuery", event.target.value, event.target.value === "drop")}><option value="drop">丢弃（drop）</option><option value="skip">跳过（skip）</option></select></label>
      <label><span>阻断查询类型</span><input value={blockTypesText} onChange={(event) => { const text = event.target.value; setBlockTypesText(text); const values = text.split(",").map((value) => value.trim()).filter(Boolean); const invalid = values.some((value) => !Number.isFinite(Number(value))); const message = invalid ? "阻断查询类型必须是逗号分隔的数字" : ""; setBlockTypesError(message); if (invalid) return; setOptional("blockTypes", values.map(Number), values.length === 0); }} placeholder="65, 28" /><small>填写 DNS query type 数字，使用英文逗号分隔。</small></label>
    </div></section>}

    {protocol === "http" && <section className="xray-wizard-section"><div className="xray-wizard-title"><div><strong>HTTP 代理服务器</strong><small>编辑首个 server/user；其余已有项保持原顺序。</small></div></div><div className="xray-wizard-grid">
      <label><span>服务器地址 *</span><input value={asString(httpServer.address)} onChange={(event) => updateHttpServer({ address: event.target.value })} placeholder="proxy.example.com" /></label>
      <label><span>服务器端口 *</span><input type="number" min="1" max="65535" value={httpServer.port == null ? "" : asString(httpServer.port)} onChange={(event) => updateHttpServer({ port: Number(event.target.value) })} placeholder="8080" /></label>
      <label><span>用户名</span><input value={asString(httpUser.user)} onChange={(event) => updateHttpUser({ user: event.target.value })} placeholder="留空表示匿名" /></label>
      <label><span>密码</span><input type="password" value={asString(httpUser.pass)} onChange={(event) => updateHttpUser({ pass: event.target.value })} placeholder="留空表示匿名" /></label>
      <label><span>用户等级</span><input type="number" min="0" max="255" value={httpUser.level == null ? "0" : asString(httpUser.level)} onChange={(event) => updateHttpUser({ level: Number(event.target.value) })} /></label>
    </div>{(httpServers.length > 1 || httpUsers.length > 1) && <p className="xray-inline-warning">检测到多个 server 或 user：表单只编辑首项，其余配置会原样保留。</p>}</section>}

    {protocol === "loopback" && <section className="xray-wizard-section"><div className="xray-wizard-title"><div><strong>回环目标</strong><small>把流量回送到指定入站。</small></div></div><div className="xray-wizard-grid"><label><span>入站标识 *</span><input value={asString(settings.inboundTag)} onChange={(event) => setOptional("inboundTag", event.target.value.trim())} placeholder="redirect-in" /></label></div></section>}
  </div>;
}

function WarpManager({ server, token, onClose, onChanged }: { server: RemoteServer; token: string; onClose: () => void; onChanged: () => void }) {
  const [status, setStatus] = useState<XrayWarpStatus | null>(null);
  const [license, setLicense] = useState("");
  const [busy, setBusy] = useState("");
  const [message, setMessage] = useState<Notice>(null);

  const refresh = useCallback(async () => {
    try { setStatus(await fetchXrayWarpStatus(token, server.id)); setMessage(null); }
    catch (error) { setMessage({ kind: "error", text: getError(error, "读取 WARP 状态失败") }); }
  }, [server.id, token]);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => { if (!busy) void refresh(); }, 5000);
    return () => window.clearInterval(timer);
  }, [busy, refresh]);

  async function run(action: "install" | "license" | "remove") {
    if (action === "remove" && !window.confirm("确认移除 Cloudflare WARP，并删除 warp-v4 / warp-v6 出站？")) return;
    setBusy(action); setMessage(null);
    try {
      if (action === "install") await installXrayWarp(token, server.id);
      if (action === "license") await updateXrayWarpLicense(token, server.id, license.trim());
      if (action === "remove") await removeXrayWarp(token, server.id);
      setLicense("");
      await refresh();
      onChanged();
      setMessage({ kind: "success", text: action === "remove" ? "WARP 已移除" : action === "license" ? "WARP+ 授权已更新" : status?.installed ? "WARP 配置已同步" : "WARP 已安装" });
    } catch (error) {
      setMessage({ kind: "error", text: getError(error, "WARP 操作失败") });
    } finally { setBusy(""); }
  }

  return <div className="xray-editor-layer" role="presentation" onClick={() => { if (!busy) onClose(); }}><section className="xray-object-editor xray-warp-editor" role="dialog" aria-label="Cloudflare WARP 管理" onClick={(event) => event.stopPropagation()}>
    <header><div><h4>Cloudflare WARP</h4><p>{server.name} · 自动管理 warp-v4 / warp-v6 出站</p></div><button type="button" disabled={Boolean(busy)} onClick={onClose} aria-label="关闭"><X /></button></header>
    <div className="xray-object-body"><section className="xray-wizard-section"><div className="xray-warp-status"><Cloud /><div><strong>{status == null ? "正在检查..." : status.installed ? "已安装" : "未安装"}</strong>{status?.license_active && <span>WARP+</span>}<p>{status?.addr_v4 ? `IPv4：${status.addr_v4}` : ""}{status?.addr_v6 ? `${status?.addr_v4 ? " · " : ""}IPv6：${status.addr_v6}` : ""}</p></div></div>{status?.installed ? <><label><span>WARP+ 授权码</span><div className="xray-input-action"><input value={license} onChange={(event) => setLicense(event.target.value)} placeholder="XXXXXXXX-XXXXXXXX-XXXXXXXX" /><button type="button" disabled={Boolean(busy) || !license.trim()} onClick={() => void run("license")}><KeyRound />更新授权</button></div><small>授权码仅提交到当前远端 Agent，不写入前端源码。</small></label><div className="xray-warp-actions"><button type="button" disabled={Boolean(busy)} onClick={() => void run("install")}><RefreshCw />同步配置</button><button type="button" className="danger" disabled={Boolean(busy)} onClick={() => void run("remove")}><Trash2 />移除 WARP</button></div></> : <button type="button" className="primary xray-warp-install" disabled={Boolean(busy) || status == null} onClick={() => void run("install")}><Cloud />{busy === "install" ? "安装中..." : "安装 Cloudflare WARP"}</button>}</section>{message && <div className={`xray-notice ${message.kind}`}><span>{message.text}</span></div>}</div>
    <footer><span /><button type="button" disabled={Boolean(busy)} onClick={onClose}>关闭</button></footer>
  </section></div>;
}

function InboundStructuredEditor({ server, token, username, nodes, item, onChange, onError, usedPorts, originalPort }: { server: RemoteServer; token: string; username: string; nodes: XrayNode[]; item: XrayObject; onChange: React.Dispatch<React.SetStateAction<XrayObject>>; onError: (message: string) => void; usedPorts: number[]; originalPort: number }) {
  const protocol = asString(item.protocol).toLowerCase();
  const protocolMode = inboundProtocolMode(item);
  const [configurationMode, setConfigurationMode] = useState<"simple" | "expert">("simple");
  const settings = asObject(item.settings);
  const stream = asObject(item.streamSettings);
  const transport = inboundTransportMode(item);
  const security = inboundSecurityMode(item);
  const combinations = inboundCombinations[protocolMode] || [{ transport: "TCP", securities: ["None"] }];
  const transportOptions = combinations.map((value) => value.transport);
  const securityOptions = combinations.find((value) => value.transport === transport)?.securities || combinations[0].securities;
  const sniffing = asObject(item.sniffing);
  const tls = asObject(stream.tlsSettings);
  const ws = asObject(stream.wsSettings);
  const grpc = asObject(stream.grpcSettings);
  const xhttp = asObject(stream.xhttpSettings);
  const portConflict = asNumber(item.port) !== originalPort && usedPorts.includes(asNumber(item.port));

  function update(updater: (current: XrayObject) => XrayObject) {
    onChange((current) => updater(current));
    onError("");
  }

  function updateSettings(values: XrayObject) {
    update((current) => ({ ...current, settings: { ...asObject(current.settings), ...values } }));
  }

  function updateStream(values: XrayObject) {
    update((current) => ({ ...current, streamSettings: { ...asObject(current.streamSettings), ...values } }));
  }

  function applySecurity(current: XrayObject, mode: string) {
    const currentSettings = asObject(current.settings);
    const currentStream = asObject(current.streamSettings);
    const clients = Array.isArray(currentSettings.clients) ? currentSettings.clients as XrayObject[] : [];
    const vision = mode.includes("Vision");
    const nextClients = clients.map((client) => {
      if (vision) return { ...client, flow: asString(client.flow) || "xtls-rprx-vision" };
      const next = { ...client };
      delete next.flow;
      return next;
    });
    const actualSecurity = mode.includes("REALITY") ? "reality" : mode.includes("TLS") ? "tls" : "none";
    const nextStream: XrayObject = { ...currentStream, security: actualSecurity };
    if (actualSecurity === "tls" && !nextStream.tlsSettings) nextStream.tlsSettings = { minVersion: "1.2" };
    if (actualSecurity === "reality" && !nextStream.realitySettings) nextStream.realitySettings = { dest: "www.lovelive-anime.jp:443", serverNames: ["www.lovelive-anime.jp"], shortIds: [""] };
    return {
      ...current,
      _wizard_security: mode,
      settings: { ...currentSettings, ...(clients.length ? { clients: nextClients } : {}), ...(mode === "Encryption" ? {} : { decryption: "none", encryption: undefined }) },
      streamSettings: nextStream,
    };
  }

  function changeTransport(nextTransport: string) {
    const nextCombination = combinations.find((value) => value.transport === nextTransport) || combinations[0];
    const nextSecurity = nextCombination.securities.includes(security) ? security : nextCombination.securities[0];
    update((current) => {
      const currentStream = asObject(current.streamSettings);
      const network = nextTransport === "GRPC" ? "grpc" : nextTransport === "XHTTP" ? "xhttp" : nextTransport === "Websocket" || nextTransport === "WSS" ? "ws" : nextTransport === "hysteria" ? "hysteria" : "tcp";
      let next: XrayObject = { ...current, _wizard_transport: nextTransport, streamSettings: { ...currentStream, network } };
      if (nextTransport === "GRPC") next = { ...next, streamSettings: { ...asObject(next.streamSettings), grpcSettings: { ...asObject(currentStream.grpcSettings), serviceName: asString(asObject(currentStream.grpcSettings).serviceName) || "grpc" } } };
      if (nextTransport === "Websocket" || nextTransport === "WSS") next = { ...next, streamSettings: { ...asObject(next.streamSettings), wsSettings: { ...asObject(currentStream.wsSettings), path: asString(asObject(currentStream.wsSettings).path) || "/ws" } } };
      if (nextTransport === "XHTTP") next = { ...next, streamSettings: { ...asObject(next.streamSettings), xhttpSettings: { ...asObject(currentStream.xhttpSettings), path: asString(asObject(currentStream.xhttpSettings).path) || "/xhttp", mode: asString(asObject(currentStream.xhttpSettings).mode) || "auto" } } };
      return applySecurity(next, nextSecurity);
    });
    onError("");
  }

  function changeSecurity(mode: string) {
    update((current) => applySecurity(current, mode));
  }

  function updateTLS(values: XrayObject) {
    updateStream({ security: "tls", tlsSettings: { ...tls, ...values } });
  }

  function applyPreset(name: "vless-reality" | "ss-2022") {
    onChange(name === "ss-2022" ? defaultInboundForUser("shadowsocks2022", username) : defaultInboundForUser("vless", username));
    onError("");
  }

  function chooseAvailablePort() {
    const unavailable = new Set(usedPorts.filter((port) => port !== originalPort));
    const candidates = Array.from({ length: 55536 }, (_, index) => index + 10000).filter((port) => !unavailable.has(port));
    const port = candidates[Math.floor(Math.random() * candidates.length)];
    if (port) update((current) => ({ ...current, port }));
  }

  return <div className="xray-inbound-wizard wide">
    <section className="xray-wizard-section"><div className="xray-wizard-title"><div><strong>快速预设</strong><small>与正式向导一致的常用起点</small></div></div><div className="xray-preset-grid"><button type="button" onClick={() => applyPreset("vless-reality")}><strong>VLESS + REALITY</strong><span>XTLS Vision · TCP · 443</span></button><button type="button" onClick={() => applyPreset("ss-2022")}><strong>Shadowsocks 2022</strong><span>轻量 · TCP/UDP · 8388</span></button></div></section>

    <section className="xray-wizard-section"><div className="xray-wizard-title"><div><strong>入站类型</strong><small>协议、传输与安全方式</small></div></div><div className="xray-wizard-grid"><label><span>协议 *</span><select value={protocolMode} onChange={(event) => { onChange(defaultInboundForUser(event.target.value, username)); onError(""); }}>{inboundProtocols.map((value) => { const embeddedOnly = ["anytls", "snell", "mieru"].includes(value.value); return <option key={value.value} value={value.value} disabled={embeddedOnly && server.xray_mode === "external"}>{value.label}{embeddedOnly && server.xray_mode === "external" ? "（需要内置 Xray）" : ""}</option>; })}</select></label><label><span>传输方式</span><select value={transport} onChange={(event) => changeTransport(event.target.value)}>{transportOptions.map((value) => <option key={value} value={value}>{displayInboundTransport(value)}</option>)}</select></label><label><span>安全方式</span><select value={security} onChange={(event) => changeSecurity(event.target.value)}>{securityOptions.map((value) => <option key={value} value={value}>{displayInboundSecurity(value)}</option>)}</select></label><label><span>配置模式</span><div className="xray-mode-switch"><button type="button" className={configurationMode === "simple" ? "active" : ""} onClick={() => setConfigurationMode("simple")}>简易模式</button><button type="button" className={configurationMode === "expert" ? "active" : ""} onClick={() => setConfigurationMode("expert")}>专家模式</button></div></label><label className="wide"><span>节点名称</span><input value={asString(item._wizard_node_name)} onChange={(event) => update((current) => ({ ...current, _wizard_node_name: event.target.value }))} placeholder="自定义订阅中的节点显示名称" /><small>可填写中文；这是节点显示名，不占用 Xray 入站 Tag。</small></label>{configurationMode === "expert" && transport !== "WSS" && <><label><span>监听地址</span><input value={asString(item.listen)} onChange={(event) => update((current) => ({ ...current, listen: event.target.value }))} placeholder="0.0.0.0" /></label><label><span>端口 *</span><div className="xray-input-action"><input type="number" min="1" max="65535" value={asString(item.port)} onChange={(event) => update((current) => ({ ...current, port: Number(event.target.value) }))} /><button type="button" onClick={chooseAvailablePort}>随机</button></div></label><label><span>入站标识（Tag）</span><input value={asString(item.tag)} onChange={(event) => update((current) => ({ ...current, tag: event.target.value }))} /></label><label className="xray-option-toggle"><span><strong>流量探测</strong><small>仅用于路由识别，不改写目标地址</small></span><input type="checkbox" checked={Boolean(sniffing.enabled)} onChange={(event) => update((current) => ({ ...current, sniffing: event.target.checked ? { ...asObject(current.sniffing), enabled: true, destOverride: security.includes("REALITY") ? ["http", "tls", "quic"] : ["http", "tls"], routeOnly: true } : { enabled: false } }))} /></label></>}</div>{transport === "WSS" && <p className="xray-assistant-note">WSS 由 Nginx 反向代理，本地监听端口和随机路径在提交后按正式流程生成。</p>}{configurationMode === "expert" && transport !== "WSS" && portConflict && <p className="xray-inline-warning">端口 {asNumber(item.port)} 已被当前服务器的其他入站占用。</p>}</section>

    {(configurationMode === "expert" || protocol === "snell") && <ProtocolSettings protocol={protocol} protocolMode={protocolMode} nodes={nodes} item={item} settings={settings} onChange={update} onSettings={updateSettings} />}

    {configurationMode === "expert" && (transport === "GRPC" || transport === "Websocket" || transport === "WSS" || transport === "XHTTP") && <section className="xray-wizard-section"><div className="xray-wizard-title"><div><strong>传输配置</strong><small>{transport}</small></div></div><div className="xray-wizard-grid">{transport === "GRPC" && <label><span>服务名称 *</span><input value={asString(grpc.serviceName)} onChange={(event) => updateStream({ grpcSettings: { ...grpc, serviceName: event.target.value } })} placeholder="grpc" /></label>}{(transport === "Websocket" || transport === "WSS") && <><label><span>路径</span><input value={asString(ws.path)} onChange={(event) => updateStream({ wsSettings: { ...ws, path: event.target.value } })} placeholder="/ws" /></label><label><span>主机请求头</span><input value={asString(asObject(ws.headers).Host)} onChange={(event) => updateStream({ wsSettings: { ...ws, headers: event.target.value ? { ...asObject(ws.headers), Host: event.target.value } : undefined } })} placeholder="example.com" /></label></>}{transport === "XHTTP" && <><label><span>路径</span><input value={asString(xhttp.path)} onChange={(event) => updateStream({ xhttpSettings: { ...xhttp, path: event.target.value } })} placeholder="/xhttp" /></label><label><span>模式</span><select value={asString(xhttp.mode) || "auto"} onChange={(event) => updateStream({ xhttpSettings: { ...xhttp, mode: event.target.value } })}><option value="auto">自动</option><option value="stream-up">上传流</option><option value="stream-one">单流</option></select></label><label><span>主机</span><input value={asString(xhttp.host)} onChange={(event) => updateStream({ xhttpSettings: { ...xhttp, host: event.target.value || undefined } })} /></label></>}</div></section>}

    {configurationMode === "expert" && asString(stream.security) === "tls" && <section className="xray-wizard-section"><div className="xray-wizard-title"><div><strong>TLS / XTLS</strong><small>{security}</small></div></div><div className="xray-wizard-grid">{protocol === "hysteria" && <label className="xray-option-toggle"><span><strong>允许不安全（自签证书）</strong><small>生成节点时启用跳过证书验证</small></span><input type="checkbox" checked={Boolean(item.insecure)} onChange={(event) => update((current) => ({ ...current, insecure: event.target.checked || undefined }))} /></label>}{!security.includes("Vision") && <label><span>服务器名称（SNI）</span><input value={asString(tls.serverName)} onChange={(event) => updateTLS({ serverName: event.target.value })} placeholder="example.com" /></label>}<label><span>证书文件 *</span><input value={asString((Array.isArray(tls.certificates) ? asObject(tls.certificates[0]).certificateFile : ""))} onChange={(event) => updateTLS({ certificates: [{ ...asObject(Array.isArray(tls.certificates) ? tls.certificates[0] : {}), certificateFile: event.target.value }] })} placeholder="/path/to/fullchain.crt" /></label><label><span>私钥文件 *</span><input value={asString((Array.isArray(tls.certificates) ? asObject(tls.certificates[0]).keyFile : ""))} onChange={(event) => updateTLS({ certificates: [{ ...asObject(Array.isArray(tls.certificates) ? tls.certificates[0] : {}), keyFile: event.target.value }] })} placeholder="/path/to/private.key" /></label>{!security.includes("Vision") && <label><span>ALPN</span><input value={Array.isArray(tls.alpn) ? (tls.alpn as unknown[]).join(",") : asString(tls.alpn)} onChange={(event) => updateTLS({ alpn: event.target.value.split(",").map((value) => value.trim()).filter(Boolean) })} placeholder={protocol === "hysteria" ? "h3" : "h2,http/1.1"} /></label>}<label><span>最低 TLS 版本</span><select value={asString(tls.minVersion) || "1.2"} onChange={(event) => updateTLS({ minVersion: event.target.value })}><option value="1.2">TLS 1.2</option><option value="1.3">TLS 1.3</option></select></label></div></section>}

    <InboundClientsEditor item={item} protocol={protocol} protocolMode={protocolMode} security={security} onChange={update} />
    <InboundAssistant server={server} token={token} item={item} protocolMode={protocolMode} expert={configurationMode === "expert"} onChange={onChange} onError={onError} />

    {configurationMode === "expert" && <details className="xray-raw-sections"><summary>原始分区 JSON</summary><div><label><span>基础设置 JSON</span><textarea value={JSON.stringify(settings, null, 2)} onChange={(event) => { try { update((current) => ({ ...current, settings: parseObject(event.target.value, "基础设置") })); } catch (error) { onError(getError(error, "基础设置错误")); } }} /></label><label><span>传输设置 JSON</span><textarea value={JSON.stringify(stream, null, 2)} onChange={(event) => { try { update((current) => ({ ...current, streamSettings: parseObject(event.target.value, "传输设置") })); } catch (error) { onError(getError(error, "传输设置错误")); } }} /></label><label><span>流量探测 JSON</span><textarea value={JSON.stringify(sniffing, null, 2)} onChange={(event) => { try { update((current) => ({ ...current, sniffing: parseObject(event.target.value, "流量探测") })); } catch (error) { onError(getError(error, "流量探测错误")); } }} /></label></div></details>}
  </div>;
}

function ProtocolSettings({ protocol, protocolMode, nodes, item, settings, onChange, onSettings }: { protocol: string; protocolMode: string; nodes: XrayNode[]; item: XrayObject; settings: XrayObject; onChange: (updater: (current: XrayObject) => XrayObject) => void; onSettings: (values: XrayObject) => void }) {
  if (!["shadowsocks", "socks", "http", "anytls", "snell", "tunnel", "mieru"].includes(protocol)) return null;
  const padding = Array.isArray(settings.paddingScheme) ? (settings.paddingScheme as unknown[]).join("\n") : asString(settings.paddingScheme);
  const snellVersion = asNumber(item._wizard_snell_version) || 4;
  const snellObfs = asString(item._wizard_snell_obfs_mode) || "none";
  function selectTunnelNode(value: string) {
    const node = nodes.find((entry) => String(entry.id) === value);
    if (!node) return;
    try {
      const clash = parseObject(node.clash_config || "{}", "节点配置");
      const address = asString(clash.server || node.server);
      const port = asNumber(clash.port || node.port);
      if (!address || !port) return;
      const slug = asString(node.node_name).toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/-+/g, "-").replace(/^-|-$/g, "").slice(0, 40) || "node";
      onChange((current) => ({ ...current, listen: "0.0.0.0", port, tag: `tunnel-${slug}-${port}`, sniffing: { enabled: true, destOverride: ["http", "tls"] }, settings: { ...asObject(current.settings), address, port, network: "tcp,udp", followRedirect: true } }));
    } catch {
      // Keep manual fields available when a legacy node has no parseable Clash config.
    }
  }
  return <section className="xray-wizard-section"><div className="xray-wizard-title"><div><strong>协议配置</strong><small>{inboundProtocols.find((value) => value.value === protocolMode)?.label}</small></div></div><div className="xray-wizard-grid">
    {protocolMode === "shadowsocks" && <><label><span>加密方式 *</span><select value={asString(settings.method) || "aes-256-gcm"} onChange={(event) => { const method = event.target.value; onSettings({ method, clients: (Array.isArray(settings.clients) ? settings.clients as XrayObject[] : []).map((client) => ({ ...client, method })) }); }}><option>aes-128-gcm</option><option>aes-256-gcm</option><option>chacha20-poly1305</option><option>chacha20-ietf-poly1305</option><option>xchacha20-poly1305</option><option>xchacha20-ietf-poly1305</option></select></label><label><span>网络</span><select value={asString(settings.network) || "tcp,udp"} onChange={(event) => onSettings({ network: event.target.value })}><option value="tcp,udp">TCP + UDP</option><option value="tcp">TCP</option><option value="udp">UDP</option></select></label></>}
    {protocolMode === "shadowsocks2022" && <><label><span>加密方式 *</span><select value={asString(settings.method) || "2022-blake3-aes-128-gcm"} onChange={(event) => { const method = event.target.value; const length = method.includes("128") ? 16 : 32; onSettings({ method, password: randomBase64(length), clients: (Array.isArray(settings.clients) ? settings.clients as XrayObject[] : []).map((client) => ({ ...client, password: randomBase64(length) })) }); }}><option>2022-blake3-aes-128-gcm</option><option>2022-blake3-aes-256-gcm</option><option>2022-blake3-chacha20-poly1305</option></select></label><label><span>服务器密码 *</span><div className="xray-input-action"><input value={asString(settings.password)} onChange={(event) => onSettings({ password: event.target.value })} /><button type="button" onClick={() => onSettings({ password: randomBase64(asString(settings.method).includes("128") ? 16 : 32) })}><KeyRound />生成</button></div></label><label><span>网络</span><select value={asString(settings.network) || "tcp,udp"} onChange={(event) => onSettings({ network: event.target.value })}><option value="tcp,udp">TCP + UDP</option><option value="tcp">TCP</option><option value="udp">UDP</option></select></label></>}
    {(protocol === "socks" || protocol === "http") && <><label><span>认证方式</span><select value={asString(settings.auth) || (protocol === "http" ? "noauth" : "password")} onChange={(event) => onSettings({ auth: event.target.value })}><option value="password">用户名和密码</option><option value="noauth">无认证</option></select></label><label className="xray-option-toggle"><span><strong>UDP</strong><small>允许 UDP 请求</small></span><input type="checkbox" checked={settings.udp !== false} onChange={(event) => onSettings({ udp: event.target.checked })} /></label>{protocol === "http" && <label className="xray-option-toggle"><span><strong>透明代理</strong><small>允许透明代理</small></span><input type="checkbox" checked={Boolean(settings.allowTransparent)} onChange={(event) => onSettings({ allowTransparent: event.target.checked })} /></label>}</>}
    {protocol === "anytls" && <label className="wide"><span>填充方案</span><textarea value={padding} onChange={(event) => onSettings({ paddingScheme: event.target.value.split("\n").map((value) => value.trim()).filter(Boolean) })} /></label>}
    {protocol === "snell" && <><label><span>Snell 版本</span><select value={String(snellVersion)} onChange={(event) => onChange((current) => ({ ...current, _wizard_snell_version: Number(event.target.value) }))}><option value="4">v4 / v5</option><option value="6">v6</option></select></label>{snellVersion === 6 ? <label><span>V6 模式</span><select value={asString(item._wizard_snell_mode) || "default"} onChange={(event) => onChange((current) => ({ ...current, _wizard_snell_mode: event.target.value }))}><option value="default">默认</option><option value="unshaped">不整形</option><option value="unsafe-raw">原始模式</option></select></label> : <><label><span>混淆模式</span><select value={snellObfs} onChange={(event) => onChange((current) => ({ ...current, _wizard_snell_obfs_mode: event.target.value }))}><option value="none">无</option><option value="http">HTTP</option><option value="tls">TLS</option></select></label>{snellObfs !== "none" && <label><span>混淆主机</span><input value={asString(item._wizard_snell_obfs_host)} onChange={(event) => onChange((current) => ({ ...current, _wizard_snell_obfs_host: event.target.value }))} placeholder="bing.com" /></label>}</>}</>}
    {protocol === "mieru" && <label><span>客户端传输</span><select value={asString(settings.transport) || "tcp"} onChange={(event) => onSettings({ transport: event.target.value })}><option value="tcp">TCP（推荐）</option><option value="udp">UDP（抗封锁）</option></select><small>服务端同时监听 TCP 和 UDP；这里决定订阅下发给客户端的传输方式。</small></label>}
    {protocol === "tunnel" && <><label className="wide"><span>从已有节点转发</span><select defaultValue="" onChange={(event) => selectTunnelNode(event.target.value)}><option value="">手动填写目标</option>{nodes.map((node) => <option key={node.id} value={node.id}>{node.node_name}</option>)}</select></label><label><span>转发地址 *</span><input value={asString(settings.address)} onChange={(event) => onSettings({ address: event.target.value })} placeholder="example.com" /></label><label><span>转发端口 *</span><input type="number" min="1" max="65535" value={asString(settings.port)} onChange={(event) => onSettings({ port: Number(event.target.value) })} /></label><label><span>网络</span><select value={asString(settings.network) || "tcp"} onChange={(event) => onSettings({ network: event.target.value })}><option value="tcp">TCP</option><option value="udp">UDP</option><option value="tcp,udp">TCP + UDP</option></select></label><label className="xray-option-toggle"><span><strong>跟随重定向</strong><small>跟随透明转发目标</small></span><input type="checkbox" checked={Boolean(settings.followRedirect)} onChange={(event) => onSettings({ followRedirect: event.target.checked })} /></label><label><span>用户等级</span><input type="number" min="0" max="255" value={asString(settings.userLevel)} onChange={(event) => onSettings({ userLevel: Number(event.target.value) })} /></label></>}
  </div></section>;
}

function InboundClientsEditor({ item, protocol, protocolMode, security, onChange }: { item: XrayObject; protocol: string; protocolMode: string; security: string; onChange: (updater: (current: XrayObject) => XrayObject) => void }) {
  const settings = asObject(item.settings);
  const key = inboundCredentialKey(protocol);
  const rows = Array.isArray(settings[key]) ? settings[key] as XrayObject[] : [];
  const noAuth = (protocol === "socks" || protocol === "http") && asString(settings.auth) === "noauth";
  if (protocol === "tunnel" || noAuth) return null;
  function fields() {
    if (protocol === "vless" || protocol === "vmess") return [{ key: "id", label: "UUID", type: "text" }, { key: "email", label: "邮箱", type: "text" }, { key: "level", label: "等级", type: "number" }, ...(protocol === "vless" && security.includes("Vision") ? [{ key: "flow", label: "流控", type: "text" }] : [])];
    if (protocol === "trojan") return [{ key: "password", label: "密码", type: "password" }, { key: "email", label: "邮箱", type: "text" }, { key: "level", label: "等级", type: "number" }, ...(security.includes("Vision") ? [{ key: "flow", label: "流控", type: "text" }] : [])];
    if (protocol === "shadowsocks") return [{ key: "password", label: protocolMode === "shadowsocks2022" ? "用户 PSK" : "密码", type: "password" }, ...(protocolMode === "shadowsocks" ? [{ key: "method", label: "加密方式", type: "text" }] : []), { key: "email", label: "邮箱", type: "text" }, { key: "level", label: "等级", type: "number" }];
    if (protocol === "socks" || protocol === "http") return [{ key: "user", label: "用户名", type: "text" }, { key: "pass", label: "密码", type: "password" }, { key: "email", label: "邮箱", type: "text" }, { key: "level", label: "等级", type: "number" }];
    if (protocol === "hysteria") return [{ key: "auth", label: "认证信息", type: "password" }, { key: "email", label: "邮箱", type: "text" }, { key: "level", label: "等级", type: "number" }];
    if (protocol === "snell") return [{ key: "psk", label: "PSK", type: "password" }, { key: "clientId", label: "客户端 ID", type: "text" }, { key: "email", label: "邮箱", type: "text" }, { key: "level", label: "等级", type: "number" }];
    if (protocol === "mieru") return [{ key: "username", label: "用户名", type: "text" }, { key: "password", label: "密码", type: "password" }, { key: "email", label: "邮箱", type: "text" }, { key: "level", label: "等级", type: "number" }];
    return [{ key: "password", label: "密码", type: "password" }, { key: "email", label: "邮箱", type: "text" }, { key: "level", label: "等级", type: "number" }];
  }
  const schema = fields();
  function newRow(): XrayObject {
    const result: XrayObject = {};
    schema.forEach((field) => {
      if (field.key === "id") result.id = crypto.randomUUID();
      else if (field.key === "password" || field.key === "pass" || field.key === "auth" || field.key === "psk") result[field.key] = protocolMode === "shadowsocks2022" ? randomBase64(asString(settings.method).includes("128") ? 16 : 32) : randomPassword();
      else if (field.key === "clientId") result.clientId = crypto.randomUUID().replace(/-/g, "").slice(0, 12);
      else if (field.key === "method") result.method = asString(settings.method) || "aes-256-gcm";
      else if (field.key === "level") result.level = 0;
      else if (field.key === "flow") result.flow = "xtls-rprx-vision";
      else result[field.key] = "";
    });
    return result;
  }
  function setRows(next: XrayObject[]) {
    onChange((current) => ({ ...current, settings: { ...asObject(current.settings), [key]: next } }));
  }
  return <section className="xray-wizard-section"><div className="xray-wizard-title"><div><strong>{key === "accounts" ? "账户" : "客户端"}</strong><small>{rows.length} 项 · 可逐字段编辑</small></div><button type="button" onClick={() => setRows([...rows, newRow()])}><Plus />添加</button></div><div className="xray-client-list">{rows.length === 0 && <p>尚未添加凭据，可手动添加或使用下方正式用户选择器。</p>}{rows.map((row, index) => <article key={index}><div className="xray-client-head"><strong>#{index + 1}</strong><button type="button" className="danger" onClick={() => setRows(rows.filter((_, position) => position !== index))} aria-label={`删除客户端 ${index + 1}`}><Trash2 /></button></div><div className="xray-wizard-grid">{schema.map((field) => <label key={field.key}><span>{field.label}</span><div className={field.type === "password" || field.key === "id" ? "xray-input-action" : undefined}><input type={field.type} value={asString(row[field.key])} onChange={(event) => { const next = [...rows]; next[index] = { ...row, [field.key]: field.type === "number" ? Number(event.target.value) : event.target.value }; setRows(next); }} />{(field.type === "password" || field.key === "id") && <button type="button" onClick={() => { const next = [...rows]; next[index] = { ...row, [field.key]: field.key === "id" ? crypto.randomUUID() : protocol === "shadowsocks" ? randomBase64(asString(settings.method).includes("128") ? 16 : 32) : randomPassword() }; setRows(next); }}><KeyRound />生成</button>}</div></label>)}</div></article>)}</div></section>;
}

function randomPassword(length = 16) {
  const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*";
  const bytes = new Uint8Array(length);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (value) => alphabet[value % alphabet.length]).join("");
}

function randomBase64(length: number) {
  const bytes = new Uint8Array(length);
  crypto.getRandomValues(bytes);
  let binary = "";
  bytes.forEach((value) => { binary += String.fromCharCode(value); });
  return btoa(binary);
}

function normalizeDomain(value: string) {
  return value.trim().replace(/^https?:\/\//i, "").split("/")[0].split(":")[0].toLowerCase();
}

function InboundAssistant({ server, token, item, protocolMode, expert, onChange, onError }: { server: RemoteServer; token: string; item: XrayObject; protocolMode: string; expert: boolean; onChange: React.Dispatch<React.SetStateAction<XrayObject>>; onError: (message: string) => void }) {
  const [users, setUsers] = useState<Array<Record<string, unknown>>>([]);
  const [certificates, setCertificates] = useState<Array<Record<string, unknown>>>([]);
  const [nginxDomains, setNginxDomains] = useState<Array<Record<string, unknown>>>([]);
  const [nginxReady, setNginxReady] = useState(false);
  const [selectedUsers, setSelectedUsers] = useState<Set<string>>(new Set());
  const [userSearch, setUserSearch] = useState("");
  const [domains, setDomains] = useState<Array<Record<string, unknown>>>([]);
  const [domainServers, setDomainServers] = useState<Record<string, Record<string, unknown>>>({});
  const [customDomain, setCustomDomain] = useState("");
  const [assistantBusy, setAssistantBusy] = useState("");
  const [protocolEncryption, setProtocolEncryption] = useState<"x25519" | "mlkem768">("mlkem768");
  const [protocolAppearance, setProtocolAppearance] = useState("native");
  const [protocolTicketLifetime, setProtocolTicketLifetime] = useState("600s");
  const [protocolPadding, setProtocolPadding] = useState("100-111-1111.75-0-111.50-0-3333");
  const [sslResults, setSSLResults] = useState<Record<number, "loading" | "success" | "error">>({});
  const [serverDomain, setServerDomain] = useState(server.domain || "");
  const [domainDraft, setDomainDraft] = useState(server.domain || "");

  const protocol = asString(item.protocol).toLowerCase();
  const stream = asObject(item.streamSettings);
  const security = asString(stream.security).toLowerCase();
  const reality = asObject(stream.realitySettings);
  const settings = asObject(item.settings);
  const securityMode = inboundSecurityMode(item);
  const isReality = security === "reality";
  const isTLS = security === "tls";
  const isWSS = inboundTransportMode(item) === "WSS";
  const showOfficialUsers = ["vless", "vmess", "trojan", "shadowsocks", "socks", "http", "hysteria", "anytls", "snell", "mieru"].includes(protocol) && !((protocol === "socks" || protocol === "http") && asString(settings.auth) === "noauth");

  useEffect(() => {
    let active = true;
    void Promise.allSettled([
      fetchXrayUsers(token),
      fetchValidCertificates(token),
      fetchNginxServerDomains(token, server.id),
      fetchXrayServiceStatus(token, server.id),
    ]).then(([userResult, certResult, nginxResult, statusResult]) => {
      if (!active) return;
      if (userResult.status === "fulfilled") setUsers(Array.isArray(userResult.value) ? userResult.value : userResult.value.users || []);
      if (certResult.status === "fulfilled") {
        const available = (certResult.value.certificates || []).filter((cert) => asNumber(cert.remote_server_id) === server.id || (asNumber(cert.remote_server_id) === 0 && Boolean(cert.auto_deploy)));
        setCertificates(available);
        const currentTLS = asObject(asObject(item.streamSettings).tlsSettings);
        if ((isTLS || isWSS) && !item.cert_id && !Array.isArray(currentTLS.certificates) && available[0]) {
          const cert = available.find((entry) => asNumber(entry.remote_server_id) === server.id) || available[0];
          onChange((current) => {
            const currentStream = asObject(current.streamSettings);
            const certificate = { certificateFile: asString(cert.cert_path), keyFile: asString(cert.key_path) };
            return { ...current, cert_id: asNumber(cert.id), streamSettings: { ...currentStream, security: isWSS ? "none" : "tls", tlsSettings: { ...asObject(currentStream.tlsSettings), serverName: server.domain || asString(cert.domain).replace(/^\*\./, ""), ...(isWSS ? {} : { certificates: [certificate] }) } } };
          });
        }
      }
      if (nginxResult.status === "fulfilled") setNginxDomains(nginxResult.value.domains || []);
      if (statusResult.status === "fulfilled") setNginxReady(Boolean(statusResult.value.nginx?.installed && statusResult.value.nginx?.running));
    });
    return () => { active = false; };
  }, [server.id, token]);

  useEffect(() => {
    if (isReality && (!asString(reality.privateKey) || !asString(reality.publicKey))) void generateRealityKeys();
    // Key generation is intentionally tied to entering a Reality mode.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isReality]);

  useEffect(() => {
    if ((!isTLS && !isWSS) || item.cert_id || !certificates.length) return;
    const cert = certificates.find((entry) => asNumber(entry.remote_server_id) === server.id) || certificates[0];
    const certDomain = asString(cert.domain);
    const concreteDomain = serverDomain || (certDomain.startsWith("*.") ? "" : certDomain);
    onChange((current) => {
      const currentStream = asObject(current.streamSettings);
      const certificate = { certificateFile: asString(cert.cert_path), keyFile: asString(cert.key_path) };
      return { ...current, cert_id: asNumber(cert.id), streamSettings: { ...currentStream, security: isWSS ? "none" : "tls", tlsSettings: { ...asObject(currentStream.tlsSettings), ...(concreteDomain ? { serverName: concreteDomain } : {}), ...(isWSS ? {} : { certificates: [certificate] }) } } };
    });
  }, [certificates, isTLS, isWSS, item.cert_id, onChange, server.id, serverDomain]);

  function updateItem(updater: (current: XrayObject) => XrayObject) {
    onChange((current) => updater(current));
    onError("");
  }

  function updateReality(values: XrayObject) {
    updateItem((current) => {
      const currentStream = asObject(current.streamSettings);
      return { ...current, streamSettings: { ...currentStream, security: "reality", realitySettings: { ...asObject(currentStream.realitySettings), ...values } } };
    });
  }

  function selectRealityDomain(domain: string, domainList = domains, serverMap = domainServers) {
    const source = serverMap[domain] || {};
    const ownServer = asNumber(source.server_id) === server.id;
    const result = domainList.find((entry) => asString(entry.domain) === domain) || {};
    const nginxPort = asNumber(result.nginx_ssl_port) || 8001;
    updateReality({ dest: ownServer ? `127.0.0.1:${nginxPort}` : `${domain}:443`, serverNames: [domain], xver: ownServer ? 1 : 0 });
  }

  async function loadRealityDomains() {
    setAssistantBusy("domains");
    onError("");
    try {
      const response = await fetchRealityDomains(token, server.id);
      const next = response.domains || [];
      setDomains(next);
      const serverMap = response.domain_servers || {};
      setDomainServers(serverMap);
      const first = [...next].filter((entry) => Boolean(entry.success)).sort((a, b) => asNumber(a.latency_ms) - asNumber(b.latency_ms))[0];
      if (first) selectRealityDomain(asString(first.domain), next, serverMap);
      if (!first) onError(response.warning || response.message || "没有探测到可用 Reality 域名");
    } catch (error) {
      onError(getError(error, "Reality 域名探测失败"));
    } finally {
      setAssistantBusy("");
    }
  }

  async function probeDomain() {
    const domain = normalizeDomain(customDomain);
    if (!domain) { onError("请输入 Reality 目标域名"); return; }
    if (!window.confirm(`确认保存并从 ${server.name} 探测 Reality 域名 ${domain}？`)) return;
    setAssistantBusy("custom-domain");
    try {
      const response = await probeCustomRealityDomain(token, server.id, domain);
      const entry: Record<string, unknown> = { ...response, domain: asString(response.domain) || domain };
      setDomains((current) => [...current.filter((value) => asString(value.domain) !== asString(entry.domain)), entry].sort((a, b) => asNumber(a.latency_ms) - asNumber(b.latency_ms)));
      if (response.success === false || response.error) throw new Error(asString(response.error) || "该域名不可用");
      selectRealityDomain(asString(entry.domain), [entry, ...domains.filter((value) => asString(value.domain) !== asString(entry.domain))], domainServers);
      setCustomDomain("");
    } catch (error) {
      onError(getError(error, "自定义域名探测失败"));
    } finally {
      setAssistantBusy("");
    }
  }

  async function generateRealityKeys() {
    setAssistantBusy("x25519");
    try {
      const response = await generateXrayX25519(token);
      updateReality({ privateKey: response.privateKey, publicKey: response.publicKey });
    } catch (error) {
      onError(getError(error, "X25519 密钥生成失败"));
    } finally {
      setAssistantBusy("");
    }
  }

  async function generateEncryptionKeys() {
    setAssistantBusy("protocol-keys");
    try {
      const response = await generateXrayProtocolKeys(token, { type: "mlkem768x25519plus", encryptionType: protocolEncryption, appearance: protocolAppearance, ticketLifetime: protocolTicketLifetime, padding: protocolPadding });
      updateItem((current) => ({ ...current, settings: { ...asObject(current.settings), decryption: response.decryptionConfig, encryption: response.encryption } }));
    } catch (error) {
      onError(getError(error, "协议密钥生成失败"));
    } finally {
      setAssistantBusy("");
    }
  }

  function selectCertificate(id: string) {
    const cert = certificates.find((entry) => String(entry.id) === id);
    if (!cert) return;
    updateItem((current) => {
      const currentStream = asObject(current.streamSettings);
      const currentTLS = asObject(currentStream.tlsSettings);
      return {
        ...current,
        cert_id: asNumber(cert.id),
        streamSettings: {
          ...currentStream,
          security: isWSS ? "none" : "tls",
          tlsSettings: {
            ...currentTLS,
            serverName: server.domain || asString(cert.domain).replace(/^\*\./, ""),
            ...(isWSS ? {} : { certificates: [{ certificateFile: asString(cert.cert_path), keyFile: asString(cert.key_path) }] }),
          },
        },
      };
    });
  }

  async function setupSSL() {
    if (!window.confirm(`确认按正式向导为 ${server.name} 配置 Nginx SSL、部署证书并 reload Nginx？`)) return;
    setAssistantBusy("ssl");
    try {
      await setupRemoteSSL(token, server.id);
      const [status, list] = await Promise.all([fetchXrayServiceStatus(token, server.id), fetchNginxServerDomains(token, server.id)]);
      setNginxReady(Boolean(status.nginx?.installed && status.nginx?.running));
      setNginxDomains(list.domains || []);
    } catch (error) {
      onError(getError(error, "Nginx SSL 配置失败"));
    } finally {
      setAssistantBusy("");
    }
  }

  async function setupSourceSSL(sourceServerId: number) {
    const source = Object.values(domainServers).find((value) => asNumber(value.server_id) === sourceServerId) || {};
    const name = asString(source.server_name) || `#${sourceServerId}`;
    if (!window.confirm(`确认按正式向导为来源服务器 ${name} 配置 Nginx SSL、部署证书并 reload Nginx？`)) return;
    setSSLResults((current) => ({ ...current, [sourceServerId]: "loading" }));
    try {
      await setupRemoteSSL(token, sourceServerId);
      setSSLResults((current) => ({ ...current, [sourceServerId]: "success" }));
    } catch (error) {
      setSSLResults((current) => ({ ...current, [sourceServerId]: "error" }));
      onError(getError(error, `${name} 的 SSL 配置失败`));
    }
  }

  async function setupAllSourceSSL() {
    const ids = [...new Set(domains.filter((entry) => !entry.success && domainServers[asString(entry.domain)]).map((entry) => asNumber(domainServers[asString(entry.domain)].server_id)).filter(Boolean))];
    if (!ids.length) return;
    if (!window.confirm(`确认按正式向导为 ${ids.length} 台来源服务器逐台配置 Nginx SSL、部署证书并 reload Nginx？`)) return;
    for (const id of ids) {
      setSSLResults((current) => ({ ...current, [id]: "loading" }));
      try {
        await setupRemoteSSL(token, id);
        setSSLResults((current) => ({ ...current, [id]: "success" }));
      } catch {
        setSSLResults((current) => ({ ...current, [id]: "error" }));
      }
    }
  }

  async function saveServerDomain() {
    const domain = normalizeDomain(domainDraft);
    if (!domain) { onError("请输入服务器域名"); return; }
    if (!window.confirm(`确认把 ${server.name} 的服务器域名修改为 ${domain}？`)) return;
    setAssistantBusy("server-domain");
    try {
      await updateRemoteServerDomain(token, server, domain);
      setServerDomain(domain);
      setDomainDraft(domain);
    } catch (error) {
      onError(getError(error, "服务器域名更新失败"));
    } finally {
      setAssistantBusy("");
    }
  }

  async function copyNginxTemplate() {
    const domain = serverDomain || "<您的域名>";
    const path = asString(asObject(stream.wsSettings).path) || "/ws";
    const template = `server {\n    listen 443 ssl http2;\n    listen [::]:443 ssl http2;\n    server_name ${domain};\n\n    ssl_certificate /usr/local/nginx/cert/${domain}.pem;\n    ssl_certificate_key /usr/local/nginx/cert/${domain}.key;\n    ssl_protocols TLSv1.2 TLSv1.3;\n\n    location = ${path} {\n        if ($http_upgrade != "websocket") { return 404; }\n        proxy_pass http://127.0.0.1:${asNumber(item.port) || 443};\n        proxy_http_version 1.1;\n        proxy_set_header Upgrade $http_upgrade;\n        proxy_set_header Connection "upgrade";\n        proxy_set_header Host $host;\n        proxy_set_header X-Real-IP $remote_addr;\n        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n        proxy_read_timeout 5d;\n    }\n}`;
    try {
      await navigator.clipboard.writeText(template);
    } catch {
      onError("无法写入剪贴板，请通过实时预览检查配置");
    }
  }

  function toggleUser(username: string) {
    setSelectedUsers((current) => {
      const next = new Set(current);
      if (next.has(username)) next.delete(username); else next.add(username);
      return next;
    });
  }

  async function addSelectedUsers() {
    const selected = users.filter((user) => selectedUsers.has(asString(user.username)));
    if (!selected.length) { onError("请至少选择一个用户"); return; }
    setAssistantBusy("users");
    try {
      const normalized: Array<{ username: string; email: string }> = [];
      for (const user of selected) {
        const username = asString(user.username);
        let email = asString(user.email);
        if (!email) {
          email = `mmw@${username}.me`;
          if (!window.confirm(`${username} 没有邮箱。确认按正式向导写入 ${email}？`)) throw new Error("已取消缺失邮箱补全");
          await updateXrayUserEmail(token, username, email);
        }
        normalized.push({ username, email });
      }
      updateItem((current) => {
        const currentSettings = asObject(current.settings);
        if (protocol === "socks" || protocol === "http") return { ...current, settings: { ...currentSettings, auth: "password", accounts: normalized.map((user) => ({ user: user.username, pass: randomPassword() })) } };
        if (protocol === "trojan") return { ...current, settings: { ...currentSettings, clients: normalized.map((user) => ({ password: randomPassword(), email: user.email })) } };
        if (protocol === "shadowsocks") return { ...current, settings: { ...currentSettings, clients: normalized.map((user) => protocolMode === "shadowsocks2022" ? { password: randomBase64(asString(currentSettings.method).includes("128") ? 16 : 32), email: user.email, level: 0 } : { method: asString(currentSettings.method) || "aes-256-gcm", password: randomPassword(), email: user.email, level: 0 }) } };
        if (protocol === "hysteria") return { ...current, settings: { ...currentSettings, clients: normalized.map((user) => ({ auth: randomPassword(), email: user.email })) } };
        if (protocol === "anytls") return { ...current, settings: { ...currentSettings, users: normalized.map((user) => ({ password: randomPassword(), email: user.email, level: 0 })) } };
        if (protocol === "snell") return { ...current, settings: { ...currentSettings, users: normalized.map((user) => ({ psk: randomPassword(), email: user.email, level: 0, ...(asNumber(current._wizard_snell_version) === 6 ? { clientId: crypto.randomUUID().replace(/-/g, "").slice(0, 12) } : {}) })) } };
        if (protocol === "mieru") return { ...current, settings: { ...currentSettings, users: normalized.map((user) => ({ username: user.username, password: randomPassword(), email: user.email, level: 0 })) } };
        return { ...current, settings: { ...currentSettings, clients: normalized.map((user) => ({ id: crypto.randomUUID(), email: user.email, level: 0, ...(protocol === "vless" && securityMode.includes("Vision") ? { flow: "xtls-rprx-vision" } : {}) })) } };
      });
      setSelectedUsers(new Set());
    } catch (error) {
      onError(getError(error, "添加用户失败"));
    } finally {
      setAssistantBusy("");
    }
  }

  const filteredUsers = users.filter((user) => `${asString(user.username)} ${asString(user.email)}`.toLowerCase().includes(userSearch.toLowerCase()));
  const domainConflict = Boolean(serverDomain && nginxDomains.some((entry) => asString(entry.domain).toLowerCase() === serverDomain.toLowerCase()));
  const failedSources = [...new Map(domains.filter((entry) => !entry.success && domainServers[asString(entry.domain)]).map((entry) => { const source = domainServers[asString(entry.domain)]; return [asNumber(source.server_id), source] as const; })).values()].filter((source) => asNumber(source.server_id));

  return <div className="xray-assistant wide">
    {showOfficialUsers && <section><div className="xray-assistant-title"><div><KeyRound /><strong>用户与凭据</strong></div><span>{users.length} 个正式用户</span></div><div className="xray-assistant-search"><Search /><input value={userSearch} onChange={(event) => setUserSearch(event.target.value)} placeholder="搜索用户名或邮箱" /></div><div className="xray-user-picker">{filteredUsers.map((user) => { const username = asString(user.username); return <label key={username}><input type="checkbox" checked={selectedUsers.has(username)} onChange={() => toggleUser(username)} /><span><strong>{username}</strong><small>{asString(user.email) || "无邮箱，选择后可按正式规则补全"}</small></span></label>; })}</div><button type="button" disabled={!selectedUsers.size || Boolean(assistantBusy)} onClick={() => void addSelectedUsers()}>添加所选用户</button></section>}

    {isReality && <section><div className="xray-assistant-title"><div><ShieldCheck /><strong>Reality</strong></div><button type="button" disabled={Boolean(assistantBusy)} onClick={() => void loadRealityDomains()}>{assistantBusy === "domains" ? "探测中..." : "自动探测"}</button></div>{domains.length > 0 && <label><span>低延迟目标域名</span><select value={Array.isArray(reality.serverNames) ? asString(reality.serverNames[0]) : asString(reality.serverNames)} onChange={(event) => selectRealityDomain(event.target.value)}>{domains.map((entry) => <option key={asString(entry.domain)} value={asString(entry.domain)} disabled={!entry.success}>{asString(entry.domain)} · {entry.success ? `${asString(entry.latency_ms) || "-"}ms` : asString(entry.error) || "不可用"}</option>)}</select></label>}{failedSources.length > 0 && <div className="xray-failed-sources"><div><strong>来源服务器 SSL 未就绪</strong><button type="button" disabled={Object.values(sslResults).includes("loading")} onClick={() => void setupAllSourceSSL()}>一键配置</button></div>{failedSources.map((source) => { const id = asNumber(source.server_id); const state = sslResults[id]; return <div key={id}><span><strong>{asString(source.server_name) || `服务器 #${id}`}</strong><small>{asString(source.domain)}</small></span><button type="button" disabled={state === "loading"} onClick={() => void setupSourceSSL(id)}>{state === "loading" ? "配置中..." : state === "success" ? "已完成" : state === "error" ? "重试" : "配置 SSL"}</button></div>; })}</div>}<div className="xray-assistant-inline"><input value={customDomain} onChange={(event) => setCustomDomain(event.target.value)} placeholder="自定义 Reality 域名" /><button type="button" disabled={!customDomain.trim() || Boolean(assistantBusy)} onClick={() => void probeDomain()}>探测</button></div><div className="xray-key-grid"><label><span>目标地址（Dest）</span><input value={asString(reality.dest)} onChange={(event) => updateReality({ dest: event.target.value })} /></label><label><span>服务器名称</span><input value={Array.isArray(reality.serverNames) ? (reality.serverNames as unknown[]).join(",") : asString(reality.serverNames)} onChange={(event) => updateReality({ serverNames: event.target.value.split(",").map((value) => value.trim()).filter(Boolean) })} /></label><label><span>私钥</span><input value={asString(reality.privateKey)} onChange={(event) => updateReality({ privateKey: event.target.value })} /></label><label><span>公钥</span><input value={asString(reality.publicKey)} onChange={(event) => updateReality({ publicKey: event.target.value })} /></label><label><span>短 ID</span><input value={Array.isArray(reality.shortIds) ? (reality.shortIds as unknown[]).join(",") : asString(reality.shortIds)} onChange={(event) => updateReality({ shortIds: event.target.value.split(",").map((value) => value.trim()) })} /></label></div><label className="xray-assistant-toggle"><span><strong>防 Reality 转发滥用</strong><small>保存时由主控创建辅助入站和路由，并重启 Xray；失败自动回滚。</small></span><input type="checkbox" checked={Boolean(item.reality_guard)} onChange={(event) => updateItem((current) => ({ ...current, reality_guard: event.target.checked }))} /></label><button type="button" disabled={Boolean(assistantBusy)} onClick={() => void generateRealityKeys()}><KeyRound />{assistantBusy === "x25519" ? "生成中..." : "重新生成 X25519"}</button></section>}

    {protocol === "vless" && securityMode === "Encryption" && <section><div className="xray-assistant-title"><div><KeyRound /><strong>VLESS 加密</strong></div><span>后量子加密</span></div><div className="xray-key-grid"><label><span>加密类型</span><select value={protocolEncryption} onChange={(event) => setProtocolEncryption(event.target.value as "x25519" | "mlkem768")}><option value="mlkem768">ML-KEM-768</option><option value="x25519">X25519</option></select></label><label><span>外观模式</span><select value={protocolAppearance} onChange={(event) => setProtocolAppearance(event.target.value)}><option value="native">原生</option><option value="xorpub">异或公钥</option><option value="random">随机</option></select></label><label><span>票据有效期</span><select value={protocolTicketLifetime} onChange={(event) => setProtocolTicketLifetime(event.target.value)}><option value="0s">0 秒</option><option value="300-600s">300-600 秒</option><option value="600s">600 秒</option></select></label><label><span>填充</span><input value={protocolPadding} onChange={(event) => setProtocolPadding(event.target.value)} /></label></div><button type="button" disabled={Boolean(assistantBusy)} onClick={() => void generateEncryptionKeys()}>{assistantBusy === "protocol-keys" ? "生成中..." : "生成协议密钥"}</button><p className="xray-assistant-note">服务端解密配置与客户端加密配置会同时写入当前配置预览。</p></section>}

    {(expert || isWSS) && (isTLS || isWSS) && <section><div className="xray-assistant-title"><div><ShieldCheck /><strong>{isWSS ? "WSS 环境与证书" : "证书与 Nginx SSL"}</strong></div><span>{nginxReady ? "Nginx 运行中" : "Nginx 未运行"}</span></div>{isWSS && <p className={nginxReady && certificates.length ? "xray-assistant-note" : "xray-inline-warning"}>WSS 由 Nginx 在 443 端口处理 TLS，Xray 入站仅监听本机 WebSocket。保存时后端会自动分配本地端口和路径。</p>}<div className="xray-assistant-inline"><input value={domainDraft} onChange={(event) => setDomainDraft(event.target.value)} placeholder="服务器域名" /><button type="button" disabled={Boolean(assistantBusy) || normalizeDomain(domainDraft) === serverDomain} onClick={() => void saveServerDomain()}>{assistantBusy === "server-domain" ? "更新中..." : "更新域名"}</button></div><label><span>托管证书</span><select value={asString(item.cert_id)} onChange={(event) => selectCertificate(event.target.value)}><option value="">选择证书...</option>{certificates.map((cert) => <option key={asString(cert.id)} value={asString(cert.id)}>{asString(cert.domain)} · {asString(cert.remote_server_name)}</option>)}</select></label><div className="xray-cert-status"><span>服务器域名：{serverDomain || "未配置"}</span><span>证书：{certificates.length}</span><span>同名 Nginx 配置：{domainConflict ? "存在" : "无"}</span></div><div className="xray-assistant-inline"><button type="button" onClick={() => void copyNginxTemplate()}>复制手工 Nginx 模板</button><button type="button" className="danger" disabled={Boolean(assistantBusy) || !serverDomain} onClick={() => void setupSSL()}>{assistantBusy === "ssl" ? "配置中..." : "按正式流程配置 SSL"}</button></div></section>}

    {expert && <section><div className="xray-assistant-title"><div><strong>中转</strong></div><span>可选</span></div><div className="xray-assistant-inline relay"><input value={asString(item.relay_server)} onChange={(event) => updateItem((current) => ({ ...current, relay_server: event.target.value || undefined }))} placeholder="中转服务器" /><input type="number" value={asString(item.relay_port)} onChange={(event) => updateItem((current) => ({ ...current, relay_port: Number(event.target.value) || undefined }))} placeholder={asString(item.port) || "中转端口"} /></div><p className="xray-assistant-note">主控会在下发 Agent 前剥离这两个字段，仅用于生成节点的中转地址。</p></section>}
  </div>;
}
