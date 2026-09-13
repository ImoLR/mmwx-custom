import { useCallback, useEffect, useMemo, useState } from "react";
import type { Dispatch, SetStateAction } from "react";
import { AlertTriangle, CheckCircle2, RefreshCw, Save } from "lucide-react";
import { fetchDetailedConnections, updateDetailedConnectionSettings } from "./api";
import type { DetailedConnectionResponse, RemoteServer, ServerConnectionSettings, TCPStateCounts, UserConnectionSettings } from "./types";

type Props = { server: RemoteServer; token: string };

const emptySettings: ServerConnectionSettings = {
  default_close_wait_timeout_seconds: null,
  online_ip_grace_period_seconds: 30,
  global_total_limit_enabled: false,
  max_global_total_connections: null,
  users: [],
};

const emptyTCP = (): TCPStateCounts => ({
  tcp_total: 0, established: 0, syn_sent: 0, syn_recv: 0,
  fin_wait_1: 0, fin_wait_2: 0, time_wait: 0, close_wait: 0,
  last_ack: 0, closing: 0, close: 0, unknown: 0,
});

export function ConnectionsManager({ server, token }: Props) {
  const [data, setData] = useState<DetailedConnectionResponse | null>(null);
  const [settings, setSettings] = useState<ServerConnectionSettings>(emptySettings);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  const load = useCallback(async (signal?: AbortSignal) => {
    setError("");
    try {
      const response = await fetchDetailedConnections(token, server.id, signal);
      setData(response);
      setSettings(mergeDiscoveredUsers(response.settings, response));
    } catch (reason) {
      if (!signal?.aborted) setError(reason instanceof Error ? reason.message : "连接详情加载失败");
    } finally {
      if (!signal?.aborted) setLoading(false);
    }
  }, [server.id, token]);

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);

  async function save() {
    setSaving(true);
    setError("");
    setNotice("");
    try {
      const response = await updateDetailedConnectionSettings(token, server.id, settings);
      setData(response);
      setSettings(mergeDiscoveredUsers(response.settings, response));
      setNotice("设置已持久化，Helper 将自动下发；只拒绝新连接，不中断现有连接");
      window.setTimeout(() => void load(), 5500);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "保存连接限制失败");
    } finally {
      setSaving(false);
    }
  }

  const snapshot = data?.record?.snapshot;
  const settingsByIdentity = useMemo(() => new Map(settings.users.map((item) => [identityKey(item), item])), [settings.users]);
  const proxyUsers = useMemo(() => {
    if (snapshot?.proxy_users?.length) return snapshot.proxy_users;
    return settings.users.map((item) => ({
      identity: item.identity, inbound_tag: item.identity.inbound_tag, user: item.identity.user,
      inbound_port: undefined, current_total: 0, inbound_active: 0, inbound_tcp: emptyTCP(), inbound_online_ips: [],
      outbound_active: 0, outbound_pending: 0, outbound_tcp: emptyTCP(), outbound_new_rate: 0,
      outbound_new_total: 0, outbound_rejected_total: 0, rejected_active_limit: 0,
      rejected_new_rate_limit: 0, rejected_user_total_limit: 0, rejected_online_ip_limit: 0,
      rejected_global_total_limit: 0, max_inbound_online_ips: item.max_inbound_online_ips,
      max_total_connections: item.max_total_connections, max_outbound_tcp_active: item.max_outbound_tcp_active,
      max_outbound_tcp_new_per_second: item.max_outbound_tcp_new_per_second,
      close_wait_timeout_seconds: item.close_wait_timeout_seconds, source: "persisted_settings",
    }));
  }, [settings.users, snapshot?.proxy_users]);

  return (
    <div className="service-dialog-body connection-manager">
      <div className="connection-manager-toolbar">
        <div>
          <strong>{server.name}</strong>
          <span>{data?.available ? `Helper ${data.record?.helper_version ?? "在线"} · Core API v${snapshot?.core.interface_version ?? "?"}` : "Helper 数据不可用"}</span>
        </div>
        <button type="button" onClick={() => void load()} disabled={loading} aria-label="刷新连接详情" title="刷新连接详情"><RefreshCw className={loading ? "spin" : ""} /></button>
      </div>

      {error && <div className="connection-message error"><AlertTriangle /> <span>{error}</span></div>}
      {notice && <div className="connection-message success"><CheckCircle2 /> <span>{notice}</span></div>}

      <section className="connection-section">
        <div className="connection-section-head"><h4>系统 TCP</h4><span className="connection-source">Linux /proc/net/tcp*</span></div>
        <TCPStateGrid states={snapshot?.system} />
      </section>

      <section className="connection-section">
        <div className="connection-section-head"><h4>入站与在线 IP</h4><span className="connection-source">Core 认证身份 + 精确四元组</span></div>
        {snapshot?.inbounds?.length ? snapshot.inbounds.map((inbound) => (
          <article className="connection-inbound" key={`${inbound.inbound_tag}-${inbound.port}`}>
            <div className="connection-card-title">
              <div><strong>{inbound.inbound_tag || `端口 ${inbound.port}`}</strong><span>{inbound.protocol || "协议未知"} · {inbound.port}</span></div>
              <em>{inbound.online_ip_count} / {displayLimit(inbound.max_online_ips)} IP</em>
            </div>
            <TCPStateGrid states={inbound.tcp} compact />
            <span className="connection-attribution">{attributionText(inbound.attribution, inbound.user)}</span>
            {inbound.online_ips?.length > 0 && <div className="connection-ip-list">{inbound.online_ips.map((item) => <div key={item.ip}><code>{item.ip}</code><strong>{item.connections}</strong></div>)}</div>}
          </article>
        )) : <EmptyConnectionState text={snapshot?.core.available ? "当前没有可归属的入站连接" : "Custom Core 接口尚未连接"} />}
      </section>

      <section className="connection-section">
        <div className="connection-section-head"><h4>认证用户连接</h4><span className="connection-source">Custom Core 身份传播</span></div>
        {proxyUsers.length ? proxyUsers.map((user) => {
          const item = settingsByIdentity.get(`${user.inbound_tag}\u0000${user.user}`) ?? fromProxyUser(user);
          return (
            <article className="connection-user" key={`${user.inbound_tag}-${user.user}`}>
              <div className="connection-card-title">
                <div><strong>{user.user}</strong><span>{user.inbound_tag}{user.inbound_port ? ` · ${user.inbound_port}` : ""}</span></div>
                <em>总连接 {user.current_total} / {displayLimit(item.max_total_connections)}</em>
              </div>
              <ConnectionSubsection title="入站 TCP" meta={`active ${user.inbound_active}`} states={user.inbound_tcp} />
              <ConnectionSubsection title="物理出站 TCP" meta={`active ${user.outbound_active} · pending ${user.outbound_pending}`} states={user.outbound_tcp} />
              <div className="connection-inline-stats">
                <span>出站 NEW <strong>{user.outbound_new_rate}/s</strong></span>
                <span>累计 Dial <strong>{user.outbound_new_total}</strong></span>
                <span>累计拒绝 <strong>{user.outbound_rejected_total}</strong></span>
              </div>
              <div className="connection-rejection-grid" aria-label="拒绝原因">
                <span>用户总数 <strong>{user.rejected_user_total_limit}</strong></span>
                <span>出站 active <strong>{user.rejected_active_limit}</strong></span>
                <span>出站 NEW/s <strong>{user.rejected_new_rate_limit}</strong></span>
                <span>在线 IP <strong>{user.rejected_online_ip_limit}</strong></span>
                <span>全局总数 <strong>{user.rejected_global_total_limit}</strong></span>
              </div>
              {user.inbound_online_ips?.length > 0 && <div className="connection-ip-list">{user.inbound_online_ips.map((source) => <div key={source.ip}><code>{source.ip}</code><strong>{source.connections}</strong></div>)}</div>}
              <div className="connection-limit-grid">
                <NullableNumber label="同时在线 IP 上限" value={item.max_inbound_online_ips} min={1} onChange={(value) => patchUser(item, "max_inbound_online_ips", value, setSettings)} />
                <NullableNumber label="每用户总连接上限" value={item.max_total_connections} min={1} onChange={(value) => patchUser(item, "max_total_connections", value, setSettings)} />
                <NullableNumber label="出站 TCP active 上限" value={item.max_outbound_tcp_active} min={1} onChange={(value) => patchUser(item, "max_outbound_tcp_active", value, setSettings)} />
                <NullableNumber label="出站 NEW/s 上限" value={item.max_outbound_tcp_new_per_second} min={1} onChange={(value) => patchUser(item, "max_outbound_tcp_new_per_second", value, setSettings)} />
                <NullableNumber label="CLOSE_WAIT 自动关闭（秒）" value={item.close_wait_timeout_seconds} min={0} placeholder="继承全局" onChange={(value) => patchUser(item, "close_wait_timeout_seconds", value, setSettings)} />
              </div>
            </article>
          );
        }) : <EmptyConnectionState text="尚无经过认证的代理用户运行数据" />}
      </section>

      <section className="connection-section connection-global-settings">
        <div className="connection-section-head"><h4>全局保护与设置</h4><span className="connection-source">Helper 持久化</span></div>
        <div className="connection-global-summary">
          <span>当前总连接 <strong>{snapshot?.global?.current_total ?? "--"}</strong></span>
          <span>全局限额拒绝 <strong>{snapshot?.global?.rejected_global_total_limit ?? "--"}</strong></span>
        </div>
        <label className="connection-toggle">
          <input type="checkbox" checked={settings.global_total_limit_enabled} onChange={(event) => setSettings((current) => ({ ...current, global_total_limit_enabled: event.target.checked }))} />
          <span><strong>启用全局总连接保护</strong><small>达到上限后只拒绝新连接，不清理现有连接</small></span>
        </label>
        <div className="connection-limit-grid">
          <NullableNumber label="全局总连接上限" value={settings.max_global_total_connections} min={1} required={settings.global_total_limit_enabled} onChange={(value) => setSettings((current) => ({ ...current, max_global_total_connections: value }))} />
          <NullableNumber label="默认 CLOSE_WAIT 自动关闭（秒）" value={settings.default_close_wait_timeout_seconds} min={0} onChange={(value) => setSettings((current) => ({ ...current, default_close_wait_timeout_seconds: value }))} />
          <NullableNumber label="在线 IP 保留秒数" value={settings.online_ip_grace_period_seconds} min={1} required onChange={(value) => setSettings((current) => ({ ...current, online_ip_grace_period_seconds: value ?? 30 }))} />
        </div>
        <button className="connection-save" type="button" onClick={() => void save()} disabled={saving || loading}><Save /> <span>{saving ? "保存中..." : "保存连接设置"}</span></button>
      </section>

      <p className="connection-boundary">用户状态来自 Core 认证身份与连接四元组；出站 TIME_WAIT 使用关闭前保留的身份和四元组与内核表精确匹配。总连接限额计算认证后入站 active、物理出站 active 与 pending，不包含已关闭的 TIME_WAIT。认证前 SYN_RECV 无用户身份，不伪归属。Mux 下物理连接数不等于逻辑流数。</p>
    </div>
  );
}

function TCPStateGrid({ states, compact = false }: { states?: Partial<TCPStateCounts>; compact?: boolean }) {
  const values: Array<[string, number | undefined]> = [
    ["TCP 总数", states?.tcp_total], ["ESTABLISHED", states?.established], ["SYN_SENT", states?.syn_sent], ["SYN_RECV", states?.syn_recv],
    ["FIN_WAIT1", states?.fin_wait_1], ["FIN_WAIT2", states?.fin_wait_2], ["TIME_WAIT", states?.time_wait], ["CLOSE_WAIT", states?.close_wait],
    ["LAST_ACK", states?.last_ack], ["CLOSING", states?.closing], ["CLOSE", states?.close], ["UNKNOWN", states?.unknown],
  ];
  return <div className={`connection-stat-grid${compact ? " compact" : ""}`}>{values.map(([label, value]) => <ConnectionStat key={label} label={label} value={value} />)}</div>;
}

function ConnectionSubsection({ title, meta, states }: { title: string; meta: string; states?: Partial<TCPStateCounts> }) {
  return <div className="connection-subsection"><div><strong>{title}</strong><span>{meta}</span></div><TCPStateGrid states={states} compact /></div>;
}

function ConnectionStat({ label, value }: { label: string; value?: number }) {
  return <div><span>{label}</span><strong>{value ?? "--"}</strong></div>;
}

function EmptyConnectionState({ text }: { text: string }) { return <div className="connection-empty">{text}</div>; }

function NullableNumber({ label, value, min, placeholder = "不限", required, onChange }: { label: string; value: number | null; min: number; placeholder?: string; required?: boolean; onChange: (value: number | null) => void }) {
  return <label><span>{label}</span><input type="number" min={min} step="1" required={required} value={value ?? ""} placeholder={placeholder} onChange={(event) => onChange(event.target.value === "" ? null : Number(event.target.value))} /></label>;
}

function identityKey(item: UserConnectionSettings) { return `${item.identity.inbound_tag}\u0000${item.identity.user}`; }

function fromProxyUser(user: NonNullable<DetailedConnectionResponse["record"]>["snapshot"]["proxy_users"][number]): UserConnectionSettings {
  return {
    identity: { inbound_tag: user.inbound_tag, user: user.user },
    max_inbound_online_ips: user.max_inbound_online_ips,
    max_total_connections: user.max_total_connections,
    max_outbound_tcp_active: user.max_outbound_tcp_active,
    max_outbound_tcp_new_per_second: user.max_outbound_tcp_new_per_second,
    close_wait_timeout_seconds: user.close_wait_timeout_seconds,
  };
}

function mergeDiscoveredUsers(settings: ServerConnectionSettings, response: DetailedConnectionResponse): ServerConnectionSettings {
  const normalized = { ...emptySettings, ...settings, users: settings.users ?? [] };
  const users = new Map(normalized.users.map((item) => [identityKey(item), { ...item, max_total_connections: item.max_total_connections ?? null }]));
  for (const user of response.record?.snapshot.proxy_users ?? []) {
    const item = fromProxyUser(user);
    if (!users.has(identityKey(item))) users.set(identityKey(item), item);
  }
  return { ...normalized, users: [...users.values()] };
}

function patchUser(item: UserConnectionSettings, field: keyof Omit<UserConnectionSettings, "identity">, value: number | null, setSettings: Dispatch<SetStateAction<ServerConnectionSettings>>) {
  setSettings((current) => ({ ...current, users: current.users.map((candidate) => identityKey(candidate) === identityKey(item) ? { ...candidate, [field]: value } : candidate) }));
}

function displayLimit(value: number | null | undefined) { return value == null ? "不限" : value.toLocaleString(); }

function attributionText(attribution: string, user?: string) {
  if (attribution === "core_identity_tuple") return "Core 认证身份与精确连接四元组归属";
  if (attribution === "single_user_inbound") return `旧接口单用户入站：${user ?? "未知用户"}`;
  return "旧接口只能归属到入站端口，不能精确归属用户";
}
