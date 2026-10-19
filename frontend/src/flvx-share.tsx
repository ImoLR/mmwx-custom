import React, { useCallback, useEffect, useMemo, useState } from "react";
import { AlertTriangle, Copy, KeyRound, Plus, RefreshCw, Server, Users } from "lucide-react";
import { CompactSegments, CompactSheet } from "./compact-ui";
import { dispatchCustomAgentAction, fetchCustomReleaseInfo, requestFLVXAdmin } from "./api";
import { flvxHelperIssue, flvxHelperUpgradable, flvxStateText, type FLVXHelper } from "./flvx-share-logic";
import "./flvx-share.css";

type ShareConfig = { port_min: number; port_max: number; traffic_limit_bytes: number; reset_day: number; max_connections: number; share_until: number | null; memory_mb: number; cpu_percent: number };
type ShareStatus = "active" | "paused" | "revoked";
type NodeStatus = { id: string; state: "active" | "error" | "removed" | "pending"; error?: string; up_bytes: number; down_bytes: number; connections: number };
type ShareNode = { id: string; owner: { id: string; name: string }; name: string; protocol: string; port: number; expires_at: number | null; max_connections: number; status: NodeStatus };
type SharedServer = { server_id: number; name: string; hosts: { ipv4: string; ipv6: string; domain: string }; status: ShareStatus; config: ShareConfig; traffic_used_bytes: number; traffic_limit_bytes: number; connections: number; max_connections: number; core: { state: string; version: string }; dual_install: boolean; native_landing?: boolean; nodes: ShareNode[]; report: { collected_at: number; applied_revision: number }; helper?: FLVXHelper };
type AvailableServer = { server_id: number; name: string; hosts: SharedServer["hosts"]; official_ports: number[]; ports_known: boolean; dual_install: boolean; native_landing: boolean; detection_error: boolean; helper?: FLVXHelper };
export type FLVXData = { shares: SharedServer[]; servers: AvailableServer[]; integration: { token_configured: boolean; last_api_at: number } };

const GB = 2 ** 30;
const gb = (bytes: number) => (bytes / GB).toLocaleString("zh-TW", { maximumFractionDigits: 2 });
const limit = (value: number) => value > 0 ? value.toLocaleString() : "不限";
const date = (ms: number | null) => ms ? new Date(ms).toLocaleDateString("zh-TW", { timeZone: "Asia/Taipei" }) : "不限";
const message = (reason: unknown) => reason instanceof Error ? reason.message : "操作失败，请重试";

export function useFLVXSharedServers(token: string, enabled: boolean) {
  const [ids, setIDs] = useState<Set<number>>(new Set());
  useEffect(() => {
    if (!enabled) return;
    let stopped = false;
    const load = async () => { try { const data = await requestFLVXAdmin<FLVXData>(token); if (!stopped) setIDs(new Set(data.shares.filter((s) => s.status !== "revoked").map((s) => s.server_id))); } catch { /* Keep the last successfully read badges. */ } };
    void load(); const timer = window.setInterval(() => { if (document.visibilityState === "visible") void load(); }, 30_000);
    return () => { stopped = true; window.clearInterval(timer); };
  }, [token, enabled]);
  return ids;
}

export function FLVXSharePage({ token }: { token: string }) {
  const [data, setData] = useState<FLVXData | null>(null);
  const [view, setView] = useState<"server" | "user">("server");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [editor, setEditor] = useState<SharedServer | "new" | null>(null);
  const [tokenOpen, setTokenOpen] = useState(false);
  const [cancelShare, setCancelShare] = useState<SharedServer | null>(null);
  const [confirmation, setConfirmation] = useState("");
  const load = useCallback(async () => { try { setData(await requestFLVXAdmin<FLVXData>(token)); setError(""); } catch (reason) { setError(message(reason)); } }, [token]);
  useEffect(() => { void load(); const timer = window.setInterval(() => { if (document.visibilityState === "visible") void load(); }, 15_000); return () => window.clearInterval(timer); }, [load]);
  const changeStatus = async (share: SharedServer, status: ShareStatus) => {
    setBusy(true); setError("");
    try { await requestFLVXAdmin(token, `/shares/${share.server_id}`, "PUT", { status }); setCancelShare(null); await load(); }
    catch (reason) { setError(message(reason)); } finally { setBusy(false); }
  };
  const users = useMemo(() => {
    const grouped = new Map<string, { name: string; rows: { share: SharedServer; node: ShareNode }[] }>();
    for (const share of data?.shares || []) for (const node of share.nodes) { const key = String(node.owner.id); const group = grouped.get(key) || { name: node.owner.name, rows: [] }; group.rows.push({ share, node }); grouped.set(key, group); }
    return [...grouped.entries()];
  }, [data]);
  const connected = Boolean(data?.integration.last_api_at && Date.now() - data.integration.last_api_at < 150_000);
  return <div className="flvx-page">
    <header className="flvx-header"><div><h1>FLVX 共享 <small>{data?.shares.filter((s) => s.status !== "revoked").length || 0}</small></h1><p>共享服务器 · FLVX 用户自建节点</p></div><div className="flvx-actions"><button type="button" onClick={() => setTokenOpen(true)}><KeyRound />接入令牌</button><button className="flvx-primary" type="button" onClick={() => setEditor("new")} disabled={!data}><Plus />共享一台服务器</button></div></header>
    <div className="flvx-connection"><span className={`flvx-dot ${connected ? "online" : ""}`} /><strong>{connected ? "FLVX 已连接" : "等待 FLVX 连接"}</strong><span>{data?.integration.last_api_at ? `最近调用 ${new Date(data.integration.last_api_at).toLocaleString("zh-TW", { timeZone: "Asia/Taipei", hour12: false })}` : "尚未收到 API 调用"}</span><button type="button" onClick={() => void load()} aria-label="刷新共享状态"><RefreshCw /></button></div>
    {error && <p className="flvx-warning" role="alert"><AlertTriangle />{error}</p>}
    <div className="flvx-view"><CompactSegments label="共享分组" value={view} options={[["server", <><Server />按服务器</>], ["user", <><Users />按用户</>]]} onChange={setView} /><span>{users.length} 位 FLVX 用户 · {data?.shares.reduce((n, s) => n + s.nodes.length, 0) || 0} 个节点</span></div>
    {!data && <p className="flvx-empty">{error ? "暂时无法读取共享状态" : "正在读取共享状态…"}</p>}
    {data?.shares.length === 0 && <div className="flvx-empty"><Server /><h2>还没有共享服务器</h2><p>设定可用端口、流量和连接上限，即可交给 FLVX 创建节点。</p><button className="flvx-primary" type="button" onClick={() => setEditor("new")}><Plus />共享一台服务器</button></div>}
    {view === "server" ? <div className="flvx-cards">{data?.shares.map((share) => <ShareCard key={share.server_id} share={share} token={token} busy={busy} onError={setError} onReload={load} onEdit={() => setEditor(share)} onStatus={(status) => void changeStatus(share, status)} onCancel={() => { setCancelShare(share); setConfirmation(""); }} />)}</div> : <div className="flvx-cards">{users.map(([id, user]) => <section className="flvx-card" key={id}><div className="flvx-card-heading"><span className="flvx-avatar">{user.name.slice(0, 1)}</span><div><h2>{user.name}</h2><p>{new Set(user.rows.map((r) => r.share.server_id)).size} 台服务器 · {user.rows.length} 个节点</p></div></div>{[...new Set(user.rows.map((r) => r.share.server_id))].map((serverID) => <details key={serverID} open className="flvx-user-group"><summary>{user.rows.find((r) => r.share.server_id === serverID)!.share.name}</summary><NodeTable nodes={user.rows.filter((r) => r.share.server_id === serverID).map((r) => r.node)} helper={user.rows.find((r) => r.share.server_id === serverID)!.share.helper} /></details>)}</section>)}</div>}
    <p className="flvx-footnote">流量按上下行合计统计，GB = 1024³ 字节。连接上限沿用 Core total 口径，包含入站和出站 TCP 各状态。节点流量上限由 FLVX 管理，当前接口未提供。</p>
    {editor && data && <ShareEditor token={token} share={editor === "new" ? undefined : editor} servers={data.servers} shares={data.shares} onRefresh={load} onClose={() => setEditor(null)} onSaved={async () => { setEditor(null); await load(); }} />}
    {tokenOpen && <TokenDialog token={token} configured={Boolean(data?.integration.token_configured)} onClose={() => setTokenOpen(false)} onChanged={load} />}
    {cancelShare && <CompactSheet title="取消共享" subtitle={cancelShare.name} className="flvx-dialog" onClose={() => !busy && setCancelShare(null)} closeDisabled={busy}>
      <p className="flvx-warning"><AlertTriangle />此服务器上的全部 FLVX 节点将被移除，端口会释放。Helper 确认完成后会删除共享 Core 实例。</p>
      <p>输入服务器名称 <strong>{cancelShare.name}</strong>，再次确认取消。</p><label className="flvx-field">确认服务器名称<input value={confirmation} onChange={(e) => setConfirmation(e.target.value)} autoComplete="off" /></label>
      {error && <p role="alert" className="flvx-warning">{error}</p>}<div className="flvx-dialog-actions"><button type="button" disabled={busy} onClick={() => setCancelShare(null)}>保留共享</button><button type="button" className="flvx-danger" disabled={busy || confirmation !== cancelShare.name} onClick={() => void changeStatus(cancelShare, "revoked")}>{busy ? "正在提交…" : "确认取消共享"}</button></div>
    </CompactSheet>}
  </div>;
}

function ShareCard({ share: s, token, busy, onError, onReload, onEdit, onStatus, onCancel }: { share: SharedServer; token: string; busy: boolean; onError: (text: string) => void; onReload: () => Promise<void>; onEdit: () => void; onStatus: (s: ShareStatus) => void; onCancel: () => void }) {
  const owners = [...new Set(s.nodes.map((n) => String(n.owner.id)))];
  const usedPorts = s.status === "revoked" && (s.core.state === "stopped" || s.core.state === "helper_outdated") ? 0 : new Set(s.nodes.map((n) => n.port)).size;
  const exhausted = s.traffic_limit_bytes > 0 && s.traffic_used_bytes >= s.traffic_limit_bytes;
  const expired = s.config.share_until && s.config.share_until <= Date.now();
  const label = s.status === "revoked" ? "已取消" : s.status === "paused" ? "已暂停" : expired ? "已到期" : exhausted ? "流量已用完" : "共享中";
  const pending = s.status !== "active" && s.nodes.some((n) => n.status.state === "active");
  const helperIssue = s.status === "revoked" ? "" : flvxHelperIssue(s.helper);
  return <article className="flvx-card">
    <div className="flvx-card-heading"><span className="flvx-server-icon"><Server /></span><div><h2>{s.name}</h2><p>{s.hosts.domain || s.hosts.ipv4 || s.hosts.ipv6}</p></div><span className={`flvx-status ${s.status === "active" && !expired && !exhausted ? "online" : ""}`}>{label}</span></div>
    {s.dual_install && <p className="flvx-inline-warning"><AlertTriangle />此服务器也安装了 FLVX 节点程序</p>}
    {s.native_landing && <p className="flvx-warning">FLVX 已启用原生落地，禁止共享。</p>}
    {helperIssue && <p className="flvx-inline-warning" role="status"><AlertTriangle />{helperIssue}，FLVX 节点不会生效。</p>}
    <div className="flvx-metrics"><div><span>流量</span><strong>{gb(s.traffic_used_bytes)} <small>/ {s.traffic_limit_bytes ? `${gb(s.traffic_limit_bytes)} GB` : "不限 GB"}</small></strong><div className={`flvx-meter ${exhausted ? "exhausted" : ""}`} role="progressbar" aria-label={`${s.name} 共享流量`} aria-valuenow={s.traffic_limit_bytes ? Math.min(100, s.traffic_used_bytes / s.traffic_limit_bytes * 100) : 0} aria-valuemin={0} aria-valuemax={100}><i style={{ width: `${s.traffic_limit_bytes ? Math.min(100, s.traffic_used_bytes / s.traffic_limit_bytes * 100) : 0}%` }} /></div><small>每月 {s.config.reset_day} 日重置 · 台湾时间</small></div><div><span>连接</span><strong>{s.connections.toLocaleString()} <small>/ {limit(s.max_connections)}</small></strong><small>{flvxStateText(s.core.state)}{s.core.version && ` · ${s.core.version}`}</small></div></div>
    <div className="flvx-card-facts"><span>端口 {s.config.port_min}–{s.config.port_max} <b>已用 {usedPorts}</b></span><span>{owners.length} 位用户 · {s.nodes.length} 个节点</span><span>共享至 {date(s.config.share_until)}</span></div>
    {pending && <p className="flvx-inline-warning">已提交，等待 Helper 确认停用节点。</p>}
    <div className="flvx-card-actions">{helperIssue && flvxHelperUpgradable(s.helper) && <HelperUpgradeButton token={token} serverID={s.server_id} helper={s.helper!} onError={onError} onDone={onReload} />}<button type="button" disabled={busy || s.status === "revoked"} onClick={onEdit}>设置</button>{s.status !== "revoked" && <><button type="button" disabled={busy || Boolean(s.native_landing)} onClick={() => onStatus(s.status === "paused" ? "active" : "paused")}>{s.status === "paused" ? "恢复" : "暂停共享"}</button><button type="button" className="flvx-text-danger" disabled={busy} onClick={onCancel}>取消共享</button></>}</div>
    {owners.length ? <details className="flvx-nodes"><summary>FLVX 用户与节点 <span>{s.nodes.length}</span></summary>{owners.map((id) => { const nodes = s.nodes.filter((n) => String(n.owner.id) === id); return <details className="flvx-user-group" key={id} open><summary><span className="flvx-avatar">{nodes[0].owner.name.slice(0, 1)}</span>{nodes[0].owner.name}<small>{nodes.length} 个节点</small></summary><NodeTable nodes={nodes} helper={s.helper} /></details>; })}</details> : <p className="flvx-no-nodes">等待 FLVX 用户创建节点</p>}
  </article>;
}

function NodeTable({ nodes, helper }: { nodes: ShareNode[]; helper?: FLVXHelper }) {
  return <table className="flvx-node-table"><thead><tr>{["名称", "协议", "端口", "已用 / 上限 GB", "连接", "状态", "到期"].map((title) => <th key={title}>{title}</th>)}</tr></thead><tbody>{nodes.map((node) => <tr key={node.id}><td className="flvx-node-name">{node.name}</td><td data-label="协议">{node.protocol === "ss2022" ? "SS2022" : "VLESS Reality Vision"}</td><td data-label="端口">{node.port}</td><td data-label="已用 / 上限 GB" title="节点流量上限由 FLVX 管理，v1 接口未提供">{gb(node.status.up_bytes + node.status.down_bytes)} / <span className="flvx-muted">未提供</span></td><td data-label="连接">{node.status.connections} / {limit(node.max_connections)}</td><td data-label="状态" title={node.status.error}><span className={`flvx-status ${node.status.state === "active" ? "online" : ""}`}>{flvxStateText(node.status.state, helper)}</span>{node.status.error && <small className="flvx-node-error">{node.status.error}</small>}</td><td data-label="到期">{date(node.expires_at)}</td></tr>)}</tbody></table>;
}

function availableRange(ports: number[]): [number, number] {
  const occupied = new Set(ports || []);
  for (let start = 30000; start <= 64536; start += 1000) if (Array.from({ length: 1000 }, (_, i) => start + i).every((p) => !occupied.has(p))) return [start, start + 999];
  for (let start = 1024; start <= 64536; start += 1000) if (Array.from({ length: 1000 }, (_, i) => start + i).every((p) => !occupied.has(p))) return [start, start + 999];
  const free = Array.from({ length: 64512 }, (_, i) => i + 1024).find((p) => !occupied.has(p));
  return free ? [free, free] : [0, 0];
}

function ShareEditor({ token, share, servers, shares, onRefresh, onClose, onSaved }: { token: string; share?: SharedServer; servers: AvailableServer[]; shares: SharedServer[]; onRefresh: () => Promise<void>; onClose: () => void; onSaved: () => Promise<void> }) {
  const [serverID, setServerID] = useState(share?.server_id || 0);
  const [config, setConfig] = useState<ShareConfig>(share?.config || { port_min: 30000, port_max: 30999, traffic_limit_bytes: 0, reset_day: 1, max_connections: 0, share_until: null, memory_mb: 256, cpu_percent: 50 });
  const [trafficGB, setTrafficGB] = useState(share ? share.config.traffic_limit_bytes / GB : 0);
  const [expiry, setExpiry] = useState(share?.config.share_until ? new Date(share.config.share_until + 8 * 3600_000).toISOString().slice(0, 10) : "");
  const [error, setError] = useState(""); const [busy, setBusy] = useState(false);
  const server = servers.find((s) => s.server_id === serverID);
  const helperIssue = flvxHelperIssue(server?.helper);
  // A Helper without the FLVX runtime would accept the share and never apply it.
  const unavailable = !server || server.native_landing || server.detection_error || !server.ports_known || (!share && Boolean(helperIssue));
  const select = (id: number) => { setServerID(id); const s = servers.find((s) => s.server_id === id); if (s) { const [port_min, port_max] = availableRange(s.official_ports); setConfig((c) => ({ ...c, port_min, port_max })); } };
  const setNumber = (field: keyof ShareConfig, value: string) => setConfig((c) => ({ ...c, [field]: Number(value) }));
  const save = async (e: React.FormEvent) => { e.preventDefault(); if (busy || unavailable) return; setBusy(true); setError(""); try { await requestFLVXAdmin(token, `/shares/${serverID}`, "PUT", { ...config, traffic_limit_bytes: Math.floor(trafficGB * GB), share_until: expiry ? Date.parse(`${expiry}T23:59:59+08:00`) : null, status: share?.status || "active" }); await onSaved(); } catch (reason) { setError(message(reason)); } finally { setBusy(false); } };
  const collisions = server?.official_ports.filter((p) => p >= config.port_min && p <= config.port_max) || [];
  return <CompactSheet title={share ? "共享设置" : "共享一台服务器"} subtitle="仅设置 FLVX 的可用资源与共享上限" className="flvx-dialog" onClose={() => !busy && onClose()} closeDisabled={busy}>
    <form className="flvx-form" onSubmit={save}>
      <label className="flvx-field">服务器<select value={serverID} disabled={Boolean(share)} required onChange={(e) => select(Number(e.target.value))}><option value="0">选择服务器</option>{servers.map((s) => <option key={s.server_id} value={s.server_id} disabled={!share && shares.some((v) => v.server_id === s.server_id)}>{s.name}{s.native_landing ? " · 已启用 FLVX 落地，禁止共享" : flvxHelperIssue(s.helper) ? " · Helper 需升级" : s.dual_install ? " · 同时装有 FLVX" : ""}{!share && shares.some((v) => v.server_id === s.server_id) ? " · 已有共享记录" : ""}</option>)}</select></label>
      {server?.native_landing ? <p className="flvx-warning"><AlertTriangle />FLVX 已在这台服务器启用原生落地，不能共享。</p> : server?.dual_install ? <p className="flvx-warning"><AlertTriangle />这台服务器也装了 FLVX 节点程序。尚未启用原生落地，可以共享。</p> : null}
      {server && (server.detection_error || !server.ports_known) && <p className="flvx-warning">暂时无法核对地址或官方入站端口，请稍后重试。</p>}
      {server && helperIssue && <div className="flvx-warning flvx-helper-warning" role="status"><AlertTriangle /><span>{helperIssue}，{share ? "FLVX 节点不会生效。" : "升级后才能共享。"}</span>{flvxHelperUpgradable(server.helper) ? <HelperUpgradeButton token={token} serverID={server.server_id} helper={server.helper!} onError={setError} onDone={onRefresh} /> : <span>请先在服务管理中安装或首次升级 Helper。</span>}</div>}
      <div className="flvx-form-row"><label className="flvx-field">端口起始<input type="number" min={1024} max={65535} required value={config.port_min} onChange={(e) => setNumber("port_min", e.target.value)} /></label><label className="flvx-field">端口结束<input type="number" min={config.port_min} max={65535} required value={config.port_max} onChange={(e) => setNumber("port_max", e.target.value)} /></label></div>
      <p className="flvx-hint">选择服务器时自动避开官方入站端口。{collisions.length ? `该范围内有 ${collisions.length} 个官方端口，FLVX 不可使用。` : "保存节点时还会再次核对端口占用。"}</p>
      <div className="flvx-form-row"><label className="flvx-field">总流量上限 GB<input type="number" min={0} max={8388607} step="any" required value={trafficGB} onChange={(e) => setTrafficGB(Number(e.target.value))} /><small>0 表示不限</small></label><label className="flvx-field">每月重置日<input type="number" min={1} max={31} required value={config.reset_day} onChange={(e) => setNumber("reset_day", e.target.value)} /><small>台湾时间，月末自动取最后一天</small></label></div>
      <label className="flvx-field">总连接数上限<input type="number" min={0} max={10000000} required value={config.max_connections} onChange={(e) => setNumber("max_connections", e.target.value)} /><small>0 表示不限；一个代理会话通常占入站、出站两个 TCP socket</small></label>
      <label className="flvx-field">共享到期<select value={expiry ? "date" : "none"} onChange={(e) => setExpiry(e.target.value === "none" ? "" : new Date(Date.now() + 30 * 86400_000).toISOString().slice(0, 10))}><option value="none">不限</option><option value="date">指定日期</option></select></label>
      {expiry && <label className="flvx-field">共享截止日期<input type="date" required value={expiry} onChange={(e) => setExpiry(e.target.value)} /><small>当日 23:59:59（台湾时间）</small></label>}
      <details className="flvx-advanced"><summary>高级 · 资源上限</summary><div className="flvx-form-row"><label className="flvx-field">内存 MB<input type="number" min={64} max={4096} required value={config.memory_mb} onChange={(e) => setNumber("memory_mb", e.target.value)} /></label><label className="flvx-field">CPU %<input type="number" min={1} max={400} required value={config.cpu_percent} onChange={(e) => setNumber("cpu_percent", e.target.value)} /></label></div></details>
      {error && <p className="flvx-warning" role="alert">{error}</p>}<div className="flvx-dialog-actions"><button type="button" disabled={busy} onClick={onClose}>取消</button><button className="flvx-primary" type="submit" disabled={busy || unavailable}>{busy ? "正在保存…" : share ? "保存设置" : "确认共享"}</button></div>
    </form>
  </CompactSheet>;
}

// The same helper.update dispatch as the 服务管理 Helper panel.
function HelperUpgradeButton({ token, serverID, helper, onError, onDone }: { token: string; serverID: number; helper: FLVXHelper; onError: (text: string) => void; onDone: () => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const upgrade = async () => {
    setBusy(true); onError("");
    try {
      const info = await fetchCustomReleaseInfo(token);
      const version = info.release.latest_helper_version, artifact = info.release.helper_artifacts?.[helper.architecture || "amd64"];
      if (!version || !artifact) throw new Error("最新版本缓存缺少当前架构的 Helper 升级产物");
      await dispatchCustomAgentAction(token, serverID, "helper.update", { ...artifact, version, activate: false });
      await onDone();
    } catch (reason) { onError(message(reason)); } finally { setBusy(false); }
  };
  return <button type="button" disabled={busy || helper.upgrading} onClick={() => void upgrade()}>{helper.upgrading ? "Helper 升级中…" : busy ? "正在下发升级…" : "升级 Helper"}</button>;
}

function TokenDialog({ token, configured, onClose, onChanged }: { token: string; configured: boolean; onClose: () => void; onChanged: () => Promise<void> }) {
  const [secret, setSecret] = useState(""); const [busy, setBusy] = useState(false); const [error, setError] = useState(""); const [copied, setCopied] = useState(false);
  const change = async (method: "POST" | "DELETE") => { setBusy(true); setError(""); try { const result = await requestFLVXAdmin<{ token: string }>(token, "/token", method); setSecret(result.token); setCopied(false); await onChanged(); } catch (reason) { setError(message(reason)); } finally { setBusy(false); } };
  return <CompactSheet title="FLVX 接入令牌" className="flvx-dialog" onClose={() => !busy && onClose()} closeDisabled={busy}>
    <p>将此令牌填入 FLVX 的妙妙屋接入设置。令牌只在生成时显示一次，请妥善保存。</p><label className="flvx-field">Custom API 地址<input readOnly value={`${location.origin}/api/custom/flvx/v1`} /></label>
    {secret ? <div className="flvx-token"><code>{secret}</code><button type="button" onClick={async () => { try { await navigator.clipboard.writeText(secret); setCopied(true); } catch { setError("复制失败，请手动选择并保存令牌"); } }}><Copy />{copied ? "已复制" : "复制令牌"}</button></div> : <p className="flvx-hint">{configured ? "已配置接入令牌，旧令牌不能再次查看。" : "当前没有有效的接入令牌。"}</p>}
    {configured && <p className="flvx-warning"><AlertTriangle />重新生成或撤销后，旧令牌立即失效，FLVX 将无法继续同步。现有共享可在本页暂停或取消。</p>}
    {error && <p className="flvx-warning" role="alert">{error}</p>}<div className="flvx-dialog-actions">{configured && <button type="button" className="flvx-text-danger" disabled={busy} onClick={() => void change("DELETE")}>撤销令牌</button>}<button type="button" className="flvx-primary" disabled={busy} onClick={() => void change("POST")}>{busy ? "正在处理…" : configured ? "重新生成令牌" : "生成接入令牌"}</button></div>
  </CompactSheet>;
}
