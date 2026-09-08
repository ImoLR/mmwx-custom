import React, { useCallback, useEffect, useMemo, useState } from "react";
import { DndContext, PointerSensor, TouchSensor, closestCenter, useSensor, useSensors, type DragEndEvent } from "@dnd-kit/core";
import { SortableContext, arrayMove, useSortable, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import {
  AlertTriangle,
  ChevronDown,
  CheckCircle2,
  Copy,
  Edit3,
  Eye,
  FileJson,
  Filter,
  GripVertical,
  Link2,
  Loader2,
  MoreHorizontal,
  Plus,
  RefreshCw,
  Route,
  Search,
  Server,
  Settings2,
  Tags,
  Trash2,
  UploadCloud,
  X,
  Zap,
} from "lucide-react";
import {
  batchCreateNodes,
  batchDeleteNodes,
  batchDisableNodeSkipCert,
  batchRenameNodes,
  batchTcpingNodes,
  batchUpdateSnellOptions,
  cancelNodeRelay,
  clearNodes,
  copyNodeWithRelay,
  createNode,
  createNodeTempSubscription,
  deleteNode,
  fetchNodeRelatedInbounds,
  fetchNodeSubscription,
  fetchNodeTags,
  fetchNodeURI,
  fetchXrayNodes,
  parseNodeURIs,
  restoreNodeServer,
  resolveDNSHostname,
  setNodeRelay,
  tcpingNode,
  updateNode,
  updateNodeConfig,
  updateNodeServer,
  fetchUserConfig,
  updateUserConfig,
} from "./api";
import { serverRegionFromFields } from "./geo";
import type { ExternalSyncCandidate, NodeMutationRequest, RemoteServer, XrayNode } from "./types";
import { ManagedNodeCreateDialog } from "./xray-manager";
import {
  ClearNodesConfirmDialog,
  DisableSkipCertDialog,
  ExternalSyncDialog,
  LandingNodeDialog,
  RoutedOutboundDialog,
  SnellOptionsDialog,
  SpeedTestDialog,
  TunnelManagerDialog,
  URIManagerDialog,
  useNodeTrafficNameSetting,
} from "./node-manager-tools";

type NodeManagementPageProps = {
  token: string;
  servers: RemoteServer[];
  username: string;
};

type Notice = { tone: "success" | "error" | "info"; text: string } | null;
type Dialog =
  | { kind: "details"; node: XrayNode; tab?: "clash" | "parsed" | "raw" | "related" | "temp" }
  | { kind: "edit"; node: XrayNode }
  | { kind: "duplicates" }
  | { kind: "manual" }
  | { kind: "batch" }
  | { kind: "add-managed" }
  | { kind: "tunnels" }
  | { kind: "routed" }
  | { kind: "landing"; node: XrayNode }
  | { kind: "speedtest" }
  | { kind: "uris" }
  | { kind: "external-sync" }
  | { kind: "skip-cert" }
  | { kind: "snell" }
  | { kind: "clear-all" }
  | null;

type ParsedProxy = Record<string, unknown>;

const ALL = "all";
const IMPORT_PROTOCOLS = ["自动识别", "vless", "vmess", "trojan", "ss", "socks5", "hysteria2", "anytls", "snell", "tuic", "wireguard"];

export function NodeManagementPage({ token, servers, username }: NodeManagementPageProps) {
  const [nodes, setNodes] = useState<XrayNode[]>([]);
  const [tags, setTags] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState<Notice>(null);
  const [query, setQuery] = useState("");
  const [protocol, setProtocol] = useState(ALL);
  const [tag, setTag] = useState(ALL);
  const [serverName, setServerName] = useState(ALL);
  const [stateFilter, setStateFilter] = useState(ALL);
  const [selected, setSelected] = useState<Set<number>>(() => new Set());
  const [dialog, setDialog] = useState<Dialog>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const [sortMode, setSortMode] = useState(false);
  const [nodeOrder, setNodeOrder] = useState<number[]>([]);
  const [userConfig, setUserConfig] = useState<Record<string, unknown>>({});
  const [externalSyncSession, setExternalSyncSession] = useState<{ sessionId: string; candidates: ExternalSyncCandidate[] } | null>(null);
  const [importOpen, setImportOpen] = useState(false);
  const [importMode, setImportMode] = useState<"manual" | "subscription" | "socks5">("manual");
  const [importText, setImportText] = useState("");
  const [subscriptionURL, setSubscriptionURL] = useState("");
  const [subscriptionUA, setSubscriptionUA] = useState("clash.meta");
  const [forceSkipCert, setForceSkipCert] = useState(false);
  const [relayEnabled, setRelayEnabled] = useState(false);
  const [relayServer, setRelayServer] = useState("");
  const [relayPort, setRelayPort] = useState("");
  const [importTag, setImportTag] = useState("");
  const [parsedProxies, setParsedProxies] = useState<ParsedProxy[]>([]);
  const [parsedImportMode, setParsedImportMode] = useState<typeof importMode | null>(null);
  const [latencies, setLatencies] = useState<Record<number, { loading?: boolean; text: string; ok?: boolean }>>({});
  const [socksName, setSocksName] = useState("");
  const [socksUsername, setSocksUsername] = useState("");
  const [socksPassword, setSocksPassword] = useState("");
  const [socksServer, setSocksServer] = useState("");
  const [socksPort, setSocksPort] = useState("");

  const loadNodes = useCallback(async () => {
    setLoading(true);
    try {
      const [nodeResp, tagResp, configResp] = await Promise.all([
        fetchXrayNodes(token),
        fetchNodeTags(token).catch(() => ({ tags: [] })),
        fetchUserConfig(token),
      ]);
      const nextNodes = nodeResp.nodes ?? [];
      setNodes(nextNodes);
      setTags(tagResp.tags ?? deriveTags(nextNodes));
      setUserConfig(configResp);
      const currentIds = new Set(nextNodes.map((node) => node.id));
      const savedOrder = Array.isArray(configResp.node_order) ? configResp.node_order.map(Number).filter((id) => Number.isFinite(id) && currentIds.has(id)) : [];
      setNodeOrder([...savedOrder, ...nextNodes.map((node) => node.id).filter((id) => !savedOrder.includes(id))]);
      setNotice(null);
    } catch (err) {
      setNotice({ tone: "error", text: err instanceof Error ? err.message : "读取节点失败" });
    } finally {
      setLoading(false);
    }
  }, [token]);

  useEffect(() => {
    void loadNodes();
  }, [loadNodes]);

  const orderedNodes = useMemo(() => {
    const positions = new Map(nodeOrder.map((id, index) => [id, index]));
    return [...nodes].sort((a, b) => (positions.get(a.id) ?? Number.MAX_SAFE_INTEGER) - (positions.get(b.id) ?? Number.MAX_SAFE_INTEGER));
  }, [nodeOrder, nodes]);
  const parsedNodes = useMemo(() => orderedNodes.map((node) => ({ node, parsed: parseNodeConfig(node) })), [orderedNodes]);
  const protocols = useMemo(() => countValues(parsedNodes.map(({ node, parsed }) => normalizeProtocol(node.protocol || stringValue(parsed.type)))), [parsedNodes]);
  const serverOptions = useMemo(() => countValues(parsedNodes.map(({ node }) => node.original_server || "外部节点")), [parsedNodes]);
  const tagOptions = useMemo(() => countValues(nodes.flatMap((node) => nodeTags(node)).filter(Boolean)), [nodes]);
  const selectedNodes = useMemo(() => nodes.filter((node) => selected.has(node.id)), [nodes, selected]);
  const duplicateGroups = useMemo(() => findDuplicateGroups(nodes), [nodes]);

  const filtered = useMemo(() => {
    const text = query.trim().toLowerCase();
    return parsedNodes.filter(({ node, parsed }) => {
      const currentProtocol = normalizeProtocol(node.protocol || stringValue(parsed.type));
      const currentTags = nodeTags(node);
      const host = stringValue(parsed.server);
      const port = stringValue(parsed.port);
      const haystack = [
        node.node_name,
        currentProtocol,
        host,
        port,
        node.original_server,
        node.inbound_tag,
        node.routed_outbound_tag,
        node.relay_orig_server,
        ...currentTags,
      ].join(" ").toLowerCase();
      if (text && !haystack.includes(text)) return false;
      if (protocol !== ALL && currentProtocol !== protocol) return false;
      if (tag !== ALL && !currentTags.includes(tag)) return false;
      if (serverName !== ALL && (node.original_server || "外部节点") !== serverName) return false;
      if (stateFilter === "enabled" && node.enabled === false) return false;
      if (stateFilter === "disabled" && node.enabled !== false) return false;
      if (stateFilter === "relay" && !node.relay_orig_server) return false;
      if (stateFilter === "routed" && node.node_type !== "routed") return false;
      return true;
    });
  }, [parsedNodes, protocol, query, serverName, stateFilter, tag]);

  const run = useCallback(async (label: string, action: () => Promise<void>) => {
    setBusy(label);
    setNotice(null);
    try {
      await action();
      await loadNodes();
    } catch (err) {
      setNotice({ tone: "error", text: err instanceof Error ? err.message : `${label}失败` });
    } finally {
      setBusy("");
    }
  }, [loadNodes]);

  const parseImport = async () => {
    const label = importMode === "subscription" ? "拉取订阅" : "解析节点";
    setBusy(label);
    setNotice(null);
    try {
      const resp = importMode === "socks5"
        ? buildSocks5ParseResponse(socksName, socksUsername, socksPassword, socksServer, socksPort, importTag)
        : importMode === "subscription"
          ? await fetchNodeSubscription(token, subscriptionURL.trim(), subscriptionUA.trim(), false)
          : await parseNodeURIs(token, importText, forceSkipCert);
      const proxies = resp.proxies ?? [];
      setParsedProxies(proxies);
      setParsedImportMode(importMode);
      if (!importTag.trim() && resp.suggested_tag) setImportTag(resp.suggested_tag);
      setNotice({ tone: "success", text: `已解析 ${resp.count ?? proxies.length} 个节点，确认后可保存` });
    } catch (err) {
      setNotice({ tone: "error", text: err instanceof Error ? err.message : `${label}失败` });
    } finally {
      setBusy("");
    }
  };

  const saveParsed = async () => {
    if (parsedProxies.length === 0) {
      setNotice({ tone: "error", text: "请先解析节点" });
      return;
    }
    const useRelay = parsedImportMode === "manual" && relayEnabled && Boolean(relayServer.trim());
    const relayPortValue = toPositiveInt(relayPort, 0);
    if (useRelay && relayPort.trim() && (relayPortValue <= 0 || relayPortValue > 65535)) {
      setNotice({ tone: "error", text: "中转端口必须留空或填写 1-65535" });
      return;
    }
    const batch = parsedProxies.map((proxy) => ({
      ...proxyToNodeRequest(proxy, importTag.trim()),
      ...(useRelay ? { relay_server: relayServer.trim(), relay_port: relayPortValue } : {}),
    }));
    await run("保存导入", async () => {
      await batchCreateNodes(token, batch);
      setImportText("");
      setSubscriptionURL("");
      setParsedProxies([]);
      setParsedImportMode(null);
      setImportTag("");
      setSocksName("");
      setSocksUsername("");
      setSocksPassword("");
      setSocksServer("");
      setSocksPort("");
    });
  };

  const testOne = async (node: XrayNode) => {
    const parsed = parseNodeConfig(node);
    const host = stringValue(parsed.server);
    const port = numberValue(parsed.port);
    if (!host || !port) {
      setLatencies((current) => ({ ...current, [node.id]: { text: "缺少地址", ok: false } }));
      return;
    }
    setLatencies((current) => ({ ...current, [node.id]: { loading: true, text: "测试中" } }));
    try {
      const result = await tcpingNode(token, { host, port, timeout: 5000, protocol: normalizeProtocol(node.protocol || stringValue(parsed.type)) });
      setLatencies((current) => ({
        ...current,
        [node.id]: { text: result.success ? `${Math.round(result.latency ?? 0)} ms` : result.error || "失败", ok: Boolean(result.success) },
      }));
    } catch (err) {
      setLatencies((current) => ({ ...current, [node.id]: { text: err instanceof Error ? err.message : "失败", ok: false } }));
    }
  };

  const testSelected = async () => {
    const targets = selectedNodes.map((node) => {
      const parsed = parseNodeConfig(node);
      return {
        node,
        req: {
          host: stringValue(parsed.server),
          port: numberValue(parsed.port),
          timeout: 5000,
          protocol: normalizeProtocol(node.protocol || stringValue(parsed.type)),
        },
      };
    }).filter((item) => item.req.host && item.req.port > 0);
    if (targets.length === 0) return;
    setBusy("批量 TCPing");
    setLatencies((current) => {
      const next = { ...current };
      targets.forEach(({ node }) => { next[node.id] = { loading: true, text: "测试中" }; });
      return next;
    });
    try {
      const results = await batchTcpingNodes(token, targets.map((item) => item.req));
      setLatencies((current) => {
        const next = { ...current };
        targets.forEach(({ node }, index) => {
          const result = results[index] ?? {};
          next[node.id] = { text: result.success ? `${Math.round(result.latency ?? 0)} ms` : result.error || "失败", ok: Boolean(result.success) };
        });
        return next;
      });
    } catch (err) {
      setNotice({ tone: "error", text: err instanceof Error ? err.message : "批量 TCPing 失败" });
    } finally {
      setBusy("");
    }
  };

  const toggleSelected = (nodeId: number) => {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(nodeId)) next.delete(nodeId);
      else next.add(nodeId);
      return next;
    });
  };

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 8 } }),
    useSensor(TouchSensor, { activationConstraint: { delay: 350, tolerance: 8 } }),
  );
  const toolNotice = useCallback((tone: "success" | "error" | "info", text: string) => setNotice({ tone, text }), []);
  const trafficName = useNodeTrafficNameSetting(token, toolNotice);
  const onDragEnd = async ({ active, over }: DragEndEvent) => {
    if (!over || active.id === over.id) return;
    const visibleIds = filtered.map(({ node }) => node.id);
    const oldIndex = visibleIds.indexOf(Number(active.id));
    const newIndex = visibleIds.indexOf(Number(over.id));
    if (oldIndex < 0 || newIndex < 0) return;
    const nextVisible = arrayMove(visibleIds, oldIndex, newIndex);
    const visible = new Set(visibleIds);
    let cursor = 0;
    const next = nodeOrder.map((id) => visible.has(id) ? nextVisible[cursor++] : id);
    setNodeOrder(next);
    try {
      const saved = await updateUserConfig(token, { ...userConfig, node_order: next });
      setUserConfig(saved);
      setNotice({ tone: "success", text: "节点顺序已保存" });
    } catch (error) {
      setNodeOrder(nodeOrder);
      setNotice({ tone: "error", text: error instanceof Error ? error.message : "保存节点顺序失败" });
    }
  };
  const openTool = (next: NonNullable<Dialog>) => { setMenuOpen(false); setDialog(next); };

  return (
    <div className="node-manager" aria-busy={loading || Boolean(busy)}>
      <section className="node-hero">
        <div>
          <h1>节点管理</h1>
          <p>导入、筛选、编辑、复制 URI、TCPing、批量处理和中转配置。</p>
        </div>
        <div className="node-global-menu-wrap">
          <button type="button" onClick={() => setMenuOpen((value) => !value)} disabled={loading || Boolean(busy)} aria-label="节点管理菜单" aria-expanded={menuOpen}><MoreHorizontal /></button>
          {menuOpen && <div className="node-global-menu" role="menu">
            <button role="menuitem" onClick={() => { setMenuOpen(false); void loadNodes(); }}><RefreshCw />刷新节点</button>
            <button role="menuitem" className={sortMode ? "active" : ""} onClick={() => { setSortMode((value) => !value); setMenuOpen(false); }}><GripVertical />{sortMode ? "退出排序模式" : "排序模式"}</button>
            <button role="menuitem" onClick={() => openTool({ kind: "add-managed" })}><Plus />添加节点</button>
            <button role="menuitem" onClick={() => openTool({ kind: "tunnels" })}><Link2 />Tunnel 管理</button>
            <button role="menuitem" onClick={() => openTool({ kind: "routed" })}><FileJson />路由出站</button>
            <button role="menuitem" onClick={() => openTool({ kind: "speedtest" })}><Zap />节点测速</button>
            <button role="menuitem" onClick={() => openTool({ kind: "uris" })}><Link2 />URI 管理</button>
            <button role="menuitem" onClick={() => openTool({ kind: "external-sync" })}><RefreshCw />同步外部订阅</button>
            {externalSyncSession && <button role="menuitem" onClick={() => openTool({ kind: "external-sync" })}><Plus />订阅解析完成，请选择需要保存的节点</button>}
            <button role="menuitem" onClick={() => openTool({ kind: "duplicates" })}><Copy />删除重复</button>
            <span className="node-global-menu-label">辅助功能</span>
            <button role="menuitem" onClick={() => openTool({ kind: "skip-cert" })}><AlertTriangle />关闭跳过证书验证</button>
            <button role="menuitem" onClick={() => openTool({ kind: "snell" })}><Settings2 />Snell 选项</button>
            <label className="node-global-menu-toggle"><input type="checkbox" checked={trafficName.enabled} disabled={trafficName.loading} onChange={() => void trafficName.toggle()} /><span>节点名称显示流量</span></label>
            <button role="menuitem" className="danger" onClick={() => openTool({ kind: "clear-all" })}><Trash2 />清空全部</button>
          </div>}
        </div>
      </section>

      {notice && <NodeNotice notice={notice} />}

      <section className={`node-import-panel ${importOpen ? "open" : ""}`}>
        <button className="node-collapse-head" type="button" onClick={() => setImportOpen((value) => !value)} aria-expanded={importOpen}>
          <div>
            <h2>导入外部节点</h2>
            <p>支持 URI、Clash YAML、base64 订阅、Surge 行；保存后同步到订阅文件。</p>
          </div>
          <ChevronDown />
        </button>
        {importOpen && (
          <>
            <div className="node-section-head compact">
              <div>
                <h3>{importMode === "manual" ? "手动导入" : importMode === "subscription" ? "订阅导入" : "SOCKS5"}</h3>
              </div>
              <button type="button" onClick={() => setDialog({ kind: "manual" })}>
                <Plus /> 新增 JSON
              </button>
            </div>
            <div className="node-import-tabs">
              <button className={importMode === "manual" ? "active" : ""} type="button" onClick={() => setImportMode("manual")}>手动输入</button>
              <button className={importMode === "subscription" ? "active" : ""} type="button" onClick={() => setImportMode("subscription")}>订阅导入</button>
              <button className={importMode === "socks5" ? "active" : ""} type="button" onClick={() => setImportMode("socks5")}>SOCKS5</button>
            </div>
            {importMode === "manual" ? (
              <textarea value={importText} onChange={(event) => setImportText(event.target.value)} placeholder="每行一个节点，或粘贴 Clash YAML / base64 订阅内容" />
            ) : importMode === "subscription" ? (
              <div className="node-form-grid">
                <label>
                  <span>订阅 URL</span>
                  <input value={subscriptionURL} onChange={(event) => setSubscriptionURL(event.target.value)} placeholder="https://..." />
                </label>
                <label>
                  <span>User-Agent</span>
                  <select value={subscriptionUA} onChange={(event) => setSubscriptionUA(event.target.value)}>
                    <option value="clash.meta">clash.meta</option>
                    <option value="clash-meta/2.4.0">clash-meta/2.4.0</option>
                    <option value="ClashforWindows/0.20.39">Clash for Windows</option>
                    <option value="v2rayN/6.45">v2rayN</option>
                  </select>
                </label>
              </div>
            ) : (
              <div className="node-form-grid">
                <label><span>节点名称</span><input value={socksName} onChange={(event) => setSocksName(event.target.value)} placeholder="留空则自动使用「服务器:端口」" /></label>
                <label><span>用户名</span><input value={socksUsername} onChange={(event) => setSocksUsername(event.target.value)} placeholder="免认证可留空" /></label>
                <label><span>密码</span><input value={socksPassword} onChange={(event) => setSocksPassword(event.target.value)} placeholder="免认证可留空" /></label>
                <label><span>服务器</span><input value={socksServer} onChange={(event) => setSocksServer(event.target.value)} placeholder="IP 或域名" /></label>
                <label><span>端口</span><input value={socksPort} onChange={(event) => setSocksPort(event.target.value)} placeholder="1-65535" inputMode="numeric" /></label>
              </div>
            )}
            <div className="node-import-options">
              <label>
                <span>节点标签</span>
                <input value={importTag} onChange={(event) => setImportTag(event.target.value)} placeholder="例如：远程:Boil HKT" />
              </label>
              {importMode === "manual" && (
                <div className="node-manual-options">
                  <div className="node-manual-switches">
                    <label className="node-check">
                      <input type="checkbox" checked={forceSkipCert} onChange={(event) => setForceSkipCert(event.target.checked)} />
                      <span>跳过证书验证</span>
                    </label>
                    <label className="node-check">
                      <input type="checkbox" checked={relayEnabled} onChange={(event) => setRelayEnabled(event.target.checked)} />
                      <span>开启中转</span>
                    </label>
                  </div>
                  {relayEnabled && (
                    <div className="node-relay-fields">
                      <input value={relayServer} onChange={(event) => setRelayServer(event.target.value)} placeholder="中转服务器 IP 或域名" />
                      <input type="number" min="1" max="65535" inputMode="numeric" value={relayPort} onChange={(event) => setRelayPort(event.target.value)} placeholder="端口（默认使用节点端口）" />
                      <p>这些节点将通过该中转服务器连接；端口留空时保留各节点自己的端口。原服务器地址会保留，可在节点列表中修改或清除中转。</p>
                    </div>
                  )}
                </div>
              )}
              <button type="button" onClick={() => void parseImport()} disabled={Boolean(busy) || (importMode === "manual" ? !importText.trim() : importMode === "subscription" ? !subscriptionURL.trim() : !socksServer.trim() || !socksPort.trim())}>
                <UploadCloud /> {busy === "解析节点" || busy === "拉取订阅" ? "处理中" : "解析节点"}
              </button>
              <button type="button" className="primary" onClick={() => void saveParsed()} disabled={Boolean(busy) || parsedProxies.length === 0}>
                <CheckCircle2 /> 保存 {parsedProxies.length || ""} 个
              </button>
            </div>
            {parsedProxies.length > 0 && (
              <div className="node-preview-list">
                {parsedProxies.slice(0, 5).map((proxy, index) => (
                  <span key={`${String(proxy.name)}-${index}`}>{String(proxy.type || "").toUpperCase() || "NODE"} · {String(proxy.name || `节点 ${index + 1}`)}</span>
                ))}
                {parsedProxies.length > 5 && <span>还有 {parsedProxies.length - 5} 个</span>}
              </div>
            )}
          </>
        )}
      </section>

      <section className="node-toolbar">
        <label className="node-search">
          <Search />
          <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索名称、地址、标签、入站、出站" />
        </label>
        <NodeSelect icon={<Filter />} value={protocol} onChange={setProtocol} label="协议" options={[{ value: ALL, label: `全部协议 (${nodes.length})` }, ...protocols.map((item) => ({ value: item.value, label: `${item.value.toUpperCase()} (${item.count})` }))]} />
        <NodeSelect icon={<Tags />} value={tag} onChange={setTag} label="标签" options={[{ value: ALL, label: `全部标签 (${nodes.length})` }, ...tagOptions.map((item) => ({ value: item.value, label: `${item.value} (${item.count})` })), ...tags.filter((value) => !tagOptions.some((item) => item.value === value)).map((value) => ({ value, label: value }))]} />
        <NodeSelect icon={<Server />} value={serverName} onChange={setServerName} label="服务器" options={[{ value: ALL, label: `全部服务器 (${nodes.length})` }, ...serverOptions.map((item) => ({ value: item.value, label: `${item.value} (${item.count})` }))]} />
        <select value={stateFilter} onChange={(event) => setStateFilter(event.target.value)} aria-label="状态筛选">
          <option value={ALL}>全部状态</option>
          <option value="enabled">已启用</option>
          <option value="disabled">已禁用</option>
          <option value="relay">中转中</option>
          <option value="routed">路由出站</option>
        </select>
      </section>

      <section className="node-list-panel">
        <div className="node-section-head">
          <div>
            <h2>节点列表 ({filtered.length})</h2>
            <p>更改和删除节点会同步订阅；删除受管节点还会清理关联入站/出站/路由。</p>
          </div>
          <div className="node-head-actions">
            <button type="button" onClick={() => setSelected(new Set(filtered.map(({ node }) => node.id)))}>全选</button>
            <button type="button" onClick={() => setSelected(new Set())}>清空</button>
            <button type="button" onClick={() => setDialog({ kind: "batch" })} disabled={selected.size === 0}>批量</button>
          </div>
        </div>
        {loading ? (
          <div className="node-empty"><Loader2 /> 正在读取节点</div>
        ) : filtered.length === 0 ? (
          <div className="node-empty">没有符合条件的节点</div>
        ) : (
          <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={(event) => void onDragEnd(event)}>
          <SortableContext items={filtered.map(({ node }) => node.id)} strategy={verticalListSortingStrategy}>
          <div className={`node-card-list ${sortMode ? "sorting" : ""}`}>
            {filtered.map(({ node, parsed }) => (
              <SortableNodeCard key={node.id} id={node.id} disabled={!sortMode}>
              <NodeCard
                node={node}
                parsed={parsed}
                selected={selected.has(node.id)}
                latency={latencies[node.id]}
                onSelect={() => toggleSelected(node.id)}
                onDetails={() => setDialog({ kind: "details", node })}
                onEdit={() => setDialog({ kind: "edit", node })}
                onLanding={() => setDialog({ kind: "landing", node })}
                onCopy={() => void copyNodeURI(token, node, setNotice)}
                onTcping={() => void testOne(node)}
                onEmoji={() => void run("添加地区 Emoji", async () => {
                  const next = buildRegionNodeName(node, servers);
                  if (!next || next === node.node_name) {
                    setNotice({ tone: "info", text: "没有可添加的地区 Emoji，或节点名称已包含地区 Emoji" });
                    return;
                  }
                  if (!window.confirm(`确认把节点名称改为「${next}」？`)) return;
                  await updateNode(token, node.id, nodeToMutation(node, { node_name: next }));
                })}
                onResolve={() => void run("解析 IP", async () => {
                  const host = stringValue(parsed.server);
                  if (!host) throw new Error("节点缺少 server 字段");
                  const resp = await resolveDNSHostname(token, host);
                  const ip = resp.ips?.[0] || "";
                  if (!ip) throw new Error("DNS 未返回可用 IP");
                  if (!window.confirm(`确认把 ${host} 改成 ${ip}？原域名可通过“恢复原始域名”恢复。`)) return;
                  await updateNodeServer(token, node.id, ip);
                })}
                onDelete={() => void run("删除节点", async () => {
                  if (!window.confirm(`确认删除节点「${node.node_name}」？受管节点会同步清理远程资源。`)) return;
                  await deleteNode(token, node.id);
                })}
                onTemp={() => setDialog({ kind: "details", node, tab: "temp" })}
                onRestore={() => void run("恢复域名", async () => {
                  if (!node.original_domain) {
                    setNotice({ tone: "info", text: "这个节点没有记录原始域名" });
                    return;
                  }
                  if (!window.confirm(`确认恢复「${node.node_name}」的原始域名？`)) return;
                  await restoreNodeServer(token, node.id);
                })}
              />
              </SortableNodeCard>
            ))}
          </div>
          </SortableContext>
          </DndContext>
        )}
      </section>

      {dialog?.kind === "details" && (
        <NodeDetailsDialog
          token={token}
          node={nodes.find((item) => item.id === dialog.node.id) ?? dialog.node}
          initialTab={dialog.tab}
          onClose={() => setDialog(null)}
          onNotice={setNotice}
        />
      )}
      {dialog?.kind === "edit" && (
        <NodeEditDialog
          token={token}
          node={nodes.find((item) => item.id === dialog.node.id) ?? dialog.node}
          nodes={nodes}
          servers={servers}
          busy={busy}
          onClose={() => setDialog(null)}
          onRun={run}
          onNotice={setNotice}
        />
      )}
      {dialog?.kind === "duplicates" && (
        <DuplicateNodesDialog
          token={token}
          groups={duplicateGroups}
          busy={busy}
          onClose={() => setDialog(null)}
          onRun={run}
        />
      )}
      {dialog?.kind === "manual" && (
        <ManualNodeDialog
          token={token}
          busy={busy}
          onClose={() => setDialog(null)}
          onRun={run}
          onNotice={setNotice}
        />
      )}
      {dialog?.kind === "batch" && (
        <BatchNodeDialog
          token={token}
          nodes={selectedNodes}
          busy={busy}
          onClose={() => setDialog(null)}
          onRun={run}
          onNotice={setNotice}
          onTcping={() => void testSelected()}
          onClearSelection={() => setSelected(new Set())}
        />
      )}
      {dialog?.kind === "add-managed" && <ManagedNodeCreateDialog servers={servers} token={token} username={username} onClose={() => setDialog(null)} onCreated={loadNodes} />}
      {dialog?.kind === "tunnels" && <TunnelManagerDialog token={token} servers={servers} nodes={nodes} onChanged={loadNodes} onClose={() => setDialog(null)} onNotice={toolNotice} />}
      {dialog?.kind === "routed" && <RoutedOutboundDialog token={token} nodes={nodes} onChanged={loadNodes} onClose={() => setDialog(null)} onNotice={toolNotice} />}
      {dialog?.kind === "landing" && <LandingNodeDialog token={token} source={nodes.find((item) => item.id === dialog.node.id) ?? dialog.node} nodes={nodes} onChanged={loadNodes} onClose={() => setDialog(null)} onNotice={toolNotice} />}
      {dialog?.kind === "speedtest" && <SpeedTestDialog token={token} nodes={nodes} onClose={() => setDialog(null)} onNotice={toolNotice} />}
      {dialog?.kind === "uris" && <URIManagerDialog token={token} onClose={() => setDialog(null)} onNotice={toolNotice} />}
      {dialog?.kind === "external-sync" && <ExternalSyncDialog token={token} initial={externalSyncSession} onSession={setExternalSyncSession} onClose={() => setDialog(null)} onNotice={toolNotice} onChanged={loadNodes} />}
      {dialog?.kind === "skip-cert" && <DisableSkipCertDialog token={token} nodes={nodes} onClose={() => setDialog(null)} onNotice={toolNotice} onChanged={loadNodes} />}
      {dialog?.kind === "snell" && <SnellOptionsDialog token={token} nodes={nodes} onClose={() => setDialog(null)} onNotice={toolNotice} onChanged={loadNodes} />}
      {dialog?.kind === "clear-all" && <ClearNodesConfirmDialog count={nodes.length} onClose={() => setDialog(null)} onConfirm={async () => { await clearNodes(token); setSelected(new Set()); await loadNodes(); setNotice({ tone: "success", text: "全部节点已清空" }); }} />}
    </div>
  );
}

function SortableNodeCard({ id, disabled, children }: { id: number; disabled: boolean; children: React.ReactNode }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id, disabled });
  return <div ref={setNodeRef} style={{ transform: CSS.Transform.toString(transform), transition, opacity: isDragging ? 0.55 : 1 }} className="node-sortable-card">
    {!disabled && <button className="node-drag-handle" type="button" aria-label="拖动排序" {...attributes} {...listeners}><GripVertical /></button>}
    {children}
  </div>;
}

function NodeCard({
  node,
  parsed,
  selected,
  latency,
  onSelect,
  onDetails,
  onEdit,
  onLanding,
  onCopy,
  onTcping,
  onEmoji,
  onResolve,
  onRestore,
  onTemp,
  onDelete,
}: {
  node: XrayNode;
  parsed: ParsedProxy;
  selected: boolean;
  latency?: { loading?: boolean; text: string; ok?: boolean };
  onSelect: () => void;
  onDetails: () => void;
  onEdit: () => void;
  onLanding: () => void;
  onCopy: () => void;
  onTcping: () => void;
  onEmoji: () => void;
  onResolve: () => void;
  onRestore: () => void;
  onTemp: () => void;
  onDelete: () => void;
}) {
  const [menuOpen, setMenuOpen] = useState(false);
  const protocol = normalizeProtocol(node.protocol || stringValue(parsed.type));
  const host = stringValue(parsed.server);
  const port = stringValue(parsed.port);
  const transport = stringValue(parsed.network || parsed.transport) || "tcp";
  const tls = parsed["reality-opts"] ? "Reality" : parsed.tls ? "TLS" : "";
  return (
    <article className="node-card">
      <div className="node-card-top">
        <label className="node-card-check">
          <input type="checkbox" checked={selected} onChange={onSelect} />
          <span className={node.enabled === false ? "off" : "on"} />
        </label>
        <div>
          <strong>{node.node_name}</strong>
          <p>{node.original_server || "外部节点"} · {host ? `${host}${port ? `:${port}` : ""}` : "未解析地址"}</p>
        </div>
        <span className="node-protocol">{protocol.toUpperCase() || "NODE"}</span>
      </div>
      <div className="node-chip-row">
        {nodeTags(node).map((item) => <span key={item}>{item}</span>)}
        {node.inbound_tag && <span>入站 {node.inbound_tag}</span>}
        {node.node_type === "routed" && <span>路由出站</span>}
        {node.chain_proxy_node_id && <span>链式 #{node.chain_proxy_node_id}</span>}
        {node.relay_orig_server && <span>中转 {node.relay_orig_server}:{node.relay_orig_port || ""}</span>}
        {transport && <span>{transport}</span>}
        {tls && <span>{tls}</span>}
        {latency && <span className={latency.ok ? "ok" : "bad"}>{latency.loading ? "测试中" : latency.text}</span>}
      </div>
      <div className="node-card-footer">
        <button className="node-card-menu-button" type="button" onClick={() => setMenuOpen((value) => !value)} aria-expanded={menuOpen}>
          <MoreHorizontal /> 操作 <ChevronDown />
        </button>
      </div>
      {menuOpen && (
        <div className="node-action-row">
          <button type="button" onClick={onEdit}><Edit3 /> 编辑名称</button>
          <button type="button" onClick={onEdit}><Link2 /> 链式出站</button>
          <button type="button" onClick={onEmoji}><Tags /> 地区 emoji</button>
          <button type="button" onClick={onResolve}><Server /> 解析 IP</button>
          <button type="button" onClick={onRestore}><RefreshCw /> 恢复域名</button>
          <button type="button" onClick={onDetails}><Eye /> 查看配置</button>
          <button type="button" onClick={onCopy}><Copy /> 复制 URI</button>
          <button type="button" onClick={onTemp}><Link2 /> 临时订阅</button>
          <button type="button" onClick={onTcping}><Zap /> TCPing</button>
          {node.node_type !== "routed" && <button type="button" onClick={onLanding}><Route /> 新增落地节点</button>}
          <button className="danger" type="button" onClick={onDelete}><Trash2 /> 删除</button>
      </div>
      )}
    </article>
  );
}

function NodeDetailsDialog({
  token,
  node,
  initialTab = "clash",
  onClose,
  onNotice,
}: {
  token: string;
  node: XrayNode;
  initialTab?: "clash" | "parsed" | "raw" | "related" | "temp";
  onClose: () => void;
  onNotice: (notice: Notice) => void;
}) {
  const [tab, setTab] = useState<"clash" | "parsed" | "raw" | "related" | "temp">(initialTab);
  const [uri, setURI] = useState("");
  const [related, setRelated] = useState<Array<Record<string, unknown>>>([]);
  const [tempURL, setTempURL] = useState("");
  const [maxAccess, setMaxAccess] = useState(1);
  const [expireSeconds, setExpireSeconds] = useState(600);
  const [busy, setBusy] = useState("");

  const loadURI = async () => {
    setBusy("uri");
    try {
      const resp = await fetchNodeURI(token, node.id);
      setURI(resp.uri || "");
      if (resp.uri) await writeClipboard(resp.uri);
    } catch (err) {
      onNotice({ tone: "error", text: err instanceof Error ? err.message : "复制 URI 失败" });
    } finally {
      setBusy("");
    }
  };
  const loadRelated = async () => {
    setBusy("related");
    try {
      const resp = await fetchNodeRelatedInbounds(token, node.id);
      setRelated(resp.inbounds ?? []);
      setTab("related");
    } catch (err) {
      onNotice({ tone: "error", text: err instanceof Error ? err.message : "读取关联入站失败" });
    } finally {
      setBusy("");
    }
  };
  const createTemp = async () => {
    const proxy = parseNodeConfig(node);
    if (!Object.keys(proxy).length) {
      onNotice({ tone: "error", text: "节点缺少 Clash 配置，无法生成临时订阅" });
      return;
    }
    setBusy("temp");
    try {
      const resp = await createNodeTempSubscription(token, [proxy], maxAccess, expireSeconds);
      const path = resp.url || "";
      const absolute = path.startsWith("http") ? path : `${window.location.origin}${path}`;
      setTempURL(absolute);
      if (absolute) await writeClipboard(absolute);
    } catch (err) {
      onNotice({ tone: "error", text: err instanceof Error ? err.message : "生成临时订阅失败" });
    } finally {
      setBusy("");
    }
  };

  return (
    <NodeDialog title="节点详情" subtitle={node.node_name} onClose={onClose}>
      <div className="node-dialog-tabs">
        <button className={tab === "clash" ? "active" : ""} type="button" onClick={() => setTab("clash")}>Clash</button>
        <button className={tab === "parsed" ? "active" : ""} type="button" onClick={() => setTab("parsed")}>解析</button>
        <button className={tab === "raw" ? "active" : ""} type="button" onClick={() => setTab("raw")}>原始</button>
        <button className={tab === "related" ? "active" : ""} type="button" onClick={() => void loadRelated()}>关联入站</button>
        <button className={tab === "temp" ? "active" : ""} type="button" onClick={() => setTab("temp")}>临时订阅</button>
      </div>
      {tab === "clash" && <pre className="node-json">{prettyJSON(node.clash_config)}</pre>}
      {tab === "parsed" && <pre className="node-json">{prettyJSON(node.parsed_config)}</pre>}
      {tab === "raw" && <pre className="node-json">{node.raw_url || "无原始 URL"}</pre>}
      {tab === "related" && (
        <div className="node-mini-list">
          {related.length === 0 ? <p>没有关联入站</p> : related.map((item, index) => <code key={index}>{prettyJSON(item)}</code>)}
        </div>
      )}
      {tab === "temp" && (
        <div className="node-form-grid">
          <label><span>最多访问次数</span><input type="number" min={1} value={maxAccess} onChange={(event) => setMaxAccess(toPositiveInt(event.target.value, 1))} /></label>
          <label><span>有效秒数</span><input type="number" min={10} value={expireSeconds} onChange={(event) => setExpireSeconds(toPositiveInt(event.target.value, 600))} /></label>
          <button type="button" onClick={() => void createTemp()} disabled={busy === "temp"}><Link2 /> 生成并复制</button>
          {tempURL && <input readOnly value={tempURL} onFocus={(event) => event.currentTarget.select()} />}
        </div>
      )}
      <div className="node-dialog-actions">
        <button type="button" onClick={() => void loadURI()} disabled={busy === "uri"}><Copy /> 复制 URI</button>
        <button type="button" onClick={onClose}>关闭</button>
      </div>
    </NodeDialog>
  );
}

function DuplicateNodesDialog({
  token,
  groups,
  busy,
  onClose,
  onRun,
}: {
  token: string;
  groups: XrayNode[][];
  busy: string;
  onClose: () => void;
  onRun: (label: string, action: () => Promise<void>) => Promise<void>;
}) {
  return (
    <NodeDialog title="重复节点检查" subtitle={`发现 ${groups.length} 组可能重复`} onClose={onClose}>
      {groups.length === 0 ? (
        <div className="node-empty">当前没有发现同协议、同地址、同端口的重复节点</div>
      ) : (
        <div className="node-mini-list">
          {groups.map((group, index) => {
            const keep = group[0];
            const extras = group.slice(1);
            return (
              <article className="node-duplicate-card" key={`${duplicateKey(keep)}-${index}`}>
                <strong>重复组 {index + 1}</strong>
                <p>保留：{keep.node_name}</p>
                {group.map((node) => <code key={node.id}>#{node.id} {node.node_name}</code>)}
                <div className="node-dialog-actions">
                  <button className="danger" type="button" disabled={Boolean(busy) || extras.length === 0} onClick={() => void onRun("删除重复节点", async () => {
                    if (!window.confirm(`确认删除该组中除「${keep.node_name}」以外的 ${extras.length} 个重复节点？`)) return;
                    await batchDeleteNodes(token, extras.map((node) => node.id));
                    onClose();
                  })}><Trash2 /> 删除其余重复项</button>
                </div>
              </article>
            );
          })}
        </div>
      )}
    </NodeDialog>
  );
}

function NodeEditDialog({
  token,
  node,
  nodes,
  servers,
  busy,
  onClose,
  onRun,
  onNotice,
}: {
  token: string;
  node: XrayNode;
  nodes: XrayNode[];
  servers: RemoteServer[];
  busy: string;
  onClose: () => void;
  onRun: (label: string, action: () => Promise<void>) => Promise<void>;
  onNotice: (notice: Notice) => void;
}) {
  const [name, setName] = useState(node.node_name);
  const [enabled, setEnabled] = useState(node.enabled !== false);
  const [tagsValue, setTagsValue] = useState(nodeTags(node).join(", "));
  const [inboundTag, setInboundTag] = useState(node.inbound_tag || "");
  const [chainProxyNodeId, setChainProxyNodeId] = useState(node.chain_proxy_node_id ? String(node.chain_proxy_node_id) : "");
  const [relayGroupName, setRelayGroupName] = useState(node.relay_group_name || "");
  const [relayGroupIds, setRelayGroupIds] = useState((node.relay_group_node_ids || []).join(", "));
  const [config, setConfig] = useState(prettyJSON(node.clash_config));
  const [serverAddress, setServerAddress] = useState(stringValue(parseNodeConfig(node).server));
  const [relayServer, setRelayServer] = useState("");
  const [relayPort, setRelayPort] = useState("");
  const [relaySuffix, setRelaySuffix] = useState("tunnel");
  const [jsonError, setJsonError] = useState("");

  const parsedConfig = () => {
    try {
      const parsed = JSON.parse(config) as ParsedProxy;
      if (name.trim()) parsed.name = name.trim();
      return parsed;
    } catch (err) {
      setJsonError(err instanceof Error ? err.message : "JSON 格式错误");
      return null;
    }
  };

  const saveBasics = async () => {
    const parsed = parsedConfig();
    if (!parsed) return;
    const tags = splitList(tagsValue);
    const body: NodeMutationRequest = {
      raw_url: node.raw_url || "",
      node_name: name.trim(),
      protocol: normalizeProtocol(node.protocol || stringValue(parsed.type)),
      parsed_config: JSON.stringify(parsed),
      clash_config: JSON.stringify(parsed),
      enabled,
      tag: tags[0] || "",
      tags,
      inbound_tag: inboundTag.trim(),
      chain_proxy_node_id: chainProxyNodeId.trim() ? Number(chainProxyNodeId) : null,
      relay_group_name: relayGroupName.trim(),
      relay_group_node_ids: relayGroupIds.trim() ? splitList(relayGroupIds).map(Number).filter((value) => Number.isFinite(value) && value > 0) : null,
    };
    if (!body.node_name) {
      setJsonError("节点名称不能为空");
      return;
    }
    await onRun("保存节点", async () => {
      await updateNode(token, node.id, body);
      onClose();
    });
  };

  const saveConfig = async () => {
    const parsed = parsedConfig();
    if (!parsed) return;
    for (const key of ["name", "type", "server", "port"]) {
      if (!(key in parsed)) {
        setJsonError(`配置缺少必需字段：${key}`);
        return;
      }
    }
    await onRun("保存配置", async () => {
      await updateNodeConfig(token, node.id, JSON.stringify(parsed));
      onClose();
    });
  };

  const saveServer = async () => {
    if (!serverAddress.trim()) {
      setJsonError("服务器地址不能为空");
      return;
    }
    await onRun("更新地址", async () => {
      await updateNodeServer(token, node.id, serverAddress.trim());
      onClose();
    });
  };

  return (
    <NodeDialog title="编辑节点" subtitle={node.node_name} onClose={onClose}>
      <div className="node-form-grid">
        <label><span>节点名称</span><input value={name} onChange={(event) => setName(event.target.value)} /></label>
        <label><span>启用状态</span><select value={enabled ? "1" : "0"} onChange={(event) => setEnabled(event.target.value === "1")}><option value="1">启用</option><option value="0">禁用</option></select></label>
        <label><span>标签（逗号分隔）</span><input value={tagsValue} onChange={(event) => setTagsValue(event.target.value)} placeholder="VIP, 香港, 测试" /></label>
        <label><span>关联入站 Tag</span><input value={inboundTag} onChange={(event) => setInboundTag(event.target.value)} /></label>
        <label><span>链式代理节点 ID</span><input value={chainProxyNodeId} onChange={(event) => setChainProxyNodeId(event.target.value)} placeholder="留空关闭" /></label>
        <label><span>中转组名称</span><input value={relayGroupName} onChange={(event) => setRelayGroupName(event.target.value)} /></label>
        <label className="wide"><span>中转组节点 ID（逗号分隔）</span><input value={relayGroupIds} onChange={(event) => setRelayGroupIds(event.target.value)} /></label>
      </div>
      <div className="node-dialog-actions">
        <button type="button" onClick={() => void saveBasics()} disabled={Boolean(busy)}><CheckCircle2 /> 保存基础信息</button>
      </div>

      <div className="node-subpanel">
        <h3>服务器地址</h3>
        <div className="node-form-grid">
          <label><span>当前 server 字段</span><input value={serverAddress} onChange={(event) => setServerAddress(event.target.value)} /></label>
          <label><span>可参考服务器</span><select onChange={(event) => setServerAddress(event.target.value)} defaultValue=""><option value="">选择服务器地址</option>{servers.map((server) => serverAddressOptions(server).map((option) => <option key={`${server.id}-${option}`} value={option}>{server.name} · {option}</option>))}</select></label>
        </div>
        <div className="node-dialog-actions">
          <button type="button" onClick={() => void saveServer()} disabled={Boolean(busy)}>更新地址</button>
          <button type="button" onClick={() => void onRun("恢复域名", async () => { await restoreNodeServer(token, node.id); onClose(); })} disabled={Boolean(busy) || !node.original_domain}>恢复原始域名</button>
        </div>
      </div>

      <div className="node-subpanel">
        <h3>中转</h3>
        <div className="node-form-grid">
          <label><span>中转服务器地址</span><input value={relayServer} onChange={(event) => setRelayServer(event.target.value)} placeholder="host 或 IP" /></label>
          <label><span>中转端口</span><input type="number" value={relayPort} onChange={(event) => setRelayPort(event.target.value)} placeholder="留空沿用原端口" /></label>
          <label><span>复制副本后缀</span><input value={relaySuffix} onChange={(event) => setRelaySuffix(event.target.value)} /></label>
        </div>
        <div className="node-dialog-actions">
          <button type="button" onClick={() => void onRun("设置中转", async () => { await setNodeRelay(token, node.id, relayServer.trim(), toPositiveInt(relayPort, 0)); onClose(); })} disabled={Boolean(busy) || !relayServer.trim()}>设置/修改中转</button>
          <button type="button" onClick={() => void onRun("复制中转", async () => { await copyNodeWithRelay(token, node.id, relayServer.trim(), toPositiveInt(relayPort, 0), relaySuffix.trim()); onClose(); })} disabled={Boolean(busy) || !relayServer.trim()}>复制为中转节点</button>
          <button type="button" className="danger" onClick={() => void onRun("取消中转", async () => { await cancelNodeRelay(token, node.id); onClose(); })} disabled={Boolean(busy) || !node.relay_orig_server}>取消中转</button>
        </div>
      </div>

      <div className="node-subpanel">
        <h3>Clash 配置 JSON</h3>
        <textarea className="node-json-editor" value={config} onChange={(event) => { setConfig(event.target.value); setJsonError(""); }} />
        {jsonError && <p className="node-error">{jsonError}</p>}
        <div className="node-dialog-actions">
          <button type="button" onClick={() => {
            try {
              setConfig(JSON.stringify(JSON.parse(config), null, 2));
              setJsonError("");
            } catch (err) {
              setJsonError(err instanceof Error ? err.message : "JSON 格式错误");
            }
          }}><FileJson /> 格式化</button>
          <button type="button" onClick={() => void saveConfig()} disabled={Boolean(busy)}><CheckCircle2 /> 保存配置</button>
          <button type="button" onClick={() => {
            const related = nodes.filter((item) => item.id !== node.id).slice(0, 8).map((item) => `${item.id}: ${item.node_name}`).join("\n");
            onNotice({ tone: "info", text: related ? `可用于链式代理的节点：${related}` : "没有其它可用节点" });
          }}>查看可链式节点</button>
        </div>
      </div>
    </NodeDialog>
  );
}

function ManualNodeDialog({ token, busy, onClose, onRun, onNotice }: { token: string; busy: string; onClose: () => void; onRun: (label: string, action: () => Promise<void>) => Promise<void>; onNotice: (notice: Notice) => void }) {
  const [name, setName] = useState("");
  const [protocol, setProtocol] = useState("ss");
  const [tag, setTag] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [config, setConfig] = useState(`{\n  "name": "",\n  "type": "ss",\n  "server": "",\n  "port": 443\n}`);
  const [error, setError] = useState("");

  const submit = async () => {
    let parsed: ParsedProxy;
    try {
      parsed = JSON.parse(config) as ParsedProxy;
    } catch (err) {
      setError(err instanceof Error ? err.message : "JSON 格式错误");
      return;
    }
    const nodeName = name.trim() || stringValue(parsed.name);
    if (!nodeName) {
      setError("节点名称不能为空");
      return;
    }
    parsed.name = nodeName;
    parsed.type = protocol || stringValue(parsed.type);
    await onRun("创建节点", async () => {
      await createNode(token, {
        node_name: nodeName,
        protocol: normalizeProtocol(protocol || stringValue(parsed.type)),
        parsed_config: JSON.stringify(parsed),
        clash_config: JSON.stringify(parsed),
        enabled,
        tag: tag.trim(),
        tags: tag.trim() ? splitList(tag) : [],
      });
      onClose();
      onNotice({ tone: "success", text: "节点已创建" });
    });
  };

  return (
    <NodeDialog title="新增节点" subtitle="以官方节点 JSON 字段保存" onClose={onClose}>
      <div className="node-form-grid">
        <label><span>节点名称</span><input value={name} onChange={(event) => setName(event.target.value)} /></label>
        <label><span>协议</span><select value={protocol} onChange={(event) => setProtocol(event.target.value)}>{IMPORT_PROTOCOLS.slice(1).map((item) => <option key={item} value={item}>{item.toUpperCase()}</option>)}</select></label>
        <label><span>标签</span><input value={tag} onChange={(event) => setTag(event.target.value)} /></label>
        <label><span>状态</span><select value={enabled ? "1" : "0"} onChange={(event) => setEnabled(event.target.value === "1")}><option value="1">启用</option><option value="0">禁用</option></select></label>
      </div>
      <textarea className="node-json-editor" value={config} onChange={(event) => { setConfig(event.target.value); setError(""); }} />
      {error && <p className="node-error">{error}</p>}
      <div className="node-dialog-actions">
        <button type="button" onClick={() => {
          try {
            setConfig(JSON.stringify(JSON.parse(config), null, 2));
            setError("");
          } catch (err) {
            setError(err instanceof Error ? err.message : "JSON 格式错误");
          }
        }}>格式化</button>
        <button type="button" onClick={() => void submit()} disabled={Boolean(busy)}><Plus /> 创建</button>
      </div>
    </NodeDialog>
  );
}

function BatchNodeDialog({
  token,
  nodes,
  busy,
  onClose,
  onRun,
  onNotice,
  onTcping,
  onClearSelection,
}: {
  token: string;
  nodes: XrayNode[];
  busy: string;
  onClose: () => void;
  onRun: (label: string, action: () => Promise<void>) => Promise<void>;
  onNotice: (notice: Notice) => void;
  onTcping: () => void;
  onClearSelection: () => void;
}) {
  const [names, setNames] = useState(nodes.map((node) => node.node_name).join("\n"));
  const [tfo, setTfo] = useState(false);
  const [udpRelay, setUdpRelay] = useState(false);

  return (
    <NodeDialog title="批量操作" subtitle={`已选 ${nodes.length} 个节点`} onClose={onClose}>
      <textarea className="node-json-editor compact" value={names} onChange={(event) => setNames(event.target.value)} />
      <div className="node-dialog-actions">
        <button type="button" onClick={() => void onRun("批量重命名", async () => {
          const nextNames = names.split("\n").map((line) => line.trim()).filter(Boolean);
          if (nextNames.length !== nodes.length) throw new Error(`名称数量 ${nextNames.length} 与选中节点数 ${nodes.length} 不一致`);
          await batchRenameNodes(token, nodes.map((node, index) => ({ node_id: node.id, new_name: nextNames[index] })));
          onClose();
        })} disabled={Boolean(busy)}>批量重命名</button>
        <button type="button" onClick={() => {
          void onTcping();
          onNotice({ tone: "info", text: "批量 TCPing 已开始，结果会显示在节点卡片上" });
          onClose();
        }}>批量 TCPing</button>
        <button type="button" onClick={() => void onRun("关闭 skip-cert-verify", async () => { await batchDisableNodeSkipCert(token, nodes.map((node) => node.id)); onClose(); })} disabled={Boolean(busy)}>关闭跳过证书</button>
      </div>
      <div className="node-subpanel">
        <h3>Snell 参数</h3>
        <label className="node-check"><input type="checkbox" checked={tfo} onChange={(event) => setTfo(event.target.checked)} /><span>开启 TFO</span></label>
        <label className="node-check"><input type="checkbox" checked={udpRelay} onChange={(event) => setUdpRelay(event.target.checked)} /><span>开启 UDP Relay</span></label>
        <div className="node-dialog-actions">
          <button type="button" onClick={() => void onRun("批量修改 Snell", async () => { await batchUpdateSnellOptions(token, nodes.map((node) => node.id), { tfo, udp_relay: udpRelay }); onClose(); })} disabled={Boolean(busy)}>保存 Snell 参数</button>
        </div>
      </div>
      <div className="node-dialog-actions danger-zone">
        <button className="danger" type="button" onClick={() => void onRun("批量删除", async () => {
          if (!window.confirm(`确认删除选中的 ${nodes.length} 个节点？受管节点会同步清理远程资源。`)) return;
          await batchDeleteNodes(token, nodes.map((node) => node.id));
          onClearSelection();
          onClose();
        })} disabled={Boolean(busy)}><Trash2 /> 删除选中</button>
        <button className="danger" type="button" onClick={() => void onRun("清空节点", async () => {
          if (!window.confirm("确认清空当前账号可删除的全部节点？这个操作会同步清理远程资源。")) return;
          await clearNodes(token);
          onClearSelection();
          onClose();
        })} disabled={Boolean(busy)}><AlertTriangle /> 清空全部</button>
      </div>
    </NodeDialog>
  );
}

function NodeDialog({ title, subtitle, children, onClose }: { title: string; subtitle: string; children: React.ReactNode; onClose: () => void }) {
  return (
    <div className="node-dialog-layer" role="presentation" onClick={onClose}>
      <section className="node-dialog" role="dialog" aria-modal="true" aria-label={title} onClick={(event) => event.stopPropagation()}>
        <header>
          <div>
            <h2>{title}</h2>
            <p>{subtitle}</p>
          </div>
          <button type="button" onClick={onClose} aria-label="关闭"><X /></button>
        </header>
        <div className="node-dialog-body">{children}</div>
      </section>
    </div>
  );
}

function NodeSelect({ icon, label, value, options, onChange }: { icon: React.ReactNode; label: string; value: string; options: Array<{ value: string; label: string }>; onChange: (value: string) => void }) {
  return (
    <label className="node-select">
      {icon}
      <span>{label}</span>
      <select value={value} onChange={(event) => onChange(event.target.value)}>{options.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</select>
    </label>
  );
}

function NodeNotice({ notice }: { notice: NonNullable<Notice> }) {
  const Icon = notice.tone === "success" ? CheckCircle2 : notice.tone === "error" ? AlertTriangle : FileJson;
  return <div className={`node-notice ${notice.tone}`}><Icon /> <span>{notice.text}</span></div>;
}

async function copyNodeURI(token: string, node: XrayNode, setNotice: (notice: Notice) => void) {
  try {
    const resp = await fetchNodeURI(token, node.id);
    if (!resp.uri) throw new Error("后端未返回 URI");
    await writeClipboard(resp.uri);
    setNotice({ tone: "success", text: `已复制「${node.node_name}」的 URI` });
  } catch (err) {
    setNotice({ tone: "error", text: err instanceof Error ? err.message : "复制 URI 失败" });
  }
}

function proxyToNodeRequest(proxy: ParsedProxy, tag: string): NodeMutationRequest {
  const name = stringValue(proxy.name) || stringValue(proxy.ps) || "未命名节点";
  const type = normalizeProtocol(stringValue(proxy.type || proxy.protocol));
  const body = JSON.stringify({ ...proxy, name, type });
  const tags = tag ? splitList(tag) : [];
  return {
    raw_url: "",
    node_name: name,
    protocol: type,
    parsed_config: body,
    clash_config: body,
    enabled: true,
    tag: tags[0] || "",
    tags,
  };
}

function buildSocks5ParseResponse(name: string, username: string, password: string, server: string, port: string, tag: string) {
  const cleanServer = server.trim();
  const cleanPort = toPositiveInt(port, 0);
  if (!cleanServer) throw new Error("SOCKS5 服务器不能为空");
  if (cleanPort <= 0 || cleanPort > 65535) throw new Error("SOCKS5 端口必须是 1-65535");
  const nodeName = name.trim() || `${cleanServer}:${cleanPort}`;
  const proxy = cleanObject({
    name: nodeName,
    type: "socks5",
    server: cleanServer,
    port: cleanPort,
    username: username.trim(),
    password: password.trim(),
    udp: true,
  });
  return {
    proxies: [proxy],
    count: 1,
    suggested_tag: tag.trim() || "手动输入",
  };
}

function nodeToMutation(node: XrayNode, overrides: Partial<NodeMutationRequest> = {}): NodeMutationRequest {
  const parsed = parseNodeConfig(node);
  const tags = nodeTags(node);
  const nodeName = overrides.node_name || node.node_name;
  const parsedConfig = setJSONName(node.parsed_config || node.clash_config || JSON.stringify(parsed), nodeName);
  const clashConfig = setJSONName(node.clash_config || node.parsed_config || JSON.stringify(parsed), nodeName);
  return {
    raw_url: node.raw_url || "",
    node_name: nodeName,
    protocol: normalizeProtocol(node.protocol || stringValue(parsed.type)),
    parsed_config: parsedConfig,
    clash_config: clashConfig,
    enabled: node.enabled !== false,
    tag: tags[0] || "",
    tags,
    inbound_tag: node.inbound_tag || "",
    chain_proxy_node_id: node.chain_proxy_node_id ?? null,
    relay_group_name: node.relay_group_name || "",
    relay_group_node_ids: node.relay_group_node_ids ?? null,
    ...overrides,
  };
}

function buildRegionNodeName(node: XrayNode, servers: RemoteServer[]) {
  const server = servers.find((item) => item.name === node.original_server);
  const region = server ? serverRegionFromFields(server) : null;
  const flag = region?.flag || "";
  const label = region?.label || "";
  if (!flag && !label) return "";
  const cleanName = node.node_name.replace(/^[\u{1F1E6}-\u{1F1FF}]{2}\s*/u, "").trim();
  const prefix = [flag, label].filter(Boolean).join(" ").trim();
  return `${prefix} ${cleanName}`.trim();
}

function cleanObject<T extends Record<string, unknown>>(value: T): T {
  Object.keys(value).forEach((key) => {
    const current = value[key];
    if (current === "" || current == null) delete value[key];
    else if (typeof current === "object" && !Array.isArray(current)) cleanObject(current as Record<string, unknown>);
  });
  return value;
}

function parseNodeConfig(node: XrayNode): ParsedProxy {
  for (const raw of [node.clash_config, node.parsed_config]) {
    if (!raw) continue;
    try {
      const parsed = JSON.parse(raw) as ParsedProxy;
      if (parsed && typeof parsed === "object") return parsed;
    } catch {
      // Ignore invalid historical config and keep the UI usable.
    }
  }
  return {};
}

function prettyJSON(value: unknown): string {
  if (typeof value === "string") {
    if (!value.trim()) return "{}";
    try {
      return JSON.stringify(JSON.parse(value), null, 2);
    } catch {
      return value;
    }
  }
  try {
    return JSON.stringify(value ?? {}, null, 2);
  } catch {
    return String(value ?? "");
  }
}

function setJSONName(raw: string, name: string) {
  try {
    const parsed = JSON.parse(raw || "{}") as ParsedProxy;
    parsed.name = name;
    return JSON.stringify(parsed);
  } catch {
    return raw;
  }
}

function nodeTags(node: XrayNode) {
  const values = [...(node.tags ?? []), node.tag ?? ""].map((item) => item.trim()).filter(Boolean);
  return [...new Set(values)];
}

function deriveTags(nodes: XrayNode[]) {
  return [...new Set(nodes.flatMap(nodeTags))].sort((a, b) => a.localeCompare(b));
}

function findDuplicateGroups(nodes: XrayNode[]) {
  const map = new Map<string, XrayNode[]>();
  nodes.forEach((node) => {
    const key = duplicateKey(node);
    if (!key) return;
    const bucket = map.get(key) ?? [];
    bucket.push(node);
    map.set(key, bucket);
  });
  return [...map.values()].filter((group) => group.length > 1);
}

function duplicateKey(node: XrayNode) {
  const parsed = parseNodeConfig(node);
  const type = normalizeProtocol(node.protocol || stringValue(parsed.type));
  const server = stringValue(parsed.server).trim().toLowerCase();
  const port = stringValue(parsed.port).trim();
  if (!type || !server || !port) return "";
  const auth = [parsed.uuid, parsed.id, parsed.password, parsed.username, parsed.cipher].map(stringValue).join("|");
  return `${type}|${server}|${port}|${auth}`;
}

function countValues(values: string[]) {
  const counts = new Map<string, number>();
  values.filter(Boolean).forEach((value) => counts.set(value, (counts.get(value) ?? 0) + 1));
  return [...counts.entries()].map(([value, count]) => ({ value, count })).sort((a, b) => b.count - a.count || a.value.localeCompare(b.value));
}

function normalizeProtocol(value: string) {
  return value.trim().toLowerCase().replace(/^shadowsocks$/, "ss").replace(/^socks$/, "socks5");
}

function stringValue(value: unknown) {
  if (value == null) return "";
  return String(value);
}

function numberValue(value: unknown) {
  if (typeof value === "number") return value;
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

function toPositiveInt(value: string | number, fallback: number) {
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed >= 0 ? Math.floor(parsed) : fallback;
}

function splitList(value: string) {
  return value.split(/[,\n，]/).map((item) => item.trim()).filter(Boolean);
}

function serverAddressOptions(server: RemoteServer) {
  return [server.domain, server.ip_address, server.pull_address, server.domain_v6, server.ip_address_v6, server.pull_address_v6].filter((value): value is string => Boolean(value));
}

async function writeClipboard(value: string) {
  await navigator.clipboard?.writeText(value);
}
