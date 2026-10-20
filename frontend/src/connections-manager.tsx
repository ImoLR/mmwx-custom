import { useCallback, useEffect, useId, useRef, useState } from "react";
import type { Dispatch, SetStateAction, SyntheticEvent } from "react";
import { AlertTriangle, CheckCircle2, RefreshCw, Save, Trash2 } from "lucide-react";
import {
  assignConnectionPort,
  deleteConnectionPortAssignment,
  fetchDetailedConnections,
  updateDetailedConnectionSettings,
} from "./api";
import type {
  ConnectionMetric,
  DetailedConnectionResponse,
  MachineProtectionSettings,
  RemoteServer,
  ServerConnectionSettings,
  TCPStateCounts,
} from "./types";
import { machineBadge, machineBreakdownRows, machineFLVXText, machineSettingsForSave, machineSettingsForm, machineStateText } from "./machine-protection-logic";

type Props = { server: RemoteServer; token: string };
type AssignmentDraft = { username: string; identity: string };

const emptySettings: ServerConnectionSettings = {
  machine_protection: { enabled: false, max_active: null, max_total: null },
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
  last_ack: 0, closing: 0, close: 0, listen: 0, unknown: 0,
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
      const machineV2 = (data?.record?.snapshot?.machine_protection?.version ?? 0) >= 2;
      const payload = settings.machine_protection ? { ...settings, machine_protection: machineSettingsForSave(settings.machine_protection, machineV2) } : settings;
      const response = await updateDetailedConnectionSettings(token, server.id, payload);
      dirtyRef.current = false;
      setData(response);
      setSettings(normalizeSettings(response.settings));
      setNotice("设置已持久化并开始下发；以 Helper 回报的生效状态为准，达到限额只拒绝新连接，不中断现有连接");
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
  const machine = snapshot?.machine_protection;
  const machineV2 = (machine?.version ?? 0) >= 2;
  const machineSettings = machineSettingsForm(settings.machine_protection);
  const patchMachine = (patch: Partial<MachineProtectionSettings>) => updateSettings((current) => ({ ...current, machine_protection: { ...machineSettingsForm(current.machine_protection), ...patch } }));
  const coreAccounting = data?.core_accounting;
  const coreInboundTCP = (snapshot?.proxy_users ?? []).reduce((total, user) => total + (user.inbound_tcp?.tcp_total ?? 0), 0);
  const coreOutboundTCP = (snapshot?.proxy_users ?? []).reduce((total, user) => total + (user.outbound_tcp?.tcp_total ?? 0), 0);
  const coreCloseWait = (snapshot?.proxy_users ?? []).reduce((total, user) => total + (user.inbound_tcp?.close_wait ?? 0) + (user.outbound_tcp?.close_wait ?? 0), 0);

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
      {notice && <div className="connection-message neutral"><CheckCircle2 /> <span>{notice}</span></div>}
      {data?.management?.warnings?.map((warning) => <div className="connection-message error" key={warning}><AlertTriangle /><span>{warning}</span></div>)}

      <section className="connection-section connection-machine-section">
        <div className="connection-section-head"><div><span className="connection-level">Level 1</span><h4>机器 TCP</h4></div><span className="connection-source">Linux 整机所有进程</span></div>
        <div className="connection-summary-grid">
          <ConnectionStat label="TCP 总数" value={snapshot?.system?.tcp_total} />
          <ConnectionStat label="ESTABLISHED" value={snapshot?.system?.established} />
          <ConnectionStat label="TIME_WAIT" value={snapshot?.system?.time_wait} />
          <ConnectionStat label="CLOSE_WAIT" value={snapshot?.system?.close_wait} />
        </div>
        <p className="connection-definition">Machine Total = /proc/net/tcp + tcp6 全部 TCP 行（含 LISTEN）。各状态仅用于解释 Total，不另造第二种总连接口径。</p>
        <details className="connection-fold connection-state-fold">
          <summary><span>完整 Linux TCP 状态<small>/proc/net/tcp + tcp6，不代表全部属于 Xray</small></span></summary>
          <div className="connection-fold-body"><TCPStateGrid states={snapshot?.system} /></div>
        </details>
        <div className="connection-protection-block">
          <div className="connection-section-head"><h5>机器连接保护</h5><span className="connection-source">整机一个负责方：妙妙屋助手</span></div>
          {!machine || !machine.supported && !machineV2 ? <div className="connection-message neutral"><AlertTriangle /><span>当前 Helper 不支持机器连接保护，需要升级 Helper；现有 Core 统计和限制不受影响。</span></div> : !machineV2 ? <>
            <div className="connection-message neutral machine-outdated"><AlertTriangle /><span>助手版本过旧：整机连接上限、TIME_WAIT 上限和 CLOSE_WAIT 超时需要升级 Helper 后才能设置和生效。</span></div>
            <div className="connection-effective-grid">
              <span>配置状态 <strong>{settings.machine_protection?.enabled ? "已启用（旧版只算 TCP）" : "未启用"}</strong></span>
              <span>受控端口 <strong>{machine.controlled_ports?.join("、") || "--"}</strong></span>
            </div>
          </> : <>
            <div className="connection-effective-grid machine-protection-grid">
              <span>整机连接 <strong>{(machine.connections?.total ?? machine.total).toLocaleString()}{machine.max_total ? ` / ${machine.max_total.toLocaleString()}` : ""}</strong></span>
              <span className={machine.blocking ? "machine-state danger" : ""}>当前动作 <strong>{machine.blocking ? "保护中" : machine.warming_up ? "即将保护" : machineSettings.enabled ? "观察中" : "未启用"}</strong></span>
              <span>已丢弃新连接 <strong>{(machine.dropped_total ?? 0).toLocaleString()}</strong></span>
              <span>受控端口 <strong>TCP {machine.tcp_ports?.length ?? 0} · UDP {machine.udp_ports?.length ?? 0}</strong></span>
            </div>
            <div className="connection-summary-grid machine-breakdown-grid" aria-label="整机连接明细">
              {machineBreakdownRows(machine.connections, machine.dropped_total).slice(0, 5).map(([label, value]) => <ConnectionStat key={label} label={label} value={value} />)}
            </div>
            <p className="connection-definition">{machineStateText({ ...machine, enabled: !!machine.configured?.enabled })}。{machineFLVXText(machine.port_sources)}。</p>
            {!machine.supported && <div className="connection-message neutral"><AlertTriangle /><span>本机没有 nftables：超过上限时无法拒绝新连接，TIME_WAIT 上限和 CLOSE_WAIT 超时照常生效。</span></div>}
            {machine.last_error && machine.supported && <div className="connection-message error"><AlertTriangle /><span>{machine.last_error}</span></div>}
            {machine.tw_buckets?.error && <div className="connection-message error"><AlertTriangle /><span>{machine.tw_buckets.error}</span></div>}
          </>}
          {(machine?.supported || machineV2) && <>
            <label className="connection-toggle">
              <input type="checkbox" checked={machineSettings.enabled} disabled={!machineV2} onChange={(event) => patchMachine({ enabled: event.target.checked })} />
              <span><strong>启用机器连接保护</strong><small>超过上限只拒绝业务端口（妙妙屋 + FLVX）的新连接，已有连接不断开；降到上限的 95% 以下自动恢复。不处理 SSH、面板、Agent、Helper 和本机回环。</small></span>
            </label>
            <div className="connection-limit-grid machine-limit-grid">
              <NullableNumber label="整机连接上限（TCP+UDP）" value={machineSettings.max_total} min={1} disabled={!machineV2} onChange={(value) => patchMachine({ max_total: value })} />
              <NullableNumber label="TIME_WAIT 上限" value={machineSettings.tw_buckets ?? null} min={0} placeholder="0 = 不改内核值" disabled={!machineV2} onChange={(value) => patchMachine({ tw_buckets: value })} />
              <NullableNumber label="CLOSE_WAIT 超时（秒）" value={machineSettings.close_wait_seconds ?? null} min={0} placeholder="0 = 不自动关闭" disabled={!machineV2} onChange={(value) => patchMachine({ close_wait_seconds: value })} />
            </div>
            {machineV2 && <p className="connection-definition machine-field-notes">TIME_WAIT 上限写入内核 tcp_max_tw_buckets，作用于整台机器，关闭保护时恢复原值{machine?.tw_buckets?.original != null ? `（原值 ${machine.tw_buckets.original.toLocaleString()}）` : ""}。CLOSE_WAIT 超时作用于妙妙屋 Core 和 FLVX 共享 Core；下方 Core 设置里单独填了「CLOSE_WAIT 超时关闭」时以那里为准。0 表示该项不启用。</p>}
          </>}
        </div>
      </section>

      <section className="connection-section connection-global-settings">
        <div className="connection-section-head"><div><span className="connection-level">Level 2</span><h4>Xray Core</h4></div><span className="connection-source">仅 Fork Core tracker</span></div>
        <div className="connection-core-accounting">
          <span>用户连接 <strong>{coreAccounting?.inbound_current ?? snapshot?.global?.current_inbound ?? "--"}</strong></span>
          <span>已归属 Management User 套接字 <strong>{coreAccounting?.attributed ?? "--"}</strong></span>
          <span>Core 未归属套接字 <strong>{coreAccounting?.unattributed ?? "--"}</strong></span>
        </div>
        <p className="connection-definition">TCP 套接字 {coreAccounting?.total ?? snapshot?.global?.current_total ?? "--"}（入站 {coreInboundTCP} + 出站 {coreOutboundTCP}，含 TIME_WAIT/CLOSE_WAIT）</p>
        {coreAccounting && !coreAccounting.balanced && <div className="connection-message error"><AlertTriangle /><span>Core tracker 对账差额 {coreAccounting.reconciliation_delta}，未把差额伪装成 pending 或未归属。</span></div>}
        <div className="connection-global-summary">
          <span>当前 CLOSE_WAIT <strong>{coreCloseWait}</strong></span>
          <span>自动关闭秒数 <strong>{displayLimit(settings.default_close_wait_timeout_seconds)}</strong></span>
          <span>全局连接拒绝 <strong>{snapshot?.global?.rejected_global_inbound_limit ?? "--"}</strong></span>
        </div>
        <div className="connection-limit-grid">
          <NullableNumber label="Core 连接上限" value={settings.max_global_inbound_connections} min={1} onChange={(value) => updateSettings((current) => ({ ...current, max_global_inbound_connections: value }))} />
          <NullableNumber label="CLOSE_WAIT 超时关闭（秒）" value={settings.default_close_wait_timeout_seconds} min={0} placeholder="0 / 留空 = 关闭" onChange={(value) => updateSettings((current) => ({ ...current, default_close_wait_timeout_seconds: value }))} />
          <NullableNumber label="在线 IP 保留秒数" value={settings.online_ip_grace_period_seconds} min={1} required onChange={(value) => updateSettings((current) => ({ ...current, online_ip_grace_period_seconds: value ?? 30 }))} />
        </div>
      </section>

      <LegacySocketLimits settings={settings} setSettings={updateSettings} snapshot={snapshot} />

      <section className="connection-section connection-user-tree">
        <div className="connection-section-head"><div><span className="connection-level">Level 3</span><h4>Management User</h4></div><span className="connection-source">正式关系优先，默认折叠</span></div>
        {managementUsers.length ? managementUsers.map((user) => (
          <details className="connection-fold connection-user-fold" key={user.username}>
            <summary>
              <span>{user.username}<small>{sourceText(user.source)} · {user.ports.length} 个 Port</small><small>连接 {user.aggregate.inbound_current}/{displayLimit(managementSetting(settings, user.username).max_inbound_connections)} · 在线 IP {user.aggregate.inbound_online_ips?.length ?? 0}/{displayLimit(managementSetting(settings, user.username).max_inbound_online_ips)} · NEW {user.aggregate.outbound_new_rate}/s/{displayLimit(managementSetting(settings, user.username).max_outbound_tcp_new_per_second)}</small><small>TCP 套接字 {user.aggregate.current_total} = 入站 {user.aggregate.inbound_tcp.tcp_total} + 出站 {user.aggregate.outbound_tcp.tcp_total}</small></span>
              <strong>连接 {user.aggregate.inbound_current}/{displayLimit(managementSetting(settings, user.username).max_inbound_connections)}</strong>
            </summary>
            <div className="connection-fold-body">
              <div className="connection-inline-stats">
                <span>连接 <strong>{user.aggregate.inbound_current}/{displayLimit(managementSetting(settings, user.username).max_inbound_connections)}</strong></span>
                <span>NEW <strong>{user.aggregate.outbound_new_rate}/s</strong></span>
                <span>在线 IP <strong>{user.aggregate.inbound_online_ips?.length ?? 0}</strong></span>
              </div>
              <SocketSummary aggregate={user.aggregate} />
              <div className="connection-rejection-grid" aria-label="管理用户拒绝原因">
                <span>用户连接 <strong>{user.aggregate.rejected_user_inbound_limit}</strong></span>
                <span>用户 NEW/s <strong>{user.aggregate.rejected_user_new_rate_limit}</strong></span>
                <span>端口连接 <strong>{user.aggregate.rejected_port_inbound_limit}</strong></span>
                <span>端口 NEW/s <strong>{user.aggregate.rejected_port_new_rate_limit}</strong></span>
                <span>用户在线 IP <strong>{user.aggregate.rejected_user_online_ip_limit}</strong></span>
                <span>端口在线 IP <strong>{user.aggregate.rejected_port_online_ip_limit}</strong></span>
                <span>全局连接 <strong>{user.aggregate.rejected_global_inbound_limit}</strong></span>
              </div>
              <div className="connection-limit-grid">
                <NullableNumber label="用户连接上限" value={managementSetting(settings, user.username).max_inbound_connections} min={1} onChange={(value) => patchManagementUser(user.username, "max_inbound_connections", value, updateSettings)} />
                <NullableNumber label="用户在线 IP 上限" value={managementSetting(settings, user.username).max_inbound_online_ips} min={0} onChange={(value) => patchManagementUser(user.username, "max_inbound_online_ips", value, updateSettings)} />
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
      <p className="connection-boundary">连接 = 客户端连到本机并通过认证的入站连接（Mux 按逻辑连接算），和「用户」页一致；用户连接数为其各端口连接数之和。TCP 套接字 = 入站+出站，含 TIME_WAIT/CLOSE_WAIT，只用于诊断。机器 TCP = 整机所有程序的 TCP 总数（含 ESTABLISHED、TIME_WAIT 等全部状态），与服务器卡片同一整机口径。达到上限只拒绝新连接，不断开已有连接。CLOSE_WAIT 仍按配置超时关闭，TIME_WAIT 不强清。</p>
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
      <span><em className="connection-level">Level 4 · Port</em>{port.inbound_tag}<small>{port.protocol || "协议未知"} · {port.port || "端口未知"} · {sourceText(port.source)}</small><small>连接 {aggregate.inbound_current}/{displayLimit(portLimit.max_inbound_connections)} · 在线 IP {aggregate.inbound_online_ips?.length ?? 0}/{displayLimit(portLimit.max_inbound_online_ips)}</small><small>TCP 套接字 {aggregate.current_total} = 入站 {aggregate.inbound_tcp.tcp_total} + 出站 {aggregate.outbound_tcp.tcp_total}</small></span>
      <strong>连接 {aggregate.inbound_current}/{displayLimit(portLimit.max_inbound_connections)}</strong>
    </summary>
    <div className="connection-fold-body">
      <div className="connection-inline-stats">
        <span>连接 <strong>{aggregate.inbound_current}/{displayLimit(portLimit.max_inbound_connections)}</strong></span>
        <span>NEW <strong>{aggregate.outbound_new_rate}/s</strong></span>
      </div>
      <SocketSummary aggregate={aggregate} />
      <ConnectionSubsection title="入站 TCP 套接字" meta={`在线 IP ${aggregate.inbound_online_ips?.length ?? 0}`} states={aggregate.inbound_tcp} />
      <ConnectionSubsection title="出站 TCP 套接字" meta={`累计 Dial ${aggregate.outbound_new_total}`} states={aggregate.outbound_tcp} />
      <div className="connection-rejection-grid">
        <span>用户连接 <strong>{aggregate.rejected_user_inbound_limit}</strong></span>
        <span>用户 NEW/s <strong>{aggregate.rejected_user_new_rate_limit}</strong></span>
        <span>端口连接 <strong>{aggregate.rejected_port_inbound_limit}</strong></span>
        <span>端口 NEW/s <strong>{aggregate.rejected_port_new_rate_limit}</strong></span>
        <span>用户在线 IP <strong>{aggregate.rejected_user_online_ip_limit}</strong></span>
        <span>端口在线 IP <strong>{aggregate.rejected_port_online_ip_limit}</strong></span>
        <span>全局连接 <strong>{aggregate.rejected_global_inbound_limit}</strong></span>
      </div>
      {aggregate.inbound_online_ips?.length > 0 && <IPList values={aggregate.inbound_online_ips} />}
      <div className="connection-limit-grid">
        <NullableNumber label="端口连接上限" value={portLimit.max_inbound_connections} min={1} onChange={(value) => patchPort(port.inbound_tag, "max_inbound_connections", value, setSettings)} />
        <NullableNumber label="端口在线 IP 上限" value={portLimit.max_inbound_online_ips} min={0} onChange={(value) => patchPort(port.inbound_tag, "max_inbound_online_ips", value, setSettings)} />
        <NullableNumber label="该端口出站 NEW/s 上限" value={portLimit.max_outbound_tcp_new_per_second} min={1} onChange={(value) => patchPort(port.inbound_tag, "max_outbound_tcp_new_per_second", value, setSettings)} />
      </div>
      {port.protocol_identities.length > 0 && <small className="connection-attribution-note">认证身份 {port.protocol_identities.length} 个，仅用于真实归属与底层调试，不作为限制配置层。</small>}
      <small className="connection-attribution-note">{port.runtime_attributed ? "本用户的统计来自唯一确定的 Core identity" : "此处仅展示管理关系；未把共享或不确定连接拆分到本用户"}</small>
      <AssignmentControls inboundTag={port.inbound_tag} identities={port.protocol_identities} assignableUsers={assignableUsers} draft={assignmentDraft} setDraft={setAssignmentDraft} onAssign={onAssign} saving={saving} />
      {(port.manual_assignments ?? []).map((identity) => <button className="connection-remove-assignment" type="button" key={identity || "tag-only"} onClick={() => void onRemoveAssignment(port.inbound_tag, user, identity)}><Trash2 />删除手工关系{identity ? `：${identity}` : "（整个端口）"}</button>)}
    </div>
  </details>;
}

function SocketSummary({ aggregate }: { aggregate: Pick<ManagementPort["aggregate"], "current_total" | "inbound_tcp" | "outbound_tcp"> }) {
  return <p className="connection-definition">TCP 套接字 {aggregate.current_total} = 入站 {aggregate.inbound_tcp.tcp_total} + 出站 {aggregate.outbound_tcp.tcp_total}</p>;
}

function LegacySocketLimits({ settings, setSettings, snapshot }: {
  settings: ServerConnectionSettings;
  setSettings: Dispatch<SetStateAction<ServerConnectionSettings>>;
  snapshot?: NonNullable<DetailedConnectionResponse["record"]>["snapshot"];
}) {
  const limits: Array<{ label: string; value: number | null; clear: () => void }> = [];
  if (settings.global_total_limit_enabled || settings.max_global_total_connections != null) {
    limits.push({ label: `Core（${settings.global_total_limit_enabled ? "已启用" : "未启用"}）`, value: settings.max_global_total_connections, clear: () => setSettings((current) => ({ ...current, global_total_limit_enabled: false, max_global_total_connections: null })) });
  }
  for (const user of settings.management_users) {
    if (user.max_total_connections != null) limits.push({ label: `用户 ${user.username}`, value: user.max_total_connections, clear: () => patchManagementUser(user.username, "max_total_connections", null, setSettings) });
  }
  for (const port of settings.ports) {
    if (port.max_total_connections != null) limits.push({ label: `端口 ${port.inbound_tag}`, value: port.max_total_connections, clear: () => patchPort(port.inbound_tag, "max_total_connections", null, setSettings) });
  }
  for (const user of settings.users) {
    if (user.max_total_connections != null) limits.push({ label: `协议身份 ${user.identity.inbound_tag} / ${user.identity.user}`, value: user.max_total_connections, clear: () => setSettings((current) => ({ ...current, users: current.users.map((item) => item.identity.inbound_tag === user.identity.inbound_tag && item.identity.user === user.identity.user ? { ...item, max_total_connections: null } : item) })) });
  }
  const counters: Array<[string, number]> = [["全局套接字拒绝", snapshot?.global?.rejected_global_total_limit ?? 0]];
  const addCounters = (label: string, aggregate: Partial<ManagementPort["aggregate"]>) => {
    counters.push(
      [`${label} · 用户旧总数拒绝`, aggregate.rejected_user_total_limit ?? 0],
      [`${label} · 用户套接字拒绝`, aggregate.rejected_user_combined_limit ?? 0],
      [`${label} · 端口旧总数拒绝`, aggregate.rejected_port_total_limit ?? 0],
      [`${label} · 端口套接字拒绝`, aggregate.rejected_port_combined_limit ?? 0],
      [`${label} · 全局套接字拒绝`, aggregate.rejected_global_total_limit ?? 0],
    );
  };
  for (const user of snapshot?.management_groups ?? []) addCounters(`用户 ${user.group}`, user);
  for (const user of snapshot?.proxy_users ?? []) addCounters(`${user.inbound_tag} / ${user.user}`, user);
  const rejected = counters.filter(([, value]) => value > 0);
  if (!limits.length && !rejected.length) return null;
  return <details className="connection-fold connection-legacy-limits">
    <summary><span>旧的 TCP 套接字上限（入站+出站）<small>保留原值；清除后需保存全部连接设置</small></span></summary>
    <div className="connection-fold-body">
      {limits.map(({ label, value, clear }) => <div className="connection-inline-stats" key={label}><span>{label} <strong>{value ?? "不限"}</strong></span><button type="button" onClick={clear}>清除 {label}</button></div>)}
      <div className="connection-rejection-grid">{rejected.map(([label, value]) => <span key={label}>{label} <strong>{value}</strong></span>)}</div>
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
    ["LAST_ACK", states?.last_ack], ["CLOSING", states?.closing], ["CLOSE", states?.close], ["LISTEN", states?.listen], ["UNKNOWN", states?.unknown],
  ];
  return <div className={`connection-stat-grid${compact ? " compact" : ""}`}>{values.map(([label, value]) => <ConnectionStat key={label} label={label} value={value} />)}</div>;
}

function ConnectionSubsection({ title, meta, states }: { title: string; meta: string; states?: Partial<TCPStateCounts> }) {
  return <div className="connection-subsection"><div><strong>{title}</strong><span>{meta}</span></div><TCPStateGrid states={states} compact /></div>;
}

function ConnectionStat({ label, value }: { label: string; value?: number }) { return <div><span>{label}</span><strong>{value ?? "--"}</strong></div>; }
function EmptyConnectionState({ text }: { text: string }) { return <div className="connection-empty">{text}</div>; }
function IPList({ values }: { values: Array<{ ip: string; connections: number }> }) { return <div className="connection-ip-list">{values.map((item) => <div key={item.ip}><code>{item.ip}</code><strong>{item.connections}</strong></div>)}</div>; }

function NullableNumber({ label, value, min, placeholder = "不限", required, disabled, onChange }: { label: string; value: number | null; min: number; placeholder?: string; required?: boolean; disabled?: boolean; onChange: (value: number | null) => void }) {
  return <label><span>{label}</span><input type="number" min={min} step="1" required={required} disabled={disabled} value={value ?? ""} placeholder={placeholder} onChange={(event) => onChange(event.target.value === "" ? null : Number(event.target.value))} /></label>;
}

/** Server card badge: the whole-machine 连接数, 「N/上限」 once protection is on, with a hover breakdown. */
export function MachineConnectionTag({ metric, onOpen }: { metric?: ConnectionMetric; onOpen: () => void }) {
  const badge = machineBadge(metric);
  const machine = metric?.machine;
  const tooltipID = useId();
  // Cards clip their overflow, so the breakdown floats at viewport coordinates.
  const [anchor, setAnchor] = useState<{ top?: number; bottom?: number; left: number } | null>(null);
  const show = (event: SyntheticEvent<HTMLElement>) => {
    const rect = event.currentTarget.getBoundingClientRect();
    const left = Math.max(8, Math.min(rect.left, window.innerWidth - 328));
    // Open upwards when the badge sits near the bottom of the screen.
    const below = window.innerHeight - rect.bottom;
    setAnchor(below < 250 && rect.top > below ? { bottom: window.innerHeight - rect.top + 6, left } : { top: rect.bottom + 6, left });
  };
  useEffect(() => {
    if (!anchor) return;
    const hide = () => setAnchor(null);
    window.addEventListener("scroll", hide, true);
    return () => window.removeEventListener("scroll", hide, true);
  }, [anchor]);
  return <span className="connection-tag-wrap" onMouseEnter={show} onMouseLeave={() => setAnchor(null)} onFocus={show} onBlur={() => setAnchor(null)}>
    <button className={`service-badge ${badge.tone} connection-tag${badge.protecting ? " protecting" : ""}`} type="button" onClick={onOpen} aria-label={`查看具体连接数，${badge.label}`} aria-describedby={metric?.available ? tooltipID : undefined}>
      <span className="connection-tag-icon" aria-hidden="true">🔌</span>
      <span className="connection-tag-value">{badge.text}</span>
      {badge.protecting && <span className="connection-tag-protecting">保护中</span>}
    </button>
    {metric?.available && <span className={`machine-breakdown${anchor ? " open" : ""}`} role="tooltip" id={tooltipID} style={anchor ?? undefined}>
      <span className="machine-breakdown-title">整机连接 {badge.text}<small>TCP + UDP，全部状态</small></span>
      {machine ? <>
        <span className="machine-breakdown-rows">{machineBreakdownRows(machine.connections, machine.dropped_total).map(([label, value]) => <span key={label}><em>{label}</em><strong>{value == null ? "--" : value.toLocaleString()}</strong></span>)}</span>
        <span className="machine-breakdown-note">{machineFLVXText(machine.port_sources)}</span>
        <span className={`machine-breakdown-note${machine.blocking ? " danger" : ""}`}>{machineStateText(machine)}</span>
      </> : <span className="machine-breakdown-note">{machineStateText(undefined)}</span>}
    </span>}
  </span>;
}

function normalizeSettings(settings: ServerConnectionSettings): ServerConnectionSettings {
  return { ...emptySettings, ...settings, machine_protection: settings.machine_protection ?? emptySettings.machine_protection, users: settings.users ?? [], ports: settings.ports ?? [], management_users: settings.management_users ?? [] };
}

function managementSetting(settings: ServerConnectionSettings, username: string) {
  return settings.management_users.find((item) => item.username === username) ?? { username, max_total_connections: null, max_inbound_connections: null, max_inbound_online_ips: null, max_outbound_tcp_active: null, max_outbound_tcp_new_per_second: null };
}

function patchManagementUser(username: string, field: "max_total_connections" | "max_inbound_connections" | "max_inbound_online_ips" | "max_outbound_tcp_active" | "max_outbound_tcp_new_per_second", value: number | null, setSettings: Dispatch<SetStateAction<ServerConnectionSettings>>) {
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
  return settings.ports.find((item) => item.inbound_tag === inboundTag) ?? { inbound_tag: inboundTag, max_total_connections: null, max_inbound_connections: null, max_inbound_online_ips: null, max_outbound_tcp_active: null, max_outbound_tcp_new_per_second: null };
}

function patchPort(inboundTag: string, field: "max_total_connections" | "max_inbound_connections" | "max_inbound_online_ips" | "max_outbound_tcp_active" | "max_outbound_tcp_new_per_second", value: number | null, setSettings: Dispatch<SetStateAction<ServerConnectionSettings>>) {
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
