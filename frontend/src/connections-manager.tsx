import { useCallback, useEffect, useMemo, useState } from "react";
import type { Dispatch, SetStateAction } from "react";
import { AlertTriangle, CheckCircle2, RefreshCw, Save } from "lucide-react";
import { fetchDetailedConnections, updateDetailedConnectionSettings } from "./api";
import type { DetailedConnectionResponse, RemoteServer, ServerConnectionSettings, UserConnectionSettings } from "./types";

type Props = {
  server: RemoteServer;
  token: string;
};

const emptySettings: ServerConnectionSettings = {
  default_close_wait_timeout_seconds: null,
  online_ip_grace_period_seconds: 30,
  users: [],
};

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
      setNotice("设置已持久化，Helper 将自动下发并重新上报真实状态");
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
    if (snapshot?.proxy_users.length) return snapshot.proxy_users;
    return settings.users.map((item) => ({
      identity: item.identity,
      inbound_tag: item.identity.inbound_tag,
      user: item.identity.user,
      inbound_port: undefined,
      inbound_active: 0,
      outbound_active: 0,
      outbound_new_rate: 0,
      outbound_new_total: 0,
      outbound_rejected_total: 0,
      max_inbound_online_ips: item.max_inbound_online_ips,
      max_outbound_tcp_active: item.max_outbound_tcp_active,
      max_outbound_tcp_new_per_second: item.max_outbound_tcp_new_per_second,
      close_wait_timeout_seconds: item.close_wait_timeout_seconds,
      source: "persisted_settings",
    }));
  }, [settings.users, snapshot?.proxy_users]);

  return (
    <div className="service-dialog-body connection-manager">
      <div className="connection-manager-toolbar">
        <div>
          <strong>{server.name}</strong>
          <span>{data?.available ? `Helper ${data.record?.helper_version ?? "在线"}` : "Helper 数据不可用"}</span>
        </div>
        <button type="button" onClick={() => void load()} disabled={loading} aria-label="刷新连接详情" title="刷新连接详情">
          <RefreshCw className={loading ? "spin" : ""} />
        </button>
      </div>

      {error && <div className="connection-message error"><AlertTriangle /> <span>{error}</span></div>}
      {notice && <div className="connection-message success"><CheckCircle2 /> <span>{notice}</span></div>}

      <section className="connection-section">
        <div className="connection-section-head">
          <h4>系统 TCP</h4>
          <span className="connection-source">Linux /proc/net/tcp*</span>
        </div>
        <div className="connection-stat-grid">
          <ConnectionStat label="TCP 总数" value={snapshot?.system.tcp_total} />
          <ConnectionStat label="ESTABLISHED" value={snapshot?.system.established} />
          <ConnectionStat label="TIME_WAIT" value={snapshot?.system.time_wait} />
          <ConnectionStat label="CLOSE_WAIT" value={snapshot?.system.close_wait} />
          <ConnectionStat label="SYN_SENT" value={snapshot?.system.syn_sent} />
          <ConnectionStat label="SYN_RECV" value={snapshot?.system.syn_recv} />
        </div>
      </section>

      <section className="connection-section">
        <div className="connection-section-head">
          <h4>入站与在线 IP</h4>
          <span className="connection-source">系统端口统计</span>
        </div>
        {snapshot?.inbounds.length ? snapshot.inbounds.map((inbound) => (
          <article className="connection-inbound" key={`${inbound.inbound_tag}-${inbound.port}`}>
            <div className="connection-card-title">
              <div><strong>{inbound.inbound_tag || `端口 ${inbound.port}`}</strong><span>{inbound.protocol || "协议未知"} · {inbound.port}</span></div>
              <em>{inbound.online_ip_count} / {displayLimit(inbound.max_online_ips)} IP</em>
            </div>
            <div className="connection-inline-stats">
              <span>ESTABLISHED <strong>{inbound.established}</strong></span>
              <span>TIME_WAIT <strong>{inbound.time_wait}</strong></span>
              <span>CLOSE_WAIT <strong>{inbound.close_wait}</strong></span>
            </div>
            <span className="connection-attribution">{inbound.attribution === "single_user_inbound" ? `单用户入站：${inbound.user}` : "仅可归属到入站端口，不能精确归属用户"}</span>
            {inbound.online_ips.length > 0 && (
              <div className="connection-ip-list">
                {inbound.online_ips.map((item) => <div key={item.ip}><code>{item.ip}</code><strong>{item.connections}</strong></div>)}
              </div>
            )}
          </article>
        )) : <EmptyConnectionState text={snapshot?.core.available ? "当前没有可归属的入站连接" : "Custom Core 接口尚未连接"} />}
      </section>

      <section className="connection-section">
        <div className="connection-section-head">
          <h4>代理用户出站</h4>
          <span className="connection-source">Custom Core 物理 TCP Dial</span>
        </div>
        {proxyUsers.length ? proxyUsers.map((user) => {
          const item = settingsByIdentity.get(`${user.inbound_tag}\u0000${user.user}`) ?? fromProxyUser(user);
          return (
            <article className="connection-user" key={`${user.inbound_tag}-${user.user}`}>
              <div className="connection-card-title">
                <div><strong>{user.user}</strong><span>{user.inbound_tag}{user.inbound_port ? ` · ${user.inbound_port}` : ""}</span></div>
                <em>{user.outbound_active} / {displayLimit(item.max_outbound_tcp_active)}</em>
              </div>
              <div className="connection-inline-stats">
                <span>入站 <strong>{user.inbound_active}</strong></span>
                <span>出站 NEW <strong>{user.outbound_new_rate}/s</strong></span>
                <span>拒绝 <strong>{user.outbound_rejected_total}</strong></span>
              </div>
              <div className="connection-limit-grid">
                <NullableNumber label="同时在线 IP" value={item.max_inbound_online_ips} min={1} onChange={(value) => patchUser(item, "max_inbound_online_ips", value, setSettings)} />
                <NullableNumber label="出站 TCP active" value={item.max_outbound_tcp_active} min={1} onChange={(value) => patchUser(item, "max_outbound_tcp_active", value, setSettings)} />
                <NullableNumber label="出站 NEW/s" value={item.max_outbound_tcp_new_per_second} min={1} onChange={(value) => patchUser(item, "max_outbound_tcp_new_per_second", value, setSettings)} />
                <NullableNumber label="CLOSE_WAIT 秒" value={item.close_wait_timeout_seconds} min={0} placeholder="继承" onChange={(value) => patchUser(item, "close_wait_timeout_seconds", value, setSettings)} />
              </div>
            </article>
          );
        }) : <EmptyConnectionState text="尚无经过认证的代理用户运行数据" />}
      </section>

      <section className="connection-section connection-global-settings">
        <div className="connection-section-head"><h4>全局设置</h4><span className="connection-source">Helper 持久化</span></div>
        <div className="connection-limit-grid">
          <NullableNumber label="默认 CLOSE_WAIT 秒" value={settings.default_close_wait_timeout_seconds} min={0} onChange={(value) => setSettings((current) => ({ ...current, default_close_wait_timeout_seconds: value }))} />
          <NullableNumber label="在线 IP 保留秒数" value={settings.online_ip_grace_period_seconds} min={1} required onChange={(value) => setSettings((current) => ({ ...current, online_ip_grace_period_seconds: value ?? 30 }))} />
        </div>
        <button className="connection-save" type="button" onClick={() => void save()} disabled={saving || loading}>
          <Save /> <span>{saving ? "保存中..." : "保存连接设置"}</span>
        </button>
      </section>

      <p className="connection-boundary">TIME_WAIT 为系统/入站端口数据；用户出站仅显示 Custom Core 可准确归属的应用持有连接。Mux 启用后物理连接数不等于逻辑流数。</p>
    </div>
  );
}

function ConnectionStat({ label, value }: { label: string; value?: number }) {
  return <div><span>{label}</span><strong>{value ?? "--"}</strong></div>;
}

function EmptyConnectionState({ text }: { text: string }) {
  return <div className="connection-empty">{text}</div>;
}

function NullableNumber({ label, value, min, placeholder = "不限", required, onChange }: { label: string; value: number | null; min: number; placeholder?: string; required?: boolean; onChange: (value: number | null) => void }) {
  return (
    <label><span>{label}</span><input type="number" min={min} step="1" required={required} value={value ?? ""} placeholder={placeholder} onChange={(event) => onChange(event.target.value === "" ? null : Number(event.target.value))} /></label>
  );
}

function identityKey(item: UserConnectionSettings) {
  return `${item.identity.inbound_tag}\u0000${item.identity.user}`;
}

function fromProxyUser(user: NonNullable<DetailedConnectionResponse["record"]>["snapshot"]["proxy_users"][number]): UserConnectionSettings {
  return {
    identity: { inbound_tag: user.inbound_tag, user: user.user },
    max_inbound_online_ips: user.max_inbound_online_ips,
    max_outbound_tcp_active: user.max_outbound_tcp_active,
    max_outbound_tcp_new_per_second: user.max_outbound_tcp_new_per_second,
    close_wait_timeout_seconds: user.close_wait_timeout_seconds,
  };
}

function mergeDiscoveredUsers(settings: ServerConnectionSettings, response: DetailedConnectionResponse) {
  const users = new Map(settings.users.map((item) => [identityKey(item), item]));
  for (const user of response.record?.snapshot.proxy_users ?? []) {
    const item = fromProxyUser(user);
    if (!users.has(identityKey(item))) users.set(identityKey(item), item);
  }
  return { ...settings, users: [...users.values()] };
}

function patchUser(item: UserConnectionSettings, field: keyof Omit<UserConnectionSettings, "identity">, value: number | null, setSettings: Dispatch<SetStateAction<ServerConnectionSettings>>) {
  setSettings((current) => ({
    ...current,
    users: current.users.map((candidate) => identityKey(candidate) === identityKey(item) ? { ...candidate, [field]: value } : candidate),
  }));
}

function displayLimit(value: number | null | undefined) {
  return value == null ? "不限" : value.toLocaleString();
}
