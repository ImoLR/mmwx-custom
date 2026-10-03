import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Activity, Loader2, X } from "lucide-react";
import { fetchNodeProbe, fetchSpeedTesters, toggleNodeProbe, updateNodeProbeSettings } from "./api";
import { nodeProbeStatesById, nodeProbeSummary, probeResyncMinutes } from "./node-auxiliary-logic";
import type { NodeProbeSample, NodeProbeSettings, NodeProbeState, NodeProbeStatus } from "./node-auxiliary-logic";
import type { SpeedTester, XrayNode } from "./types";

type ToolNotice = (tone: "success" | "error" | "info", text: string) => void;

export function useNodeProbe(token: string) {
  const [status, setStatus] = useState<NodeProbeStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const pending = useRef<Promise<void> | null>(null);
  const refresh = useCallback(() => {
    if (pending.current) return pending.current;
    const request = fetchNodeProbe(token).then((value) => { setStatus(value); setError(""); }).catch((err) => {
      setError(err instanceof Error ? err.message : "读取探测状态失败");
    }).finally(() => { setLoading(false); pending.current = null; });
    pending.current = request;
    return request;
  }, [token]);
  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => { void refresh(); }, 30000);
    return () => window.clearInterval(timer);
  }, [refresh]);
  const states = useMemo(() => nodeProbeStatesById(status), [status]);
  return { status, states, loading, error, refresh };
}

export function NodeProbeBadge({ node, state, onClick }: { node: XrayNode; state?: NodeProbeState; onClick: () => void }) {
  if (node.original_server || !node.probe_enabled) return null;
  const { last, down, availability, failStreak } = nodeProbeSummary(state);
  const text = !last ? "等待首次探测" : down ? "不可用" : last.ok ? `${last.latency_ms} ms` : "超时";
  const title = [state?.source ? `探测源 ${state.source}` : "", availability === null ? "" : `可用率 ${availability}%`, `连续失败 ${failStreak} 次`].filter(Boolean).join(" · ");
  return <button type="button" className={`node-probe-badge ${down || last?.ok === false ? "bad" : last ? "ok" : ""}`} title={title || "外部节点探测"} onClick={(event) => { event.stopPropagation(); onClick(); }}><Activity size={12} />{text}</button>;
}

function ProbeSamples({ samples }: { samples: NodeProbeSample[] }) {
  const recent = samples.slice(-40);
  const max = Math.max(...recent.map((sample) => sample.ok ? sample.latency_ms : 0), 1);
  return <div className="node-probe-samples" aria-hidden="true">{recent.map((sample, index) => <i key={`${sample.at}-${index}`} className={sample.ok ? "" : "bad"} style={{ height: `${sample.ok ? Math.max(12, sample.latency_ms / max * 100) : 100}%` }} />)}</div>;
}

export function NodeProbeDialog({ token, nodes, status, loading, error, onRefresh, onChanged, onClose, onNotice }: {
  token: string;
  nodes: XrayNode[];
  status: NodeProbeStatus | null;
  loading: boolean;
  error: string;
  onRefresh: () => void | Promise<unknown>;
  onChanged: () => void | Promise<unknown>;
  onClose: () => void;
  onNotice: ToolNotice;
}) {
  const [testers, setTesters] = useState<SpeedTester[]>([]);
  const [testerError, setTesterError] = useState("");
  const [busy, setBusy] = useState(false);
  const [minutes, setMinutes] = useState(String(status?.resync_minutes ?? 0));
  const external = useMemo(() => nodes.filter((node) => !node.original_server), [nodes]);
  const states = useMemo(() => nodeProbeStatesById(status), [status]);
  useEffect(() => { setMinutes(String(status?.resync_minutes ?? 0)); }, [status?.resync_minutes]);
  useEffect(() => {
    let active = true;
    fetchSpeedTesters(token).then((value) => { if (active) setTesters(value.testers ?? []); }).catch((err) => {
      if (active) { const message = err instanceof Error ? err.message : "读取测速端失败"; setTesterError(/PRO|403/.test(message) ? "节点测速是 PRO 功能,请升级许可证" : message); }
    });
    return () => { active = false; };
  }, [token]);
  const save = async (settings: NodeProbeSettings) => {
    setBusy(true);
    try {
      const result = await updateNodeProbeSettings(token, settings);
      if (result.success === false) throw new Error(result.error || "保存失败");
      await onRefresh();
    } catch (err) { onNotice("error", err instanceof Error ? err.message : "保存失败"); }
    finally { setBusy(false); }
  };
  const toggle = async (node: XrayNode, enabled: boolean) => {
    setBusy(true);
    try {
      const result = await toggleNodeProbe(token, { node_id: node.id, enabled });
      if (result.success === false) throw new Error(result.error || "操作失败");
      await Promise.all([onRefresh(), onChanged()]);
    } catch (err) { onNotice("error", err instanceof Error ? err.message : "操作失败"); }
    finally { setBusy(false); }
  };
  const saveMinutes = () => {
    const value = probeResyncMinutes(minutes);
    setMinutes(String(value));
    if (value !== (status?.resync_minutes ?? 0)) void save({ resync_minutes: value });
  };
  return <div className="node-dialog-layer" role="presentation" onClick={() => { if (!busy) onClose(); }}>
    <section className="node-dialog node-tool-dialog" role="dialog" aria-modal="true" aria-label="外部节点探测" onClick={(event) => event.stopPropagation()}>
      <header><div><h2>外部节点探测</h2><p>用 mihomo 走完整协议定时真连一次,测出连通性与真实延迟。只对外部导入的节点生效,每 {Math.round((status?.interval_sec ?? 300) / 60)} 分钟一轮。</p></div><button type="button" disabled={busy} onClick={onClose} aria-label="关闭"><X /></button></header>
      <div className="node-dialog-body">
        {error && <div className="node-danger-panel" role="alert"><p>{error}</p><button type="button" onClick={() => void onRefresh()}>重试</button></div>}
        {loading && <div className="node-empty"><Loader2 className="spin" />正在读取</div>}
        <div className="node-form-grid">
          <label className="wide node-package-option"><input type="checkbox" checked={!!status?.enabled} disabled={!status || loading || busy} onChange={(event) => void save({ enabled: event.target.checked })} /><span>启用定时探测</span></label>
          <p className="wide node-help">关闭后已勾选的节点会保留,只是不再执行探测</p>
          <label className="wide"><span>探测源</span><select value={status?.tester_id ?? 0} disabled={!status || loading || busy} onChange={(event) => void save({ tester_id: Number(event.target.value) })}>
            <option value={0}>主控本机</option>{status?.tester_id && !testers.some((tester) => tester.id === status.tester_id) ? <option value={status.tester_id}>测速端 #{status.tester_id}</option> : null}
            {testers.map((tester) => <option key={tester.id} value={tester.id} title={tester.online ? "" : "该测速端当前离线,探测会自动回退到主控"}>{tester.name}{tester.online ? "" : "(离线)"}</option>)}
          </select></label>
          <p className="wide node-help">选家宽测速端能测出更贴近用户的延迟;测速端不可用时自动回退到主控本机。</p>
          {testerError && <p className="wide node-help" role="status">{testerError}</p>}
          <label className="wide"><span>掉线自动重新同步外部订阅</span><input type="number" min={0} max={1440} value={minutes} onChange={(event) => setMinutes(event.target.value)} onBlur={saveMinutes} disabled={!status || loading || busy} /><small>分钟(0 = 关闭)</small></label>
          <p className="wide node-help">节点连续掉线满设定分钟数后,自动重新拉取该节点所属用户的外部订阅。机场换服务器时订阅里的地址会变,重新同步一次往往就自愈了。同一用户最短 15 分钟才会重同步一次,避免频繁请求机场接口。</p>
        </div>
        <p className="node-help">可探测的外部节点 · 已勾选 {status?.enabled_count ?? 0} / {external.length}</p>
        {!external.length ? <p className="node-help">还没有外部导入的节点。自建服务器上的节点由 agent 心跳监控,不需要也不会在这里出现。</p> : <div className="node-tool-list selectable">{external.map((node) => {
          const state = states.get(node.id);
          const { last, availability, failStreak, down } = nodeProbeSummary(state);
          return <label key={node.id}><input type="checkbox" checked={!!node.probe_enabled} disabled={!status || busy} onChange={(event) => void toggle(node, event.target.checked)} /><div><strong>{node.node_name}</strong><p>{node.protocol} {node.enabled === false ? "· 已禁用" : ""} {down ? "· 不可用" : ""}</p>{node.probe_enabled && <p>{state?.source ? `探测源 ${state.source} · ` : ""}{availability !== null ? `可用率 ${availability}% · ` : ""}连续失败 {failStreak} 次</p>}</div>{node.probe_enabled ? <div><ProbeSamples samples={state?.samples ?? []} /><p>{last ? last.ok ? `${last.latency_ms} ms` : "超时" : "等待首次探测"}</p></div> : <small>未探测</small>}</label>;
        })}</div>}
      </div>
    </section>
  </div>;
}
