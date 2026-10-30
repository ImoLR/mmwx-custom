import type { XrayObject } from "./types";

// Inbound port check against the machine's listening ports reported by the
// Helper (GET /api/custom/servers/{id}/listening-ports, Helper v0.7.14+).

export type ListeningPort = { port: number; protocol: "tcp" | "udp"; address?: string; pid?: number; process?: string; owner?: "core" | "agent"; inbound_tag?: string };
export type ListeningPortsResponse = {
  success?: boolean;
  available: boolean;
  supported: boolean;
  helper_version?: string;
  listening?: { scanned_at?: string; ports: ListeningPort[]; truncated?: boolean; error?: string } | null;
};
export type PortCheck = { kind: "free" | "unavailable" | "inbound" | "machine"; blocking: boolean; message: string };

export const listeningUnavailableMessage = "无法检测本机端口占用（Helper 未上报/需升级）";
export const listeningUnreachable: ListeningPortsResponse = { available: false, supported: false };

function object(value: unknown): XrayObject {
  return value && typeof value === "object" && !Array.isArray(value) ? value as XrayObject : {};
}

function lower(value: unknown) {
  return typeof value === "string" ? value.trim().toLowerCase() : "";
}

function networks(value: unknown, fallback: string) {
  const list = lower(value) || fallback;
  return { tcp: list.includes("tcp"), udp: list.includes("udp") };
}

// inboundListenProtocols says which socket kinds an inbound binds on its port.
// Shadowsocks (tcp,udp unless narrowed), Mieru and Snell serve both on one
// port, so either kind conflicts; QUIC-like transports are UDP only.
export function inboundListenProtocols(item: XrayObject) {
  const protocol = lower(item.protocol);
  const settings = object(item.settings);
  const network = lower(object(item.streamSettings).network);
  if (protocol === "shadowsocks") return networks(settings.network, "tcp,udp");
  if (protocol === "mieru" || protocol === "snell") return { tcp: true, udp: true };
  if (protocol === "wireguard" || protocol === "hysteria" || ["kcp", "mkcp", "hysteria", "quic"].includes(network)) return { tcp: false, udp: true };
  if (protocol === "dokodemo-door" || protocol === "tunnel") return networks(settings.network, "tcp");
  if (protocol === "socks") return { tcp: true, udp: settings.udp === true };
  return { tcp: true, udp: false };
}

// A unix socket (path or @abstract) binds no port.
export function inboundBindsPort(item: XrayObject) {
  const listen = String(item.listen ?? "").trim();
  return !listen.startsWith("/") && !listen.startsWith("@");
}

function wildcard(address: string) {
  return !address || address === "*" || address === "0.0.0.0" || address === "::" || address === "[::]";
}

// Two binds collide unless both name different concrete addresses.
function addressesCollide(listen: unknown, address: string | undefined) {
  const own = String(listen ?? "").trim().toLowerCase().replace(/^\[|\]$/g, "");
  const other = String(address ?? "").trim().toLowerCase();
  return wildcard(own) || wildcard(other) || own === other;
}

function usable(report: ListeningPortsResponse | null | undefined) {
  return report?.available && report.supported && report.listening && !report.listening.error ? report.listening : null;
}

function holderName(entry: ListeningPort) {
  if (!entry.pid) return "";
  return `${entry.process || "未知进程"} (PID ${entry.pid})`;
}

// checkInboundPort judges the inbound's port. usedPorts are the ports of the
// server's inbounds in the current config (the edited one included);
// originalPort is the edited inbound's saved port (0 when creating). Every
// conflict blocks saving; only a missing or unusable Helper report does not.
export function checkInboundPort(report: ListeningPortsResponse | null | undefined, item: XrayObject, options: { originalPort: number; usedPorts: number[] }): PortCheck {
  const port = Number(item.port);
  if (!Number.isInteger(port) || port < 1 || port > 65535 || !inboundBindsPort(item)) return { kind: "free", blocking: false, message: "" };
  const unchanged = options.originalPort > 0 && port === options.originalPort;
  if (!unchanged && options.usedPorts.includes(port)) return { kind: "inbound", blocking: true, message: `端口 ${port} 已被其他入站占用` };
  const listening = usable(report);
  if (!listening) return { kind: "unavailable", blocking: false, message: listeningUnavailableMessage };
  const want = inboundListenProtocols(item);
  const holders = listening.ports.filter((entry) => entry.port === port && (entry.protocol === "udp" ? want.udp : want.tcp) && addressesCollide(item.listen, entry.address));
  // On the unchanged port the official Core/Agent holder is this inbound itself.
  const others = unchanged ? holders.filter((entry) => !entry.owner) : holders;
  const inbound = others.find((entry) => entry.owner && entry.inbound_tag);
  if (inbound) return { kind: "inbound", blocking: true, message: `端口 ${port} 已被其他入站（${inbound.inbound_tag}）占用` };
  if (!others.length) return { kind: "free", blocking: false, message: "" };
  const names = Array.from(new Set(others.map(holderName).filter(Boolean)));
  const message = names.length
    ? `端口 ${port} 已被本机进程 ${names.join("、")} 占用`
    : `端口 ${port} 已被本机占用（系统内核或已退出的进程）`;
  return { kind: "machine", blocking: true, message };
}

// unavailablePorts are ports the 随机 button must skip: other inbounds and
// every port something on the machine listens on (except the inbound's own).
export function unavailablePorts(report: ListeningPortsResponse | null | undefined, usedPorts: number[], originalPort: number) {
  const ports = new Set(usedPorts.filter((port) => port !== originalPort));
  for (const entry of usable(report)?.ports || []) {
    if (entry.port !== originalPort || !entry.owner) ports.add(entry.port);
  }
  return ports;
}

// 随机 prefers ports below the Linux ephemeral range (32768–60999): a port
// picked there can later be held by an outbound connection and keep Xray from
// binding. The preference is not a wall: when it is full the picker falls back
// to 61000–65535, then to any port from 1024 outside the ephemeral range.
export const RANDOM_PORT_START = 10000;
export const RANDOM_PORT_END = 32767;
export const FALLBACK_PORT_START = 61000;
// The Helper does not report the machine's ip_local_port_range yet, so the
// Linux default stands in for it.
export const LINUX_EPHEMERAL_RANGE: PortRange = [32768, 60999];
export const noFreePortMessage = "没有可用的空闲端口（10000–32767、61000–65535 和 1024 以上的其余端口都已被占用）";

export type PortRange = [number, number];

// portStages are the ranges in the order they are tried; the last one skips
// the ephemeral range.
function portStages(ephemeral: PortRange): PortRange[][] {
  const [low, high] = ephemeral;
  return [[[RANDOM_PORT_START, RANDOM_PORT_END]], [[FALLBACK_PORT_START, 65535]], [[1024, low - 1], [high + 1, 65535]].filter(([start, end]) => start <= end) as PortRange[]];
}

function freeIn(ranges: PortRange[], unavailable: Set<number>) {
  const ports: number[] = [];
  for (const [start, end] of ranges) for (let port = start; port <= end; port++) if (!unavailable.has(port)) ports.push(port);
  return ports;
}

// pickAvailablePort draws a random free port from the first stage that has
// one; null when every stage is full.
export function pickAvailablePort(unavailable: Set<number>, random = Math.random, ephemeral = LINUX_EPHEMERAL_RANGE): number | null {
  for (const ranges of portStages(ephemeral)) {
    const candidates = freeIn(ranges, unavailable);
    if (candidates.length) return candidates[Math.floor(random() * candidates.length)];
  }
  return null;
}

// nextFreePort is the port 「换成」 offers for a held port: the next free one
// upward from it inside its stage (wrapping round), the preferred stage first,
// then the fallback stages; null when every stage is full.
export function nextFreePort(unavailable: Set<number>, from: number, ephemeral = LINUX_EPHEMERAL_RANGE): number | null {
  for (const ranges of portStages(ephemeral)) {
    const candidates = freeIn(ranges, unavailable);
    if (candidates.length) return candidates.find((port) => port > from) ?? candidates[0];
  }
  return null;
}

// suggestFreePort pairs nextFreePort with the same unavailable set 随机 uses.
export function suggestFreePort(report: ListeningPortsResponse | null | undefined, usedPorts: number[], originalPort: number, from: number) {
  return nextFreePort(unavailablePorts(report, usedPorts, originalPort), from);
}

// portBlockedMessage adds the free port to offer (or says none is left).
export function portBlockedMessage(message: string, free: number | null) {
  return free ? `${message}，可换成 ${free}` : `${message}；${noFreePortMessage}`;
}
