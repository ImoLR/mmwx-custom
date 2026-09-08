import React, { useCallback, useEffect, useMemo, useState } from "react";
import {
  AlertTriangle,
  ArrowDown,
  ArrowRight,
  ArrowUp,
  Check,
  Edit3,
  Link2,
  LoaderCircle,
  Plus,
  RefreshCw,
  Save,
  Search,
  Server,
  Share2,
  Trash2,
  Unlink,
  X,
} from "lucide-react";
import {
  createForwardChain,
  createForwardChainNode,
  createForwardGroup,
  deleteForwardChain,
  fetchForwardCertificates,
  fetchForwardChains,
  fetchForwardGroups,
  fetchForwardNodes,
  fetchForwardServers,
  probeForwardServers,
  probeForwardTargets,
  updateForwardChain,
  updateForwardChainGroups,
  updateForwardGroup,
} from "./api";
import type {
  ForwardBalanceStrategy,
  ForwardBoundNode,
  ForwardChain,
  ForwardGroup,
  RemoteServer,
  ValidCertificate,
  XrayNode,
} from "./types";

type Notice = { tone: "success" | "error" | "info"; text: string } | null;
type Latency = { state: "loading" | "ok" | "error"; ms?: number; method?: string };
type GroupRole = "entry" | "middle" | "exit";
type GroupDraft = {
  key: string;
  backendId?: number;
  role: GroupRole;
  name: string;
  strategy: ForwardBalanceStrategy;
  serverIds: number[];
};

const PORT_START = 50520;
const PORT_END = 51314;
const strategies: Array<{ value: ForwardBalanceStrategy; label: string; help: string }> = [
  { value: "round_robin", label: "轮询", help: "按顺序轮换服务器，不参考权重或当前负载。" },
  { value: "least_conn", label: "最少连接", help: "优先选择当前活动连接数最少的服务器。" },
  { value: "percentage", label: "按剩余流量", help: "根据服务器剩余流量动态分配权重，每 5 分钟重新计算。" },
  { value: "cycle", label: "按周期", help: "根据服务器距离流量重置日的时间动态分配，每 5 分钟重新计算。" },
  { value: "sticky", label: "连接保持（源 IP 哈希）", help: "同一源 IP 固定到同一服务器；用于中间组时看到的是上游转发机 IP。" },
];

export function ForwardManagementPage({ token }: { token: string }) {
  const [chains, setChains] = useState<ForwardChain[]>([]);
  const [groups, setGroups] = useState<ForwardGroup[]>([]);
  const [servers, setServers] = useState<RemoteServer[]>([]);
  const [certificates, setCertificates] = useState<ValidCertificate[]>([]);
  const [nodes, setNodes] = useState<XrayNode[]>([]);
  const [issues, setIssues] = useState<Record<string, unknown>>({});
  const [latencies, setLatencies] = useState<Record<string, Latency>>({});
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState<Notice>(null);
  const [editor, setEditor] = useState<ForwardChain | "new" | null>(null);
  const [nodeDialog, setNodeDialog] = useState<ForwardChain | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<ForwardChain | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [chainResult, groupResult, serverResult, certificateResult, nodeResult] = await Promise.all([
        fetchForwardChains(token),
        fetchForwardGroups(token),
        fetchForwardServers(token),
        fetchForwardCertificates(token).catch(() => ({ certificates: [] })),
        fetchForwardNodes(token).catch(() => ({ nodes: [] })),
      ]);
      setChains(chainResult.chains ?? []);
      setGroups(groupResult.groups ?? []);
      setServers(serverResult.servers ?? []);
      setCertificates(certificateResult.certificates ?? []);
      setNodes(nodeResult.nodes ?? []);
      setIssues((chainResult.issues ?? {}) as Record<string, unknown>);
      setNotice(null);
    } catch (error) {
      setNotice({ tone: "error", text: messageOf(error, "读取转发配置失败") });
    } finally {
      setLoading(false);
    }
  }, [token]);

  useEffect(() => { void load(); }, [load]);

  const groupById = useMemo(() => new Map(groups.map((group) => [group.id, group])), [groups]);
  const serverById = useMemo(() => new Map(servers.map((server) => [server.id, server])), [servers]);

  const probe = useCallback(async () => {
    const jobs: Array<Promise<void>> = [];
    for (const chain of chains) {
      const hops = sortedHops(chain);
      for (let index = 0; index < hops.length - 1; index += 1) {
        const left = groupById.get(hops[index].group_id)?.members ?? [];
        const right = groupById.get(hops[index + 1].group_id)?.members?.[0]?.server_id;
        if (!right) continue;
        for (const member of left) {
          if (!member.server_id || member.server_id === right) continue;
          const key = `${chain.id}:server:${member.server_id}:${right}`;
          setLatencies((current) => ({ ...current, [key]: { state: "loading" } }));
          jobs.push(probeForwardServers(token, member.server_id, right).then((result) => {
            const sample = result.results?.[0];
            const ms = numberOrUndefined(result.latency_ms ?? sample?.latency_ms);
            setLatencies((current) => ({ ...current, [key]: ms == null || result.success === false || sample?.success === false
              ? { state: "error" }
              : { state: "ok", ms, method: result.method ?? sample?.method } }));
          }).catch(() => setLatencies((current) => ({ ...current, [key]: { state: "error" } }))));
        }
      }
      const exitServer = groupById.get(hops[hops.length - 1]?.group_id ?? -1)?.members?.[0]?.server_id;
      if (!exitServer) continue;
      for (const boundNode of chain.bound_nodes ?? []) {
        if (!boundNode.terminus_addr) continue;
        const key = `${chain.id}:target:${boundNode.node_id}`;
        setLatencies((current) => ({ ...current, [key]: { state: "loading" } }));
        jobs.push(probeForwardTargets(token, exitServer, [boundNode.terminus_addr]).then((result) => {
          const sample = result.results?.[0];
          const ms = numberOrUndefined(result.latency_ms ?? sample?.latency_ms);
          setLatencies((current) => ({ ...current, [key]: ms == null || result.success === false || sample?.success === false
            ? { state: "error" }
            : { state: "ok", ms, method: result.method ?? sample?.method } }));
        }).catch(() => setLatencies((current) => ({ ...current, [key]: { state: "error" } }))));
      }
    }
    await Promise.all(jobs);
  }, [chains, groupById, token]);

  useEffect(() => {
    if (!chains.length || !groups.length) return;
    void probe();
    const timer = window.setInterval(() => void probe(), 30_000);
    return () => window.clearInterval(timer);
  }, [chains.length, groups.length, probe]);

  async function removeChain() {
    if (!deleteTarget || busy) return;
    setBusy(`delete-${deleteTarget.id}`);
    try {
      await deleteForwardChain(token, deleteTarget.id);
      setDeleteTarget(null);
      setNotice({ tone: "success", text: `转发链“${deleteTarget.name}”已删除` });
      await load();
    } catch (error) {
      setNotice({ tone: "error", text: messageOf(error, "删除转发链失败") });
    } finally {
      setBusy("");
    }
  }

  return (
    <div className="forward-manager">
      <section className="forward-hero">
        <div>
          <h1>转发管理</h1>
          <p>配置入口、中间与出口服务器，管理转发链节点和链路状态。</p>
        </div>
        <div className="forward-head-actions">
          <button type="button" onClick={() => void load()} disabled={loading} aria-label="刷新转发配置" title="刷新">
            <RefreshCw className={loading ? "spin" : ""} />
          </button>
          <button className="primary" type="button" onClick={() => setEditor("new")}><Plus />创建转发链</button>
        </div>
      </section>

      {notice && <div className={`forward-notice ${notice.tone}`}><span>{notice.text}</span><button type="button" onClick={() => setNotice(null)} aria-label="关闭提示"><X /></button></div>}

      {loading ? (
        <div className="forward-empty"><LoaderCircle className="spin" /><span>正在读取转发链...</span></div>
      ) : chains.length === 0 ? (
        <div className="forward-empty"><Share2 /><strong>还没有转发链</strong><span>点击“创建转发链”开始配置。</span></div>
      ) : (
        <section className="forward-chain-list">
          {chains.map((chain) => {
            const hops = sortedHops(chain);
            const chainIssues = issueText(issues[String(chain.id)] ?? issues[chain.name]);
            return <article className="forward-chain-card" key={chain.id}>
              <header>
                <div><h2>{chain.name}</h2><p>端口 {chain.port_range_start ?? PORT_START}–{chain.port_range_end ?? PORT_END}</p></div>
                <span className="forward-code">#{chain.id}</span>
              </header>
              {(chain.dns_domain || chain.dns_domain_v6) && <div className="forward-domains">
                {chain.dns_domain && <span>A · {chain.dns_domain}</span>}
                {chain.dns_domain_v6 && <span>AAAA · {chain.dns_domain_v6}</span>}
              </div>}
              <div className="forward-path" aria-label={`${chain.name} 转发路径`}>
                {!hops.length && <p className="forward-no-hops">这条转发链还没有配置路径</p>}
                {hops.map((hop, index) => {
                  const group = groupById.get(hop.group_id);
                  const role = index === 0 ? "入口" : index === hops.length - 1 ? "出口" : "中间";
                  const members = group?.members ?? [];
                  const nextServer = index < hops.length - 1 ? groupById.get(hops[index + 1].group_id)?.members?.[0]?.server_id : undefined;
                  const memberLatencies = nextServer ? members.map((member) => latencies[`${chain.id}:server:${member.server_id}:${nextServer}`]).filter(Boolean) : [];
                  return <React.Fragment key={`${hop.group_id}-${hop.order}`}>
                    <div className="forward-hop">
                      <small>{role}</small>
                      <strong>{group?.name || hop.group_name || `组 ${hop.group_id}`}</strong>
                      <div className="forward-hop-members">{members.length ? members.map((member) => <span key={member.server_id}>{serverById.get(member.server_id)?.name || `服务器 ${member.server_id}`}{nextServer && member.server_id !== nextServer && <LatencyBadge value={latencies[`${chain.id}:server:${member.server_id}:${nextServer}`]} />}</span>) : <span>落地节点</span>}</div>
                      {members.length > 1 && <em>{strategyLabel(group?.balance_strategy)}</em>}
                    </div>
                    {index < hops.length - 1 && <LatencyArrow value={averageLatency(memberLatencies)} />}
                  </React.Fragment>;
                })}
              </div>
              <div className="forward-bound-list">
                <div><strong>关联节点（{chain.bound_nodes?.length ?? 0}）</strong><span>链路末端</span></div>
                {(chain.bound_nodes ?? []).length ? (chain.bound_nodes ?? []).map((node) => <div className="forward-bound-node" key={node.node_id}>
                  <span><Link2 />{node.node_name || `节点 ${node.node_id}`}</span>
                  <span>{node.port ? `:${node.port}` : "自动端口"}</span>
                  {node.terminus_addr && <LatencyBadge value={latencies[`${chain.id}:target:${node.node_id}`]} />}
                </div>) : <p>还没有绑定转发链节点</p>}
              </div>
              {chainIssues && <div className="forward-issue"><AlertTriangle />{chainIssues}</div>}
              <footer>
                <button type="button" onClick={() => setEditor(chain)}><Edit3 />编辑链路</button>
                <button type="button" onClick={() => setNodeDialog(chain)}><Plus />添加链节点</button>
                <button className="danger" type="button" onClick={() => setDeleteTarget(chain)}><Trash2 />删除</button>
              </footer>
            </article>;
          })}
        </section>
      )}

      {editor && <ForwardChainEditor
        token={token}
        chain={editor === "new" ? undefined : editor}
        groups={groups}
        servers={servers}
        certificates={certificates}
        nodes={nodes}
        onClose={() => setEditor(null)}
        onSaved={async (text) => { setEditor(null); setNotice({ tone: "success", text }); await load(); }}
      />}
      {nodeDialog && <ForwardNodeDialog
        token={token}
        chain={nodeDialog}
        groups={groups}
        nodes={nodes}
        onClose={() => setNodeDialog(null)}
        onSaved={async (text) => { setNodeDialog(null); setNotice({ tone: "success", text }); await load(); }}
      />}
      {deleteTarget && <div className="forward-dialog-layer" role="presentation" onMouseDown={() => setDeleteTarget(null)}>
        <section className="forward-confirm" role="alertdialog" aria-modal="true" onMouseDown={(event) => event.stopPropagation()}>
          <AlertTriangle />
          <h2>删除转发链？</h2>
          <p>确认删除“{deleteTarget.name}”？{(deleteTarget.bound_nodes?.length ?? 0) > 0 ? ` 此操作还会删除关联的 ${deleteTarget.bound_nodes?.length} 个落地节点及其转发规则。` : ""}</p>
          <div><button type="button" onClick={() => setDeleteTarget(null)}>取消</button><button className="danger" type="button" disabled={Boolean(busy)} onClick={() => void removeChain()}><Trash2 />确认删除</button></div>
        </section>
      </div>}
    </div>
  );
}

function ForwardChainEditor({ token, chain, groups, servers, certificates, nodes, onClose, onSaved }: {
  token: string;
  chain?: ForwardChain;
  groups: ForwardGroup[];
  servers: RemoteServer[];
  certificates: ValidCertificate[];
  nodes: XrayNode[];
  onClose: () => void;
  onSaved: (text: string) => Promise<void>;
}) {
  const editing = Boolean(chain);
  const initial = useMemo(() => makeDrafts(chain, groups), [chain, groups]);
  const initialCertificate = useMemo(() => matchCertificate(chain?.dns_domain || chain?.dns_domain_v6 || "", certificates) || (!chain ? preferredCertificate(certificates) : undefined), [chain, certificates]);
  const [name, setName] = useState(chain?.name ?? "");
  const [portStart, setPortStart] = useState(String(chain?.port_range_start ?? PORT_START));
  const [portEnd, setPortEnd] = useState(String(chain?.port_range_end ?? PORT_END));
  const [drafts, setDrafts] = useState<GroupDraft[]>(initial);
  const [certId, setCertId] = useState(initialCertificate?.id ? String(initialCertificate.id) : "");
  const [domainPrefix, setDomainPrefix] = useState(domainPrefixFor(chain?.dns_domain ?? "", initialCertificate?.domain ?? ""));
  const [domainPrefixV6, setDomainPrefixV6] = useState(domainPrefixFor(chain?.dns_domain_v6 ?? "", initialCertificate?.domain ?? ""));
  const [exitNodeId, setExitNodeId] = useState("");
  const [entryPort, setEntryPort] = useState(() => String(randomPort(chain?.port_range_start ?? PORT_START, chain?.port_range_end ?? PORT_END)));
  const [search, setSearch] = useState("");
  const [targetGroup, setTargetGroup] = useState(initial[0]?.key ?? "");
  const [editorLatencies, setEditorLatencies] = useState<Record<string, Latency>>({});
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const assigned = useMemo(() => new Set(drafts.flatMap((draft) => draft.serverIds)), [drafts]);
  const available = useMemo(() => servers.filter((server) => !assigned.has(server.id) && server.name.toLowerCase().includes(search.trim().toLowerCase())), [assigned, search, servers]);
  const selectableNodes = useMemo(() => nodes.filter((node) => node.node_type !== "routed"), [nodes]);
  const selectedCertificate = certificates.find((certificate) => String(certificate.id) === certId);

  useEffect(() => {
    const timer = window.setTimeout(() => {
      for (let index = 0; index < drafts.length - 1; index += 1) {
        const from = drafts[index].serverIds[0];
        const to = drafts[index + 1].serverIds[0];
        const key = `${drafts[index].key}:${drafts[index + 1].key}`;
        if (!from || !to) {
          setEditorLatencies((current) => { const next = { ...current }; delete next[key]; return next; });
          continue;
        }
        setEditorLatencies((current) => ({ ...current, [key]: { state: "loading" } }));
        void probeForwardServers(token, from, to).then((result) => {
          const sample = result.results?.[0];
          const ms = numberOrUndefined(result.latency_ms ?? sample?.latency_ms);
          setEditorLatencies((current) => ({ ...current, [key]: ms == null || result.success === false || sample?.success === false
            ? { state: "error" }
            : { state: "ok", ms, method: result.method ?? sample?.method } }));
        }).catch(() => setEditorLatencies((current) => ({ ...current, [key]: { state: "error" } })));
      }
    }, 400);
    return () => window.clearTimeout(timer);
  }, [drafts, token]);

  function updateDraft(key: string, patch: Partial<GroupDraft>) {
    setDrafts((current) => current.map((draft) => draft.key === key ? { ...draft, ...patch } : draft));
  }

  function addServer(serverId: number, groupKey = targetGroup) {
    if (!groupKey) return;
    setDrafts((current) => current.map((draft) => ({
      ...draft,
      serverIds: draft.key === groupKey ? [...draft.serverIds.filter((id) => id !== serverId), serverId] : draft.serverIds.filter((id) => id !== serverId),
    })));
    if (drafts.find((draft) => draft.key === groupKey)?.role === "exit") setExitNodeId("");
  }

  function moveMember(groupKey: string, index: number, direction: -1 | 1) {
    setDrafts((current) => current.map((draft) => {
      if (draft.key !== groupKey) return draft;
      const next = [...draft.serverIds];
      const target = index + direction;
      if (target < 0 || target >= next.length) return draft;
      [next[index], next[target]] = [next[target], next[index]];
      return { ...draft, serverIds: next };
    }));
  }

  function addMiddle() {
    const exitIndex = drafts.findIndex((draft) => draft.role === "exit");
    const next = [...drafts];
    const key = `new-${Date.now()}`;
    next.splice(Math.max(1, exitIndex), 0, { key, role: "middle", name: `中间 ${drafts.filter((draft) => draft.role === "middle").length + 1}`, strategy: "round_robin", serverIds: [] });
    setDrafts(next);
    setTargetGroup(key);
  }

  function moveMiddle(index: number, direction: -1 | 1) {
    const target = index + direction;
    if (target <= 0 || target >= drafts.length - 1) return;
    const next = [...drafts];
    [next[index], next[target]] = [next[target], next[index]];
    setDrafts(next);
  }

  async function save() {
    if (saving) return;
    const start = Number(portStart);
    const end = Number(portEnd);
    const names = drafts.map((draft) => draft.name.trim()).filter(Boolean);
    const exit = drafts[drafts.length - 1];
    const selectedExitNode = Number(exitNodeId) || 0;
    if (!name.trim()) return setError("请填写转发链名称");
    if (!Number.isInteger(start) || !Number.isInteger(end) || start < 1 || end > 65535 || start > end) return setError("端口范围必须为 1–65535，且起始端口不能大于结束端口");
    if (names.length !== new Set(names).size) return setError("同一条链中不能使用重复的组名");
    if (drafts.some((draft) => draft.role !== "exit" && draft.serverIds.length === 0)) return setError("入口组和每个中间组至少需要一台服务器");
    if (selectedExitNode && (exit?.serverIds.length ?? 0) > 0) return setError("出口组不能同时使用服务器和落地节点");
    if (editing && selectedExitNode) return setError("编辑链路时不能更换出口落地节点，请在节点管理中操作");
    if (!selectedExitNode && !exit?.serverIds.length && !(chain?.bound_nodes?.length)) return setError("出口组至少需要一台服务器或一个落地节点");
    const bindPort = Number(entryPort);
    if (selectedExitNode && (!Number.isInteger(bindPort) || bindPort < start || bindPort > end)) return setError(`入口端口必须位于 ${start}–${end}`);
    if ((domainPrefix.trim() || domainPrefixV6.trim()) && !selectedCertificate) return setError("设置入口域名前缀时必须选择有效证书");
    const dnsDomain = composeDomain(domainPrefix, selectedCertificate?.domain);
    const dnsDomainV6 = composeDomain(domainPrefixV6, selectedCertificate?.domain);
    if (dnsDomain && dnsDomainV6 && dnsDomain === dnsDomainV6) return setError("IPv4 与 IPv6 入口域名不能相同");

    setSaving(true);
    setError("");
    try {
      const groupIds: number[] = [];
      for (const draft of drafts) {
        const body = { name: draft.name.trim() || roleLabel(draft.role), balance_strategy: draft.strategy, members: draft.serverIds.map((serverId) => ({ server_id: serverId, weight: 1 })) };
        if (editing && draft.backendId) {
          await updateForwardGroup(token, draft.backendId, body);
          groupIds.push(draft.backendId);
        } else {
          const result = await createForwardGroup(token, body);
          const id = result.id ?? result.group_id ?? result.group?.id;
          if (!id) throw new Error(`创建组“${body.name}”后未返回 ID`);
          groupIds.push(id);
        }
      }
      const config = { port_range_start: start, port_range_end: end, dns_domain: dnsDomain, dns_domain_v6: dnsDomainV6, dns_provider_id: (dnsDomain || dnsDomainV6) ? Number(selectedCertificate?.dns_provider_id) || 0 : 0 };
      const warnings: string[] = [];
      if (chain) {
        await updateForwardChain(token, chain.id, config);
        const result = await updateForwardChainGroups(token, chain.id, groupIds);
        warnings.push(...(result.warnings ?? []));
      } else {
        const result = await createForwardChain(token, { name: name.trim(), group_ids: groupIds, ...config });
        const chainId = result.id ?? result.chain_id ?? result.chain?.id;
        if (selectedExitNode) {
          if (!chainId) throw new Error("创建转发链后未返回 ID，无法绑定落地节点");
          await createForwardChainNode(token, chainId, { existing_node_id: selectedExitNode, port: bindPort, relay_protocol: "tcp" });
        }
      }
      await onSaved(`${editing ? "转发链已更新" : selectedExitNode ? "转发链已创建并绑定落地节点" : "转发链已创建"}${warnings.length ? `；${warnings.join("；")}` : ""}`);
    } catch (caught) {
      setError(messageOf(caught, editing ? "更新转发链失败" : "创建转发链失败"));
    } finally {
      setSaving(false);
    }
  }

  return <div className="forward-dialog-layer" role="presentation" onMouseDown={onClose}>
    <section className="forward-editor" role="dialog" aria-modal="true" aria-label={editing ? "编辑转发链" : "创建转发链"} onMouseDown={(event) => event.stopPropagation()}>
      <header><div><h2>{editing ? "编辑转发链" : "创建转发链"}</h2><p>配置入口、中间、出口、端口范围与入口域名。</p></div><button type="button" onClick={onClose} aria-label="关闭"><X /></button></header>
      <div className="forward-editor-body">
        {error && <div className="forward-notice error"><AlertTriangle /><span>{error}</span></div>}
        <fieldset className="forward-form-section"><legend>基础设置</legend><div className="forward-form-grid">
          <label className="wide"><span>转发链名称 *</span><input value={name} disabled={editing} onChange={(event) => setName(event.target.value)} placeholder="例如：香港入口到美国出口" />{editing && <small>正式版当前不支持修改已创建转发链的名称。</small>}</label>
          <label><span>起始端口 *</span><input type="number" min="1" max="65535" value={portStart} onChange={(event) => setPortStart(event.target.value)} /></label>
          <label><span>结束端口 *</span><input type="number" min="1" max="65535" value={portEnd} onChange={(event) => setPortEnd(event.target.value)} /></label>
        </div></fieldset>

        <fieldset className="forward-form-section"><legend>链路组</legend><p className="forward-section-note">服务器可拖入目标组；手机上也可以通过下方选择器加入。组内服务器可排序。</p>
          <div className="forward-group-list">
            {drafts.map((draft, index) => <article className={`forward-group-card ${draft.role}`} key={draft.key} onDragOver={(event) => event.preventDefault()} onDrop={(event) => { event.preventDefault(); const id = Number(event.dataTransfer.getData("text/forward-server")); if (id) addServer(id, draft.key); }}>
              <header><span>{roleLabel(draft.role)}{index > 0 && <LatencyBadge value={editorLatencies[`${drafts[index - 1].key}:${draft.key}`]} />}</span><div>
                {draft.role === "middle" && <button type="button" onClick={() => moveMiddle(index, -1)} disabled={index <= 1} aria-label="中间组上移" title="上移"><ArrowUp /></button>}
                {draft.role === "middle" && <button type="button" onClick={() => moveMiddle(index, 1)} disabled={index >= drafts.length - 2} aria-label="中间组下移" title="下移"><ArrowDown /></button>}
                {draft.role === "middle" && <button className="danger" type="button" onClick={() => setDrafts((current) => current.filter((item) => item.key !== draft.key))} aria-label="删除中间组" title="删除"><Trash2 /></button>}
              </div></header>
              <label><span>组名称 *</span><input value={draft.name} onChange={(event) => updateDraft(draft.key, { name: event.target.value })} /></label>
              {draft.serverIds.length >= 2 && <label><span>负载策略</span><select value={draft.strategy} onChange={(event) => updateDraft(draft.key, { strategy: event.target.value })}>{strategies.map((strategy) => <option value={strategy.value} key={strategy.value}>{strategy.label}</option>)}</select><small>{strategies.find((strategy) => strategy.value === draft.strategy)?.help}</small></label>}
              <div className="forward-member-list">
                {draft.serverIds.map((serverId, memberIndex) => <div key={serverId}>
                  <span><Server />{servers.find((server) => server.id === serverId)?.name || `服务器 ${serverId}`}</span>
                  <div><button type="button" disabled={memberIndex === 0} onClick={() => moveMember(draft.key, memberIndex, -1)} aria-label="服务器上移"><ArrowUp /></button><button type="button" disabled={memberIndex === draft.serverIds.length - 1} onClick={() => moveMember(draft.key, memberIndex, 1)} aria-label="服务器下移"><ArrowDown /></button><button className="danger" type="button" onClick={() => updateDraft(draft.key, { serverIds: draft.serverIds.filter((id) => id !== serverId) })} aria-label="移出服务器"><X /></button></div>
                </div>)}
                {!draft.serverIds.length && <p>{draft.role === "exit" && exitNodeId ? "已选择落地节点" : "将服务器拖到这里"}</p>}
              </div>
            </article>)}
          </div>
          <button type="button" onClick={addMiddle}><Plus />添加中间组</button>
        </fieldset>

        <fieldset className="forward-form-section"><legend>可用服务器</legend>
          <div className="forward-server-controls"><label><Search /><input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索服务器" /></label><select value={targetGroup} onChange={(event) => setTargetGroup(event.target.value)} aria-label="目标链路组">{drafts.map((draft) => <option value={draft.key} key={draft.key}>加入{roleLabel(draft.role)}：{draft.name}</option>)}</select></div>
          <div className="forward-server-pool">{available.map((server) => <button draggable type="button" key={server.id} onDragStart={(event) => event.dataTransfer.setData("text/forward-server", String(server.id))} onClick={() => addServer(server.id)}><Server /><span>{server.name}</span><Plus /></button>)}{!available.length && <p>没有未分配的服务器</p>}</div>
        </fieldset>

        {!editing && <fieldset className="forward-form-section"><legend>出口落地节点</legend><p className="forward-section-note">出口服务器与落地节点二选一；一条链只能指定一个落地节点。</p><div className="forward-form-grid">
          <label className="wide"><span>现有节点</span><select value={exitNodeId} onChange={(event) => { setExitNodeId(event.target.value); if (event.target.value) { const exit = drafts[drafts.length - 1]; if (exit) updateDraft(exit.key, { serverIds: [] }); } }}><option value="">不使用落地节点</option>{selectableNodes.map((node) => <option value={node.id} key={node.id}>{nodeName(node)}</option>)}</select></label>
          {exitNodeId && <label className="wide"><span>入口监听端口 *</span><input type="number" min={Number(portStart) || 1} max={Number(portEnd) || 65535} value={entryPort} onChange={(event) => setEntryPort(event.target.value)} /><small>必须位于转发链端口范围内。</small></label>}
        </div></fieldset>}

        <fieldset className="forward-form-section"><legend>入口域名</legend><div className="forward-form-grid">
          <label className="wide"><span>有效证书</span><select value={certId} onChange={(event) => setCertId(event.target.value)}><option value="">不设置入口域名</option>{certificates.filter((certificate) => certificate.id && certificate.domain).map((certificate) => <option value={certificate.id} key={certificate.id}>{normalizeCertDomain(certificate.domain || "")}</option>)}</select></label>
          <label><span>IPv4 前缀</span><input value={domainPrefix} onChange={(event) => setDomainPrefix(cleanPrefix(event.target.value))} placeholder="relay" /><small>A：{composeDomain(domainPrefix, selectedCertificate?.domain) || "未设置"}</small></label>
          <label><span>IPv6 前缀</span><input value={domainPrefixV6} onChange={(event) => setDomainPrefixV6(cleanPrefix(event.target.value))} placeholder="relay6" /><small>AAAA：{composeDomain(domainPrefixV6, selectedCertificate?.domain) || "未设置"}</small></label>
        </div></fieldset>
      </div>
      <footer><button type="button" onClick={onClose}>取消</button><button className="primary" type="button" disabled={saving} onClick={() => void save()}><Save />{saving ? "保存中..." : "保存转发链"}</button></footer>
    </section>
  </div>;
}

function ForwardNodeDialog({ token, chain, groups, nodes, onClose, onSaved }: {
  token: string;
  chain: ForwardChain;
  groups: ForwardGroup[];
  nodes: XrayNode[];
  onClose: () => void;
  onSaved: (text: string) => Promise<void>;
}) {
  const [mode, setMode] = useState<"create" | "bind">("create");
  const [name, setName] = useState("");
  const [entrySeparate, setEntrySeparate] = useState(false);
  const [exitSeparate, setExitSeparate] = useState(false);
  const [existingNodeId, setExistingNodeId] = useState("");
  const [port, setPort] = useState(String(randomPort(chain.port_range_start ?? PORT_START, chain.port_range_end ?? PORT_END)));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const hops = sortedHops(chain);
  const groupById = useMemo(() => new Map(groups.map((group) => [group.id, group])), [groups]);
  const entryCount = groupById.get(hops[0]?.group_id ?? -1)?.members?.length ?? 0;
  const exitCount = groupById.get(hops[hops.length - 1]?.group_id ?? -1)?.members?.length ?? 0;
  const count = Math.max(1, entrySeparate ? entryCount : 1) * Math.max(1, exitSeparate ? exitCount : 1);
  const selectableNodes = nodes.filter((node) => node.node_type !== "routed");
  const childCount = nodes.filter((node) => String(node.parent_node_id ?? "") === existingNodeId).length;

  async function save() {
    if (saving) return;
    if (mode === "create" && exitSeparate && exitCount === 0) return setError("出口组没有服务器，不能启用出口分离");
    if (mode === "bind" && !existingNodeId) return setError("请选择要绑定的现有节点");
    const parsedPort = Number(port);
    const start = chain.port_range_start ?? PORT_START;
    const end = chain.port_range_end ?? PORT_END;
    if (mode === "bind" && (!Number.isInteger(parsedPort) || parsedPort < start || parsedPort > end)) return setError(`端口必须位于 ${start}–${end}`);
    setSaving(true);
    setError("");
    try {
      if (mode === "create") {
        const result = await createForwardChainNode(token, chain.id, { node_name: name.trim(), relay_protocol: "tcp", entry_separate: entrySeparate, exit_separate: exitSeparate });
        await onSaved(`已创建 ${result.count ?? count} 个转发链节点`);
      } else {
        await createForwardChainNode(token, chain.id, { existing_node_id: Number(existingNodeId), port: parsedPort, relay_protocol: "tcp" });
        await onSaved("现有节点已绑定并新增入口节点");
      }
    } catch (caught) {
      setError(messageOf(caught, "添加转发链节点失败"));
    } finally {
      setSaving(false);
    }
  }

  return <div className="forward-dialog-layer" role="presentation" onMouseDown={onClose}>
    <section className="forward-node-dialog" role="dialog" aria-modal="true" aria-label="添加转发链节点" onMouseDown={(event) => event.stopPropagation()}>
      <header><div><h2>转发链节点：{chain.name}</h2><p>两种模式都会新增节点，不会替换已有节点。</p></div><button type="button" onClick={onClose} aria-label="关闭"><X /></button></header>
      <div className="forward-editor-body">
        {(chain.bound_nodes?.length ?? 0) > 0 && <section className="forward-existing"><strong>已有 {chain.bound_nodes?.length} 个节点</strong>{chain.bound_nodes?.map((node) => <span key={node.node_id}>{node.node_name || `节点 ${node.node_id}`}<em>:{node.port ?? "自动"}</em></span>)}<small>改动已有节点请前往“节点管理”；此处只会再添加一个。</small></section>}
        {error && <div className="forward-notice error"><AlertTriangle /><span>{error}</span></div>}
        <div className="forward-mode-tabs"><button className={mode === "create" ? "active" : ""} type="button" onClick={() => setMode("create")}>新建节点</button><button className={mode === "bind" ? "active" : ""} type="button" onClick={() => setMode("bind")}>绑定已有节点</button></div>
        {mode === "create" ? <div className="forward-form-grid">
          <label className="wide"><span>节点名称（可选）</span><input value={name} onChange={(event) => setName(event.target.value)} placeholder={`转发链-${chain.name}`} /><small>协议固定为 VLESS+TCP，凭据全链共享；端口从链路范围自动分配。</small></label>
          <label className="forward-switch wide"><span><strong>入口分离</strong><small>按每台入口服务器 IP 各创建一个节点；关闭时使用入口域名。</small></span><input type="checkbox" checked={entrySeparate} onChange={(event) => setEntrySeparate(event.target.checked)} /></label>
          <label className="forward-switch wide"><span><strong>出口分离</strong><small>每台出口服务器分配独立端口并固定；关闭时由出口组负载均衡。</small></span><input type="checkbox" checked={exitSeparate} onChange={(event) => setExitSeparate(event.target.checked)} /></label>
          <div className="forward-count wide">将创建 <strong>{count}</strong> 个节点 <span>（{entrySeparate ? entryCount : 1} 入口 × {exitSeparate ? exitCount : 1} 出口）</span></div>
        </div> : <div className="forward-form-grid">
          <label className="wide"><span>选择已有节点 *</span><select value={existingNodeId} onChange={(event) => setExistingNodeId(event.target.value)}><option value="">请选择节点</option>{selectableNodes.map((node) => <option value={node.id} key={node.id}>{nodeName(node)}</option>)}</select></label>
          {childCount > 0 && <div className="forward-issue wide"><AlertTriangle />该节点下有 {childCount} 个路由出站子节点。端口转发只到父节点 IP:端口，不会应用子节点分流规则。</div>}
          <label className="wide"><span>端口 *</span><input type="number" min={chain.port_range_start ?? PORT_START} max={chain.port_range_end ?? PORT_END} value={port} onChange={(event) => setPort(event.target.value)} /><small>必须位于链路端口范围 {chain.port_range_start ?? PORT_START}–{chain.port_range_end ?? PORT_END}。</small></label>
        </div>}
      </div>
      <footer><button type="button" onClick={onClose}>取消</button><button className="primary" type="button" disabled={saving} onClick={() => void save()}><Check />{saving ? "处理中..." : mode === "create" ? "创建新节点" : "绑定并新增入口节点"}</button></footer>
    </section>
  </div>;
}

function LatencyArrow({ value }: { value?: Latency }) {
  return <div className="forward-latency-arrow"><ArrowRight /><LatencyBadge value={value} /></div>;
}

function LatencyBadge({ value }: { value?: Latency }) {
  if (!value || value.state === "loading") return <span className="forward-latency pending">···</span>;
  if (value.state === "error") return <span className="forward-latency error">不通</span>;
  return <span className={`forward-latency ${latencyClass(value.ms ?? 0)}`} title={value.method === "icmp" ? "仅 ICMP 可达，端口未验证" : undefined}>{value.ms}ms{value.method === "icmp" ? "*" : ""}</span>;
}

function sortedHops(chain: ForwardChain) {
  return [...(chain.hops ?? [])].sort((left, right) => left.order - right.order);
}

function makeDrafts(chain: ForwardChain | undefined, groups: ForwardGroup[]): GroupDraft[] {
  const groupById = new Map(groups.map((group) => [group.id, group]));
  const hops = chain ? sortedHops(chain) : [];
  if (!hops.length) return [
    { key: "entry", role: "entry", name: "入口组", strategy: "round_robin", serverIds: [] },
    { key: "exit", role: "exit", name: "出口组", strategy: "round_robin", serverIds: [] },
  ];
  return hops.map((hop, index) => {
    const group = groupById.get(hop.group_id);
    const role: GroupRole = index === 0 ? "entry" : index === hops.length - 1 ? "exit" : "middle";
    return { key: `group-${hop.group_id}`, backendId: hop.group_id, role, name: group?.name || hop.group_name || roleLabel(role), strategy: group?.balance_strategy || "round_robin", serverIds: (group?.members ?? []).map((member) => member.server_id) };
  });
}

function roleLabel(role: GroupRole) { return role === "entry" ? "入口组" : role === "exit" ? "出口组" : "中间组"; }
function strategyLabel(value?: ForwardBalanceStrategy) { return strategies.find((strategy) => strategy.value === value)?.label || value || "轮询"; }
function normalizeCertDomain(value: string) { return value.trim().replace(/^\*\./, "").replace(/^\.+/, "").toLowerCase(); }
function cleanPrefix(value: string) { return value.trimStart().replace(/[^a-zA-Z0-9.-]/g, "").replace(/^\.+/, ""); }
function composeDomain(prefix: string, certificateDomain?: string) {
  const clean = prefix.trim().replace(/^\.+|\.+$/g, "");
  const suffix = normalizeCertDomain(certificateDomain || "");
  return clean && suffix ? `${clean}.${suffix}` : "";
}
function matchCertificate(domain: string, certificates: ValidCertificate[]) {
  const clean = domain.trim().toLowerCase();
  return [...certificates].filter((certificate) => {
    const suffix = normalizeCertDomain(certificate.domain || "");
    return suffix && (clean === suffix || clean.endsWith(`.${suffix}`));
  }).sort((left, right) => normalizeCertDomain(right.domain || "").length - normalizeCertDomain(left.domain || "").length)[0];
}
function preferredCertificate(certificates: ValidCertificate[]) {
  return certificates.find((certificate) => certificate.domain?.trim().startsWith("*.")) || certificates[0];
}
function domainPrefixFor(domain: string, certificateDomain: string) {
  const clean = domain.trim();
  const suffix = normalizeCertDomain(certificateDomain);
  if (!clean || !suffix) return "";
  if (clean.toLowerCase() === suffix) return "";
  return clean.toLowerCase().endsWith(`.${suffix}`) ? clean.slice(0, -(suffix.length + 1)) : clean;
}
function randomPort(start: number, end: number) {
  const safeStart = Math.max(1, Math.min(65535, Number(start) || PORT_START));
  const safeEnd = Math.max(safeStart, Math.min(65535, Number(end) || PORT_END));
  return Math.floor(Math.random() * (safeEnd - safeStart + 1)) + safeStart;
}
function numberOrUndefined(value: unknown) { const number = Number(value); return Number.isFinite(number) ? Math.round(number) : undefined; }
function nodeName(node: XrayNode) { return String((node as XrayNode & { name?: string }).name || node.node_name || `节点 ${node.id}`); }
function averageLatency(values: Latency[]): Latency | undefined {
  if (!values.length) return undefined;
  if (values.some((value) => value.state === "loading")) return { state: "loading" };
  const successful = values.filter((value) => value.state === "ok" && typeof value.ms === "number");
  if (!successful.length) return { state: "error" };
  return { state: "ok", ms: Math.round(successful.reduce((sum, value) => sum + (value.ms ?? 0), 0) / successful.length) };
}
function latencyClass(ms: number) { return ms < 100 ? "fast" : ms < 250 ? "medium" : "slow"; }
function issueText(value: unknown): string {
  if (!value) return "";
  if (Array.isArray(value)) return value.map(issueText).filter(Boolean).join("；");
  if (typeof value === "object") return Object.values(value as Record<string, unknown>).map(issueText).filter(Boolean).join("；");
  return String(value);
}
function messageOf(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback;
}
