import type {
  AdminTrafficResponse,
  AgentSyncNodesResponse,
  AgentVersionInfo,
  ConnectionMetricsResponse,
  GeoLookupResponse,
  HelperInstallTokenResponse,
  LoginResponse,
  NodeConnectionsResponse,
  SystemMetrics,
  NodeTotalsResponse,
  NodeTrafficItem,
  PeriodUserTrafficItem,
  DNSProvidersResponse,
  MasterUrlResponse,
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

const SESSION_KEY = "mmwx-session";
const MMWX_API_BASE_URL = normalizeBaseUrl(import.meta.env.VITE_MMWX_API_BASE_URL ?? "");
const MMWX_CUSTOM_API_BASE_URL = normalizeBaseUrl(import.meta.env.VITE_MMWX_CUSTOM_API_BASE_URL ?? "");
const MMWX_SECURE_AUDIENCE = normalizeBaseUrl(import.meta.env.VITE_MMWX_SECURE_AUDIENCE ?? "https://mmwx.imgamer.top");
const SECURE_CHANNEL_VERSION = "v1";
const SECURE_CHANNEL_PROTO = "v2";
const SECURE_ENVELOPE_VERSION = 0x01;
const SECURE_CHANNEL_STATIC_PUB_B64 = "r2ItZKepeBU5sB40geAHZ6cFwznLAZx4ww9GOtITXnA=";
const X25519_BASEPOINT = new Uint8Array([9, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]);
const X25519_P = (1n << 255n) - 19n;

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
  sendKey: CryptoKey;
  recvKey: CryptoKey;
  sendNonce: Uint8Array;
  recvNonce: Uint8Array;
  sendSeq: number;
};

let activeSecureChannel: Promise<SecureChannel | null> | null = null;

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
  const privateKey = new Uint8Array(32);
  crypto.getRandomValues(privateKey);
  const publicKey = x25519(privateKey, X25519_BASEPOINT);
  const handshakeUrl = new URL(joinUrl(apiBaseFromPath(path), "/api/securechan/handshake"), window.location.origin);
  const audience = secureAudienceFromPath(path);
  const response = await fetch(handshakeUrl.toString(), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ client_pub_b64: bytesToBase64(publicKey), audience, proto: SECURE_CHANNEL_PROTO }),
  });
  if (!response.ok) throw new Error(`secure channel handshake failed (${response.status})`);
  const body = await response.json() as { session_id?: string; server_pub_b64?: string };
  if (!body.session_id || !body.server_pub_b64) throw new Error("secure channel handshake response invalid");
  const serverPublicKey = base64ToBytes(body.server_pub_b64);
  const ephemeralShared = x25519(privateKey, serverPublicKey);
  const staticShared = x25519(privateKey, base64ToBytes(SECURE_CHANNEL_STATIC_PUB_B64));
  const shared = concatBytes(ephemeralShared, staticShared);
  const salt = concatBytes(publicKey, serverPublicKey);
  const hkdfKey = await crypto.subtle.importKey("raw", shared, "HKDF", false, ["deriveBits"]);
  const derived = new Uint8Array(await crypto.subtle.deriveBits({ name: "HKDF", hash: "SHA-256", salt, info: textToBytes(`securechan-v2\n${body.session_id}`) }, hkdfKey, 704));
  const masterToAgentKey = derived.slice(0, 32);
  const agentToMasterKey = derived.slice(32, 64);
  const masterToAgentNonce = derived.slice(64, 76);
  const agentToMasterNonce = derived.slice(76, 88);
  return {
    sessionId: body.session_id,
    sendKey: await crypto.subtle.importKey("raw", agentToMasterKey, "AES-GCM", false, ["encrypt"]),
    recvKey: await crypto.subtle.importKey("raw", masterToAgentKey, "AES-GCM", false, ["decrypt"]),
    sendNonce: agentToMasterNonce,
    recvNonce: masterToAgentNonce,
    sendSeq: 0,
  };
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
  channel.sendSeq += 1;
  const seq = channel.sendSeq;
  const nonce = secureNonce(channel.sendNonce, seq);
  const ciphertext = new Uint8Array(await crypto.subtle.encrypt({ name: "AES-GCM", iv: nonce }, channel.sendKey, toArrayBuffer(plaintext)));
  const envelope = new Uint8Array(9 + ciphertext.length);
  envelope[0] = SECURE_ENVELOPE_VERSION;
  writeUint64BE(envelope, 1, seq);
  envelope.set(ciphertext, 9);
  return envelope;
}

async function decryptEnvelope(channel: SecureChannel, envelope: Uint8Array) {
  if (envelope.length < 25 || envelope[0] !== SECURE_ENVELOPE_VERSION) throw new Error("secure channel response invalid");
  const seq = readUint64BE(envelope, 1);
  const nonce = secureNonce(channel.recvNonce, seq);
  return new Uint8Array(await crypto.subtle.decrypt({ name: "AES-GCM", iv: nonce }, channel.recvKey, toArrayBuffer(envelope.slice(9))));
}

function secureNonce(base: Uint8Array, seq: number) {
  const nonce = new Uint8Array(base);
  const seqBytes = new Uint8Array(12);
  writeUint64BE(seqBytes, 4, seq);
  for (let index = 0; index < nonce.length; index += 1) nonce[index] ^= seqBytes[index];
  return nonce;
}

function clampX25519PrivateKey(key: Uint8Array) {
  key[0] &= 248;
  key[31] &= 127;
  key[31] |= 64;
}

function x25519(privateKey: Uint8Array, publicKey: Uint8Array) {
  const scalarBytes = new Uint8Array(privateKey);
  clampX25519PrivateKey(scalarBytes);
  const publicBytes = new Uint8Array(publicKey);
  publicBytes[31] &= 127;
  const scalar = littleEndianToBigInt(scalarBytes);
  const x1 = littleEndianToBigInt(publicBytes) % X25519_P;
  let x2 = 1n;
  let z2 = 0n;
  let x3 = x1;
  let z3 = 1n;
  let swap = 0n;

  for (let t = 254; t >= 0; t -= 1) {
    const bit = (scalar >> BigInt(t)) & 1n;
    swap ^= bit;
    [x2, x3] = conditionalSwap(swap, x2, x3);
    [z2, z3] = conditionalSwap(swap, z2, z3);
    swap = bit;

    const a = mod(x2 + z2);
    const aa = mod(a * a);
    const b = mod(x2 - z2);
    const bb = mod(b * b);
    const e = mod(aa - bb);
    const c = mod(x3 + z3);
    const d = mod(x3 - z3);
    const da = mod(d * a);
    const cb = mod(c * b);
    x3 = mod((da + cb) ** 2n);
    z3 = mod(x1 * mod((da - cb) ** 2n));
    x2 = mod(aa * bb);
    z2 = mod(e * mod(aa + 121665n * e));
  }
  [x2, x3] = conditionalSwap(swap, x2, x3);
  [z2, z3] = conditionalSwap(swap, z2, z3);
  return bigIntToLittleEndian(mod(x2 * modInverse(z2)));
}

function conditionalSwap(swap: bigint, a: bigint, b: bigint): [bigint, bigint] {
  return swap ? [b, a] : [a, b];
}

function mod(value: bigint) {
  const result = value % X25519_P;
  return result >= 0n ? result : result + X25519_P;
}

function modInverse(value: bigint) {
  return modPow(value, X25519_P - 2n);
}

function modPow(base: bigint, exponent: bigint) {
  let result = 1n;
  let value = mod(base);
  let power = exponent;
  while (power > 0n) {
    if (power & 1n) result = mod(result * value);
    value = mod(value * value);
    power >>= 1n;
  }
  return result;
}

function littleEndianToBigInt(bytes: Uint8Array) {
  let value = 0n;
  for (let index = bytes.length - 1; index >= 0; index -= 1) value = (value << 8n) + BigInt(bytes[index]);
  return value;
}

function bigIntToLittleEndian(value: bigint) {
  const bytes = new Uint8Array(32);
  let current = value;
  for (let index = 0; index < bytes.length; index += 1) {
    bytes[index] = Number(current & 255n);
    current >>= 8n;
  }
  return bytes;
}

function concatBytes(...parts: Uint8Array[]) {
  const out = new Uint8Array(parts.reduce((total, part) => total + part.length, 0));
  let offset = 0;
  for (const part of parts) {
    out.set(part, offset);
    offset += part.length;
  }
  return out;
}

function toArrayBuffer(bytes: Uint8Array) {
  const copy = new Uint8Array(bytes.byteLength);
  copy.set(bytes);
  return copy.buffer;
}

function writeUint64BE(bytes: Uint8Array, offset: number, value: number) {
  let current = BigInt(value);
  for (let index = 7; index >= 0; index -= 1) {
    bytes[offset + index] = Number(current & 255n);
    current >>= 8n;
  }
}

function readUint64BE(bytes: Uint8Array, offset: number) {
  let value = 0n;
  for (let index = 0; index < 8; index += 1) value = (value << 8n) + BigInt(bytes[offset + index]);
  return Number(value);
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
