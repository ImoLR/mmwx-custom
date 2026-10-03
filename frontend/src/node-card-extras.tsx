import { useCallback, useEffect, useMemo, useState } from "react";
import { X } from "lucide-react";
import { cancelNodeRelay, fetchBlockedNodeIds, fetchNodeTunnels, fetchNodeUnlocks, fetchRemoteRouting, fetchXrayOutbounds, setNodeRelay } from "./api";
import { externalNodeSource, nodeCardConfig, nodeManagedServer, nodeTunnelChain, nodeTunnels, resolveWholeOutbound, tunnelEntryHost, type NodeRoutingState } from "./node-card-logic";
import { removeNodeTunnel } from "./node-manager-tools";
import type { NodeTunnel, NodeTunnelChain, RemoteServer, XrayNode } from "./types";

type Notice = (tone: "success" | "error" | "info", text: string) => void;

export function useNodeCardExtras(token: string, nodes: XrayNode[], servers: RemoteServer[]) {
  const [blocked, setBlocked] = useState<Set<number>>(new Set());
  const [unlocks, setUnlocks] = useState<Record<string, { unlocked: number; total: number }>>({});
  const [tunnels, setTunnels] = useState<NodeTunnel[]>([]);
  const [chains, setChains] = useState<NodeTunnelChain[]>([]);
  const [routing, setRouting] = useState<Record<number, NodeRoutingState>>({});
  const [revision, setRevision] = useState(0);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const serverIds = [...new Set(nodes.map((node) => nodeManagedServer(node, servers)?.id).filter((id): id is number => id != null))].sort((a, b) => a - b).join(",");
  const refresh = useCallback(async () => { setRevision((value) => value + 1); }, []);
  useEffect(() => {
    let active = true;
    const load = async () => {
      const results = await Promise.allSettled([fetchBlockedNodeIds(token), fetchNodeTunnels(token)]);
      if (!active) return;
      const [blockResult, tunnelResult] = results;
      if (blockResult.status === "fulfilled") setBlocked(new Set(blockResult.value.node_ids || []));
      if (tunnelResult.status === "fulfilled") { setTunnels(tunnelResult.value.tunnels || []); setChains(tunnelResult.value.chains || []); }
      setErrors((current) => ({ ...current, card: results.some((result) => result.status === "rejected") ? "部分节点状态读取失败" : "" }));
    };
    void load(); const timer = window.setInterval(() => { if (!document.hidden) void load(); }, 60000);
    return () => { active = false; window.clearInterval(timer); };
  }, [token, revision]);
  useEffect(() => {
    let active = true;
    const load = async () => {
      try { const response = await fetchNodeUnlocks(token); if (active) { setUnlocks(response.nodes || {}); setErrors((current) => ({ ...current, unlocks: "" })); } }
      catch { if (active) setErrors((current) => ({ ...current, unlocks: "解锁状态读取失败" })); }
    };
    void load(); const timer = window.setInterval(() => { if (!document.hidden) void load(); }, 300000);
    return () => { active = false; window.clearInterval(timer); };
  }, [token]);
  useEffect(() => {
    let active = true, inFlight = false;
    const load = async () => {
      if (inFlight) return;
      inFlight = true;
      try {
        const next: Record<number, NodeRoutingState> = {}, failures: number[] = [];
        // One pair of reads per server, irrespective of its node count.
        for (const id of serverIds.split(",").filter(Boolean).map(Number)) {
          const [rules, outbounds] = await Promise.allSettled([fetchRemoteRouting(token, id), fetchXrayOutbounds(token, id)]);
          if (!active) return;
          if (rules.status === "fulfilled" && outbounds.status === "fulfilled") next[id] = { ...rules.value.routing, outbounds: outbounds.value.outbounds || [] };
          else failures.push(id);
        }
        if (active) { setRouting(next); setErrors((current) => ({ ...current, routing: failures.length ? "部分服务器出站状态读取失败" : "" })); }
      } finally { inFlight = false; }
    };
    void load(); const timer = window.setInterval(() => { if (!document.hidden) void load(); }, 60000);
    return () => { active = false; window.clearInterval(timer); };
  }, [token, serverIds, revision]);
  const cards = useMemo(() => new Map(nodes.map((node) => {
    const server = nodeManagedServer(node, servers);
    return [node.id, { blocked: blocked.has(node.id), unlock: unlocks[String(node.id)], source: externalNodeSource(node, servers), tunnels: nodeTunnels(node, tunnels, servers), chain: nodeTunnelChain(node, chains, servers), whole: resolveWholeOutbound(node, server ? routing[server.id] : undefined, nodes, servers, tunnels) }];
  })), [nodes, servers, blocked, unlocks, tunnels, chains, routing]);
  return { cards, refresh, error: Object.values(errors).filter(Boolean).join("；") };
}

type CardState = ReturnType<typeof useNodeCardExtras>["cards"] extends Map<number, infer State> ? State : never;
export function NodeCardBadges({ node, state, servers }: { node: XrayNode; state?: CardState; servers: RemoteServer[] }) {
  const chain = state?.chain, chainEntry = chain ? servers.find((server) => server.id === chain.entry_server) : undefined;
  return <>
    {state?.blocked && <span className="bad">被墙</span>}
    {node.multiplier != null && node.multiplier !== 1 && <span title={`此节点流量按 ${node.multiplier}× 计入套餐配额`}>×{node.multiplier}</span>}
    {state?.source && <span>{state.source}</span>}
    {!!state?.unlock?.total && <span title="解锁服务数量">解锁 {state.unlock.unlocked}/{state.unlock.total}</span>}
    {!!state?.tunnels.length && <span title={`以下 tunnel 入站转发到此节点:\n${state.tunnels.map((tunnel) => `${tunnel.server_name}:${tunnel.listen_port} → ${tunnel.target_address}:${tunnel.target_port} · ${tunnel.tag}`).join("\n")}`}>被 tunnel 转发</span>}
    {node.relay_orig_server && <span>中转原服务器 {node.relay_orig_server}:{node.relay_orig_port}</span>}
    {chain && <span title={`链式隧道路径\n${[...(chain.hops || []).map((hop) => hop.server_name || `#${hop.server_id}`), node.node_name].join(" → ")}`}>链式隧道 {chainEntry?.name || chainEntry?.ip_address || ""}:{chain.entry_port}</span>}
    {state?.whole && <span>整个节点出站: {state.whole.label}</span>}
  </>;
}

export function NodeCardExtras({ node, state, servers, onTunnel, onRelay, onRevertChain, onSwitchWhole, onCancelWhole }: { node: XrayNode; state?: CardState; servers: RemoteServer[]; onTunnel: (tunnel: NodeTunnel) => void; onRelay: () => void; onRevertChain: (entry: string) => void; onSwitchWhole: () => void; onCancelWhole: () => void }) {
  const chain = state?.chain, chainEntry = chain ? servers.find((server) => server.id === chain.entry_server) : undefined;
  const entry = chain ? `${chainEntry?.name || chainEntry?.ip_address || ""}:${chain.entry_port}` : "";
  const parsed = nodeCardConfig(node);
  return <div className="node-card-extras">
    {node.relay_orig_server && <button type="button" className="node-state-link" onClick={onRelay} title="点击修改 / 取消中转">中转原服务器 {node.relay_orig_server}:{node.relay_orig_port}</button>}
    {chain && <button type="button" className="node-state-link" onClick={() => onRevertChain(entry)} title={`链式隧道路径\n${[...(chain.hops || []).map((hop) => hop.server_name || `#${hop.server_id}`), node.node_name].join(" → ")}\n点击切回源节点地址`}>链式隧道 {entry}</button>}
    {state?.tunnels.map((tunnel) => {
      const host = tunnelEntryHost(tunnel, servers), current = host === parsed.server && Number(tunnel.listen_port) === Number(parsed.port);
      return <button key={`${tunnel.server_id}:${tunnel.tag}`} type="button" className="node-state-link" title="点击管理此 tunnel 转发" onClick={() => onTunnel(tunnel)}>{host}:{tunnel.listen_port}{current ? "" : " (其他入口)"}</button>;
    })}
    {state?.whole && <details className="node-whole-outbound"><summary title="点击管理整个节点出站">整个节点出站: {state.whole.label}</summary>{state.whole.members.length > 0 && <p>负载均衡节点: {state.whole.members.join("、")}</p>}<div className="node-dialog-actions"><button type="button" onClick={onSwitchWhole}>切换出站节点</button><button type="button" onClick={onCancelWhole}>取消整个节点出站</button></div></details>}
  </div>;
}

export function NodeRelayActionDialog({ token, node, tunnel, servers, onClose, onChanged, onNotice }: { token: string; node: XrayNode; tunnel?: NodeTunnel; servers: RemoteServer[]; onClose: () => void; onChanged: () => Promise<void>; onNotice: Notice }) {
  const parsed = nodeCardConfig(node);
  const [host, setHost] = useState(String(parsed.server || ""));
  const [port, setPort] = useState(String(parsed.port || ""));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const title = tunnel ? "Tunnel 中转" : "中转配置", entry = tunnel ? tunnelEntryHost(tunnel, servers) : "";
  const run = async (action: () => Promise<unknown>, text: string) => {
    setBusy(true); setError("");
    try { await action(); await onChanged(); onNotice("success", text); onClose(); }
    catch (err) { setError(err instanceof Error ? err.message : "操作失败"); }
    finally { setBusy(false); }
  };
  return <div className="node-dialog-layer" onClick={() => !busy && onClose()}><section className="node-dialog node-tool-dialog" role="dialog" aria-modal="true" aria-label={title} onClick={(event) => event.stopPropagation()}>
    <header><div><h2>{title}</h2><p>{tunnel ? "把节点地址切换为 tunnel 入口地址,或切回原地址,或删除此 tunnel 端口转发。" : "节点通过中转服务器连接;clash 的 server/port 走中转地址。取消后还原为下方原服务器。"}</p></div><button type="button" disabled={busy} onClick={onClose} aria-label="关闭"><X /></button></header>
    <div className="node-dialog-body"><p>节点: {node.node_name}</p>{node.relay_orig_server && <p>原服务器: {node.relay_orig_server}:{node.relay_orig_port}</p>}
      {tunnel ? <><p>tunnel 入口: {entry}:{tunnel.listen_port}</p><p>转发目标: {tunnel.target_address}:{tunnel.target_port}</p></> : <div className="node-form-grid"><label><span>中转服务器 (IP / 域名)</span><input value={host} onChange={(event) => setHost(event.target.value)} /></label><label><span>中转端口</span><input type="number" min={1} max={65535} value={port} onChange={(event) => setPort(event.target.value)} /></label></div>}
      {error && <p role="alert" className="node-error">{error}</p>}
      <div className="node-dialog-actions">
        {tunnel ? <button type="button" disabled={busy || !entry || !tunnel.listen_port} onClick={() => void run(() => setNodeRelay(token, node.id, entry, Number(tunnel.listen_port)), "中转已更新")}>切换节点地址为 tunnel 入口</button> : <button type="button" disabled={busy || !host.trim() || !Number.isInteger(Number(port)) || Number(port) < 1 || Number(port) > 65535} onClick={() => void run(() => setNodeRelay(token, node.id, host.trim(), Number(port)), "中转已更新")}>保存</button>}
        {node.relay_orig_server && <button type="button" disabled={busy} onClick={() => void run(() => cancelNodeRelay(token, node.id), "已取消中转")}>{tunnel ? "切回原服务器地址" : "取消中转"}</button>}
        {tunnel && <button type="button" className="danger" disabled={busy} onClick={() => { if (window.confirm("确定删除此 tunnel 端口转发?")) void run(() => removeNodeTunnel(token, tunnel), "tunnel 端口转发已删除"); }}>删除此 tunnel</button>}
      </div>
    </div>
  </section></div>;
}
