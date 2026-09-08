import { useEffect, useMemo, useState } from "react";
import {
  AlertTriangle,
  ArrowDown,
  ArrowUp,
  Check,
  Copy,
  Edit3,
  Gauge,
  KeyRound,
  Link2,
  Loader2,
  Network,
  Play,
  Plus,
  RefreshCw,
  Route,
  Save,
  Search,
  Trash2,
  X,
} from "lucide-react";
import {
  batchDisableNodeSkipCert,
  batchUpdateSnellOptions,
  cancelNodeRelay,
  confirmExternalSync,
  copyNodeWithRelay,
  createRoutedOutbound,
  deleteRoutedOutbound,
  createTunnelChain,
  fetchNodeTunnels,
  fetchNodeURIs,
  fetchPackageNodeTrafficName,
  fetchRemoteRouting,
  fetchSpeedTestResults,
  fetchSpeedTesters,
  fetchRoutedOutbounds,
  mutateRemoteInbound,
  mutateRemoteOutbound,
  mutateRemoteRouting,
  runSpeedTest,
  setNodeRelay,
  syncExternalSubscriptions,
  updateNode,
  updatePackageNodeTrafficName,
} from "./api";
import type {
  ExternalSyncCandidate,
  NodeTunnel,
  NodeTunnelChain,
  NodeURIItem,
  RemoteServer,
  SpeedTestResult,
  SpeedTester,
  XrayNode,
} from "./types";

type ToolNotice = (tone: "success" | "error" | "info", text: string) => void;

function ToolDialog({ title, subtitle, onClose, children }: { title: string; subtitle: string; onClose: () => void; children: React.ReactNode }) {
  return <div className="node-dialog-layer" role="presentation" onClick={onClose}>
    <section className="node-dialog node-tool-dialog" role="dialog" aria-modal="true" aria-label={title} onClick={(event) => event.stopPropagation()}>
      <header><div><h2>{title}</h2><p>{subtitle}</p></div><button type="button" onClick={onClose} aria-label="关闭"><X /></button></header>
      <div className="node-dialog-body">{children}</div>
    </section>
  </div>;
}

function ToolLoading({ text = "正在读取" }: { text?: string }) {
  return <div className="node-empty"><Loader2 className="spin" /> {text}</div>;
}

export function URIManagerDialog({ token, onClose, onNotice }: { token: string; onClose: () => void; onNotice: ToolNotice }) {
  const [items, setItems] = useState<NodeURIItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [query, setQuery] = useState("");
  const [username, setUsername] = useState("all");
  const [server, setServer] = useState("all");
  useEffect(() => { fetchNodeURIs(token).then((value) => setItems(value.items ?? [])).catch((error) => onNotice("error", error instanceof Error ? error.message : "读取 URI 失败")).finally(() => setLoading(false)); }, [onNotice, token]);
  const users = useMemo(() => [...new Set(items.map((item) => item.username))].sort(), [items]);
  const servers = useMemo(() => [...new Set(items.map((item) => item.server_name).filter(Boolean) as string[])].sort(), [items]);
  const filtered = useMemo(() => {
    const keyword = query.trim().toLowerCase();
    return items.filter((item) => (username === "all" || item.username === username) && (server === "all" || item.server_name === server) && (!keyword || `${item.username} ${item.node_name} ${item.server_name || ""} ${item.protocol || ""}`.toLowerCase().includes(keyword)));
  }, [items, query, server, username]);
  const copy = async (value: string, message: string) => { await navigator.clipboard.writeText(value); onNotice("success", message); };
  return <ToolDialog title="URI 管理" subtitle="按用户和服务器查看、筛选并复制成品节点 URI" onClose={onClose}>
    <div className="node-form-grid">
      <label className="wide"><span>搜索</span><div className="node-inline-input"><Search /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="用户、节点、服务器、协议" /></div></label>
      <label><span>用户</span><select value={username} onChange={(event) => setUsername(event.target.value)}><option value="all">全部用户</option>{users.map((value) => <option key={value}>{value}</option>)}</select></label>
      <label><span>服务器</span><select value={server} onChange={(event) => setServer(event.target.value)}><option value="all">全部服务器</option>{servers.map((value) => <option key={value}>{value}</option>)}</select></label>
    </div>
    <div className="node-dialog-actions"><button type="button" disabled={!filtered.length} onClick={() => void copy(filtered.map((item) => item.uri).join("\n"), `已复制 ${filtered.length} 条 URI`)}><Copy />复制筛选结果</button></div>
    {loading ? <ToolLoading /> : <div className="node-tool-list">{filtered.map((item) => <article key={`${item.username}-${item.node_id}`}><div><strong>{item.node_name}</strong><p>{item.username} · {item.server_name || "外部节点"} · {(item.protocol || "node").toUpperCase()}</p></div><button type="button" onClick={() => void copy(item.uri, `已复制「${item.node_name}」`)} aria-label={`复制 ${item.node_name}`}><Copy /></button></article>)}</div>}
  </ToolDialog>;
}

export function SpeedTestDialog({ token, nodes, onClose, onNotice }: { token: string; nodes: XrayNode[]; onClose: () => void; onNotice: ToolNotice }) {
  const [selected, setSelected] = useState<Set<number>>(() => new Set());
  const [testers, setTesters] = useState<SpeedTester[]>([]);
  const [results, setResults] = useState<SpeedTestResult[]>([]);
  const [tester, setTester] = useState("master");
  const [threads, setThreads] = useState(4);
  const [buffer, setBuffer] = useState(1);
  const [busy, setBusy] = useState(false);
  const load = async () => {
    const [testerResp, resultResp] = await Promise.all([fetchSpeedTesters(token), fetchSpeedTestResults(token, undefined, true)]);
    setTesters(testerResp.testers ?? []); setResults(resultResp.results ?? []);
  };
  useEffect(() => { void load().catch((error) => onNotice("error", error instanceof Error ? error.message : "读取测速信息失败")); }, [token]);
  const latest = useMemo(() => new Map(results.map((item) => [item.node_id, item])), [results]);
  const start = async (latencyOnly: boolean) => {
    const ids = [...selected]; if (!ids.length) return;
    setBusy(true);
    try {
      await Promise.all(ids.map((nodeId) => runSpeedTest(token, { node_id: nodeId, threads, buf_size: buffer * 1024 * 1024, ...(tester !== "master" ? { tester_id: Number(tester) } : {}), ...(latencyOnly ? { latency_only: true } : {}) })));
      onNotice("success", `${ids.length} 个节点的${latencyOnly ? "延迟" : "下载"}测速已开始`);
      await new Promise((resolve) => window.setTimeout(resolve, 1200)); await load();
    } catch (error) { onNotice("error", error instanceof Error ? error.message : "启动测速失败"); } finally { setBusy(false); }
  };
  return <ToolDialog title="节点测速" subtitle="选择测速源、并发和缓冲区后批量测试延迟或下载速度" onClose={onClose}>
    <div className="node-form-grid">
      <label><span>测速源</span><select value={tester} onChange={(event) => setTester(event.target.value)}><option value="master">主控服务器</option>{testers.map((item) => <option key={item.id} value={item.id}>{item.name}{item.online === false ? "（离线）" : ""}</option>)}</select></label>
      <label><span>并发线程</span><select value={threads} onChange={(event) => setThreads(Number(event.target.value))}>{[1, 2, 4, 8].map((value) => <option key={value}>{value}</option>)}</select></label>
      <label><span>缓冲区</span><select value={buffer} onChange={(event) => setBuffer(Number(event.target.value))}>{[1, 2, 4, 8].map((value) => <option key={value} value={value}>{value} MB</option>)}</select></label>
    </div>
    <div className="node-dialog-actions"><button type="button" onClick={() => setSelected(selected.size === nodes.length ? new Set() : new Set(nodes.map((node) => node.id)))}>{selected.size === nodes.length ? "取消全选" : "全选"}</button><button type="button" disabled={busy || !selected.size} onClick={() => void start(true)}><Gauge />延迟测试</button><button className="primary" type="button" disabled={busy || !selected.size} onClick={() => void start(false)}><Play />下载测速</button></div>
    <div className="node-tool-list selectable">{nodes.map((node) => { const result = latest.get(node.id); return <label key={node.id}><input type="checkbox" checked={selected.has(node.id)} onChange={() => setSelected((current) => { const next = new Set(current); next.has(node.id) ? next.delete(node.id) : next.add(node.id); return next; })} /><div><strong>{node.node_name}</strong><p>{result ? `${result.status || "完成"} · ${result.latency_ms != null ? `${Math.round(result.latency_ms)} ms` : "--"} · ${result.down_mbps != null ? `${result.down_mbps.toFixed(2)} Mbps` : "--"}` : "暂无测速结果"}</p></div></label>; })}</div>
  </ToolDialog>;
}

function nodeEndpoint(node: XrayNode) {
  if (node.relay_orig_server?.trim()) return { server: node.relay_orig_server.trim(), port: Number(node.relay_orig_port) || 0 };
  for (const value of [node.clash_config, node.parsed_config]) {
    try {
      const parsed = JSON.parse(value || "{}") as Record<string, unknown>;
      if (typeof parsed.server === "string" && parsed.server.trim()) return { server: parsed.server.trim(), port: Number(parsed.port) || 0 };
    } catch {
      // Historical node configs can be invalid; omit them from target selectors.
    }
  }
  return null;
}

function serverEntryHost(server?: RemoteServer) {
  return server?.domain?.trim() || server?.ip_address?.trim() || server?.pull_address?.trim() || "";
}

export function TunnelManagerDialog({ token, servers, nodes, onChanged, onClose, onNotice }: { token: string; servers: RemoteServer[]; nodes: XrayNode[]; onChanged: () => Promise<void>; onClose: () => void; onNotice: ToolNotice }) {
  const [tab, setTab] = useState<"tunnel" | "relay">("tunnel");
  const [mode, setMode] = useState<"list" | "port" | "chain">("list");
  const [tunnels, setTunnels] = useState<NodeTunnel[]>([]);
  const [chains, setChains] = useState<NodeTunnelChain[]>([]);
  const [loading, setLoading] = useState(true);
  const [serverId, setServerId] = useState(servers[0] ? String(servers[0].id) : "");
  const [reuseTag, setReuseTag] = useState("");
  const [listenPort, setListenPort] = useState("");
  const [target, setTarget] = useState("");
  const [targetPort, setTargetPort] = useState("");
  const [label, setLabel] = useState("");
  const [chainServers, setChainServers] = useState<number[]>([]);
  const [targetNodeId, setTargetNodeId] = useState("");
  const [createNodeCopy, setCreateNodeCopy] = useState(true);
  const [relayAdding, setRelayAdding] = useState(false);
  const [relayNodeId, setRelayNodeId] = useState("");
  const [relayServer, setRelayServer] = useState("");
  const [relayPort, setRelayPort] = useState("");
  const [relayCreateNode, setRelayCreateNode] = useState(true);
  const [busy, setBusy] = useState(false);
  const load = async () => { setLoading(true); try { const resp = await fetchNodeTunnels(token); setTunnels(resp.tunnels ?? []); setChains(resp.chains ?? []); } finally { setLoading(false); } };
  useEffect(() => { void load().catch((error) => onNotice("error", error instanceof Error ? error.message : "读取 Tunnel 失败")); }, [token]);
  const removeTunnel = async (item: NodeTunnel) => {
    if (!window.confirm(`确认删除 Tunnel「${item.tag}」？`)) return;
    if (item.kind === "inbound") await mutateRemoteInbound(token, item.server_id, { action: "remove", tag: item.tag });
    else {
      const routing = await fetchRemoteRouting(token, item.server_id); const rules = routing.routing?.rules ?? [];
      for (let index = rules.length - 1; index >= 0; index -= 1) if (rules[index]?.outboundTag === item.tag) await mutateRemoteRouting(token, item.server_id, { action: "remove_rule", index });
      await mutateRemoteOutbound(token, item.server_id, { action: "remove", tag: item.tag });
    }
    await load(); onNotice("success", "Tunnel 已删除并重新读取");
  };
  const createPort = async () => {
    const sid = Number(serverId); const destinationPort = Number(targetPort); const port = Number(listenPort); const clean = label.trim().replace(/^tunnel-/, "");
    if (!sid || !target.trim() || !destinationPort || !clean) throw new Error("服务器、标识、目标地址和目标端口不能为空");
    if (destinationPort < 1 || destinationPort > 65535) throw new Error("目标端口必须是 1-65535");
    setBusy(true);
    try {
    if (reuseTag) {
      const tag = `tunnel-${clean}`; await mutateRemoteOutbound(token, sid, { action: "add", outbound: { protocol: "freedom", settings: { domainStrategy: "AsIs", redirect: `${target.trim()}:${destinationPort}` }, tag } });
      const ip = /^(?:\d{1,3}\.){3}\d{1,3}$/.test(target.trim()) || target.includes(":"); await mutateRemoteRouting(token, sid, { action: "add_rule", rule: { inboundTag: [reuseTag], outboundTag: tag, ...(ip ? { ip: [target.trim()] } : { domain: [target.trim()] }) } });
    } else {
      if (!port || port < 1 || port > 65535) throw new Error("监听端口必须是 1-65535");
      await mutateRemoteInbound(token, sid, { action: "add", inbound: { tag: `tunnel-${clean}`, protocol: "tunnel", port, settings: { address: target.trim(), network: "tcp,udp", port: destinationPort } } });
    }
    const selectedNode = nodes.find((node) => String(node.id) === targetNodeId);
    const entryHost = serverEntryHost(servers.find((server) => server.id === sid));
    const entryPort = reuseTag ? Number(tunnels.find((item) => item.server_id === sid && item.tag === reuseTag)?.listen_port) : port;
    if (selectedNode && entryHost && entryPort > 0) {
      if (createNodeCopy) await copyNodeWithRelay(token, selectedNode.id, entryHost, entryPort, `tunnel-${clean}`);
      else await setNodeRelay(token, selectedNode.id, entryHost, entryPort);
      await onChanged();
    }
    await load(); setMode("list"); onNotice("success", selectedNode ? "端口转发和节点中转已创建并重新读取" : "端口转发已创建并重新读取");
    } finally { setBusy(false); }
  };
  const createChain = async () => {
    if (chainServers.length < 2) throw new Error("转发链至少选择两台服务器");
    const destinationPort = Number(targetPort);
    if (!target.trim()) throw new Error("目标地址不能为空");
    if (!destinationPort || destinationPort < 1 || destinationPort > 65535) throw new Error("目标端口必须是 1-65535");
    setBusy(true);
    try {
      const resp = await createTunnelChain(token, { label: label.trim(), server_ids: chainServers, entry_port: Number(listenPort) || 0, target_address: target.trim(), target_port: destinationPort });
      const selectedNode = nodes.find((node) => String(node.id) === targetNodeId);
      if (selectedNode && resp.entry_host && Number(resp.entry_port) > 0) {
        const suffix = `chain-${label.trim() || "relay"}`;
        if (createNodeCopy) await copyNodeWithRelay(token, selectedNode.id, resp.entry_host, Number(resp.entry_port), suffix);
        else await setNodeRelay(token, selectedNode.id, resp.entry_host, Number(resp.entry_port));
        await onChanged();
      }
      await load(); setMode("list"); onNotice("success", selectedNode ? "转发链和节点中转已创建并重新读取" : "转发链已创建并重新读取");
    } finally { setBusy(false); }
  };
  const relayNodes = nodes.filter((node) => node.relay_orig_server);
  const relayCandidates = nodes.filter((node) => !node.relay_orig_server && nodeEndpoint(node));
  const targetNodes = nodes.filter((node) => nodeEndpoint(node));
  const selectTargetNode = (value: string) => {
    setTargetNodeId(value);
    const selected = nodes.find((node) => String(node.id) === value);
    const endpoint = selected && nodeEndpoint(selected);
    if (endpoint) { setTarget(endpoint.server); setTargetPort(endpoint.port ? String(endpoint.port) : ""); }
  };
  const resetRelayForm = () => { setRelayAdding(false); setRelayNodeId(""); setRelayServer(""); setRelayPort(""); setRelayCreateNode(true); };
  const addRelay = async () => {
    const selected = nodes.find((node) => String(node.id) === relayNodeId);
    const port = Number(relayPort) || 0;
    if (!selected) throw new Error("请选择要添加中转的节点");
    if (!relayServer.trim()) throw new Error("请填写中转服务器地址");
    if (relayPort.trim() && (port < 1 || port > 65535)) throw new Error("中转端口必须留空或填写 1-65535");
    setBusy(true);
    try {
      if (relayCreateNode) await copyNodeWithRelay(token, selected.id, relayServer.trim(), port, "relay");
      else await setNodeRelay(token, selected.id, relayServer.trim(), port);
      await onChanged(); resetRelayForm(); onNotice("success", "中转已添加并重新读取");
    } finally { setBusy(false); }
  };
  const removeRelay = async (node: XrayNode) => {
    if (!window.confirm(`确定取消节点「${node.node_name}」的中转吗？将还原为原始服务器地址。`)) return;
    setBusy(true);
    try { await cancelNodeRelay(token, node.id); await onChanged(); onNotice("success", "已取消中转并重新读取"); } finally { setBusy(false); }
  };
  return <ToolDialog title="Tunnel 管理" subtitle="集中管理端口转发、转发链和节点中转" onClose={onClose}>
    <div className="node-dialog-tabs"><button className={tab === "tunnel" ? "active" : ""} onClick={() => setTab("tunnel")}>隧道配置</button><button className={tab === "relay" ? "active" : ""} onClick={() => setTab("relay")}>中转配置</button></div>
    {tab === "relay" ? <div className="node-relay-manager">
      <p className="node-relay-notice">这里的中转是指外部配置的中转，妙妙屋X只做入口 IP 更换；如需使用妙妙屋X中转，请使用隧道创建中转。</p>
      <div className="node-section-head compact"><div><h3>{relayNodes.length ? `共 ${relayNodes.length} 个中转节点` : "外部中转"}</h3></div>{!relayAdding && <button type="button" onClick={() => setRelayAdding(true)}><Network />添加中转</button>}</div>
      {relayAdding && <div className="node-subpanel"><h3>添加中转</h3><div className="node-form-grid">
        <label className="wide"><span>选择节点</span><select value={relayNodeId} onChange={(event) => setRelayNodeId(event.target.value)}><option value="">选择要添加中转的节点</option>{relayCandidates.map((node) => { const endpoint = nodeEndpoint(node); return <option key={node.id} value={node.id}>{node.node_name}{endpoint ? ` (${endpoint.server}:${endpoint.port || "--"})` : ""}</option>; })}</select>{!relayCandidates.length && <small>没有可添加中转的节点</small>}</label>
        <label><span>中转服务器地址</span><input value={relayServer} onChange={(event) => setRelayServer(event.target.value)} placeholder="中转服务器 IP 或域名" /></label>
        <label><span>中转端口</span><input inputMode="numeric" value={relayPort} onChange={(event) => setRelayPort(event.target.value)} placeholder="默认使用节点端口" /></label>
      </div><label className="node-check node-option-card"><input type="checkbox" checked={relayCreateNode} onChange={(event) => setRelayCreateNode(event.target.checked)} /><span><strong>新增节点</strong><small>默认保留原节点，并新增一个使用此外部中转入口的节点；取消勾选后将直接修改原节点。</small></span></label><div className="node-dialog-actions"><button type="button" onClick={resetRelayForm}>取消</button><button className="primary" type="button" disabled={busy || !relayCandidates.length} onClick={() => void addRelay().catch((error) => onNotice("error", error instanceof Error ? error.message : "中转操作失败"))}><Check />确认</button></div></div>}
      <div className="node-tool-list">{relayNodes.length ? relayNodes.map((node) => { const via = nodeEndpoint({ ...node, relay_orig_server: undefined, relay_orig_port: undefined }); return <article key={node.id}><div><strong>{node.node_name}</strong><p className="node-relay-via">中转地址：{via ? `${via.server}:${via.port || "--"}` : "--"}</p><p>原服务器：{node.relay_orig_server}:{node.relay_orig_port || "--"}</p></div><button className="danger" type="button" disabled={busy} onClick={() => void removeRelay(node).catch((error) => onNotice("error", error instanceof Error ? error.message : "中转操作失败"))} aria-label={`取消 ${node.node_name} 的中转`}><Trash2 /></button></article>; }) : <div className="node-empty">暂无配置中转的节点</div>}</div>
    </div> : <>
      {mode === "list" && <div className="node-dialog-actions"><button onClick={() => setMode("chain")}><Network />转发链</button><button onClick={() => setMode("port")}><Link2 />端口转发</button><button onClick={() => void load()}><RefreshCw />刷新</button></div>}
      {mode !== "list" && <div className="node-subpanel"><h3>{mode === "chain" ? "新建转发链" : "新建端口转发"}</h3><div className="node-form-grid">
        {mode === "port" && <label><span>服务器</span><select value={serverId} onChange={(event) => setServerId(event.target.value)}>{servers.map((server) => <option key={server.id} value={server.id}>{server.name}</option>)}</select></label>}
        {mode === "port" && <label><span>复用现有 Tunnel</span><select value={reuseTag} onChange={(event) => setReuseTag(event.target.value)}><option value="">新建监听端口</option>{tunnels.filter((item) => item.kind === "inbound" && String(item.server_id) === serverId).map((item) => <option key={item.tag} value={item.tag}>{item.tag} :{item.listen_port}</option>)}</select></label>}
        <label><span>标识</span><input value={label} onChange={(event) => setLabel(event.target.value)} placeholder="例如 hk-forward" /></label>
        <label><span>监听端口</span><input inputMode="numeric" value={listenPort} onChange={(event) => setListenPort(event.target.value)} placeholder={mode === "chain" ? "0 表示自动" : reuseTag ? "复用时无需填写" : "1-65535"} /></label>
        <label className="wide"><span>目标节点（可选）</span><select value={targetNodeId} onChange={(event) => selectTargetNode(event.target.value)}><option value="">手动填写目标地址</option>{targetNodes.map((node) => { const endpoint = nodeEndpoint(node); return <option key={node.id} value={node.id}>{node.node_name}{endpoint ? ` (${endpoint.server}:${endpoint.port || "--"})` : ""}</option>; })}</select></label>
        <label><span>目标地址</span><input value={target} onChange={(event) => { setTarget(event.target.value); setTargetNodeId(""); }} placeholder="IP 或域名" /></label>
        <label><span>目标端口</span><input inputMode="numeric" value={targetPort} onChange={(event) => setTargetPort(event.target.value)} placeholder="1-65535" /></label>
      </div>{targetNodeId && <label className="node-check node-option-card"><input type="checkbox" checked={createNodeCopy} onChange={(event) => setCreateNodeCopy(event.target.checked)} /><span><strong>新增节点</strong><small>保留目标节点，并新增一个使用当前隧道入口的节点；取消勾选后直接修改所选节点。</small></span></label>}{mode === "chain" && <><div className="node-chain-order">{chainServers.map((id, index) => { const server = servers.find((item) => item.id === id); return <div key={id}><span>{index + 1}</span><strong>{server?.name || `#${id}`}</strong><button type="button" disabled={index === 0} onClick={() => setChainServers((current) => { const next = [...current]; [next[index - 1], next[index]] = [next[index], next[index - 1]]; return next; })} aria-label="上移"><ArrowUp /></button><button type="button" disabled={index === chainServers.length - 1} onClick={() => setChainServers((current) => { const next = [...current]; [next[index], next[index + 1]] = [next[index + 1], next[index]]; return next; })} aria-label="下移"><ArrowDown /></button><button className="danger" type="button" onClick={() => setChainServers((current) => current.filter((value) => value !== id))} aria-label="移除"><Trash2 /></button></div>; })}</div><label className="node-chain-add"><span>链路服务器（按顺序）</span><select value="" onChange={(event) => { const id = Number(event.target.value); if (id) setChainServers((current) => current.includes(id) ? current : [...current, id]); }}><option value="">选择要加入链路的服务器</option>{servers.filter((server) => !chainServers.includes(server.id)).map((server) => <option key={server.id} value={server.id}>{server.name}</option>)}</select><small>第一台为入口、最后一台为出口；可用箭头调整顺序。</small></label></>}<div className="node-dialog-actions"><button onClick={() => setMode("list")}>取消</button><button className="primary" disabled={busy} onClick={() => void (mode === "chain" ? createChain() : createPort()).catch((error) => onNotice("error", error instanceof Error ? error.message : "创建失败"))}><Save />创建</button></div></div>}
      {mode === "list" && (loading ? <ToolLoading /> : <div className="node-tool-list">{chains.map((chain) => <article key={`chain-${chain.label}`}><div><strong>{chain.label}</strong><p>{chain.hops.map((hop) => `${hop.server_name}:${hop.listen_port || "--"}`).join(" → ")} → {chain.final_target}</p></div><button className="danger" onClick={() => void (async () => { if (!window.confirm(`确认删除转发链「${chain.label}」？`)) return; for (const hop of chain.hops) await mutateRemoteInbound(token, hop.server_id, { action: "remove", tag: hop.tag }); await load(); onNotice("success", "转发链已删除"); })()}><Trash2 /></button></article>)}{tunnels.map((item) => <article key={`${item.server_id}-${item.tag}`}><div><strong>{item.tag}</strong><p>{item.server_name} · :{item.listen_port || "--"} → {item.target_address || "--"}:{item.target_port || "--"}</p></div><button className="danger" onClick={() => void removeTunnel(item).catch((error) => onNotice("error", error instanceof Error ? error.message : "删除失败"))}><Trash2 /></button></article>)}{!chains.length && !tunnels.length && <div className="node-empty">当前没有 Tunnel 配置</div>}</div>)}
    </>}
  </ToolDialog>;
}

type RoutedEntry = { node: XrayNode; parent: XrayNode; raw: Record<string, unknown> };

function routedValue(item: Record<string, unknown>, snake: string, pascal: string) {
  return item[snake] ?? item[pascal];
}

function routedNode(item: Record<string, unknown>): XrayNode {
  const nested = item.node && typeof item.node === "object" ? item.node as Record<string, unknown> : item;
  return {
    id: Number(routedValue(nested, "id", "ID")) || 0,
    node_name: String(routedValue(nested, "node_name", "NodeName") || "路由出站"),
    protocol: String(routedValue(nested, "protocol", "Protocol") || ""),
    raw_url: String(routedValue(nested, "raw_url", "RawURL") || ""),
    parsed_config: String(routedValue(nested, "parsed_config", "ParsedConfig") || ""),
    clash_config: String(routedValue(nested, "clash_config", "ClashConfig") || ""),
    enabled: routedValue(nested, "enabled", "Enabled") !== false,
    tag: String(routedValue(nested, "tag", "Tag") || ""),
    original_server: String(routedValue(nested, "original_server", "OriginalServer") || ""),
    original_domain: String(routedValue(nested, "original_domain", "OriginalDomain") || ""),
    inbound_tag: String(routedValue(nested, "inbound_tag", "InboundTag") || ""),
    node_type: "routed",
    parent_node_id: Number(routedValue(nested, "parent_node_id", "ParentNodeID")) || null,
    routed_outbound_tag: String(routedValue(item, "routed_outbound_tag", "RoutedOutboundTag") || routedValue(nested, "routed_outbound_tag", "RoutedOutboundTag") || ""),
  };
}

function nodeMutation(node: XrayNode, overrides: Partial<{ node_name: string; chain_proxy_node_id: number | null }> = {}) {
  return {
    raw_url: node.raw_url || "",
    node_name: overrides.node_name ?? node.node_name,
    protocol: node.protocol || "",
    parsed_config: node.parsed_config || "",
    clash_config: node.clash_config || "",
    enabled: node.enabled !== false,
    tag: node.tag || "",
    tags: node.tags || [],
    inbound_tag: node.inbound_tag || "",
    chain_proxy_node_id: overrides.chain_proxy_node_id ?? node.chain_proxy_node_id ?? null,
    relay_group_name: node.relay_group_name || "",
    relay_group_node_ids: node.relay_group_node_ids || null,
  };
}

function prettyRoutedJSON(value: unknown) {
  if (typeof value !== "string") return JSON.stringify(value ?? {}, null, 2);
  try { return JSON.stringify(JSON.parse(value), null, 2); } catch { return value || "{}"; }
}

export function RoutedOutboundDialog({ token, nodes, onChanged, onClose, onNotice }: { token: string; nodes: XrayNode[]; onChanged: () => Promise<void>; onClose: () => void; onNotice: ToolNotice }) {
  const [items, setItems] = useState<RoutedEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [credentials, setCredentials] = useState<RoutedEntry | null>(null);
  const [renaming, setRenaming] = useState<RoutedEntry | null>(null);
  const [nextName, setNextName] = useState("");
  const parents = useMemo(() => nodes.filter((node) => node.node_type !== "routed" && node.inbound_tag && node.original_server), [nodes]);
  const load = async () => {
    setLoading(true);
    try {
      const batches = await Promise.all(parents.map(async (parent) => ({ parent, response: await fetchRoutedOutbounds(token, parent.id) })));
      setItems(batches.flatMap(({ parent, response }) => (response.items ?? []).map((raw) => ({ parent, raw, node: routedNode(raw) }))));
    } finally { setLoading(false); }
  };
  useEffect(() => { void load().catch((error) => onNotice("error", error instanceof Error ? error.message : "读取路由出站失败")); }, [token, parents.map((node) => node.id).join(",")]);
  const remove = async (entry: RoutedEntry) => {
    if (!window.confirm(`确认删除路由出站「${entry.node.node_name}」？\n\n这会清理 Agent 上的出站、路由规则、管理员客户端和已绑定用户子账号；凭据会按正式版规则保留用于续费恢复。`)) return;
    setBusy(true);
    try { await deleteRoutedOutbound(token, entry.node.id); await onChanged(); await load(); onNotice("success", "路由出站已删除并重新读取"); }
    catch (error) { onNotice("error", error instanceof Error ? error.message : "删除路由出站失败"); }
    finally { setBusy(false); }
  };
  const rename = async () => {
    if (!renaming || !nextName.trim()) return;
    setBusy(true);
    try { await updateNode(token, renaming.node.id, { node_name: nextName.trim() }); await onChanged(); await load(); setRenaming(null); onNotice("success", "显示名称已更新，出站 Tag 保持不变"); }
    catch (error) { onNotice("error", error instanceof Error ? error.message : "改名失败"); }
    finally { setBusy(false); }
  };
  return <>
    <ToolDialog title="路由出站管理" subtitle="管理挂在物理节点下、共用父入站并路由到独立出站的虚拟节点" onClose={onClose}>
      <section className="node-routed-explanation">
        <h3>说明</h3>
        <p>路由出站是挂在物理节点下的虚拟节点，共用父节点 inbound，但流量会被路由到独立的出站。</p>
        <p>把 routed 节点加入套餐后，用户绑定套餐时会自动创建子账号并加入 rule.user；退订时下线，凭据仍会保留以便续费恢复。</p>
        <p><strong>创建路由出站：</strong>关闭此窗口，在节点列表找到目标物理节点，打开“操作”并选择“新增落地节点”。</p>
      </section>
      {loading ? <ToolLoading text="正在读取路由出站" /> : items.length ? <div className="node-tool-list node-routed-list">{items.map((entry) => <article key={entry.node.id}>
        <div><strong>{entry.node.node_name}</strong><p>父节点 #{entry.parent.id} · {entry.parent.node_name}</p><p>服务器：{entry.node.original_server || entry.parent.original_server || "--"}</p><p>出站 Tag：{entry.node.routed_outbound_tag || "--"}</p></div>
        <div className="node-routed-actions"><button type="button" onClick={() => setCredentials(entry)}><KeyRound />管理员凭据</button><button type="button" onClick={() => { setRenaming(entry); setNextName(entry.node.node_name); }}><Edit3 />改名</button><button className="danger" type="button" disabled={busy} onClick={() => void remove(entry)}><Trash2 />删除</button></div>
      </article>)}</div> : <div className="node-routed-empty"><Route /><strong>还没有路由出站</strong><p>请从物理节点的“新增落地节点”开始创建</p></div>}
    </ToolDialog>
    {credentials && <ToolDialog title="管理员凭据" subtitle={credentials.node.node_name} onClose={() => setCredentials(null)}>
      <label className="node-readonly-field"><span>Email</span><div><code>{String(routedValue(credentials.raw, "routed_admin_email", "RoutedAdminEmail") || "--")}</code><button type="button" onClick={() => void navigator.clipboard.writeText(String(routedValue(credentials.raw, "routed_admin_email", "RoutedAdminEmail") || ""))}><Copy /></button></div></label>
      <label><span>凭据 JSON</span><textarea className="node-json-editor compact" readOnly value={prettyRoutedJSON(routedValue(credentials.raw, "routed_admin_credential", "RoutedAdminCredential"))} /></label>
      <label><span>出站 JSON</span><textarea className="node-json-editor compact" readOnly value={prettyRoutedJSON(routedValue(credentials.raw, "routed_outbound_json", "RoutedOutboundJSON"))} /></label>
    </ToolDialog>}
    {renaming && <ToolDialog title="修改显示名称" subtitle="只修改订阅显示名称，不改变 Outbound Tag" onClose={() => setRenaming(null)}><div className="node-form-grid"><label className="wide"><span>节点名称</span><input value={nextName} onChange={(event) => setNextName(event.target.value)} /></label></div><div className="node-dialog-actions"><button type="button" onClick={() => setRenaming(null)}>取消</button><button className="primary" type="button" disabled={busy || !nextName.trim()} onClick={() => void rename()}><Save />保存</button></div></ToolDialog>}
  </>;
}

function parseProxy(node: XrayNode) {
  for (const raw of [node.clash_config, node.parsed_config]) {
    try { const parsed = JSON.parse(raw || "") as Record<string, unknown>; if (parsed && typeof parsed === "object") return parsed; } catch { /* Ignore invalid historical data. */ }
  }
  return {} as Record<string, unknown>;
}

function compactObject(value: Record<string, unknown>): Record<string, unknown> {
  Object.keys(value).forEach((key) => {
    const current = value[key];
    if (current === "" || current == null) delete value[key];
    else if (typeof current === "object" && !Array.isArray(current)) compactObject(current as Record<string, unknown>);
  });
  return value;
}

function targetOutbound(node: XrayNode) {
  const proxy = parseProxy(node); const type = String(proxy.type || node.protocol || "").toLowerCase().replace("shadowsocks", "ss"); const address = String(proxy.server || ""); const port = Number(proxy.port) || 0;
  const network = String(proxy.network || ""); const security = proxy["reality-opts"] ? "reality" : proxy.tls ? "tls" : "";
  const streamSettings = compactObject({ network, security, wsSettings: proxy["ws-opts"], grpcSettings: proxy["grpc-opts"], realitySettings: proxy["reality-opts"], tlsSettings: proxy.sni || proxy.servername || proxy.alpn ? compactObject({ serverName: proxy.sni || proxy.servername, alpn: proxy.alpn }) : undefined });
  if (type === "ss") return compactObject({ protocol: "shadowsocks", settings: { servers: [compactObject({ address, port, method: proxy.cipher, password: proxy.password })] }, streamSettings });
  if (type === "trojan") return compactObject({ protocol: "trojan", settings: { servers: [compactObject({ address, port, password: proxy.password })] }, streamSettings });
  if (type === "vless" || type === "vmess") return compactObject({ protocol: type, settings: { vnext: [{ address, port, users: [compactObject({ id: proxy.uuid || proxy.id, encryption: proxy.encryption || "none", flow: proxy.flow })] }] }, streamSettings });
  if (type === "socks5" || type === "socks") return compactObject({ protocol: "socks", settings: { servers: [compactObject({ address, port, users: proxy.username || proxy.password ? [{ user: proxy.username || "", pass: proxy.password || "" }] : undefined })] } });
  return compactObject({ protocol: type || "freedom", settings: { servers: [compactObject({ address, port })] }, streamSettings });
}

function routedLabelFor(node: XrayNode) {
  const clean = node.node_name.normalize("NFKD").replace(/[^a-zA-Z0-9-]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 27);
  return `rout-${clean || node.id}`.slice(0, 32);
}

export function LandingNodeDialog({ token, source, nodes, onChanged, onClose, onNotice }: { token: string; source: XrayNode; nodes: XrayNode[]; onChanged: () => Promise<void>; onClose: () => void; onNotice: ToolNotice }) {
  const [scope, setScope] = useState<"all" | "routed">("all");
  const [query, setQuery] = useState("");
  const [targetId, setTargetId] = useState(0);
  const [label, setLabel] = useState("");
  const [busy, setBusy] = useState(false);
  const targets = useMemo(() => nodes.filter((node) => node.id !== source.id && node.node_type !== "routed" && !node.chain_proxy_node_id && node.clash_config && `${node.node_name} ${node.protocol || ""} ${(node.tags || []).join(" ")}`.toLowerCase().includes(query.trim().toLowerCase())), [nodes, query, source.id]);
  const selected = nodes.find((node) => node.id === targetId);
  const select = (node: XrayNode) => { setTargetId(node.id); if (!label.trim()) setLabel(routedLabelFor(node)); };
  const save = async () => {
    if (!selected) throw new Error("请选择落地节点");
    setBusy(true);
    try {
      if (scope === "all") await updateNode(token, source.id, nodeMutation(source, { chain_proxy_node_id: selected.id }));
      else {
        const clean = label.trim();
        if (!/^[a-zA-Z0-9-]{2,32}$/.test(clean)) throw new Error("Label 只能包含字母、数字和短横线，长度 2-32");
        if (!source.original_server || !source.inbound_tag) throw new Error("源节点缺少服务器或关联入站，不能创建路由出站");
        await createRoutedOutbound(token, { parent_node_id: source.id, target_node_id: selected.id, label: clean, outbound: targetOutbound(selected), node_name: `${source.node_name}-${clean}` });
      }
      await onChanged(); onNotice("success", scope === "all" ? "整个节点的落地已配置" : "路由出站子节点已创建"); onClose();
    } finally { setBusy(false); }
  };
  return <ToolDialog title="新增落地节点" subtitle={`为「${source.node_name}」选择落地节点`} onClose={onClose}>
    <div className="node-subpanel"><h3>作用范围</h3><div className="node-dialog-tabs"><button className={scope === "all" ? "active" : ""} onClick={() => setScope("all")}><strong>整个节点</strong><small>源入站的所有用户共享此落地</small></button><button className={scope === "routed" ? "active" : ""} onClick={() => setScope("routed")}><strong>子节点（路由出站）</strong><small>创建可单独加入套餐的虚拟子节点</small></button></div></div>
    <div className="node-inline-input"><Search /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索节点名称、协议或标签" /></div>
    <div className="node-tool-list selectable node-landing-targets">{targets.map((node) => <label key={node.id} className={targetId === node.id ? "selected" : ""}><input type="radio" name="landing-target" checked={targetId === node.id} onChange={() => select(node)} /><div><strong>{node.node_name}</strong><p>{(node.protocol || "node").toUpperCase()} · {node.original_server || "外部节点"}</p><p>{(node.tags || []).join(" · ")}</p></div></label>)}{!targets.length && <div className="node-empty">没有可用的落地节点</div>}</div>
    {scope === "routed" && <div className="node-form-grid"><label className="wide"><span>Label</span><input value={label} onChange={(event) => setLabel(event.target.value)} placeholder="例如 rout-HK" /><small>仅允许字母、数字和短横线，长度 2-32；选择节点后会自动填写。</small></label></div>}
    {selected && <div className="node-selected-target"><Check /><span>已选择</span><strong>{selected.node_name}</strong></div>}
    <div className="node-dialog-actions"><button type="button" onClick={onClose}>取消</button><button className="primary" type="button" disabled={busy || !selected || (scope === "routed" && !/^[a-zA-Z0-9-]{2,32}$/.test(label.trim()))} onClick={() => void save().catch((error) => onNotice("error", error instanceof Error ? error.message : "配置失败"))}><Plus />{scope === "all" ? "保存落地" : "创建路由出站"}</button></div>
  </ToolDialog>;
}

export function ExternalSyncDialog({ token, initial, onSession, onClose, onNotice, onChanged }: { token: string; initial?: { sessionId: string; candidates: ExternalSyncCandidate[] } | null; onSession: (value: { sessionId: string; candidates: ExternalSyncCandidate[] } | null) => void; onClose: () => void; onNotice: ToolNotice; onChanged: () => Promise<void> }) {
  const [busy, setBusy] = useState(false); const [sessionId, setSessionId] = useState(initial?.sessionId || ""); const [candidates, setCandidates] = useState<ExternalSyncCandidate[]>(initial?.candidates || []); const [selected, setSelected] = useState<Set<string>>(() => new Set((initial?.candidates || []).map((item) => item.id)));
  const sync = async () => { setBusy(true); try { const resp = await syncExternalSubscriptions(token, true); const next = resp.new_nodes ?? []; const nextSession = { sessionId: resp.session_id || "", candidates: next }; setSessionId(nextSession.sessionId); setCandidates(next); setSelected(new Set(next.map((item) => item.id))); onSession(nextSession.sessionId && next.length ? nextSession : null); await onChanged(); onNotice("success", next.length ? `同步完成，发现 ${next.length} 个新节点，请选择保存` : resp.message || "外部订阅同步完成"); } catch (error) { onNotice("error", error instanceof Error ? error.message : "同步失败"); } finally { setBusy(false); } };
  const save = async () => { setBusy(true); try { const resp = await confirmExternalSync(token, sessionId, [...selected]); onSession(null); await onChanged(); onNotice("success", resp.message || `已保存 ${selected.size} 个节点`); onClose(); } catch (error) { onNotice("error", error instanceof Error ? error.message : "保存失败"); } finally { setBusy(false); } };
  return <ToolDialog title="同步外部订阅" subtitle="拉取已配置的外部订阅；已有节点会更新，新节点由你选择后保存" onClose={onClose}>
    {!sessionId && <div className="node-confirm-panel"><RefreshCw /><p>同步会访问当前账号已保存的外部订阅，不会自动保存本次发现的新节点。</p><button className="primary" disabled={busy} onClick={() => void sync()}>{busy ? "同步中" : "开始同步"}</button></div>}
    {sessionId && <><div className="node-dialog-actions"><button onClick={() => setSelected(selected.size === candidates.length ? new Set() : new Set(candidates.map((item) => item.id)))}>{selected.size === candidates.length ? "取消全选" : "全选"}</button><button className="primary" disabled={busy} onClick={() => void save()}><Save />保存 {selected.size} 个</button></div><div className="node-tool-list selectable">{candidates.map((item) => <label key={item.id}><input type="checkbox" checked={selected.has(item.id)} onChange={() => setSelected((current) => { const next = new Set(current); next.has(item.id) ? next.delete(item.id) : next.add(item.id); return next; })} /><div><strong>{item.name}</strong><p>{item.subscription_name} · {item.protocol.toUpperCase()} · {item.server}:{item.port || "--"}</p></div></label>)}</div></>}
  </ToolDialog>;
}

export function DisableSkipCertDialog({ token, nodes, onClose, onNotice, onChanged }: { token: string; nodes: XrayNode[]; onClose: () => void; onNotice: ToolNotice; onChanged: () => Promise<void> }) {
  const eligible = useMemo(() => nodes.filter((node) => { try { return JSON.parse(node.clash_config || "{}")["skip-cert-verify"] === true; } catch { return false; } }), [nodes]);
  const [selected, setSelected] = useState<Set<number>>(() => new Set(eligible.map((node) => node.id)));
  const save = async () => { if (!selected.size || !window.confirm(`确认关闭 ${selected.size} 个节点的跳过证书验证？`)) return; await batchDisableNodeSkipCert(token, [...selected]); await onChanged(); onNotice("success", `已关闭 ${selected.size} 个节点的跳过证书验证`); onClose(); };
  return <ToolDialog title="关闭跳过证书验证" subtitle="仅显示当前启用了 skip-cert-verify 的节点" onClose={onClose}><div className="node-dialog-actions"><button onClick={() => setSelected(selected.size === eligible.length ? new Set() : new Set(eligible.map((node) => node.id)))}>{selected.size === eligible.length ? "取消全选" : "全选"}</button><button className="primary" disabled={!selected.size} onClick={() => void save().catch((error) => onNotice("error", error instanceof Error ? error.message : "修改失败"))}><Check />确认关闭</button></div><div className="node-tool-list selectable">{eligible.map((node) => <label key={node.id}><input type="checkbox" checked={selected.has(node.id)} onChange={() => setSelected((current) => { const next = new Set(current); next.has(node.id) ? next.delete(node.id) : next.add(node.id); return next; })} /><div><strong>{node.node_name}</strong><p>{node.original_server || "外部节点"}</p></div></label>)}{!eligible.length && <div className="node-empty">没有启用跳过证书验证的节点</div>}</div></ToolDialog>;
}

export function SnellOptionsDialog({ token, nodes, onClose, onNotice, onChanged }: { token: string; nodes: XrayNode[]; onClose: () => void; onNotice: ToolNotice; onChanged: () => Promise<void> }) {
  const eligible = useMemo(() => nodes.filter((node) => node.protocol?.toLowerCase() === "snell" || (() => { try { return JSON.parse(node.clash_config || "{}").type?.toLowerCase() === "snell"; } catch { return false; } })()), [nodes]);
  const [selected, setSelected] = useState<Set<number>>(() => new Set(eligible.map((node) => node.id))); const [tfo, setTfo] = useState("unchanged"); const [udp, setUdp] = useState("unchanged");
  const save = async () => { const options: { tfo?: boolean; udp_relay?: boolean } = {}; if (tfo !== "unchanged") options.tfo = tfo === "true"; if (udp !== "unchanged") options.udp_relay = udp === "true"; if (!Object.keys(options).length) throw new Error("至少选择一项要修改的参数"); await batchUpdateSnellOptions(token, [...selected], options); await onChanged(); onNotice("success", `已更新 ${selected.size} 个 Snell 节点`); onClose(); };
  return <ToolDialog title="Snell 选项" subtitle="批量调整 Snell 节点的 TFO 和 UDP Relay" onClose={onClose}><div className="node-form-grid"><label><span>TFO</span><select value={tfo} onChange={(event) => setTfo(event.target.value)}><option value="unchanged">保持不变</option><option value="true">开启</option><option value="false">关闭</option></select></label><label><span>UDP Relay</span><select value={udp} onChange={(event) => setUdp(event.target.value)}><option value="unchanged">保持不变</option><option value="true">开启</option><option value="false">关闭</option></select></label></div><div className="node-dialog-actions"><button onClick={() => setSelected(selected.size === eligible.length ? new Set() : new Set(eligible.map((node) => node.id)))}>{selected.size === eligible.length ? "取消全选" : "全选"}</button><button className="primary" disabled={!selected.size} onClick={() => void save().catch((error) => onNotice("error", error instanceof Error ? error.message : "修改失败"))}><Save />保存</button></div><div className="node-tool-list selectable">{eligible.map((node) => <label key={node.id}><input type="checkbox" checked={selected.has(node.id)} onChange={() => setSelected((current) => { const next = new Set(current); next.has(node.id) ? next.delete(node.id) : next.add(node.id); return next; })} /><div><strong>{node.node_name}</strong><p>{node.original_server || "外部节点"}</p></div></label>)}{!eligible.length && <div className="node-empty">当前没有 Snell 节点</div>}</div></ToolDialog>;
}

export function useNodeTrafficNameSetting(token: string, onNotice: ToolNotice) {
  const [enabled, setEnabled] = useState(false); const [loading, setLoading] = useState(true);
  useEffect(() => { fetchPackageNodeTrafficName(token).then((resp) => setEnabled(Boolean(resp.enabled))).catch(() => undefined).finally(() => setLoading(false)); }, [token]);
  const toggle = async () => { const next = !enabled; try { const resp = await updatePackageNodeTrafficName(token, next); setEnabled(Boolean(resp.enabled)); onNotice("success", next ? "节点名称显示流量已开启" : "节点名称显示流量已关闭"); } catch (error) { onNotice("error", error instanceof Error ? error.message : "更新失败"); } };
  return { enabled, loading, toggle };
}

export function ClearNodesConfirmDialog({ count, onClose, onConfirm }: { count: number; onClose: () => void; onConfirm: () => Promise<void> }) {
  const [confirmText, setConfirmText] = useState(""); const [busy, setBusy] = useState(false);
  return <ToolDialog title="清空全部节点" subtitle="此操作会同步清理受管节点关联的远端入站、出站和路由" onClose={onClose}><div className="node-danger-panel"><AlertTriangle /><p>将删除当前账号可删除的全部 {count} 个节点。请输入“清空全部”确认。</p><input value={confirmText} onChange={(event) => setConfirmText(event.target.value)} placeholder="清空全部" /><button className="danger" disabled={busy || confirmText !== "清空全部"} onClick={() => void (async () => { setBusy(true); try { await onConfirm(); onClose(); } finally { setBusy(false); } })()}><Trash2 />确认清空</button></div></ToolDialog>;
}
