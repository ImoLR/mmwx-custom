import type {
  AdminTrafficResponse,
  AgentSyncNodesResponse,
  AgentVersionInfo,
  ConnectionMetricsResponse,
  GeoLookupResponse,
  HelperInstallTokenResponse,
  LoginResponse,
  NodeMutationRequest,
  NodeMutationResponse,
  NodeConnectionsResponse,
  NodeParseResponse,
  NodeRelatedInboundsResponse,
  NodeTagsResponse,
  NodeTCPingResponse,
  NodeTempSubscriptionResponse,
  SpeedTestResultsResponse,
  SpeedTestRunResponse,
  SpeedTestersResponse,
  ExternalSyncResponse,
  ForwardCertificatesResponse,
  ForwardChainsResponse,
  ForwardGroup,
  ForwardGroupsResponse,
  ForwardMutationResponse,
  ForwardNodesResponse,
  ForwardProbeResponse,
  ForwardServersResponse,
  NodeTunnel,
  NodeTunnelChain,
  NodeURIItem,
  UserConfigResponse,
  SystemMetrics,
  NodeTotalsResponse,
  NodeTrafficItem,
  NodeURIResponse,
  PeriodUserTrafficItem,
  DNSProvidersResponse,
  MasterUrlResponse,
  CarpoolPublishRequest,
  PackageForwardChainsResponse,
  PackageMutationResponse,
  PackagePayload,
  PackagesResponse,
  PackageTemplatesResponse,
  RemoteServerCreateRequest,
  RemoteServerMutationResponse,
  RemoteServer,
  RemoteServersResponse,
  RemoteSystemInfo,
  RemoteWebsitesResponse,
  Session,
  SharedServerAddRequest,
  SharedServerAddResponse,
  TrafficPeriodResponse,
  TrafficRange,
  TrafficSummary,
  UserConnectionsResponse,
  UserSpeedsResponse,
  UsersTrafficResponse,
  XrayConfigResponse,
  XrayInboundsResponse,
  XrayNode,
  XrayNodesResponse,
  XrayObject,
  XrayOutboundsResponse,
  XrayRoutingResponse,
  XrayServerNICsResponse,
  XrayServiceStatusResponse,
  XrayRecoveryStatusResponse,
  XraySnapshotItem,
  XraySnapshotsResponse,
  XraySystemConfig,
  XraySystemConfigResponse,
  XrayWarpStatus,
  WebsiteMutationResponse,
} from "./types";
import nacl from "tweetnacl";

const SESSION_KEY = "mmwx-session";
const MMWX_API_BASE_URL = normalizeBaseUrl(import.meta.env.VITE_MMWX_API_BASE_URL ?? "");
const MMWX_CUSTOM_API_BASE_URL = normalizeBaseUrl(import.meta.env.VITE_MMWX_CUSTOM_API_BASE_URL ?? "");
const MMWX_SECURE_AUDIENCE = normalizeBaseUrl(import.meta.env.VITE_MMWX_SECURE_AUDIENCE ?? "https://mmwx.imgamer.top");
const SECURE_CHANNEL_VERSION = "v1";
const SECURE_CHANNEL_PROTO = "v2";
const SECURE_ENVELOPE_VERSION = 0x01;
const SECURE_CHANNEL_WASM_URL = "/assets/securechan-CEps0XQO.wasm";
const SECURE_CHANNEL_BUFFER_LIMIT = 1024 * 1024;
const SECURE_CHANNEL_RUNTIME_PROOF_PREFIX = "mmwx-runtime-proof-v1\n";
const SECURE_CHANNEL_RUNTIME_PUBLIC_KEY_B64 = "BAusWULXB7lQxBbQUByyXi4Eg5NBVW/UTpaYNpSDFQQ=";

function normalizeBaseUrl(value: string) {
  return value.trim().replace(/\/+$/, "");
}

function joinUrl(baseUrl: string, path: string) {
  return baseUrl ? `${baseUrl}${path}` : path;
}

export function loadSession(): Session | null {
  const raw = localStorage.getItem(SESSION_KEY);
  if (!raw) return null;
  try {
    const session = JSON.parse(raw) as Session;
    if (!session.token || new Date(session.expiresAt).getTime() <= Date.now()) {
      localStorage.removeItem(SESSION_KEY);
      return null;
    }
    return session;
  } catch {
    localStorage.removeItem(SESSION_KEY);
    return null;
  }
}

export function saveSession(response: LoginResponse): Session {
  const session: Session = {
    token: response.token,
    username: response.username,
    nickname: response.nickname,
    avatarUrl: response.avatar_url,
    role: response.role,
    isAdmin: response.is_admin,
    expiresAt: response.expires_at,
  };
  localStorage.setItem(SESSION_KEY, JSON.stringify(session));
  return session;
}

export function clearSession() {
  localStorage.removeItem(SESSION_KEY);
}

async function request<T>(path: string, token?: string, init?: RequestInit): Promise<T> {
  const channel = await activeSecureChannel;
  if (channel) return requestWithSecureChannel<T>(path, token, init, channel, false);

  const headers = new Headers(init?.headers);
  if (token) headers.set("MM-Authorization", token);
  if (init?.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  const response = await fetch(path, { ...init, headers });
  if (await secureChannelRequired(response.clone())) {
    const nextChannel = await getSecureChannel(path);
    return requestWithSecureChannel<T>(path, token, init, nextChannel, true);
  }
  if (!response.ok) {
    let message = `请求失败 (${response.status})`;
    try {
      const body = (await response.json()) as { error?: string; message?: string };
      message = body.error || body.message || message;
    } catch {
      // Keep the status-based message.
    }
    throw new Error(message);
  }
  return (await response.json()) as T;
}

type SecureChannel = {
  sessionId: string;
  sendSeq: bigint;
  recvMaxSeq: bigint;
  recvBitmap: bigint;
};

// v0.5.3 keeps key generation, derivation and AEAD inside the official secure-channel module.
type SecureChannelWasm = {
  memory: WebAssembly.Memory;
  a: () => number;
  b: () => number;
  c: (infoLength: number, clientMode: number) => number;
  d: (inputLength: number, sequenceHigh: number, sequenceLow: number) => number;
  e: (inputLength: number, sequenceHigh: number, sequenceLow: number) => number;
};

let activeSecureChannel: Promise<SecureChannel | null> | null = null;
let secureChannelWasm: Promise<SecureChannelWasm> | null = null;

async function requestWithSecureChannel<T>(path: string, token: string | undefined, init: RequestInit | undefined, channel: SecureChannel, allowRetry: boolean): Promise<T> {
  const headers = new Headers(init?.headers);
  headers.set("X-Secure-Channel", SECURE_CHANNEL_VERSION);
  headers.set("X-Session-Id", channel.sessionId);
  if (token) headers.set("MM-Authorization", token);

  const method = (init?.method ?? "GET").toUpperCase();
  const hasEncryptedBody = ["POST", "PUT", "PATCH"].includes(method);
  let body = init?.body;
  if (hasEncryptedBody) {
    const plain = typeof body === "string" ? body : body == null ? "" : String(body);
    body = bytesToBase64(await encryptEnvelope(channel, textToBytes(plain)));
    headers.set("Content-Type", "text/plain; charset=utf-8");
  } else if (body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }

  const response = await fetch(path, { ...init, method, headers, body });
  if (response.status === 412 && response.headers.get("X-Secure-Channel-Expired") === "1" && allowRetry) {
    activeSecureChannel = null;
    const nextChannel = await getSecureChannel(path);
    return requestWithSecureChannel<T>(path, token, init, nextChannel, false);
  }
  const plaintext = response.headers.get("X-Secure-Channel") === SECURE_CHANNEL_VERSION
    ? bytesToText(await decryptEnvelope(channel, base64ToBytes((await response.text()).trim())))
    : await response.text();

  let parsed: unknown = null;
  if (plaintext.trim()) {
    try {
      parsed = JSON.parse(plaintext);
    } catch {
      parsed = { error: plaintext };
    }
  }

  if (!response.ok) {
    const bodyObject = parsed && typeof parsed === "object" ? parsed as { error?: string; message?: string } : {};
    throw new Error(bodyObject.error || bodyObject.message || `请求失败 (${response.status})`);
  }
  return parsed as T;
}

async function secureChannelRequired(response: Response) {
  if (response.status !== 403) return false;
  try {
    const body = await response.json() as { code?: string; error?: string };
    return body.code === "SECURE_CHANNEL_REQUIRED" || body.error === "secure channel required";
  } catch {
    return false;
  }
}

async function getSecureChannel(path: string) {
  if (!activeSecureChannel) activeSecureChannel = createSecureChannel(path).catch((error) => {
    activeSecureChannel = null;
    throw error;
  });
  const channel = await activeSecureChannel;
  if (!channel) throw new Error("secure channel unavailable");
  return channel;
}

async function createSecureChannel(path: string): Promise<SecureChannel> {
  const wasm = await getSecureChannelWasm();
  const pointer = wasm.a();
  const memory = secureChannelMemory(wasm);
  memory.set(crypto.getRandomValues(new Uint8Array(32)), pointer);
  if (wasm.b() !== 0) throw new Error("secure channel key generation failed");
  const publicKey = secureChannelMemory(wasm).slice(pointer, pointer + 32);
  const handshakeUrl = new URL(joinUrl(apiBaseFromPath(path), "/api/securechan/handshake"), window.location.origin);
  const audience = secureAudienceFromPath(path);
  const response = await fetch(handshakeUrl.toString(), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ client_pub_b64: bytesToBase64(publicKey), audience, proto: SECURE_CHANNEL_PROTO }),
  });
  if (!response.ok) throw new Error(`secure channel handshake failed (${response.status})`);
  const body = await response.json() as { proto?: string; session_id?: string; server_pub_b64?: string; runtime_proof?: string };
  if (!body.session_id || !body.server_pub_b64 || !body.runtime_proof) throw new Error("secure channel handshake response invalid");
  const serverPublicKey = base64ToBytes(body.server_pub_b64);
  if (body.proto !== SECURE_CHANNEL_PROTO || serverPublicKey.length !== 32) {
    throw new Error("secure channel protocol mismatch");
  }
  const proofMessage = textToBytes(`${SECURE_CHANNEL_RUNTIME_PROOF_PREFIX}${body.session_id}\n${bytesToBase64(publicKey)}\n${body.server_pub_b64}\n${audience}`);
  const proofValid = nacl.sign.detached.verify(
    proofMessage,
    base64ToBytes(body.runtime_proof),
    base64ToBytes(SECURE_CHANNEL_RUNTIME_PUBLIC_KEY_B64),
  );
  if (!proofValid) throw new Error("secure channel runtime proof invalid");
  const info = textToBytes(`securechan-v2\n${body.session_id}`);
  if (32 + info.length > SECURE_CHANNEL_BUFFER_LIMIT) throw new Error("secure channel handshake response too large");
  const nextMemory = secureChannelMemory(wasm);
  nextMemory.set(serverPublicKey, pointer);
  nextMemory.set(info, pointer + 32);
  if (wasm.c(info.length, 1) !== 0) throw new Error("secure channel key derivation failed");
  return {
    sessionId: body.session_id,
    sendSeq: 0n,
    recvMaxSeq: 0n,
    recvBitmap: 0n,
  };
}

async function getSecureChannelWasm() {
  if (!secureChannelWasm) {
    secureChannelWasm = (async () => {
      const response = await fetch(SECURE_CHANNEL_WASM_URL);
      if (!response.ok) throw new Error(`secure channel module unavailable (${response.status})`);
      const { instance } = await WebAssembly.instantiate(await response.arrayBuffer(), {});
      const wasm = instance.exports as unknown as SecureChannelWasm;
      if (!wasm.memory || !wasm.a || !wasm.b || !wasm.c || !wasm.d || !wasm.e) {
        throw new Error("secure channel module invalid");
      }
      const pointer = wasm.a();
      if (pointer + SECURE_CHANNEL_BUFFER_LIMIT > wasm.memory.buffer.byteLength) {
        throw new Error("secure channel module memory is too small");
      }
      return wasm;
    })().catch((error) => {
      secureChannelWasm = null;
      throw error;
    });
  }
  return secureChannelWasm;
}

function secureChannelMemory(wasm: SecureChannelWasm) {
  return new Uint8Array(wasm.memory.buffer);
}

function secureAudienceFromPath(path: string) {
  try {
    const url = new URL(path, window.location.origin);
    if (url.origin !== window.location.origin) return url.origin;
  } catch {
    // Fall through to configured deployment audience.
  }
  return MMWX_SECURE_AUDIENCE || window.location.origin;
}

function apiBaseFromPath(path: string) {
  try {
    const url = new URL(path, window.location.origin);
    const origin = url.origin === window.location.origin ? "" : url.origin;
    return origin;
  } catch {
    return "";
  }
}

async function encryptEnvelope(channel: SecureChannel, plaintext: Uint8Array) {
  if (plaintext.length > SECURE_CHANNEL_BUFFER_LIMIT) throw new Error("secure channel request is too large");
  const wasm = await getSecureChannelWasm();
  const pointer = wasm.a();
  secureChannelMemory(wasm).set(plaintext, pointer);
  channel.sendSeq += 1n;
  const seq = channel.sendSeq;
  const [sequenceHigh, sequenceLow] = splitUint64(seq);
  const outputLength = wasm.d(plaintext.length, sequenceHigh, sequenceLow);
  if (outputLength < 0 || outputLength > SECURE_CHANNEL_BUFFER_LIMIT) throw new Error("secure channel encryption failed");
  const ciphertext = secureChannelMemory(wasm).slice(pointer, pointer + outputLength);
  const envelope = new Uint8Array(9 + ciphertext.length);
  envelope[0] = SECURE_ENVELOPE_VERSION;
  writeUint64BE(envelope, 1, seq);
  envelope.set(ciphertext, 9);
  return envelope;
}

async function decryptEnvelope(channel: SecureChannel, envelope: Uint8Array) {
  if (envelope.length < 25 || envelope[0] !== SECURE_ENVELOPE_VERSION) throw new Error("secure channel response invalid");
  const seq = readUint64BE(envelope, 1);
  if (!rememberSecureSequence(channel, seq)) throw new Error("secure channel response replayed");
  const ciphertext = envelope.slice(9);
  if (ciphertext.length > SECURE_CHANNEL_BUFFER_LIMIT) throw new Error("secure channel response is too large");
  const wasm = await getSecureChannelWasm();
  const pointer = wasm.a();
  secureChannelMemory(wasm).set(ciphertext, pointer);
  const [sequenceHigh, sequenceLow] = splitUint64(seq);
  const outputLength = wasm.e(ciphertext.length, sequenceHigh, sequenceLow);
  if (outputLength < 0 || outputLength > SECURE_CHANNEL_BUFFER_LIMIT) throw new Error("secure channel decryption failed");
  return secureChannelMemory(wasm).slice(pointer, pointer + outputLength);
}

function rememberSecureSequence(channel: SecureChannel, sequence: bigint) {
  if (sequence <= 0n) return false;
  if (sequence > channel.recvMaxSeq) {
    const shift = sequence - channel.recvMaxSeq;
    channel.recvBitmap = shift >= 64n ? 0n : (channel.recvBitmap << shift) & ((1n << 64n) - 1n);
    channel.recvMaxSeq = sequence;
    channel.recvBitmap |= 1n;
    return true;
  }
  const delta = channel.recvMaxSeq - sequence;
  if (delta >= 64n) return false;
  const bit = 1n << delta;
  if ((channel.recvBitmap & bit) !== 0n) return false;
  channel.recvBitmap |= bit;
  return true;
}

function splitUint64(value: bigint): [number, number] {
  return [Number((value >> 32n) & 0xffffffffn), Number(value & 0xffffffffn)];
}

function writeUint64BE(bytes: Uint8Array, offset: number, value: bigint) {
  let current = value;
  for (let index = 7; index >= 0; index -= 1) {
    bytes[offset + index] = Number(current & 255n);
    current >>= 8n;
  }
}

function readUint64BE(bytes: Uint8Array, offset: number) {
  let value = 0n;
  for (let index = 0; index < 8; index += 1) value = (value << 8n) + BigInt(bytes[offset + index]);
  return value;
}

function textToBytes(value: string) {
  return new TextEncoder().encode(value);
}

function bytesToText(value: Uint8Array) {
  return new TextDecoder().decode(value);
}

function bytesToBase64(bytes: Uint8Array) {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function base64ToBytes(value: string) {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index);
  return bytes;
}

async function requestCustomApi<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  const response = await fetch(path, { ...init, headers });
  if (!response.ok) {
    let message = `请求失败 (${response.status})`;
    try {
      const body = (await response.json()) as { error?: string; message?: string };
      message = body.error || body.message || message;
    } catch {
      // Keep the status-based message.
    }
    throw new Error(message);
  }
  return (await response.json()) as T;
}

function requestOperation<T>(token: string, operation: string, payload: unknown = null) {
  return request<T>(joinUrl(MMWX_API_BASE_URL, "/api/v3"), token, {
    method: "POST",
    body: JSON.stringify({ op: operation, payload }),
  });
}

function operationWithParams(hash: string, params: unknown[], suffix = "") {
  const encoded = bytesToBase64(textToBytes(JSON.stringify([params, suffix])))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
  return `${hash}!${encoded}`;
}

export async function login(username: string, password: string, rememberMe: boolean) {
  return request<LoginResponse>(joinUrl(MMWX_API_BASE_URL, "/api/login"), undefined, {
    method: "POST",
    body: JSON.stringify({
      username,
      password,
      remember_me: rememberMe,
      turnstile_token: "",
    }),
  });
}

export function fetchPackages(token: string) {
  return requestOperation<PackagesResponse>(token, "97e3e31737510104");
}

export function createPackage(token: string, body: PackagePayload) {
  return requestOperation<PackageMutationResponse>(token, "e917e65c1964a8e3", body);
}

export function updatePackage(token: string, body: PackagePayload & { id: number }) {
  return requestOperation<PackageMutationResponse>(token, "41cc1a7b5ae7df46", body);
}

export function deletePackage(token: string, packageId: number) {
  return requestOperation<PackageMutationResponse>(
    token,
    operationWithParams("d73b8428778bd126", [packageId]),
    { id: packageId },
  );
}

export function fetchPackageTemplates(token: string) {
  return requestOperation<PackageTemplatesResponse>(token, "25493fc5941face7");
}

export function fetchPackageForwardChains(token: string) {
  return requestOperation<PackageForwardChainsResponse>(token, "d927e7f3010e60c9");
}

export function fetchForwardChains(token: string) {
  return requestOperation<ForwardChainsResponse>(token, "d927e7f3010e60c9");
}

export function fetchForwardGroups(token: string) {
  return requestOperation<ForwardGroupsResponse>(token, "ce24c9cd335994fe");
}

export async function fetchForwardServers(token: string) {
  const result = await requestOperation<ForwardServersResponse | RemoteServer[]>(token, "b16e74baa40c6a77");
  return Array.isArray(result) ? { success: true, servers: result } : result;
}

export function fetchForwardCertificates(token: string) {
  return requestOperation<ForwardCertificatesResponse>(token, "4cb3efd640ac3fda");
}

export async function fetchForwardNodes(token: string) {
  const result = await requestOperation<ForwardNodesResponse | XrayNode[]>(token, "b02ec184f40f46f3");
  return Array.isArray(result) ? { success: true, nodes: result } : result;
}

export function probeForwardServers(token: string, fromServerId: number, toServerId: number) {
  return requestOperation<ForwardProbeResponse>(token, "5f2a6b70ac6ce6c9", {
    from_server_id: fromServerId,
    to_server_id: toServerId,
    timeout_ms: 3000,
  });
}

export function probeForwardTargets(token: string, serverId: number, targets: string[]) {
  return requestOperation<ForwardProbeResponse>(token, "9827bede7148e683", {
    server_id: serverId,
    targets,
    timeout_ms: 3000,
  });
}

export function createForwardGroup(token: string, body: Omit<ForwardGroup, "id">) {
  return requestOperation<ForwardMutationResponse>(token, "c4d4cc7f618ee06e", body);
}

export function updateForwardGroup(token: string, groupId: number, body: Omit<ForwardGroup, "id">) {
  return requestOperation<ForwardMutationResponse>(token, operationWithParams("f8fa871ba60b0eb1", [groupId]), body);
}

export function createForwardChain(token: string, body: {
  name: string;
  group_ids: number[];
  port_range_start: number;
  port_range_end: number;
  dns_domain: string;
  dns_domain_v6: string;
  dns_provider_id: number;
}) {
  return requestOperation<ForwardMutationResponse>(token, "6a7a41a553bc180a", body);
}

export function updateForwardChain(token: string, chainId: number, body: {
  port_range_start: number;
  port_range_end: number;
  dns_domain: string;
  dns_domain_v6: string;
  dns_provider_id: number;
}) {
  return requestOperation<ForwardMutationResponse>(token, operationWithParams("6f90132d4dc05c73", [chainId]), body);
}

export function updateForwardChainGroups(token: string, chainId: number, groupIds: number[]) {
  return requestOperation<ForwardMutationResponse>(token, operationWithParams("0702ef1df2036182", [chainId]), {
    group_ids: groupIds,
  });
}

export function createForwardChainNode(token: string, chainId: number, body: {
  node_name?: string;
  relay_protocol: "tcp";
  entry_separate: boolean;
  exit_separate: boolean;
} | {
  existing_node_id: number;
  port: number;
  relay_protocol: "tcp";
}) {
  return requestOperation<ForwardMutationResponse>(token, operationWithParams("f88a43929a335dea", [chainId]), body);
}

export function deleteForwardChain(token: string, chainId: number) {
  return requestOperation<ForwardMutationResponse>(token, operationWithParams("7dbacb2248bbdaf1", [chainId]));
}

export function publishCarpoolPackage(token: string, body: CarpoolPublishRequest) {
  return requestOperation<PackageMutationResponse>(token, "8449163f7a3871b3", body);
}

export function unpublishCarpoolPackage(token: string, packageId: number) {
  return requestOperation<PackageMutationResponse>(token, "8e84cec95bcf3f7e", { package_id: packageId });
}

export function fetchTrafficSummary(token: string) {
  return request<TrafficSummary>(joinUrl(MMWX_API_BASE_URL, "/api/traffic/summary"), token);
}

export function fetchRemoteServers(token: string) {
  return request<RemoteServersResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/remote-servers"), token);
}

export function createRemoteServer(token: string, body: RemoteServerCreateRequest) {
  return request<RemoteServerMutationResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/remote-servers/create"), token, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function addSharedRemoteServer(token: string, body: SharedServerAddRequest) {
  return request<SharedServerAddResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/remote-servers/add-shared"), token, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function revealRemoteServerToken(token: string, serverId: number) {
  return request<{ token?: string; agent_token?: string }>(
    joinUrl(MMWX_API_BASE_URL, `/api/admin/remote-servers/reveal-token?server_id=${encodeURIComponent(String(serverId))}`),
    token,
  );
}

export function fetchMasterUrl(token: string) {
  return request<MasterUrlResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/system-settings/master-url"), token);
}

export function fetchDNSProviders(token: string) {
  return request<DNSProvidersResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/dns-providers"), token);
}

export function resolveDNSHostname(token: string, hostname: string) {
  return request<{ ips?: string[] }>(joinUrl(MMWX_API_BASE_URL, `/api/dns/resolve?hostname=${encodeURIComponent(hostname)}`), token);
}

export function fetchLocalSystemMetrics(_token: string, signal?: AbortSignal) {
  return requestCustomApi<SystemMetrics>(joinUrl(MMWX_CUSTOM_API_BASE_URL, "/api/custom/dashboard/system"), { signal });
}

export function fetchConnectionMetrics(signal?: AbortSignal) {
  return requestCustomApi<ConnectionMetricsResponse>(joinUrl(MMWX_CUSTOM_API_BASE_URL, "/api/custom/agent/metrics"), { signal });
}

export function createHelperInstallToken(token: string, serverId: number) {
  return requestCustomApi<HelperInstallTokenResponse>(joinUrl(MMWX_CUSTOM_API_BASE_URL, "/api/custom/helper/install-token"), {
    method: "POST",
    headers: {
      "MM-Authorization": token,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ server_id: serverId }),
  });
}

export function fetchGeoLookup(host: string, signal?: AbortSignal) {
  return requestCustomApi<GeoLookupResponse>(joinUrl(MMWX_CUSTOM_API_BASE_URL, `/api/custom/geo/lookup?host=${encodeURIComponent(host)}`), { signal });
}

export function fetchNodeTotals(token: string, date: string) {
  return request<NodeTotalsResponse>(joinUrl(MMWX_API_BASE_URL, `/api/admin/traffic/node-totals?date=${encodeURIComponent(date)}`), token);
}

export function fetchTrafficPeriod(token: string, range: TrafficRange, view: "nodes"):
  Promise<TrafficPeriodResponse<NodeTrafficItem>>;
export function fetchTrafficPeriod(token: string, range: TrafficRange, view: "users"):
  Promise<TrafficPeriodResponse<PeriodUserTrafficItem>>;
export function fetchTrafficPeriod(token: string, range: TrafficRange, view: "nodes" | "users") {
  return request<TrafficPeriodResponse<NodeTrafficItem | PeriodUserTrafficItem>>(
    joinUrl(MMWX_API_BASE_URL, `/api/admin/traffic/period?range=${encodeURIComponent(range)}&view=${encodeURIComponent(view)}`),
    token,
  );
}

export function fetchNodeConnections(token: string) {
  return request<NodeConnectionsResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/traffic/node-connections"), token);
}

export function fetchUsers(token: string) {
  return request<UsersTrafficResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/traffic/users"), token);
}

export function fetchUserConnections(token: string) {
  return request<UserConnectionsResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/traffic/user-connections"), token);
}

export function fetchUserSpeeds(token: string, serverId: number) {
  return request<UserSpeedsResponse>(joinUrl(MMWX_API_BASE_URL, `/api/admin/remote/user-speeds?server_id=${encodeURIComponent(String(serverId))}`), token);
}

export function fetchAdminTraffic(token: string) {
  return request<AdminTrafficResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/traffic/servers"), token);
}

export function controlRemoteService(token: string, serverId: number, service: "xray", action: "start" | "stop" | "restart") {
  return request<{ success?: boolean; message?: string }>(joinUrl(MMWX_API_BASE_URL, `/api/admin/remote/services/control?server_id=${encodeURIComponent(String(serverId))}`), token, {
    method: "POST",
    body: JSON.stringify({ service, action }),
  });
}

function remoteUrl(path: string, serverId: number) {
  return joinUrl(MMWX_API_BASE_URL, `${path}?server_id=${encodeURIComponent(String(serverId))}`);
}

export function fetchXrayServiceStatus(token: string, serverId: number) {
  return request<XrayServiceStatusResponse>(remoteUrl("/api/admin/remote/services/status", serverId), token);
}

export function fetchRemoteSystemInfo(token: string, serverId: number) {
  return request<RemoteSystemInfo>(remoteUrl("/api/admin/remote/system/info", serverId), token);
}

export function fetchAgentVersionInfo(token: string, serverId: number) {
  return request<AgentVersionInfo>(remoteUrl("/api/admin/remote/agent/version-info", serverId), token);
}

export function syncRemoteNodes(token: string, serverId: number, body: { server_host?: string; force_override?: boolean }) {
  return request<AgentSyncNodesResponse>(remoteUrl("/api/admin/remote/sync-nodes", serverId), token, {
    method: "POST",
    body: JSON.stringify({
      server_host: body.server_host || "",
      force_override: Boolean(body.force_override),
    }),
  });
}

export function syncRemoteNodeAddress(token: string, serverId: number) {
  return request<{ success?: boolean; message?: string }>(joinUrl(MMWX_API_BASE_URL, "/api/admin/remote-servers/sync-node-address"), token, {
    method: "POST",
    body: JSON.stringify({ id: serverId }),
  });
}

export function deployRemoteDefaultConfig(token: string, serverId: number) {
  return request<{ success?: boolean; message?: string }>(remoteUrl("/api/admin/remote/deploy-steal-self", serverId), token, {
    method: "POST",
  });
}

export function fetchXraySnapshots(token: string, serverId: number, options?: { limit?: number; withConfig?: boolean }) {
  const params = new URLSearchParams({ server_id: String(serverId) });
  if (options?.limit) params.set("limit", String(options.limit));
  if (options?.withConfig) params.set("with_config", "true");
  return request<XraySnapshotsResponse>(joinUrl(MMWX_API_BASE_URL, `/api/admin/xray-snapshots/list?${params}`), token);
}

export function fetchXrayRecoveryStatus(token: string, serverId: number, withConfig = false) {
  const params = new URLSearchParams({ server_id: String(serverId) });
  if (withConfig) params.set("with_config", "true");
  return request<XrayRecoveryStatusResponse>(joinUrl(MMWX_API_BASE_URL, `/api/admin/xray-snapshots/recovery-status?${params}`), token);
}

export function restoreXraySnapshot(token: string, snapshotId: number) {
  return request<{ success?: boolean; message?: string; server_id?: number; config_hash?: string }>(joinUrl(MMWX_API_BASE_URL, `/api/admin/xray-snapshots/restore?snapshot_id=${encodeURIComponent(String(snapshotId))}`), token, {
    method: "POST",
  });
}

export function applyXrayRecovery(token: string, serverId: number) {
  return request<{ success?: boolean; applied_id?: number; config_hash?: string }>(remoteUrl("/api/admin/xray-snapshots/recovery-apply", serverId), token, {
    method: "POST",
  });
}

export function acceptXrayRecovery(token: string, serverId: number) {
  return request<{ success?: boolean }>(remoteUrl("/api/admin/xray-snapshots/recovery-accept", serverId), token, {
    method: "POST",
  });
}

export function expectXrayRecovery(token: string, serverId: number) {
  return request<{ success?: boolean }>(remoteUrl("/api/admin/xray-snapshots/expect-recovery", serverId), token, {
    method: "POST",
  });
}

export function fetchRemoteWebsites(token: string, serverId: number) {
  return request<RemoteWebsitesResponse>(remoteUrl("/api/admin/remote/nginx/websites", serverId), token);
}

export function installRemoteNginx(token: string, serverId: number) {
  return request<{ success?: boolean; message?: string }>(remoteUrl("/api/admin/remote/nginx/install", serverId), token, {
    method: "POST",
  });
}

export function validateRemoteWebsite(token: string, body: { server_id: number; site_type: "static" | "proxy"; site_value: string; entry_mode: string }) {
  return request<WebsiteMutationResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/remote/website/validate"), token, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function addRemoteWebsite(token: string, body: { server_id: number; domain: string; site_type: "static" | "proxy"; site_value: string; entry_mode: string }) {
  return request<WebsiteMutationResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/remote/website/add"), token, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function deleteRemoteWebsite(token: string, serverId: number, domain: string) {
  return request<WebsiteMutationResponse>(remoteUrl("/api/admin/remote/nginx/websites", serverId), token, {
    method: "DELETE",
    body: JSON.stringify({ domain }),
  });
}

export async function streamAgentAction(
  token: string,
  serverId: number,
  action: "upgrade" | "uninstall",
  onEvent: (event: Record<string, unknown>) => void,
) {
  const path = action === "upgrade" ? "/api/admin/remote/agent/upgrade-stream" : "/api/admin/remote/agent/uninstall-stream";
  const response = await fetch(remoteUrl(path, serverId), {
    method: "POST",
    headers: {
      "MM-Authorization": token,
      "Content-Type": "application/json",
    },
  });
  if (!response.ok) throw new Error(`请求失败 (${response.status})`);
  const reader = response.body?.getReader();
  if (!reader) throw new Error("浏览器不支持读取操作日志");

  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    const parts = buffer.split(/\r?\n\r?\n/);
    buffer = parts.pop() ?? "";
    for (const part of parts) {
      const lines = part.split(/\r?\n/);
      for (const line of lines) {
        if (!line.startsWith("data:")) continue;
        const raw = line.slice(5).trim();
        if (!raw) continue;
        try {
          onEvent(JSON.parse(raw) as Record<string, unknown>);
        } catch {
          onEvent({ type: "output", data: raw });
        }
      }
    }
  }
}

export function fetchXrayConfig(token: string, serverId: number) {
  return request<XrayConfigResponse>(remoteUrl("/api/admin/remote/xray/config", serverId), token);
}

export function testXrayConfig(token: string, serverId: number, config: string) {
  return request<{ success?: boolean; ok?: boolean; message?: string; error?: string; method?: string; output?: string }>(remoteUrl("/api/admin/remote/xray/test-config", serverId), token, {
    method: "POST",
    body: JSON.stringify({ config }),
  });
}

export function saveXrayConfig(token: string, serverId: number, config: string) {
  return request<{ success?: boolean; message?: string }>(remoteUrl("/api/admin/remote/xray/config", serverId), token, {
    method: "POST",
    body: JSON.stringify({ config }),
  });
}

export function fetchXraySystemConfig(token: string, serverId: number) {
  return request<XraySystemConfigResponse>(remoteUrl("/api/admin/remote/xray/system-config", serverId), token);
}

export function saveXraySystemConfig(token: string, serverId: number, config: XraySystemConfig) {
  return request<{ success?: boolean; message?: string }>(remoteUrl("/api/admin/remote/xray/system-config", serverId), token, {
    method: "POST",
    body: JSON.stringify(config),
  });
}

export function fetchXrayInbounds(token: string, serverId: number) {
  return request<XrayInboundsResponse>(remoteUrl("/api/admin/remote/inbounds", serverId), token);
}

export function fetchXrayNodes(token: string) {
  return request<XrayNodesResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/nodes"), token);
}

export function fetchNodeTags(token: string) {
  return request<NodeTagsResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/nodes/tags"), token);
}

export function createNode(token: string, body: NodeMutationRequest) {
  return request<NodeMutationResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/nodes"), token, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function batchCreateNodes(token: string, nodes: NodeMutationRequest[]) {
  return request<NodeMutationResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/nodes/batch"), token, {
    method: "POST",
    body: JSON.stringify({ nodes }),
  });
}

export function updateNode(token: string, nodeId: number, body: NodeMutationRequest) {
  return request<NodeMutationResponse>(joinUrl(MMWX_API_BASE_URL, `/api/admin/nodes/${encodeURIComponent(String(nodeId))}`), token, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

export function deleteNode(token: string, nodeId: number, deleteInbound = false) {
  const suffix = deleteInbound ? "?delete_inbound=true" : "";
  return request<NodeMutationResponse>(joinUrl(MMWX_API_BASE_URL, `/api/admin/nodes/${encodeURIComponent(String(nodeId))}${suffix}`), token, {
    method: "DELETE",
  });
}

export function clearNodes(token: string) {
  return request<NodeMutationResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/nodes/clear"), token, { method: "POST" });
}

export function batchDeleteNodes(token: string, nodeIds: number[]) {
  return request<NodeMutationResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/nodes/batch-delete"), token, {
    method: "POST",
    body: JSON.stringify({ node_ids: nodeIds }),
  });
}

export function batchRenameNodes(token: string, updates: Array<{ node_id: number; new_name: string }>) {
  return request<NodeMutationResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/nodes/batch-rename"), token, {
    method: "POST",
    body: JSON.stringify({ updates }),
  });
}

export function batchDisableNodeSkipCert(token: string, nodeIds: number[]) {
  return request<NodeMutationResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/nodes/batch-disable-skip-cert"), token, {
    method: "POST",
    body: JSON.stringify({ node_ids: nodeIds }),
  });
}

export function batchUpdateSnellOptions(token: string, nodeIds: number[], options: { tfo?: boolean; udp_relay?: boolean }) {
  return request<NodeMutationResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/nodes/batch-snell-options"), token, {
    method: "POST",
    body: JSON.stringify({ node_ids: nodeIds, ...options }),
  });
}

export function parseNodeURIs(token: string, content: string, forceNodeSkipCert: boolean) {
  return request<NodeParseResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/nodes/parse-uris"), token, {
    method: "POST",
    body: JSON.stringify({ content, force_node_skip_cert: forceNodeSkipCert }),
  });
}

export function fetchNodeSubscription(token: string, url: string, userAgent: string, forceNodeSkipCert: boolean) {
  return request<NodeParseResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/nodes/fetch-subscription"), token, {
    method: "POST",
    body: JSON.stringify({ url, user_agent: userAgent, force_node_skip_cert: forceNodeSkipCert }),
  });
}

export function fetchNodeURI(token: string, nodeId: number) {
  return request<NodeURIResponse>(joinUrl(MMWX_API_BASE_URL, `/api/admin/nodes/${encodeURIComponent(String(nodeId))}/uri`), token);
}

export function fetchNodeURIs(token: string) {
  return request<{ items?: NodeURIItem[] }>(joinUrl(MMWX_API_BASE_URL, "/api/admin/node-uris"), token);
}

export function fetchNodeTunnels(token: string) {
  return request<{ success?: boolean; tunnels?: NodeTunnel[]; chains?: NodeTunnelChain[] }>(joinUrl(MMWX_API_BASE_URL, "/api/admin/tunnels"), token);
}

export function createTunnelChain(token: string, body: { label: string; server_ids: number[]; entry_port: number; target_address: string; target_port: number }) {
  return request<{ success?: boolean; entry_host?: string; entry_port?: number; message?: string }>(joinUrl(MMWX_API_BASE_URL, "/api/admin/tunnel-chains"), token, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function mutateRemoteInbound(token: string, serverId: number, body: Record<string, unknown>) {
  return request<{ success?: boolean; message?: string }>(joinUrl(MMWX_API_BASE_URL, `/api/admin/remote/inbounds?server_id=${encodeURIComponent(String(serverId))}`), token, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function mutateRemoteOutbound(token: string, serverId: number, body: Record<string, unknown>) {
  return request<{ success?: boolean; message?: string }>(joinUrl(MMWX_API_BASE_URL, `/api/admin/remote/outbounds?server_id=${encodeURIComponent(String(serverId))}`), token, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function fetchRemoteRouting(token: string, serverId: number) {
  return request<{ success?: boolean; routing?: { rules?: Array<Record<string, unknown>> } }>(joinUrl(MMWX_API_BASE_URL, `/api/admin/remote/routing?server_id=${encodeURIComponent(String(serverId))}`), token);
}

export function mutateRemoteRouting(token: string, serverId: number, body: Record<string, unknown>) {
  return request<{ success?: boolean; message?: string }>(joinUrl(MMWX_API_BASE_URL, `/api/admin/remote/routing?server_id=${encodeURIComponent(String(serverId))}`), token, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function fetchUserConfig(token: string) {
  return request<UserConfigResponse>(joinUrl(MMWX_API_BASE_URL, "/api/user/config"), token);
}

export function updateUserConfig(token: string, body: UserConfigResponse) {
  return request<UserConfigResponse>(joinUrl(MMWX_API_BASE_URL, "/api/user/config"), token, {
    method: "PUT",
    body: JSON.stringify(body),
  });
}

export function syncExternalSubscriptions(token: string, selection = true) {
  return request<ExternalSyncResponse>(joinUrl(MMWX_API_BASE_URL, `/api/user/sync-external-subscriptions${selection ? "?selection=1" : ""}`), token, { method: "POST" });
}

export function confirmExternalSync(token: string, sessionId: string, candidateIds: string[]) {
  return request<{ message?: string; created_count?: number }>(joinUrl(MMWX_API_BASE_URL, "/api/user/sync-external-subscriptions/confirm"), token, {
    method: "POST",
    body: JSON.stringify({ session_id: sessionId, candidate_ids: candidateIds }),
  });
}

export function fetchPackageNodeTrafficName(token: string) {
  return request<{ enabled?: boolean }>(joinUrl(MMWX_API_BASE_URL, "/api/admin/system-settings/package-node-traffic-name"), token);
}

export function updatePackageNodeTrafficName(token: string, enabled: boolean) {
  return request<{ enabled?: boolean }>(joinUrl(MMWX_API_BASE_URL, "/api/admin/system-settings/package-node-traffic-name"), token, {
    method: "PUT",
    body: JSON.stringify({ enabled }),
  });
}

export function fetchNodeRelatedInbounds(token: string, nodeId: number) {
  return request<NodeRelatedInboundsResponse>(joinUrl(MMWX_API_BASE_URL, `/api/admin/nodes/${encodeURIComponent(String(nodeId))}/related-inbounds`), token);
}

export function updateNodeServer(token: string, nodeId: number, server: string) {
  return request<NodeMutationResponse>(joinUrl(MMWX_API_BASE_URL, `/api/admin/nodes/${encodeURIComponent(String(nodeId))}/server`), token, {
    method: "PUT",
    body: JSON.stringify({ server }),
  });
}

export function restoreNodeServer(token: string, nodeId: number) {
  return request<NodeMutationResponse>(joinUrl(MMWX_API_BASE_URL, `/api/admin/nodes/${encodeURIComponent(String(nodeId))}/restore-server`), token, {
    method: "PUT",
  });
}

export function updateNodeConfig(token: string, nodeId: number, clashConfig: string) {
  return request<NodeMutationResponse>(joinUrl(MMWX_API_BASE_URL, `/api/admin/nodes/${encodeURIComponent(String(nodeId))}/config`), token, {
    method: "PUT",
    body: JSON.stringify({ clash_config: clashConfig }),
  });
}

export function setNodeRelay(token: string, nodeId: number, relayServer: string, relayPort: number) {
  return request<NodeMutationResponse>(joinUrl(MMWX_API_BASE_URL, `/api/admin/nodes/${encodeURIComponent(String(nodeId))}/relay`), token, {
    method: "PUT",
    body: JSON.stringify({ relay_server: relayServer, relay_port: relayPort }),
  });
}

export function copyNodeWithRelay(token: string, nodeId: number, relayServer: string, relayPort: number, nameSuffix: string) {
  return request<NodeMutationResponse>(joinUrl(MMWX_API_BASE_URL, `/api/admin/nodes/${encodeURIComponent(String(nodeId))}/relay-copy`), token, {
    method: "POST",
    body: JSON.stringify({ relay_server: relayServer, relay_port: relayPort, name_suffix: nameSuffix }),
  });
}

export function cancelNodeRelay(token: string, nodeId: number) {
  return request<NodeMutationResponse>(joinUrl(MMWX_API_BASE_URL, `/api/admin/nodes/${encodeURIComponent(String(nodeId))}/relay`), token, {
    method: "DELETE",
  });
}

export function tcpingNode(token: string, body: { host: string; port: number; timeout?: number; protocol?: string }) {
  return request<NodeTCPingResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/tcping"), token, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function batchTcpingNodes(token: string, requests: Array<{ host: string; port: number; timeout?: number; protocol?: string }>) {
  return request<NodeTCPingResponse[]>(joinUrl(MMWX_API_BASE_URL, "/api/admin/tcping/batch"), token, {
    method: "POST",
    body: JSON.stringify(requests),
  });
}

export function fetchSpeedTestResults(token: string, nodeId?: number, latest = false) {
  const query = latest ? "?latest=1" : nodeId ? `?node_id=${encodeURIComponent(String(nodeId))}&limit=20` : "?limit=50";
  return request<SpeedTestResultsResponse>(joinUrl(MMWX_API_BASE_URL, `/api/admin/speedtest/results${query}`), token);
}

export function runSpeedTest(token: string, body: { node_id: number; bytes?: number; url?: string; tester_id?: number; threads?: number; buf_size?: number; latency_only?: boolean }) {
  return request<SpeedTestRunResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/speedtest/run"), token, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function fetchSpeedTesters(token: string) {
  return request<SpeedTestersResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/speedtest/testers"), token);
}

export function createNodeTempSubscription(token: string, proxies: Array<Record<string, unknown>>, maxAccess: number, expireSeconds: number) {
  return request<NodeTempSubscriptionResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/temp-subscription"), token, {
    method: "POST",
    body: JSON.stringify({ proxies, max_access: maxAccess, expire_seconds: expireSeconds }),
  });
}

export function fetchRoutedOutbounds(token: string, parentNodeId: number) {
  return request<{ items?: Array<Record<string, unknown>> }>(joinUrl(MMWX_API_BASE_URL, `/api/admin/routed-outbound?parent_id=${encodeURIComponent(String(parentNodeId))}`), token);
}

export function createRoutedOutbound(token: string, body: { parent_node_id: number; target_node_id?: number; label: string; outbound: Record<string, unknown>; node_name?: string }) {
  return request<NodeMutationResponse>(joinUrl(MMWX_API_BASE_URL, "/api/admin/routed-outbound"), token, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function deleteRoutedOutbound(token: string, id: number) {
  return request<NodeMutationResponse>(joinUrl(MMWX_API_BASE_URL, `/api/admin/routed-outbound?id=${encodeURIComponent(String(id))}`), token, {
    method: "DELETE",
  });
}

export function fetchXrayUsers(token: string) {
  return request<Array<Record<string, unknown>> | { users?: Array<Record<string, unknown>> }>(joinUrl(MMWX_API_BASE_URL, "/api/admin/users"), token);
}

export function updateXrayUserEmail(token: string, username: string, email: string) {
  return request<{ success?: boolean; message?: string }>(joinUrl(MMWX_API_BASE_URL, "/api/admin/users/update-email"), token, {
    method: "POST",
    body: JSON.stringify({ username, email }),
  });
}

export function generateXrayX25519(token: string) {
  return request<{ privateKey: string; publicKey: string }>(joinUrl(MMWX_API_BASE_URL, "/api/admin/xray/generate-x25519"), token, { method: "POST" });
}

export function generateXrayProtocolKeys(token: string, body: { type: "mlkem768x25519plus"; encryptionType: "x25519" | "mlkem768"; appearance: string; ticketLifetime: string; padding: string }) {
  return request<{ decryptionConfig: string; encryption: string }>(joinUrl(MMWX_API_BASE_URL, "/api/admin/xray/generate-keys"), token, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function fetchRealityDomains(token: string, serverId: number) {
  return request<{ success?: boolean; message?: string; warning?: string; domains?: Array<Record<string, unknown>>; domain_servers?: Record<string, Record<string, unknown>> }>(remoteUrl("/api/admin/remote/reality-domains", serverId), token);
}

export function probeCustomRealityDomain(token: string, serverId: number, domain: string) {
  return request<Record<string, unknown>>(joinUrl(MMWX_API_BASE_URL, "/api/admin/remote/reality-domains/custom"), token, {
    method: "POST",
    body: JSON.stringify({ domain, server_id: serverId }),
  });
}

export function fetchValidCertificates(token: string) {
  return request<{ success?: boolean; certificates?: Array<Record<string, unknown>> }>(joinUrl(MMWX_API_BASE_URL, "/api/admin/certificates/valid"), token);
}

export function fetchNginxServerDomains(token: string, serverId: number) {
  return request<{ success?: boolean; domains?: Array<Record<string, unknown>> }>(remoteUrl("/api/admin/remote/nginx/servers-list", serverId), token);
}

export function setupRemoteSSL(token: string, serverId: number) {
  return request<{ success?: boolean; message?: string; cert_deployed?: boolean }>(remoteUrl("/api/admin/remote/setup-ssl", serverId), token, { method: "POST" });
}

export function updateRemoteServerDomain(token: string, server: RemoteServer, domain: string) {
  return request<{ success?: boolean; message?: string }>(joinUrl(MMWX_API_BASE_URL, "/api/admin/remote-servers/update"), token, {
    method: "POST",
    body: JSON.stringify({
      id: server.id,
      name: server.name,
      domain,
      traffic_limit: server.traffic_limit ?? 0,
      traffic_reset_day: server.traffic_reset_day ?? 0,
      connection_mode: server.connection_mode,
      listen_port: server.listen_port ?? 0,
      pull_address: server.pull_address || "",
      pull_port: server.pull_port ?? 0,
      xray_mode: server.xray_mode,
      fallback_to_pull: Boolean(server.fallback_to_pull),
    }),
  });
}

export function mutateXrayInbound(token: string, serverId: number, body: { action: "add"; inbound: XrayObject; node_name?: string } | { action: "update"; tag: string; inbound: XrayObject; node_name?: string } | { action: "remove"; tag: string }) {
  return request<{ success?: boolean; message?: string }>(remoteUrl("/api/admin/remote/inbounds", serverId), token, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function fetchXrayOutbounds(token: string, serverId: number) {
  return request<XrayOutboundsResponse>(remoteUrl("/api/admin/remote/outbounds", serverId), token);
}

export function mutateXrayOutbound(token: string, serverId: number, body: { action: "add"; outbound: XrayObject } | { action: "remove"; tag: string } | { action: "update"; tag: string; outbound: XrayObject } | { action: "reorder"; tags: string[] }) {
  return request<{ success?: boolean; message?: string }>(remoteUrl("/api/admin/remote/outbounds", serverId), token, {
    method: "POST",
    body: JSON.stringify(body),
  });
}

export function fetchXrayServerNICs(token: string, serverId: number) {
  return request<XrayServerNICsResponse>(remoteUrl("/api/admin/server-nics", serverId), token);
}

export function fetchXrayWarpStatus(token: string, serverId: number) {
  return request<XrayWarpStatus>(remoteUrl("/api/admin/remote/warp/status", serverId), token);
}

export function installXrayWarp(token: string, serverId: number) {
  return request<XrayWarpStatus>(remoteUrl("/api/admin/remote/warp/install", serverId), token, { method: "POST" });
}

export function updateXrayWarpLicense(token: string, serverId: number, license: string) {
  return request<XrayWarpStatus>(remoteUrl("/api/admin/remote/warp/license", serverId), token, {
    method: "POST",
    body: JSON.stringify({ license }),
  });
}

export function removeXrayWarp(token: string, serverId: number) {
  return request<XrayWarpStatus>(remoteUrl("/api/admin/remote/warp/remove", serverId), token, { method: "POST" });
}

export function fetchXrayRouting(token: string, serverId: number) {
  return request<XrayRoutingResponse>(remoteUrl("/api/admin/remote/routing", serverId), token);
}

export function mutateXrayRouting(token: string, serverId: number, body: { action: "add_rule"; rule: XrayObject } | { action: "remove_rule"; index: number } | { action: "set"; routing: XrayObject }) {
  return request<{ success?: boolean; message?: string }>(remoteUrl("/api/admin/remote/routing", serverId), token, {
    method: "POST",
    body: JSON.stringify(body),
  });
}
