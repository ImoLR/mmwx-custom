import { useCallback, useEffect, useRef, useState } from "react";
import type { Dispatch, SetStateAction } from "react";
import { AlertTriangle, CheckCircle2, RefreshCw, Save, Trash2 } from "lucide-react";
import {
  assignConnectionPort,
  deleteConnectionPortAssignment,
  fetchDetailedConnections,
  updateDetailedConnectionSettings,
} from "./api";
import type {
  DetailedConnectionResponse,
  RemoteServer,
  ServerConnectionSettings,
  TCPStateCounts,
} from "./types";

type Props = { server: RemoteServer; token: string };
type AssignmentDraft = { username: string; identity: string };

const emptySettings: ServerConnectionSettings = {
  default_close_wait_timeout_seconds: null,
  online_ip_grace_period_seconds: 30,
  global_total_limit_enabled: false,
  max_global_total_connections: null,
  max_global_inbound_connections: null,
  users: [],
  ports: [],
  management_users: [],
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
  const [assignmentDrafts, setAssignmentDrafts] = useState<Record<string, AssignmentDraft>>({});
  const dirtyRef = useRef(false);

  const load = useCallback(async (signal?: AbortSignal, forceSettings = false) => {
    try {
      const response = await fetchDetailedConnections(token, server.id, signal);
      setData(response);
      if (forceSettings || !dirtyRef.current) setSettings(normalizeSettings(response.settings));
      if (response.ownership_error) setError(response.ownership_error);
      else setError("");
    } catch (reason) {
      if (!signal?.aborted) setError(reason instanceof Error ? reason.message : "连接详情加载失败");
    } finally {
      if (!signal?.aborted) setLoading(false);
    }
  }, [server.id, token]);

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal, true);
    const timer = window.setInterval(() => void load(controller.signal), 5000);
    return () => { controller.abort(); window.clearInterval(timer); };
  }, [load]);

  const updateSettings: Dispatch<SetStateAction<ServerConnectionSettings>> = (next) => {
    dirtyRef.current = true;
    setSettings(next);
  };

  async function save() {
    setSaving(true);
    setError("");
    setNotice("");
    try {
      const response = await updateDetailedConnectionSettings(token, server.id, settings);
      dirtyRef.current = false;
      setData(response);
      setSettings(normalizeSettings(response.settings));
      setNotice("设置已持久化并将自动下发；达到限额只拒绝新连接，不中断现有连接");
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "保存连接限制失败");
    } finally {
      setSaving(false);
    }
  }

  async function saveAssignment(inboundTag: string, requireIdentity = false) {
    const draft = assignmentDrafts[inboundTag] ?? { username: data?.management?.assignable_users?.[0] ?? "", identity: "" };
    if (!draft.username) { setError("请选择管理用户"); return; }
    if (requireIdentity && !draft.identity) { setError("该端口已有部分身份被分配，请明确选择剩余协议身份"); return; }
    setSaving(true);
    setError("");
    try {
      await assignConnectionPort(token, server.id, { inbound_tag: inboundTag, management_username: draft.username, protocol_identity: draft.identity || undefined });
      setNotice("用户关系已追加；运行时统计和限流仍只使用唯一可确定的真实协议身份");
      await load(undefined, true);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "保存手工归属失败");
    } finally {
      setSaving(false);
    }
  }

  async function removeAssignment(inboundTag: string, username: string, identity: string) {
    setSaving(true);
    setError("");
    try {
      await deleteConnectionPortAssignment(token, server.id, { inbound_tag: inboundTag, management_username: username, protocol_identity: identity || undefined });
      setNotice("手工归属已删除");
      await load(undefined, true);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "删除手工归属失败");
    } finally {
      setSaving(false);
    }
  }

  const snapshot = data?.record?.snapshot;
  const managementUsers = data?.management?.users ?? [];

  return (
    <div className="service-dialog-body connection-manager">
      <div className="connection-manager-toolbar">
        <div>
          <strong>{server.name}</strong>
          <span>{data?.available ? `Helper ${data.record?.helper_version ?? "在线"} · Core API v${snapshot?.core.interface_version ?? "?"}` : "Helper 数据不可用"}</span>
        </div>
        <button type="button" onClick={() => void load(undefined, !dirtyRef.current)} disabled={loading} aria-label="刷新连接详情" title="刷新连接详情"><RefreshCw className={loading ? "spin" : ""} /></button>
      </div>

      {error && <div className="connection-message error"><AlertTriangle /> <span>{error}</span></div>}
      {notice && <div className="connection-message success"><CheckCircle2 /> <span>{notice}</span></div>}
      {data?.management?.warnings?.map((warning) => <div className="connection-message error" key={warning}><AlertTriangle /><span>{warning}</span></div>)}

      <details className="connection-section connection-fold">
        <summary><span>机器 TCP</span><small>总数 {snapshot?.system?.tcp_total ?? "--"} · ESTABLISHED {snapshot?.system?.established ?? "--"}</small></summary>
        <div className="connection-fold-body"><TCPStateGrid states={snapshot?.system} /></div>
      </details>

      <section className="connection-section connection-global-settings">
        <div className="connection-section-head"><h4>全局保护与设置</h4><span className="connection-source">Helper 持久化</span></div>
        <div className="connection-global-summary">
          <span>当前总连接 <strong>{snapshot?.global?.current_total ?? "--"}</strong></span>
          <span>全局限额拒绝 <strong>{snapshot?.global?.rejected_global_total_limit ?? "--"}</strong></span>
          <span>当前入站逻辑连接 <strong>{snapshot?.global?.current_inbound ?? "--"}</strong></span>
          <span>入站限额拒绝 <strong>{snapshot?.global?.rejected_global_inbound_limit ?? "--"}</strong></span>
        </div>
        <label className="connection-toggle">
          <input type="checkbox" checked={settings.global_total_limit_enabled} onChange={(event) => updateSettings((current) => ({ ...current, global_total_limit_enabled: event.target.checked }))} />
          <span><strong>启用全局总连接保护</strong><small>达到上限后只拒绝新连接，不清理现有连接</small></span>
        </label>
        <div className="connection-limit-grid">
          <NullableNumber label="全局总连接上限" value={settings.max_global_total_connections} min={1} required={settings.global_total_limit_enabled} onChange={(value) => updateSettings((current) => ({ ...current, max_global_total_connections: value }))} />
          <NullableNumber label="服务器入站总连接上限" value={settings.max_global_inbound_connections} min={0} onChange={(value) => updateSettings((current) => ({ ...current, max_global_inbound_connections: value }))} />
          <NullableNumber label="默认 CLOSE_WAIT 自动关闭（秒）" value={settings.default_close_wait_timeout_seconds} min={0} onChange={(value) => updateSettings((current) => ({ ...current, default_close_wait_timeout_seconds: value }))} />
          <NullableNumber label="在线 IP 保留秒数" value={settings.online_ip_grace_period_seconds} min={1} required onChange={(value) => updateSettings((current) => ({ ...current, online_ip_grace_period_seconds: value ?? 30 }))} />
        </div>
      </section>

      <section className="connection-section connection-user-tree">
        <div className="connection-section-head"><h4>管理用户</h4><span className="connection-source">正式关系优先，默认折叠</span></div>
        {managementUsers.length ? managementUsers.map((user) => (
          <details className="connection-fold connection-user-fold" key={user.username}>
            <summary>
              <span>{user.username}<small>{sourceText(user.source)} · {user.ports.length} 个端口</small><small>入站逻辑 {user.aggregate.inbound_current}/{displayLimit(managementSetting(settings, user.username).max_inbound_connections)} · 在线 IP {user.aggregate.inbound_online_ips?.length ?? 0}/{displayLimit(managementSetting(settings, user.username).max_inbound_online_ips)} · 出站 {user.aggregate.outbound_active}/{displayLimit(managementSetting(settings, user.username).max_outbound_tcp_active)}</small></span>
              <strong>{user.aggregate.current_total} 总连接</strong>
            </summary>
            <div className="connection-fold-body">
              <div className="connection-inline-stats">
                <span>入站逻辑连接 <strong>{user.aggregate.inbound_current}</strong></span>
                <span>出站 active <strong>{user.aggregate.outbound_active}</strong></span>
                <span>pending <strong>{user.aggregate.outbound_pending}</strong></span>
                <span>NEW <strong>{user.aggregate.outbound_new_rate}/s</strong></span>
                <span>在线 IP <strong>{user.aggregate.inbound_online_ips?.length ?? 0}</strong></span>
              </div>
              <div className="connection-rejection-grid" aria-label="管理用户拒绝原因">
                <span>用户总数 <strong>{user.aggregate.rejected_user_total_limit}</strong></span>
                <span>用户 NEW/s <strong>{user.aggregate.rejected_user_new_rate_limit}</strong></span>
                <span>端口总数 <strong>{user.aggregate.rejected_port_total_limit}</strong></span>
                <span>端口 NEW/s <strong>{user.aggregate.rejected_port_new_rate_limit}</strong></span>
                <span>用户入站 <strong>{user.aggregate.rejected_user_inbound_limit}</strong></span>
                <span>端口入站 <strong>{user.aggregate.rejected_port_inbound_limit}</strong></span>
                <span>用户在线 IP <strong>{user.aggregate.rejected_user_online_ip_limit}</strong></span>
                <span>端口在线 IP <strong>{user.aggregate.rejected_port_online_ip_limit}</strong></span>
                <span>全局入站 <strong>{user.aggregate.rejected_global_inbound_limit}</strong></span>
                <span>全局总数 <strong>{user.aggregate.rejected_global_total_limit}</strong></span>
              </div>
              <div className="connection-limit-grid">
                <NullableNumber label="用户入站连接上限" value={managementSetting(settings, user.username).max_inbound_connections} min={0} onChange={(value) => patchManagementUser(user.username, "max_inbound_connections", value, updateSettings)} />
                <NullableNumber label="用户在线 IP 上限" value={managementSetting(settings, user.username).max_inbound_online_ips} min={0} onChange={(value) => patchManagementUser(user.username, "max_inbound_online_ips", value, updateSettings)} />
                <NullableNumber label="该管理用户出站 active 总上限" value={managementSetting(settings, user.username).max_outbound_tcp_active} min={1} onChange={(value) => patchManagementUser(user.username, "max_outbound_tcp_active", value, updateSettings)} />
                <NullableNumber label="该管理用户出站 NEW/s 总上限" value={managementSetting(settings, user.username).max_outbound_tcp_new_per_second} min={1} onChange={(value) => patchManagementUser(user.username, "max_outbound_tcp_new_per_second", value, updateSettings)} />
              </div>
              <div className="connection-port-list">
                {user.ports.map((port) => (
                  <PortDetails key={`${user.username}-${port.inbound_tag}`} user={user.username} port={port} settings={settings} setSettings={updateSettings} onRemoveAssignment={removeAssignment} assignableUsers={data?.management?.assignable_users ?? []} assignmentDraft={assignmentDrafts[port.inbound_tag]} setAssignmentDraft={(draft) => setAssignmentDrafts((current) => ({ ...current, [port.inbound_tag]: draft }))} onAssign={(requireIdentity) => saveAssignment(port.inbound_tag, requireIdentity)} saving={saving} />
                ))}
              </div>
            </div>
          </details>
        )) : <EmptyConnectionState text={snapshot?.core.available ? "当前没有可归属的管理用户连接" : "Custom Core 接口尚未连接"} />}
      </section>

      {(data?.management?.unassigned_ports?.length ?? 0) > 0 && (
        <section className="connection-section">
          <div className="connection-section-head"><h4>未归属身份 / 端口</h4><span className="connection-source">可追加用户关系</span></div>
          {data!.management.unassigned_ports.map((port) => {
            const draft = assignmentDrafts[port.inbound_tag] ?? { username: data!.management.assignable_users[0] ?? "", identity: "" };
            return <article className="connection-assignment" key={port.inbound_tag}>
              <div className="connection-card-title"><div><strong>{port.inbound_tag}</strong><span>{port.protocol || "协议未知"} · {port.port || "端口未知"}</span></div></div>
              <select aria-label={`${port.inbound_tag} 管理用户`} value={draft.username} onChange={(event) => setAssignmentDrafts((current) => ({ ...current, [port.inbound_tag]: { ...draft, username: event.target.value } }))}>
                <option value="">选择管理用户</option>{data!.management.assignable_users.map((username) => <option key={username} value={username}>{username}</option>)}
              </select>
              {port.protocol_identities.length > 0 && <select aria-label={`${port.inbound_tag} 协议身份`} value={draft.identity} onChange={(event) => setAssignmentDrafts((current) => ({ ...current, [port.inbound_tag]: { ...draft, identity: event.target.value } }))}>
                <option value="">{port.reason === "manual_identity_assignment_incomplete" ? "选择剩余协议身份（必选）" : "整个端口（全部协议身份）"}</option>{port.protocol_identities.map((identity) => <option key={identity} value={identity}>{identity}</option>)}
              </select>}
              <button type="button" onClick={() => void saveAssignment(port.inbound_tag, port.reason === "manual_identity_assignment_incomplete")} disabled={saving || !draft.username}>追加用户关系</button>
            </article>;
          })}
        </section>
      )}

      <button className="connection-save" type="button" onClick={() => void save()} disabled={saving || loading}><Save /> <span>{saving ? "保存中..." : "保存全部连接设置"}</span></button>
      <p className="connection-boundary">管理关系允许同一端口关联多个用户，追加关系不会覆盖已有关系。入站限制在协议认证成功后、路由与出站前，按服务器、管理用户、端口依次检查；用户在线 IP 跨其全部端口去重，端口在线 IP 独立计算。Identity 仅用于真实归属，不再作为管理员限制层；无法区分身份的共享密钥端口不做假拆分。出站限制、TIME_WAIT/CLOSE_WAIT 与精确四元组统计保持独立；Mux 按逻辑连接准入。</p>
    </div>
  );
}

type ManagementPort = DetailedConnectionResponse["management"]["users"][number]["ports"][number];

function PortDetails({ user, port, settings, setSettings, onRemoveAssignment, assignableUsers, assignmentDraft, setAssignmentDraft, onAssign, saving }: {
  user: string;
  port: ManagementPort;
  settings: ServerConnectionSettings;
  setSettings: Dispatch<SetStateAction<ServerConnectionSettings>>;
  onRemoveAssignment: (tag: string, username: string, identity: string) => Promise<void>;
  assignableUsers: string[];
  assignmentDraft?: AssignmentDraft;
  setAssignmentDraft: (draft: AssignmentDraft) => void;
  onAssign: (requireIdentity: boolean) => Promise<void>;
  saving: boolean;
}) {
  const aggregate = port.aggregate;
  const portLimit = portSetting(settings, port.inbound_tag);
  return <details className="connection-fold connection-port-fold">
    <summary>
      <span>{port.inbound_tag}<small>{port.protocol || "协议未知"} · {port.port || "端口未知"} · {sourceText(port.source)}</small><small>入站逻辑 {aggregate.inbound_current}/{displayLimit(portLimit.max_inbound_connections)} · 在线 IP {aggregate.inbound_online_ips?.length ?? 0}/{displayLimit(portLimit.max_inbound_online_ips)} · 出站 {aggregate.outbound_active}/{displayLimit(portLimit.max_outbound_tcp_active)}</small></span>
      <strong>{aggregate.current_total} 总连接</strong>
    </summary>
    <div className="connection-fold-body">
      <div className="connection-inline-stats">
        <span>入站逻辑连接 <strong>{aggregate.inbound_current}</strong></span>
        <span>入站物理 active <strong>{aggregate.inbound_active}</strong></span>
        <span>出站 active <strong>{aggregate.outbound_active}</strong></span>
        <span>pending <strong>{aggregate.outbound_pending}</strong></span>
        <span>NEW <strong>{aggregate.outbound_new_rate}/s</strong></span>
      </div>
      <ConnectionSubsection title="入站 TCP" meta={`在线 IP ${aggregate.inbound_online_ips?.length ?? 0}`} states={aggregate.inbound_tcp} />
      <ConnectionSubsection title="物理出站 TCP" meta={`累计 Dial ${aggregate.outbound_new_total}`} states={aggregate.outbound_tcp} />
      <div className="connection-rejection-grid">
        <span>用户总数 <strong>{aggregate.rejected_user_total_limit}</strong></span>
        <span>用户 NEW/s <strong>{aggregate.rejected_user_new_rate_limit}</strong></span>
        <span>端口总数 <strong>{aggregate.rejected_port_total_limit}</strong></span>
        <span>端口 NEW/s <strong>{aggregate.rejected_port_new_rate_limit}</strong></span>
        <span>用户入站 <strong>{aggregate.rejected_user_inbound_limit}</strong></span>
        <span>端口入站 <strong>{aggregate.rejected_port_inbound_limit}</strong></span>
        <span>用户在线 IP <strong>{aggregate.rejected_user_online_ip_limit}</strong></span>
        <span>端口在线 IP <strong>{aggregate.rejected_port_online_ip_limit}</strong></span>
      </div>
      {aggregate.inbound_online_ips?.length > 0 && <IPList values={aggregate.inbound_online_ips} />}
      <div className="connection-limit-grid">
        <NullableNumber label="端口入站连接上限" value={portLimit.max_inbound_connections} min={0} onChange={(value) => patchPort(port.inbound_tag, "max_inbound_connections", value, setSettings)} />
        <NullableNumber label="端口在线 IP 上限" value={portLimit.max_inbound_online_ips} min={0} onChange={(value) => patchPort(port.inbound_tag, "max_inbound_online_ips", value, setSettings)} />
        <NullableNumber label="该端口出站 active 上限" value={portLimit.max_outbound_tcp_active} min={1} onChange={(value) => patchPort(port.inbound_tag, "max_outbound_tcp_active", value, setSettings)} />
        <NullableNumber label="该端口出站 NEW/s 上限" value={portLimit.max_outbound_tcp_new_per_second} min={1} onChange={(value) => patchPort(port.inbound_tag, "max_outbound_tcp_new_per_second", value, setSettings)} />
      </div>
      {port.protocol_identities.length > 0 && <small className="connection-attribution-note">认证身份 {port.protocol_identities.length} 个，仅用于真实归属与底层调试，不作为限制配置层。</small>}
      <small className="connection-attribution-note">{port.runtime_attributed ? "本用户的统计来自唯一确定的 Core identity" : "此处仅展示管理关系；未把共享或不确定连接拆分到本用户"}</small>
      <AssignmentControls inboundTag={port.inbound_tag} identities={port.protocol_identities} assignableUsers={assignableUsers} draft={assignmentDraft} setDraft={setAssignmentDraft} onAssign={onAssign} saving={saving} />
      {(port.manual_assignments ?? []).map((identity) => <button className="connection-remove-assignment" type="button" key={identity || "tag-only"} onClick={() => void onRemoveAssignment(port.inbound_tag, user, identity)}><Trash2 />删除手工关系{identity ? `：${identity}` : "（整个端口）"}</button>)}
    </div>
  </details>;
}

function AssignmentControls({ inboundTag, identities, assignableUsers, draft, setDraft, onAssign, saving }: {
  inboundTag: string;
  identities: string[];
  assignableUsers: string[];
  draft?: AssignmentDraft;
  setDraft: (draft: AssignmentDraft) => void;
  onAssign: (requireIdentity: boolean) => Promise<void>;
  saving: boolean;
}) {
  const value = draft ?? { username: assignableUsers[0] ?? "", identity: "" };
  return <div className="connection-assignment connection-assignment-inline">
    <strong>追加关联用户</strong>
    <select aria-label={`${inboundTag} 追加管理用户`} value={value.username} onChange={(event) => setDraft({ ...value, username: event.target.value })}>
      <option value="">选择管理用户</option>{assignableUsers.map((username) => <option key={username} value={username}>{username}</option>)}
    </select>
    {identities.length > 0 && <select aria-label={`${inboundTag} 追加协议身份`} value={value.identity} onChange={(event) => setDraft({ ...value, identity: event.target.value })}>
      <option value="">整个端口（仅管理关系；不改写既有真实归属）</option>{identities.map((identity) => <option key={identity} value={identity}>{identity}</option>)}
    </select>}
    <button type="button" onClick={() => void onAssign(false)} disabled={saving || !value.username}>追加关系</button>
  </div>;
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

function ConnectionStat({ label, value }: { label: string; value?: number }) { return <div><span>{label}</span><strong>{value ?? "--"}</strong></div>; }
function EmptyConnectionState({ text }: { text: string }) { return <div className="connection-empty">{text}</div>; }
function IPList({ values }: { values: Array<{ ip: string; connections: number }> }) { return <div className="connection-ip-list">{values.map((item) => <div key={item.ip}><code>{item.ip}</code><strong>{item.connections}</strong></div>)}</div>; }

function NullableNumber({ label, value, min, placeholder = "不限", required, onChange }: { label: string; value: number | null; min: number; placeholder?: string; required?: boolean; onChange: (value: number | null) => void }) {
  return <label><span>{label}</span><input type="number" min={min} step="1" required={required} value={value ?? ""} placeholder={placeholder} onChange={(event) => onChange(event.target.value === "" ? null : Number(event.target.value))} /></label>;
}

function normalizeSettings(settings: ServerConnectionSettings): ServerConnectionSettings {
  return { ...emptySettings, ...settings, users: settings.users ?? [], ports: settings.ports ?? [], management_users: settings.management_users ?? [] };
}

function managementSetting(settings: ServerConnectionSettings, username: string) {
  return settings.management_users.find((item) => item.username === username) ?? { username, max_inbound_connections: null, max_inbound_online_ips: null, max_outbound_tcp_active: null, max_outbound_tcp_new_per_second: null };
}

function patchManagementUser(username: string, field: "max_inbound_connections" | "max_inbound_online_ips" | "max_outbound_tcp_active" | "max_outbound_tcp_new_per_second", value: number | null, setSettings: Dispatch<SetStateAction<ServerConnectionSettings>>) {
  setSettings((current) => {
    const management_users = [...current.management_users];
    const index = management_users.findIndex((item) => item.username === username);
    const item = managementSetting(current, username);
    if (index >= 0) management_users[index] = { ...item, [field]: value };
    else management_users.push({ ...item, [field]: value });
    return { ...current, management_users };
  });
}

function portSetting(settings: ServerConnectionSettings, inboundTag: string) {
  return settings.ports.find((item) => item.inbound_tag === inboundTag) ?? { inbound_tag: inboundTag, max_inbound_connections: null, max_inbound_online_ips: null, max_outbound_tcp_active: null, max_outbound_tcp_new_per_second: null };
}

function patchPort(inboundTag: string, field: "max_inbound_connections" | "max_inbound_online_ips" | "max_outbound_tcp_active" | "max_outbound_tcp_new_per_second", value: number | null, setSettings: Dispatch<SetStateAction<ServerConnectionSettings>>) {
  setSettings((current) => {
    const ports = [...current.ports];
    const index = ports.findIndex((item) => item.inbound_tag === inboundTag);
    const item = portSetting(current, inboundTag);
    if (index >= 0) ports[index] = { ...item, [field]: value };
    else ports.push({ ...item, [field]: value });
    return { ...current, ports };
  });
}

function sourceText(source: string) {
  if (source === "binding") return "正式绑定";
  if (source === "owner") return "节点所有者";
  if (source === "manual") return "手工归属";
  return "未归属";
}

function displayLimit(value: number | null | undefined) { return value == null || value <= 0 ? "不限" : value.toLocaleString(); }
