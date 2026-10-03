import { useEffect, useMemo, useState } from "react";
import { Loader2, Plus, RefreshCw, Trash2, X } from "lucide-react";
import { controlRemoteService, fetchRemoteRouting, fetchXrayOutbounds, mutateRemoteOutbound, mutateRemoteRouting } from "./api";
import { nodeRoutingQuickRules, nodeRuleName, routingAppliedHot, splitNodeRoutingRules, withRoutingTarget } from "./node-routing-logic";
import type { IndexedNodeRule, NodeRoutingRule } from "./node-routing-logic";
import type { RemoteServer, XrayNode } from "./types";

export function NodeRoutingDialog({ token, node, server, onChanged, onClose, onNotice }: { token: string; node: XrayNode; server: RemoteServer; onChanged?: () => Promise<void>; onClose: () => void; onNotice: (tone: "success" | "error" | "info", text: string) => void }) {
  const [rules, setRules] = useState<NodeRoutingRule[]>([]);
  const [outbounds, setOutbounds] = useState<NodeRoutingRule[]>([]);
  const [balancers, setBalancers] = useState<NodeRoutingRule[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [custom, setCustom] = useState(false);
  const [scope, setScope] = useState("dedicated");
  const [kind, setKind] = useState("domain");
  const [match, setMatch] = useState("");
  const [target, setTarget] = useState("");
  const [mark, setMark] = useState("");
  const [quick, setQuick] = useState<NodeRoutingRule | null>(null);
  const inboundTag = node.inbound_tag || "";
  const split = useMemo(() => splitNodeRoutingRules(rules, inboundTag, node.routed_outbound_tag || ""), [rules, inboundTag, node.routed_outbound_tag]);
  const load = async () => {
    const [routing, outbound] = await Promise.all([fetchRemoteRouting(token, server.id), fetchXrayOutbounds(token, server.id)]);
    setRules(routing.routing?.rules || []);
    setBalancers(routing.routing?.balancers || []);
    setOutbounds(outbound.outbounds || []);
  };
  useEffect(() => { void load().catch((reason) => setError(reason instanceof Error ? reason.message : "加载路由配置失败")).finally(() => setLoading(false)); }, [token, server.id]);
  const mutate = async (body: Record<string, unknown>, removed?: NodeRoutingRule) => {
    setBusy(true); setError("");
    try {
      const response = await mutateRemoteRouting(token, server.id, body);
      if (!response.success) throw new Error(response.message || (removed ? "删除失败" : "添加失败"));
      const removedTag = String(removed?.outboundTag || "");
      if (removedTag && !["direct", "block", "api", "freedom"].includes(removedTag)) {
        try { await mutateRemoteOutbound(token, server.id, { action: "remove", tag: removedTag }); } catch { /* Match the official best-effort outbound cleanup. */ }
      }
      const hot = routingAppliedHot(response);
      let restartError = "";
      if (!hot) {
        try {
          const restarted = await controlRemoteService(token, server.id, "xray", "restart");
          if (restarted.success === false) throw new Error(restarted.message || "重启 Xray 失败");
        } catch (reason) { restartError = reason instanceof Error ? reason.message : "重启 Xray 失败"; }
      }
      if (restartError) onNotice("error", `路由规则已${removed ? "删除" : "添加"}，但重启 Xray 失败: ${restartError}`);
      else onNotice("success", removed ? hot ? "路由规则已删除并热生效 (未重启 Xray)" : "路由规则已删除并重启 Xray" : hot ? "路由规则已添加并热生效 (未重启 Xray)" : "路由规则已添加并重启 Xray");
      setCustom(false); setQuick(null); setMatch(""); setMark(""); setTarget("");
      await load(); await onChanged?.();
    } catch (reason) { setError(reason instanceof Error ? reason.message : "路由配置失败"); }
    finally { setBusy(false); }
  };
  const targetOptions = <><option value="">选择出站</option>{outbounds.filter((item) => item.tag).map((item) => <option key={String(item.tag)} value={String(item.tag)}>{String(item.tag)} ({String(item.protocol || "")})</option>)}{balancers.filter((item) => item.tag).map((item) => <option key={`balancer:${item.tag}`} value={`balancer:${item.tag}`}>⚖ {String(item.tag)} (负载均衡)</option>)}</>;
  const renderRule = ({ rule, originalIndex }: IndexedNodeRule) => {
    const field = ["protocol", "domain", "ip"].find((name) => Array.isArray(rule[name]) && (rule[name] as unknown[]).length);
    const condition = field ? (rule[field] as unknown[]).join(", ") : rule.port ? String(rule.port) : Array.isArray(rule.inboundTag) && rule.inboundTag.length ? "全部流量" : "—";
    return <article key={originalIndex}><div><strong>{nodeRuleName(rule) || field || "入站匹配"} → {rule.balancerTag ? `⚖ ${rule.balancerTag}` : String(rule.outboundTag || "未设置")}</strong><p style={{ overflowWrap: "anywhere" }}>{condition}</p><details><summary>路由规则详情</summary><pre className="node-json-editor compact">{JSON.stringify(rule, null, 2)}</pre></details></div><button type="button" disabled={busy} aria-label="删除路由规则" onClick={() => { if (window.confirm("确定要删除此路由规则吗？删除后将自动重启 Xray 生效。")) void mutate({ action: "remove_rule", index: originalIndex }, rule); }}><Trash2 /></button></article>;
  };
  return <div className="node-dialog-layer" role="presentation" onClick={busy ? undefined : onClose}><section className="node-dialog node-tool-dialog" role="dialog" aria-modal="true" aria-label="节点路由" onClick={(event) => event.stopPropagation()}>
    <header><div><h2>节点路由 — {node.node_name}</h2><p>服务器: {server.name} | 入站: {inboundTag}</p></div><button type="button" disabled={busy} onClick={onClose} aria-label="关闭"><X /></button></header>
    <div className="node-dialog-body">
      {error && <div role="alert"><p className="node-error">{error}</p><button type="button" disabled={busy || loading} onClick={() => { setError(""); setLoading(true); void load().catch((reason) => setError(reason instanceof Error ? reason.message : "加载路由配置失败")).finally(() => setLoading(false)); }}><RefreshCw />重试</button></div>}
      {loading ? <div className="node-empty"><Loader2 className="spin" />加载路由配置...</div> : <>
        <section className="node-subpanel"><h3>专属路由规则 ({split.dedicatedRules.length})</h3><p>针对此入站</p><div className="node-tool-list">{split.dedicatedRules.map(renderRule)}</div>{!split.dedicatedRules.length && <p>无专属规则，流量将按全局规则处理</p>}</section>
        {split.catchAll && <p className="node-error">全部流量已被路由到 {String(split.catchAll.rule.balancerTag || split.catchAll.rule.outboundTag || "未设置")}，后续全局规则和默认出站不再生效</p>}
        <section className="node-subpanel"><h3>全局路由规则 ({split.globalRules.length})</h3><p>对所有入站生效</p><div className="node-tool-list">{split.globalRules.map(renderRule)}</div>{!split.globalRules.length && <p>无全局规则</p>}</section>
        <section className="node-subpanel"><h3>默认出站</h3><p>无规则匹配时</p><strong>{outbounds[0] ? `${outbounds[0].tag || "(无tag)"} (${outbounds[0].protocol || ""})` : "无出站配置"}</strong></section>
        <section className="node-subpanel"><h3>快捷添加</h3><div className="node-dialog-actions" style={{ flexWrap: "wrap" }}>{nodeRoutingQuickRules.map((item) => <button key={String(item.rule.marktag)} type="button" disabled={busy || !inboundTag} onClick={() => { const rule = { ...item.rule, inboundTag: [inboundTag] }; if (item.needOutbound) { setQuick(rule); setTarget(""); setCustom(false); } else void mutate({ action: "add_rule", rule }); }}>{item.name}</button>)}</div></section>
        <div className="node-dialog-actions"><button type="button" disabled={busy || !inboundTag} onClick={() => { setCustom(!custom); setQuick(null); setTarget(""); }}><Plus />自定义规则</button></div>
        {quick && <section className="node-subpanel"><h3>选择出站</h3><div className="node-form-grid"><label className="wide"><span>出站</span><select value={target} onChange={(event) => setTarget(event.target.value)}>{targetOptions}</select></label></div><div className="node-dialog-actions"><button type="button" disabled={busy} onClick={() => setQuick(null)}>取消</button><button className="primary" type="button" disabled={busy || !target} onClick={() => void mutate({ action: "add_rule", rule: withRoutingTarget(quick, target) })}>添加</button></div></section>}
        {custom && <section className="node-subpanel"><h3>添加自定义规则</h3><p>为入站 {inboundTag} 添加路由规则</p><div className="node-form-grid">
          <label><span>作用范围</span><select value={scope} onChange={(event) => setScope(event.target.value)}><option value="dedicated">仅此入站 ({inboundTag})</option><option value="global">全局 (所有入站)</option></select></label>
          <label><span>规则类型</span><select value={kind} onChange={(event) => setKind(event.target.value)}><option value="domain">域名 (domain)</option><option value="ip">IP 地址 (ip)</option><option value="protocol">协议 (protocol)</option></select></label>
          <label className="wide"><span>匹配条件</span><input value={match} onChange={(event) => setMatch(event.target.value)} placeholder="多个条件用逗号分隔" /></label>
          <label><span>出站</span><select value={target} onChange={(event) => setTarget(event.target.value)}>{targetOptions}</select></label>
          <label><span>标记 (可选)</span><input value={mark} onChange={(event) => setMark(event.target.value)} placeholder="规则标记" /></label>
        </div><div className="node-dialog-actions"><button type="button" disabled={busy} onClick={() => setCustom(false)}>取消</button><button className="primary" type="button" disabled={busy || !target || !match.split(",").some((value) => value.trim())} onClick={() => void mutate({ action: "add_rule", rule: withRoutingTarget({ type: "field", [kind]: match.split(",").map((value) => value.trim()).filter(Boolean), ...(mark.trim() ? { marktag: mark.trim() } : {}), ...(scope === "dedicated" ? { inboundTag: [inboundTag] } : {}) }, target) })}>添加</button></div></section>}
      </>}
    </div>
  </section></div>;
}
