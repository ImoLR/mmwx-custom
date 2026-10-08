import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
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
  cancelNodeRelay,
  clearNodes,
  copyNodeWithRelay,
  createNode,
  createNodeTempSubscription,
  deleteNode,
  deleteNodeWholeOutbound,
  registerExternalSubscription,
  fetchNodeRelatedInbounds,
  fetchNodeSubscription,
  fetchNodeTags,
  fetchNodeOwners,
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
import type { ExternalSyncCandidate, NodeMutationRequest, NodeTunnel, RemoteServer, XrayNode } from "./types";
import { NodeRoutingDialog } from "./node-routing";
import { NodeCardBadges, NodeCardExtras, NodeRelayActionDialog, useNodeCardExtras } from "./node-card-extras";
import { nodeManagedServer } from "./node-card-logic";
import { NodePackageChip, useNodePackages } from "./node-package-chip";
import { NodeProbeBadge, NodeProbeDialog, useNodeProbe } from "./node-probe";
import { nodeProbeStatesById, nodeProbeSummary } from "./node-auxiliary-logic";
import { RelayCredentialRepairDialog } from "./node-relay-repair";
import { NodeSpeedTestActions, SpeedTestHistoryDialog, SpeedTesterManagerDialog, useNodeSpeedTests } from "./node-speedtest";
import { duplicateNodeKey as duplicateKey, duplicateNodeGroups as findDuplicateGroups, subscriptionDefaultTag, batchRenameTransform, moveSelectedNodes, groupNodes, nodeOwnership, nodeOwnerHint, nodeRelayRows, isNodeRelay, toggleNodeSelection, matchesNodeFilters, type NodeGrouping, type NodeOwners } from "./node-manager-logic";
import { speedTestLatency, speedTestState } from "./node-speedtest-logic";
import { ManagedNodeCreateDialog, ManagedNodeEditDialog, NodeFlowRepairDialog } from "./xray-manager";
import {
  ChainProxyDialog,
  RelayGroupDialog,
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
  | { kind: "chain"; node: XrayNode }
  | { kind: "relay-group"; node: XrayNode }
  | { kind: "managed-edit"; node: XrayNode }
  | { kind: "flow-repair"; node: XrayNode }
  | { kind: "resolve"; node: XrayNode; ips: string[] }
  | { kind: "batch-rename" }
  | { kind: "batch-tag" }
  | { kind: "batch-temp" }
  | { kind: "duplicates" }
  | { kind: "manual" }
  | { kind: "batch" }
  | { kind: "add-managed" }
  | { kind: "tunnels" }
  | { kind: "routed" }
  | { kind: "landing"; node: XrayNode }
  | { kind: "speedtest" }
  | { kind: "speed-history"; node?: XrayNode }
  | { kind: "speed-testers" }
  | { kind: "node-routing"; node: XrayNode; server: RemoteServer }
  | { kind: "node-probe" }
  | { kind: "relay-repair" }
  | { kind: "relay-action"; node: XrayNode; tunnel?: NodeTunnel }
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
  const [filterTags, setFilterTags] = useState<string[]>([]);
  const [sourceFilter, setSourceFilter] = useState<"all" | "manual" | "subscription">("all");
  const [serverName, setServerName] = useState(ALL);
  const [stateFilter, setStateFilter] = useState(ALL);
  const [selected, setSelected] = useState<Set<number>>(() => new Set());
  const [dialog, setDialog] = useState<Dialog>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const [owners, setOwners] = useState<NodeOwners>({});
  const [ownerError, setOwnerError] = useState("");
  const [filterOpen, setFilterOpen] = useState(false);
  const [selectionMode, setSelectionMode] = useState(false);
  const [selectionMenu, setSelectionMenu] = useState(false);
  const [actionNodeId, setActionNodeId] = useState<number | null>(null);
  const [grouping, setGrouping] = useState<NodeGrouping>(() => {
    const value = readNodePreference("grouping", "user");
    return ["user", "server", "package", "none"].includes(value) ? value as NodeGrouping : "user";
  });
  const [collapsed, setCollapsed] = useState<string[]>(() => {
    try { const value = JSON.parse(readNodePreference("collapsed", "[]")); return Array.isArray(value) ? value.filter((key): key is string => typeof key === "string") : []; } catch { return []; }
  });
  const [expandedRelays, setExpandedRelays] = useState<Set<string>>(new Set());
  const [sortMode, setSortMode] = useState(false);
  const [nodeOrder, setNodeOrder] = useState<number[]>([]);
  const [userConfig, setUserConfig] = useState<Record<string, unknown>>({});
  const [externalSyncSession, setExternalSyncSession] = useState<{ sessionId: string; candidates: ExternalSyncCandidate[] } | null>(null);
  const [importOpen, setImportOpen] = useState(false);
  const [importMode, setImportMode] = useState<"manual" | "subscription" | "socks5">("manual");
  const [importText, setImportText] = useState("");
  const [subscriptionURL, setSubscriptionURL] = useState("");
  const [subscriptionUA, setSubscriptionUA] = useState("clash.meta");
  const [customSubscriptionUA, setCustomSubscriptionUA] = useState("");
  const [parsedSubscriptionURL, setParsedSubscriptionURL] = useState("");
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
      const [nodeResp, tagResp, configResp, ownerResp] = await Promise.all([
        fetchXrayNodes(token),
        fetchNodeTags(token).catch(() => ({ tags: [] })),
        fetchUserConfig(token),
        fetchNodeOwners(token).then((response) => { setOwnerError(""); return response; }).catch(() => {
          setOwnerError("节点归属读取失败，请刷新重试；暂时归入外部节点 / 未归属。"); return { owners: {} };
        }),
      ]);
      const nextNodes = nodeResp.nodes ?? [];
      setNodes(nextNodes);
      setOwners(ownerResp.owners);
      setSelected((current) => new Set([...current].filter((id) => nextNodes.some((node) => node.id === id))));
      setTags(tagResp.tags ?? deriveTags(nextNodes));
      setUserConfig(configResp);
      const currentIds = new Set(nextNodes.map((node) => node.id));
      const savedOrder = Array.isArray(configResp.node_order) ? configResp.node_order.map(Number).filter((id) => Number.isFinite(id) && currentIds.has(id)) : [];
      setNodeOrder([...savedOrder, ...nextNodes.map((node) => node.id).filter((id) => !savedOrder.includes(id))]);
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

  const filtered = useMemo(() => parsedNodes.filter(({ node, parsed }) => matchesNodeFilters(node, parsed, nodeOwnership(node, owners), {
    query, protocol, tags: filterTags, source: sourceFilter, server: serverName, state: stateFilter,
  })), [parsedNodes, owners, protocol, query, serverName, stateFilter, filterTags, sourceFilter]);

  useEffect(() => { try { localStorage.setItem("mmwx-node-grouping", grouping); } catch { /* Storage may be disabled. */ } }, [grouping]);
  useEffect(() => { try { localStorage.setItem("mmwx-node-collapsed", JSON.stringify(collapsed)); } catch { /* Storage may be disabled. */ } }, [collapsed]);

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
      const userAgent = (subscriptionUA === "custom" ? customSubscriptionUA : subscriptionUA).trim();
      const url = subscriptionURL.trim();
      if (importMode === "subscription" && subscriptionUA === "custom" && !userAgent) throw new Error("请输入自定义 User-Agent");
      const resp = importMode === "socks5"
        ? buildSocks5ParseResponse(socksName, socksUsername, socksPassword, socksServer, socksPort, importTag)
        : importMode === "subscription"
          ? await fetchNodeSubscription(token, url, userAgent, false)
          : await parseNodeURIs(token, importText, forceSkipCert);
      const proxies = resp.proxies ?? [];
      setParsedProxies(proxies);
      setParsedImportMode(importMode);
      const tag = importMode === "subscription" ? subscriptionDefaultTag(url, importTag, resp.suggested_tag) : importTag.trim() || "手动输入";
      setImportTag(tag);
      setParsedSubscriptionURL(importMode === "subscription" ? url : "");
      if (importMode === "subscription") await registerExternalSubscription(token, { name: splitList(tag)[0] || tag, url, user_agent: userAgent }).catch(() => undefined);
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
      ...proxyToNodeRequest(proxy, importTag.trim() || (parsedImportMode === "subscription" ? subscriptionDefaultTag(parsedSubscriptionURL) : "手动输入"), parsedSubscriptionURL),
      ...(useRelay ? { relay_server: relayServer.trim(), relay_port: relayPortValue } : {}),
    }));
    await run("保存导入", async () => {
      const created = await batchCreateNodes(token, batch);
      const ids = (created.nodes ?? []).map((node) => node.id);
      if (ids.length) {
        const next = [...ids, ...nodeOrder.filter((id) => !ids.includes(id))];
        try {
          setUserConfig(await updateUserConfig(token, { ...userConfig, node_order: next }));
          setNodeOrder(next);
        } catch (error) {
          setNotice({ tone: "error", text: `节点已导入，但保存置顶顺序失败：${error instanceof Error ? error.message : "请重试排序"}` });
        }
      }
      setImportText("");
      setSubscriptionURL("");
      setParsedProxies([]);
      setParsedImportMode(null);
      setParsedSubscriptionURL("");
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
  const cardExtras = useNodeCardExtras(token, nodes, servers);
  const packages = useNodePackages(token, toolNotice, nodes.map((node) => node.id).sort((a, b) => a - b).join(","));
  const probe = useNodeProbe(token);
  const probeStates = useMemo(() => nodeProbeStatesById(probe.status), [probe.status]);
  const speedTests = useNodeSpeedTests(token, toolNotice);
  const refreshExtras = async () => { await loadNodes(); await Promise.all([cardExtras.refresh(), packages.refresh(), probe.refresh()]); };
  const cancelWholeOutbound = (node: XrayNode) => {
    if (!window.confirm(`将删除“${node.node_name}”的整个节点路由规则及其专用出站配置，但不会删除节点本身。`)) return;
    void run("取消整个节点出站", async () => { await deleteNodeWholeOutbound(token, node.id); await cardExtras.refresh(); });
  };
  const saveOrder = async (next: number[]) => {
    setBusy("保存节点顺序");
    setNodeOrder(next);
    try {
      setUserConfig(await updateUserConfig(token, { ...userConfig, node_order: next }));
      setNotice({ tone: "success", text: "节点顺序已保存" });
    } catch (error) {
      setNodeOrder(nodeOrder);
      setNotice({ tone: "error", text: error instanceof Error ? error.message : "保存节点顺序失败" });
    } finally { setBusy(""); }
  };
  const addSelectedEmoji = async () => {
    setBusy("添加地区 Emoji");
    let success = 0; let skip = 0; let fail = 0;
    try {
      for (const node of selectedNodes) {
        if (/^[\u{1F1E6}-\u{1F1FF}]{2}/u.test(node.node_name)) { skip += 1; continue; }
        const name = buildRegionNodeName(node, servers);
        if (!name) { fail += 1; continue; }
        try { await updateNode(token, node.id, nodeToMutation(node, { node_name: name })); success += 1; }
        catch { fail += 1; }
      }
      await loadNodes();
      setNotice({ tone: fail || skip ? "info" : "success", text: `成功 ${success}，跳过 ${skip} (已有emoji)，失败 ${fail}` });
    } finally { setBusy(""); }
  };
  const onDragEnd = async ({ active, over }: DragEndEvent) => {
    if (grouping !== "none" || !sortMode || !over || active.id === over.id) return;
    const visibleIds = filtered.map(({ node }) => node.id);
    const oldIndex = visibleIds.indexOf(Number(active.id));
    const newIndex = visibleIds.indexOf(Number(over.id));
    if (oldIndex < 0 || newIndex < 0) return;
    const nextVisible = arrayMove(visibleIds, oldIndex, newIndex);
    const visible = new Set(visibleIds);
    let cursor = 0;
    const next = nodeOrder.map((id) => visible.has(id) ? nextVisible[cursor++] : id);
    await saveOrder(next);
  };
  const openTool = (next: NonNullable<Dialog>) => { setMenuOpen(false); setSelectionMenu(false); setDialog(next); };

  const groups = useMemo(() => groupNodes(filtered.map(({ node }) => node), grouping, owners, packages.memberships), [filtered, grouping, owners, packages.memberships]);
  const filterChips = [
    ...(protocol !== ALL ? [{ label: `协议：${protocol.toUpperCase()}`, clear: () => setProtocol(ALL) }] : []),
    ...filterTags.map((tag) => ({ label: `标签：${tag}`, clear: () => setFilterTags((current) => current.filter((item) => item !== tag)) })),
    ...(sourceFilter !== ALL ? [{ label: `来源：${sourceFilter === "manual" ? "手动输入" : "订阅导入"}`, clear: () => setSourceFilter("all") }] : []),
    ...(serverName !== ALL ? [{ label: `服务器：${serverName}`, clear: () => setServerName(ALL) }] : []),
    ...(stateFilter !== ALL ? [{ label: `状态：${({ enabled: "已启用", disabled: "已禁用", relay: "中转中", routed: "路由出站" } as Record<string, string>)[stateFilter]}`, clear: () => setStateFilter(ALL) }] : []),
  ];
  const resetFilters = () => { setProtocol(ALL); setFilterTags([]); setSourceFilter("all"); setServerName(ALL); setStateFilter(ALL); };
  const actionNode = nodes.find((node) => node.id === actionNodeId);
  const renderActions = (node: XrayNode) => {
    const parsed = parseNodeConfig(node);
    return (<NodeActionSheet
      node={node}
      onClose={() => setActionNodeId(null)}
      onSpeed={() => void speedTests.start([node])}
      speedBusy={speedTestState(speedTests.latest.get(node.id), speedTests.now) === "running"}
      notice={notice}
      ownerHint={nodeOwnerHint(nodeOwnership(node, owners))}
      packageNames={(packages.memberships[node.id] || []).map((pkg) => pkg.package_name).join("、")}
      parsed={parsed}
      latency={latencies[node.id]}
      onDetails={() => setDialog({ kind: "details", node })}
      onEdit={() => setDialog({ kind: "edit", node })}
      onEditInbound={() => setDialog({ kind: "managed-edit", node })}
      onChain={() => setDialog({ kind: "chain", node })}
      onRelayGroup={() => setDialog({ kind: "relay-group", node })}
      onCancelWholeOutbound={() => cancelWholeOutbound(node)}
      onRouting={node.inbound_tag && nodeManagedServer(node, servers) ? () => setDialog({ kind: "node-routing", node, server: nodeManagedServer(node, servers)! }) : undefined}
      extraActions={<NodeCardExtras node={node} state={cardExtras.cards.get(node.id)} servers={servers}
        onTunnel={(tunnel) => setDialog({ kind: "relay-action", node, tunnel })}
        onRelay={() => setDialog({ kind: "relay-action", node })}
        onRevertChain={(entry) => { if (window.confirm(`切回源服务器地址?\n节点「${node.node_name}」当前经链式隧道入口 ${entry} 连接。切回后将拆除该节点的中转配置,恢复为源服务器地址。`)) void run("切回源服务器地址", async () => { await cancelNodeRelay(token, node.id); await cardExtras.refresh(); }); }}
        onSwitchWhole={() => setDialog({ kind: "landing", node })} onCancelWhole={() => cancelWholeOutbound(node)} onRepairFlow={() => setDialog({ kind: "flow-repair", node })} />}
      extras={<><NodeCardBadges node={node} state={cardExtras.cards.get(node.id)} servers={servers} />
        <NodeProbeBadge node={node} state={probeStates.get(node.id)} onClick={() => setDialog({ kind: "node-probe" })} />
      </>}
      packageAction={<NodePackageChip token={token} nodeId={node.id} loading={packages.loading} memberships={packages.memberships[String(node.id)] || []} packages={packages.packages} onChanged={async () => { await packages.refresh(); await loadNodes(); }} onNotice={toolNotice} />}
      speedActions={<NodeSpeedTestActions node={node} controller={speedTests} onHistory={() => setDialog({ kind: "speed-history", node })} />}
      onLanding={() => setDialog({ kind: "landing", node })}
      onCopy={() => void copyNodeURI(token, node, setNotice)}
      onTcping={() => void testOne(node)}
      onEmoji={() => void run("添加地区 Emoji", async () => {
        const next = /^[\u{1F1E6}-\u{1F1FF}]{2}/u.test(node.node_name) ? "" : buildRegionNodeName(node, servers);
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
        const ips = [...new Set(resp.ips ?? [])];
        if (!ips.length) throw new Error("DNS 未返回可用 IP");
        if (ips.length > 1) { setDialog({ kind: "resolve", node, ips }); return; }
        const ip = ips[0];
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
              />);
  };
  const renderRow = (node: XrayNode, relay = false, relayCount = 0) => {
    const state = cardExtras.cards.get(node.id), owner = nodeOwnership(node, owners), parsed = parseNodeConfig(node);
    const probeState = nodeProbeSummary(probeStates.get(node.id));
    const failures = [state?.blocked && "被墙", state?.flow?.warning && "流控不一致", (probeState.failStreak > 0 || probeState.last?.ok === false) && "探测失败"].filter(Boolean).join(" · ");
    const badges = [
      ...(failures ? [{ text: failures.includes(" · ") ? "异常" : failures, tone: "bad", title: failures }] : []),
      ...(owner.shared ? [{ text: "共用", tone: "shared", title: owner.users.join("、") }] : []),
      ...(state?.whole ? [{ text: `出站 ${state.whole.label}`, tone: "out", title: `整个节点出站 ${state.whole.label}` }] : []),
      ...(relay || relayCount ? [{ text: relay ? "中转" : `中转 ${relayCount}`, tone: "relay", title: relay ? "中转节点" : `${relayCount} 个中转节点` }] : []),
      ...(parsed["reality-opts"] || parsed.tls ? [{ text: parsed["reality-opts"] ? "Reality" : "TLS", tone: "tls", title: "传输安全" }] : []),
      ...(node.node_type === "routed" ? [{ text: "路由", tone: "out", title: "路由出站" }] : []),
    ].slice(0, 3);
    const speed = speedTests.latest.get(node.id), speedState = speedTestState(speed, speedTests.now);
    const latency = latencies[node.id] || (speed ? { loading: speedState === "running", text: speedState === "running" ? "测试中" : speedState === "timeout" ? "超时" : speedTestLatency(speed), ok: speedState === "ok" && Number(speed.latency_ms) >= 0 } : undefined);
    return <CompactNodeRow node={node} parsed={parsed} relay={relay} badges={badges} latency={latency}
      selected={selected.has(node.id)} selectionMode={selectionMode} onSelect={() => toggleSelected(node.id)}
      onOpen={() => setActionNodeId(node.id)} ownerHint={nodeOwnerHint(owner)}
      packageNames={(packages.memberships[node.id] || []).map((pkg) => pkg.package_name).join("、")} />;
  };

  return (
    <div className="node-manager node-redesign" aria-busy={loading || Boolean(busy)}>
      <section className="node-hero">
        <h1>节点 <small>{nodes.length}</small></h1>
        {selectionMode ? <button type="button" onClick={() => { setSelectionMode(false); setSelected(new Set()); setSelectionMenu(false); }}>完成</button>
          : <button type="button" className="primary" onClick={() => setImportOpen(true)}><Plus /> 导入</button>}
        <div className="node-global-menu-wrap">
          <button type="button" onClick={() => setMenuOpen((value) => !value)} disabled={loading || Boolean(busy)} aria-label="节点管理菜单" aria-expanded={menuOpen}><MoreHorizontal /></button>
          {menuOpen && <div className="node-global-menu" role="menu">
            <button role="menuitem" onClick={() => { setSelectionMode(true); setMenuOpen(false); }}><CheckCircle2 />选择节点</button>
            <button role="menuitem" onClick={() => { setMenuOpen(false); void refreshExtras(); }}><RefreshCw />刷新节点</button>
            <button role="menuitem" className={sortMode ? "active" : ""} onClick={() => { setSortMode((value) => !value); setMenuOpen(false); }}><GripVertical />{sortMode ? "退出排序模式" : "排序模式"}</button>
            <button role="menuitem" onClick={() => openTool({ kind: "add-managed" })}><Plus />添加节点</button>
            <button role="menuitem" onClick={() => openTool({ kind: "tunnels" })}><Link2 />Tunnel 管理</button>
            <button role="menuitem" onClick={() => openTool({ kind: "routed" })}><FileJson />路由出站</button>
            <button role="menuitem" onClick={() => openTool({ kind: "speedtest" })}><Zap />节点测速</button>
            <button role="menuitem" onClick={() => openTool({ kind: "speed-history" })}><Zap />测速历史</button>
            <button role="menuitem" onClick={() => openTool({ kind: "speed-testers" })}><Server />测速端管理</button>
            <button role="menuitem" onClick={() => openTool({ kind: "uris" })}><Link2 />URI 管理</button>
            <button role="menuitem" onClick={() => openTool({ kind: "external-sync" })}><RefreshCw />同步外部订阅</button>
            {externalSyncSession && <button role="menuitem" onClick={() => openTool({ kind: "external-sync" })}><Plus />订阅解析完成，请选择需要保存的节点</button>}
            <button role="menuitem" onClick={() => openTool({ kind: "duplicates" })}><Copy />删除重复</button>
            <span className="node-global-menu-label">辅助功能</span>
            <button role="menuitem" onClick={() => openTool({ kind: "node-probe" })}><Zap />外部节点探测</button>
            <button role="menuitem" onClick={() => openTool({ kind: "relay-repair" })}><Settings2 />落地凭据迁移</button>
            <button role="menuitem" onClick={() => openTool({ kind: "skip-cert" })}><AlertTriangle />关闭跳过证书验证</button>
            <button role="menuitem" onClick={() => openTool({ kind: "snell" })}><Settings2 />Snell 选项</button>
            <label className="node-global-menu-toggle"><input type="checkbox" checked={trafficName.enabled} disabled={trafficName.loading} onChange={() => void trafficName.toggle()} /><span>节点名称显示流量</span></label>
            <button role="menuitem" className="danger" onClick={() => openTool({ kind: "clear-all" })}><Trash2 />清空全部</button>
          </div>}
        </div>
      </section>

      {notice && <NodeNotice notice={notice} />}
      {ownerError && <p className="node-form-hint" role="status">{ownerError}</p>}
      {cardExtras.error && <details className="node-status-warning"><summary>部分节点状态读取失败</summary><p>{cardExtras.error}</p></details>}

      {importOpen && !dialog && <NodeSheet title="导入外部节点" className="node-import-sheet" onClose={() => setImportOpen(false)}>
        <p className="node-form-hint">支持 URI、Clash YAML、base64 订阅、Surge 行；保存后同步到订阅文件。</p>
        {notice && <NodeNotice notice={notice} />}
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
                    <option value="custom">手动输入</option>
                  </select>
                  {subscriptionUA === "custom" && <input value={customSubscriptionUA} onChange={(event) => setCustomSubscriptionUA(event.target.value)} placeholder="输入自定义 User-Agent" />}
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
                {parsedProxies.map((proxy, index) => (
                  <label key={index} className="node-inline-input">
                    <span>{String(proxy.type || "").toUpperCase() || "NODE"}</span>
                    <input aria-label={`预览节点 ${index + 1} 名称`} value={stringValue(proxy.name)} onChange={(event) => setParsedProxies((current) => current.map((item, i) => i === index ? { ...item, name: event.target.value } : item))} />
                    <button type="button" aria-label={`移除预览节点 ${index + 1}`} onClick={() => setParsedProxies((current) => current.filter((_, i) => i !== index))}><X /></button>
                  </label>
                ))}
              </div>
            )}
      </NodeSheet>}

      <section className="node-toolbar">
        <label className="node-search">
          <Search />
          <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索节点、用户、端口" aria-label="搜索节点、用户、端口" />
        </label>
        <button type="button" className="node-filter-button" onClick={() => setFilterOpen(true)}><Filter />筛选{filterChips.length > 0 && <b>{filterChips.length}</b>}</button>
      </section>
      {filterChips.length > 0 && <div className="node-active-filters">{filterChips.map((chip) => <button key={chip.label} type="button" onClick={chip.clear} title={`移除${chip.label}`}>{chip.label}<X /></button>)}</div>}
      <div className="node-grouping" aria-label="节点分组">{([["user", "按用户"], ["server", "按服务器"], ["package", "按套餐"], ["none", "不分组"]] as const).map(([value, label]) => <button type="button" key={value} aria-pressed={grouping === value} onClick={() => setGrouping(value)}>{label}</button>)}</div>
      {sortMode && <p className="node-sort-hint">{grouping === "none" ? "拖动左侧手柄排序，顺序自动保存。" : <>排序请切换到<button type="button" onClick={() => setGrouping("none")}>不分组</button>视图。</>}</p>}
      {filterOpen && <NodeSheet title="筛选节点" className="node-filter-sheet" onClose={() => setFilterOpen(false)}>
        <NodeSelect icon={<Filter />} value={protocol} onChange={setProtocol} label="协议" options={[{ value: ALL, label: `全部协议 (${nodes.length})` }, ...protocols.map((item) => ({ value: item.value, label: `${item.value.toUpperCase()} (${item.count})` }))]} />
        <details className="node-select node-tag-filter" open>
          <summary><Tags />标签{filterTags.length ? ` (${filterTags.length})` : "：全部"}</summary>
          <button type="button" onClick={() => setFilterTags([])}>全部标签</button>
          {[...new Set([...tagOptions.map((item) => item.value), ...tags])].map((value) => <label className="node-check" key={value}><input type="checkbox" checked={filterTags.includes(value)} onChange={() => setFilterTags((current) => current.includes(value) ? current.filter((item) => item !== value) : [...current, value])} /><span>{value} ({tagOptions.find((item) => item.value === value)?.count ?? 0})</span></label>)}
        </details>
        <NodeSelect icon={<Filter />} value={sourceFilter} onChange={(value) => setSourceFilter(value as typeof sourceFilter)} label="来源" options={[{ value: "all", label: "全部" }, { value: "manual", label: "手动输入" }, { value: "subscription", label: "订阅导入" }]} />
        <NodeSelect icon={<Server />} value={serverName} onChange={setServerName} label="服务器" options={[{ value: ALL, label: `全部服务器 (${nodes.length})` }, ...serverOptions.map((item) => ({ value: item.value, label: `${item.value} (${item.count})` }))]} />
        <select value={stateFilter} onChange={(event) => setStateFilter(event.target.value)} aria-label="状态筛选">
          <option value={ALL}>全部状态</option>
          <option value="enabled">已启用</option>
          <option value="disabled">已禁用</option>
          <option value="relay">中转中</option>
          <option value="routed">路由出站</option>
        </select>
        <div className="node-sheet-footer"><button type="button" onClick={resetFilters}>重置筛选</button><button type="button" className="primary" onClick={() => setFilterOpen(false)}>显示 {filtered.length} 个节点</button></div>
      </NodeSheet>}

      <section className="node-group-list" aria-label={`节点列表 (${filtered.length})`}>
        {loading ? <div className="node-empty"><Loader2 /> 正在读取节点</div> : filtered.length === 0 ? <div className="node-empty">没有符合条件的节点</div> : grouping === "none" ? (
          <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={(event) => void onDragEnd(event)}>
            <SortableContext items={filtered.map(({ node }) => node.id)} strategy={verticalListSortingStrategy}>
              <div className={`node-group ${sortMode ? "sorting" : ""}`}>{filtered.map(({ node }) => <SortableNodeCard key={node.id} id={node.id} disabled={!sortMode}>{renderRow(node)}</SortableNodeCard>)}</div>
            </SortableContext>
          </DndContext>
        ) : groups.map((group) => {
          const key = `${grouping}:${group.key}`, closed = collapsed.includes(key);
          const relays = group.nodes.filter(isNodeRelay).length;
          return <section className="node-group" key={group.key}>
            <div className="node-group-head">
              {selectionMode && <GroupCheckbox checked={group.nodes.every((node) => selected.has(node.id))} mixed={group.nodes.some((node) => selected.has(node.id))} label={`选择 ${group.name} 的节点`} onChange={() => setSelected((current) => toggleNodeSelection(current, group.nodes.map((node) => node.id)))} />}
              <button type="button" className="node-group-toggle" aria-expanded={!closed} onClick={() => setCollapsed((current) => closed ? current.filter((item) => item !== key) : [...current, key])}>
                <span className={`node-avatar tone-${group.name.charCodeAt(0) % 5}`}>{group.key === "admin" ? "我" : group.name.slice(0, 1)}</span>
                <span className="node-group-label"><strong>{group.name}</strong><small title={group.subtitle}>{group.subtitle}</small></span>
                <span className="node-group-count">{group.nodes.length - relays}{relays ? ` + ${relays} 中转` : ""}</span><ChevronDown className={closed ? "closed" : ""} />
              </button>
            </div>
            {!closed && nodeRelayRows(group.nodes, owners).map(({ node, children }) => {
              const relayKey = `${key}:${node.id}`, expanded = expandedRelays.has(relayKey);
              return <React.Fragment key={node.id}>
                {renderRow(node, false, children.length)}
                {(expanded ? children : children.slice(0, 1)).map((child) => <React.Fragment key={child.id}>{renderRow(child, true)}</React.Fragment>)}
                {children.length > 1 && <button className="node-relay-expand" type="button" onClick={() => setExpandedRelays((current) => { const next = new Set(current); expanded ? next.delete(relayKey) : next.add(relayKey); return next; })}>{expanded ? "收起中转" : `再显示 ${children.length - 1} 个中转`}<ChevronDown /></button>}
              </React.Fragment>;
            })}
          </section>;
        })}
      </section>

      {selectionMode && <div className="node-selection-bar" aria-label="已选节点操作">
        <strong>已选 {selectedNodes.length} 个</strong>
        <button type="button" disabled={!selectedNodes.length || Boolean(busy)} onClick={() => void testSelected()}>测延迟</button>
        <button type="button" disabled={!selectedNodes.length || Boolean(busy)} onClick={() => openTool({ kind: "batch-rename" })}>改名</button>
        <button type="button" disabled={!selectedNodes.length || Boolean(busy)} onClick={() => openTool({ kind: "batch-tag" })}>标签</button>
        <div className="node-selection-menu-wrap"><button type="button" aria-expanded={selectionMenu} onClick={() => setSelectionMenu((value) => !value)}>更多</button>
          {selectionMenu && <div className="node-selection-menu">
            <button type="button" onClick={() => setSelected(new Set(filtered.map(({ node }) => node.id)))}>全选筛选结果</button>
            <button type="button" onClick={() => setSelected(new Set())}>清空选择</button>
            <button type="button" disabled={!selectedNodes.length || Boolean(busy)} onClick={() => { setSelectionMenu(false); void addSelectedEmoji(); }}>添加 emoji</button>
            <button type="button" disabled={!selectedNodes.length} onClick={() => openTool({ kind: "batch-temp" })}>临时订阅</button>
            <button type="button" disabled={!selectedNodes.length} onClick={() => openTool({ kind: "batch" })}>批量操作</button>
            {sortMode && (["top", "up", "down", "bottom"] as const).map((direction, index) => <button type="button" key={direction} disabled={!selectedNodes.length || Boolean(busy) || grouping !== "none"} onClick={() => void saveOrder(moveSelectedNodes(nodeOrder, selected, direction))}>{["置顶", "上移", "下移", "置底"][index]}</button>)}
          </div>}
        </div>
      </div>}
      {actionNode && !dialog && renderActions(actionNode)}

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
          servers={servers}
          busy={busy}
          onClose={() => setDialog(null)}
          onRun={run}
        />
      )}
      {dialog?.kind === "managed-edit" && <ManagedNodeEditDialog token={token} node={dialog.node} servers={servers} username={username} onClose={() => setDialog(null)} onSaved={refreshExtras} />}
      {dialog?.kind === "flow-repair" && <NodeFlowRepairDialog token={token} node={dialog.node} servers={servers} onClose={() => setDialog(null)} onSaved={refreshExtras} />}
      {dialog?.kind === "chain" && <ChainProxyDialog token={token} source={dialog.node} nodes={nodes} onChanged={loadNodes} onClose={() => setDialog(null)} onNotice={toolNotice} />}
      {dialog?.kind === "relay-group" && <RelayGroupDialog token={token} source={dialog.node} nodes={nodes} onChanged={loadNodes} onClose={() => setDialog(null)} onNotice={toolNotice} />}
      {dialog?.kind === "resolve" && <NodeIPDialog token={token} node={dialog.node} ips={dialog.ips} busy={busy} onClose={() => setDialog(null)} onRun={run} />}
      {dialog?.kind === "batch-rename" && <BatchRenameDialog token={token} nodes={selectedNodes} busy={busy} onClose={() => setDialog(null)} onRun={run} onNotice={setNotice} />}
      {dialog?.kind === "batch-tag" && <BatchTagDialog token={token} nodes={selectedNodes} tags={tags} onChanged={loadNodes} busy={busy} onClose={() => setDialog(null)} onRun={run} onNotice={setNotice} />}
      {dialog?.kind === "batch-temp" && <BatchTempDialog token={token} nodes={selectedNodes} onClose={() => setDialog(null)} onNotice={setNotice} />}
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
      {dialog?.kind === "tunnels" && <TunnelManagerDialog token={token} servers={servers} nodes={nodes} onChanged={refreshExtras} onClose={() => setDialog(null)} onNotice={toolNotice} />}
      {dialog?.kind === "routed" && <RoutedOutboundDialog token={token} nodes={nodes} onChanged={refreshExtras} onClose={() => setDialog(null)} onNotice={toolNotice} />}
      {dialog?.kind === "landing" && <LandingNodeDialog token={token} username={username} servers={servers} source={nodes.find((item) => item.id === dialog.node.id) ?? dialog.node} nodes={nodes} onChanged={refreshExtras} onClose={() => setDialog(null)} onNotice={toolNotice} />}
      {dialog?.kind === "speedtest" && <SpeedTestDialog token={token} nodes={nodes} controller={speedTests} onClose={() => setDialog(null)} onNotice={toolNotice} />}
      {dialog?.kind === "speed-history" && <SpeedTestHistoryDialog token={token} nodes={nodes} node={dialog.node} onClose={() => setDialog(null)} onNotice={toolNotice} />}
      {dialog?.kind === "speed-testers" && <SpeedTesterManagerDialog token={token} onClose={() => setDialog(null)} onNotice={toolNotice} />}
      {dialog?.kind === "node-routing" && <NodeRoutingDialog token={token} node={dialog.node} server={dialog.server} onChanged={cardExtras.refresh} onClose={() => setDialog(null)} onNotice={toolNotice} />}
      {dialog?.kind === "node-probe" && <NodeProbeDialog token={token} nodes={nodes} status={probe.status} loading={probe.loading} error={probe.error} onRefresh={probe.refresh} onChanged={loadNodes} onClose={() => setDialog(null)} onNotice={toolNotice} />}
      {dialog?.kind === "relay-repair" && <RelayCredentialRepairDialog token={token} onChanged={refreshExtras} onClose={() => setDialog(null)} onNotice={toolNotice} />}
      {dialog?.kind === "relay-action" && <NodeRelayActionDialog token={token} node={dialog.node} tunnel={dialog.tunnel} servers={servers} onChanged={refreshExtras} onClose={() => setDialog(null)} onNotice={toolNotice} />}
      {dialog?.kind === "uris" && <URIManagerDialog token={token} onClose={() => setDialog(null)} onNotice={toolNotice} />}
      {dialog?.kind === "external-sync" && <ExternalSyncDialog token={token} initial={externalSyncSession} onSession={setExternalSyncSession} onClose={() => setDialog(null)} onNotice={toolNotice} onChanged={loadNodes} />}
      {dialog?.kind === "skip-cert" && <DisableSkipCertDialog token={token} nodes={nodes} onClose={() => setDialog(null)} onNotice={toolNotice} onChanged={loadNodes} />}
      {dialog?.kind === "snell" && <SnellOptionsDialog token={token} nodes={nodes} onClose={() => setDialog(null)} onNotice={toolNotice} onChanged={loadNodes} />}
      {dialog?.kind === "clear-all" && <ClearNodesConfirmDialog count={nodes.length} onClose={() => setDialog(null)} onConfirm={async () => { await clearNodes(token); setSelected(new Set()); await loadNodes(); setNotice({ tone: "success", text: "全部节点已清空" }); }} />}
    </div>
  );
}

function readNodePreference(key: string, fallback: string) {
  try { return localStorage.getItem(`mmwx-node-${key}`) || fallback; } catch { return fallback; }
}

function GroupCheckbox({ checked, mixed, label, onChange }: { checked: boolean; mixed: boolean; label: string; onChange: () => void }) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => { if (ref.current) ref.current.indeterminate = !checked && mixed; }, [checked, mixed]);
  return <input ref={ref} type="checkbox" checked={checked} aria-label={label} onChange={onChange} />;
}

function CompactNodeRow({ node, parsed, relay, badges, latency, selected, selectionMode, onSelect, onOpen, ownerHint, packageNames }: {
  node: XrayNode; parsed: ParsedProxy; relay: boolean; badges: Array<{ text: string; tone: string; title: string }>;
  latency?: { text: string; ok?: boolean; loading?: boolean }; selected: boolean; selectionMode: boolean;
  onSelect: () => void; onOpen: () => void; ownerHint: string; packageNames: string;
}) {
  const protocol = normalizeProtocol(node.protocol || stringValue(parsed.type));
  return <article className={`node-compact-row${relay ? " is-relay" : ""}${selected && selectionMode ? " selected" : ""}`} data-node-id={node.id}>
    {selectionMode && <input type="checkbox" checked={selected} onChange={onSelect} aria-label={`选择 ${node.node_name}`} />}
    <span className={`node-status-dot ${node.enabled === false ? "off" : "on"}`} title={node.enabled === false ? "已禁用" : "已启用"} />
    <div className="node-row-content">
      <div className="node-row-title"><button type="button" onClick={onOpen} title={`${node.node_name}\n${ownerHint}`}>{relay && "↳ "}{node.node_name}</button>
        <span className={`node-row-latency ${latency?.ok ? "ok" : latency ? "bad" : ""}`} title={latency?.text}>{latency?.loading ? "测试中" : latency?.ok ? latency.text : latency ? "失败" : "—"}</span>
      </div>
      <div className="node-row-meta">
        <span className="node-row-protocol">{protocol.toUpperCase() || "NODE"}</span>
        <span className="node-row-server" title={node.original_server}>{node.original_server || "外部节点"}</span>
        <span className="node-row-port">:{stringValue(parsed.port) || "—"}</span>
        {badges.map((badge) => <span key={badge.tone} className={`node-row-tag ${badge.tone}`} title={badge.title}>{badge.text}</span>)}
        <span className="node-row-desktop" title={`${stringValue(parsed.server)} · ${stringValue(parsed.network || parsed.transport) || "tcp"} · ${packageNames}`}>{stringValue(parsed.server)} · {stringValue(parsed.network || parsed.transport) || "tcp"}{packageNames && ` · ${packageNames}`}</span>
      </div>
    </div>
    <button type="button" className="node-row-more" aria-label={`操作 ${node.node_name}`} onClick={onOpen}><MoreHorizontal /></button>
  </article>;
}

function NodeSheet({ title, subtitle, className = "", onClose, children }: { title: string; subtitle?: string; className?: string; onClose: () => void; children: React.ReactNode }) {
  const ref = useRef<HTMLElement>(null);
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const overflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    ref.current?.focus();
    return () => { document.body.style.overflow = overflow; previous?.focus(); };
  }, []);
  return <div className="node-dialog-layer node-sheet-layer" onClick={onClose}>
    <section ref={ref} className={`node-dialog node-sheet ${className}`} role="dialog" aria-modal="true" aria-label={title} tabIndex={-1} onClick={(event) => event.stopPropagation()} onKeyDown={(event) => {
      if ((event.target as HTMLElement).closest('[role="dialog"]') !== ref.current) return;
      if (event.key === "Escape") { event.stopPropagation(); onClose(); }
      if (event.key === "Tab") {
        const elements = [...ref.current!.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), summary, [tabindex="0"]')].filter((element) => element.getClientRects().length > 0);
        const first = elements[0], last = elements[elements.length - 1];
        if (event.shiftKey && (document.activeElement === first || document.activeElement === ref.current)) { event.preventDefault(); last?.focus(); }
        else if (!event.shiftKey && (document.activeElement === last || document.activeElement === ref.current)) { event.preventDefault(); first?.focus(); }
      }
    }}>
      <div className="node-sheet-grab" />
      <header><div><h2>{title}</h2>{subtitle && <p>{subtitle}</p>}</div><button type="button" onClick={onClose} aria-label="关闭面板"><X /></button></header>
      <div className="node-dialog-body">{children}</div>
    </section>
  </div>;
}

function SortableNodeCard({ id, disabled, children }: { id: number; disabled: boolean; children: React.ReactNode }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id, disabled });
  return <div ref={setNodeRef} style={{ transform: CSS.Transform.toString(transform), transition, opacity: isDragging ? 0.55 : 1 }} className="node-sortable-card">
    {!disabled && <button className="node-drag-handle" type="button" aria-label="拖动排序" {...attributes} {...listeners}><GripVertical /></button>}
    {children}
  </div>;
}

function NodeActionSheet({
  node, onClose, onSpeed, speedBusy, notice, ownerHint, packageNames, packageAction,
  parsed,
  latency,
  onDetails,
  onEdit,
  onLanding,
  onEditInbound,
  onChain,
  onRelayGroup,
  onCancelWholeOutbound,
  onRouting,
  extras,
  extraActions,
  speedActions,
  onCopy,
  onTcping,
  onEmoji,
  onResolve,
  onRestore,
  onTemp,
  onDelete,
}: {
  node: XrayNode;
  onClose: () => void;
  onSpeed: () => void;
  speedBusy: boolean;
  notice: Notice;
  ownerHint: string;
  packageNames: string;
  packageAction: React.ReactNode;
  parsed: ParsedProxy;
  latency?: { loading?: boolean; text: string; ok?: boolean };
  onDetails: () => void;
  onEdit: () => void;
  onLanding: () => void;
  onEditInbound: () => void;
  onChain: () => void;
  onRelayGroup: () => void;
  onCancelWholeOutbound: () => void;
  onRouting?: () => void;
  extras: React.ReactNode;
  extraActions: React.ReactNode;
  speedActions: React.ReactNode;
  onCopy: () => void;
  onTcping: () => void;
  onEmoji: () => void;
  onResolve: () => void;
  onRestore: () => void;
  onTemp: () => void;
  onDelete: () => void;
}) {
  const protocol = normalizeProtocol(node.protocol || stringValue(parsed.type));
  const managed = (node.inbound_tag || node.original_server || node.tag?.startsWith("远程:")) && node.node_type !== "routed";
  return <NodeSheet title={node.node_name} subtitle={`${protocol.toUpperCase()} · ${node.original_server || "外部节点"} · ${stringValue(parsed.server)}:${stringValue(parsed.port)} · ${packageNames || "未加入套餐"}`} onClose={onClose} className="node-action-sheet">
    <p className="node-owner-hint">{ownerHint}</p>
    {notice && <NodeNotice notice={notice} />}
    <div className="node-quick-actions">
      <button type="button" onClick={onCopy}><Copy />复制 URI</button>
      <button type="button" onClick={onTcping}><Zap />测延迟</button>
      <button type="button" disabled={speedBusy} onClick={onSpeed}><Zap />测速</button>
      <button type="button" onClick={managed ? onEditInbound : onEdit}><Edit3 />编辑</button>
    </div>
    {latency && <p className={latency.ok ? "node-ok" : "node-error"}>TCPing：{latency.text}</p>}
    <h3 className="node-sheet-section">路由与中转</h3>
    {extraActions}
    <details className="node-sheet-submenu"><summary>节点路由 · 链式出站 · 中转组</summary>
      <div className="node-sheet-items">
        {onRouting && <button type="button" onClick={onRouting}><Route />节点路由</button>}
        <button type="button" onClick={onChain}><Link2 />链式出站</button>
        <button type="button" onClick={onRelayGroup}><Link2 />中转组</button>
        {node.node_type !== "routed" && <button type="button" onClick={onLanding}><Route />新增落地节点 / 整个节点出站</button>}
        {node.inbound_tag && node.node_type !== "routed" && <button type="button" onClick={onCancelWholeOutbound}><Route />取消整个节点出站</button>}
      </div>
    </details>
    <h3 className="node-sheet-section">更多</h3>
    <div className="node-sheet-package"><span>套餐归属</span>{packageAction}</div>
    <details className="node-sheet-submenu"><summary>测速历史 · 探测与状态</summary>
      <div className="node-action-details"><div className="node-chip-row node-card-badges">{extras}
        {nodeTags(node).map((tag) => <span key={tag}>{tag}</span>)}
        {node.inbound_tag && <span>入站 {node.inbound_tag}</span>}
        {node.chain_proxy_node_id && <span>链式 #{node.chain_proxy_node_id}</span>}
        {node.node_type === "routed" && <span>路由出站</span>}
        <span>{stringValue(parsed.network || parsed.transport) || "tcp"}</span>
        {Boolean(parsed["reality-opts"] || parsed.tls) && <span>{parsed["reality-opts"] ? "Reality" : "TLS"}</span>}
      </div></div>
      {speedActions}
    </details>
    <details className="node-sheet-submenu"><summary>查看配置 · 临时订阅 · 更多</summary>
      <div className="node-sheet-items">
        <button type="button" onClick={onEdit}><Edit3 />编辑名称 / 中转配置</button>
        {managed && <button type="button" onClick={onEditInbound}><Edit3 />编辑节点</button>}
        <button type="button" onClick={onDetails}><Eye />查看配置</button>
        <button type="button" onClick={onTemp}><Link2 />临时订阅</button>
        <button type="button" onClick={onResolve}><Server />解析 IP</button>
        <button type="button" onClick={onRestore}><RefreshCw />恢复域名</button>
        <button type="button" onClick={onEmoji}><Tags />地区 emoji</button>
      </div>
    </details>
    <button className="node-delete-action danger" type="button" onClick={onDelete}><Trash2 />删除节点</button>
  </NodeSheet>;
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
        <div className="node-empty">当前没有发现名称和 Clash 配置相同的重复节点</div>
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
  servers,
  busy,
  onClose,
  onRun,
}: {
  token: string;
  node: XrayNode;
  servers: RemoteServer[];
  busy: string;
  onClose: () => void;
  onRun: (label: string, action: () => Promise<void>) => Promise<void>;
}) {
  const [name, setName] = useState(node.node_name);
  const [enabled, setEnabled] = useState(node.enabled !== false);
  const [tagsValue, setTagsValue] = useState(nodeTags(node).join(", "));
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
      inbound_tag: node.inbound_tag || "",
      chain_proxy_node_id: node.chain_proxy_node_id ?? null,
      relay_group_name: node.relay_group_name || "",
      relay_group_node_ids: node.relay_group_node_ids ?? null,
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
        tag: tag.trim() || "手动输入",
        tags: tag.trim() ? splitList(tag) : ["手动输入"],
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

function NodeIPDialog({ token, node, ips, busy, onClose, onRun }: { token: string; node: XrayNode; ips: string[]; busy: string; onClose: () => void; onRun: (label: string, action: () => Promise<void>) => Promise<void> }) {
  const [ip, setIP] = useState(ips[0]);
  return <NodeDialog title="选择IP地址" subtitle={node.node_name} onClose={onClose}>
    <div className="node-tool-list selectable">{ips.map((value) => <label key={value}><input type="radio" name="resolved-ip" checked={value === ip} onChange={() => setIP(value)} /><span>{value}</span></label>)}</div>
    <div className="node-dialog-actions"><button type="button" disabled={Boolean(busy)} onClick={() => void onRun("解析 IP", async () => { await updateNodeServer(token, node.id, ip); onClose(); })}>确认修改</button></div>
  </NodeDialog>;
}

function BatchRenameDialog({ token, nodes, busy, onClose, onRun, onNotice }: { token: string; nodes: XrayNode[]; busy: string; onClose: () => void; onRun: (label: string, action: () => Promise<void>) => Promise<void>; onNotice: (notice: Notice) => void }) {
  const [names, setNames] = useState(nodes.map((node) => node.node_name).join("\n"));
  const [find, setFind] = useState("");
  const [replace, setReplace] = useState("");
  const [prefix, setPrefix] = useState("");
  const [suffix, setSuffix] = useState("");
  return <NodeDialog title="批量修改节点名称" subtitle={`修改选中的 ${nodes.length} 个节点名称`} onClose={onClose}>
    <div className="node-form-grid">
      <label><span>查找内容</span><input value={find} onChange={(event) => setFind(event.target.value)} placeholder="输入要查找的文本" /></label>
      <label><span>替换为</span><input value={replace} onChange={(event) => setReplace(event.target.value)} placeholder="输入替换后的文本" /></label>
    </div>
    <div className="node-dialog-actions"><button type="button" disabled={!find} onClick={() => setNames(batchRenameTransform(names, { find, replace }))}>替换</button></div>
    <div className="node-form-grid">
      <label><span>前缀</span><input value={prefix} onChange={(event) => setPrefix(event.target.value)} placeholder="添加到名称前面" /></label>
      <label><span>后缀</span><input value={suffix} onChange={(event) => setSuffix(event.target.value)} placeholder="添加到名称后面" /></label>
    </div>
    <div className="node-dialog-actions"><button type="button" disabled={!prefix && !suffix} onClick={() => { setNames(batchRenameTransform(names, { prefix, suffix })); setPrefix(""); setSuffix(""); }}>应用</button></div>
    <label><span>节点名称 (每行一个，共 {names.split("\n").length} 行)</span><textarea className="node-json-editor compact" value={names} onChange={(event) => setNames(event.target.value)} placeholder="每行一个节点名称" /></label>
    <div className="node-dialog-actions"><button type="button" disabled={Boolean(busy) || !names.trim()} onClick={() => void onRun("批量修改名称", async () => {
      const next = names.split("\n").map((name) => name.trim()).filter(Boolean);
      if (next.length !== nodes.length) throw new Error(`名称数量 ${next.length} 与选中节点数 ${nodes.length} 不一致`);
      const result = await batchRenameNodes(token, nodes.map((node, index) => ({ node_id: node.id, new_name: next[index] })));
      onClose(); onNotice({ tone: "success", text: `成功修改 ${result.success ?? nodes.length} 个节点名称` });
    })}>确认修改</button></div>
  </NodeDialog>;
}

function editableNodeTags(node: XrayNode) {
  return node.tags?.length ? node.tags : node.tag && !node.tag.startsWith("远程:") ? [node.tag] : [];
}

function BatchTagDialog({ token, nodes, tags, busy, onClose, onRun, onNotice, onChanged }: { token: string; nodes: XrayNode[]; tags: string[]; busy: string; onChanged: () => Promise<void>; onClose: () => void; onRun: (label: string, action: () => Promise<void>) => Promise<void>; onNotice: (notice: Notice) => void }) {
  const [mode, setMode] = useState<"add" | "rename" | "delete">("add");
  const [name, setName] = useState("");
  const [oldTag, setOldTag] = useState("");
  const [removed, setRemoved] = useState<string[]>([]);
  const existing = [...new Set(nodes.flatMap(editableNodeTags))];
  const save = async () => {
    let count = 0;
    for (const node of nodes) {
      let next = editableNodeTags(node);
      if (mode === "add") next = [...next, name.trim()];
      if (mode === "rename") next = next.map((tag) => tag === oldTag ? name.trim() : tag);
      if (mode === "delete") next = next.filter((tag) => !removed.includes(tag));
      next = [...new Set(next.map((tag) => tag.trim()).filter(Boolean))];
      try { await updateNode(token, node.id, { node_name: node.node_name, enabled: node.enabled, tag: next[0] || "", tags: next }); count += 1; }
      catch (error) { await onChanged(); throw new Error(`已更新 ${count}/${nodes.length} 个节点：${error instanceof Error ? error.message : "批量更新标签失败"}`); }
    }
    onClose(); onNotice({ tone: "success", text: mode === "add" ? `成功为 ${count} 个节点添加标签` : mode === "rename" ? `成功修改 ${count} 个节点的标签` : `成功删除 ${count} 个节点的标签` });
  };
  return <NodeDialog title="批量修改标签" subtitle={`将为选中的 ${nodes.length} 个节点修改标签`} onClose={onClose}>
    <div className="node-dialog-tabs">{(["add", "rename", "delete"] as const).map((value, index) => <button type="button" className={mode === value ? "active" : ""} key={value} onClick={() => setMode(value)}>{["添加标签", "修改标签", "删除标签"][index]}</button>)}</div>
    {mode !== "add" && <div className="node-subpanel"><h3>选择要操作的标签</h3>{!existing.length ? <p>选中的节点暂无标签</p> : existing.map((tag) => <label className="node-check" key={tag}><input type={mode === "delete" ? "checkbox" : "radio"} name="existing-tag" checked={mode === "delete" ? removed.includes(tag) : oldTag === tag} onChange={() => mode === "delete" ? setRemoved((current) => current.includes(tag) ? current.filter((value) => value !== tag) : [...current, tag]) : setOldTag(tag)} /><span>{tag}</span></label>)}{mode === "delete" && <button type="button" onClick={() => setRemoved(removed.length === existing.length ? [] : existing)}>{removed.length === existing.length ? "清空" : "全选"}</button>}</div>}
    {mode !== "delete" && <label><span>新标签名称</span><input value={name} onChange={(event) => setName(event.target.value)} placeholder="输入标签名称" /></label>}
    {mode === "add" && tags.length > 0 && <div className="node-subpanel"><h3>快速选择标签</h3><div className="node-dialog-actions">{tags.map((tag) => <button type="button" key={tag} onClick={() => setName(tag)}>{tag}</button>)}</div></div>}
    <div className="node-dialog-actions"><button type="button" disabled={Boolean(busy) || (mode === "delete" ? !removed.length : !name.trim() || mode === "rename" && (!oldTag || name.trim() === oldTag))} onClick={() => void onRun("批量修改标签", save)}>{mode === "add" ? "添加" : mode === "rename" ? "保存" : "删除"}</button></div>
  </NodeDialog>;
}

function BatchTempDialog({ token, nodes, onClose, onNotice }: { token: string; nodes: XrayNode[]; onClose: () => void; onNotice: (notice: Notice) => void }) {
  const [maxAccess, setMaxAccess] = useState(1);
  const [expireSeconds, setExpireSeconds] = useState(600);
  const [url, setURL] = useState("");
  const [busy, setBusy] = useState(false);
  const create = async () => {
    setBusy(true);
    try {
      const response = await createNodeTempSubscription(token, { node_ids: nodes.map((node) => node.id) }, maxAccess, expireSeconds);
      if (!response.url) throw new Error("后端未返回临时订阅地址");
      const absolute = new URL(response.url, window.location.origin).href;
      setURL(absolute); await writeClipboard(absolute);
      onNotice({ tone: "success", text: "临时订阅已生成并复制" });
    } catch (error) { onNotice({ tone: "error", text: error instanceof Error ? error.message : "生成临时订阅失败" }); }
    finally { setBusy(false); }
  };
  return <NodeDialog title="生成临时订阅" subtitle={`已选 ${nodes.length} 个节点`} onClose={onClose}>
    <div className="node-form-grid">
      <label><span>最多访问次数</span><input type="number" min={1} value={maxAccess} onChange={(event) => setMaxAccess(toPositiveInt(event.target.value, 1))} /></label>
      <label><span>有效秒数</span><input type="number" min={10} value={expireSeconds} onChange={(event) => setExpireSeconds(toPositiveInt(event.target.value, 600))} /></label>
      {url && <label className="wide"><span>临时订阅</span><input readOnly value={url} onFocus={(event) => event.currentTarget.select()} /></label>}
    </div>
    <div className="node-dialog-actions"><button type="button" disabled={busy || maxAccess < 1 || expireSeconds < 10} onClick={() => void create()}><Link2 />生成并复制</button></div>
  </NodeDialog>;
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

function proxyToNodeRequest(proxy: ParsedProxy, tag: string, rawURL: string): NodeMutationRequest {
  const name = stringValue(proxy.name) || stringValue(proxy.ps) || "未命名节点";
  const type = normalizeProtocol(stringValue(proxy.type || proxy.protocol));
  const body = JSON.stringify({ ...proxy, name, type });
  const tags = tag ? splitList(tag) : [];
  return {
    raw_url: rawURL,
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
  const serverName = node.original_server || (node.tag?.startsWith("远程:") ? node.tag.slice(3) : "");
  const server = servers.find((item) => item.name === serverName);
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
  const values = (node.tags?.length ? node.tags : [node.tag ?? ""]).map((item) => item.trim()).filter(Boolean);
  return [...new Set(values)];
}

function deriveTags(nodes: XrayNode[]) {
  return [...new Set(nodes.flatMap(nodeTags))].sort((a, b) => a.localeCompare(b));
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
