import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Copy, Gauge, History, Loader2, Play, RefreshCw, Trash2, X } from "lucide-react";
import { createSpeedTester, fetchSpeedTesterUpdateInfo, fetchSpeedTesters, fetchSpeedTestResults, revokeSpeedTester, rotateSpeedTesterToken, runSpeedTest, updateAllSpeedTesters } from "./api";
import type { SpeedTester, SpeedTesterUpdate, SpeedTestResult, XrayNode } from "./types";
import { filterSpeedTestNodes, sortSpeedTestResults, SPEED_TEST_PRO_REQUIRED, speedTestError, speedTestLatest, speedTestLatency, speedTestNodeTags, speedTestState, speedTesterCommands, toggleVisibleSpeedTestNodes } from "./node-speedtest-logic";

type Notice = (tone: "success" | "error" | "info", text: string) => void;
type TestOptions = { tester_id?: number; threads?: number; buf_size?: number };
const threadOptions = [1, 8, 16, 32, 64];
const bufferOptions = [1, 4, 8, 16];

function savedNumber(key: string, allowed: number[], fallback: number) {
  try { const value = Number(localStorage.getItem(key)); return allowed.includes(value) ? value : fallback; } catch { return fallback; }
}

function savedSource() {
  try { return localStorage.getItem("mmwx-speedtest-source") || "master"; } catch { return "master"; }
}

function defaultOptions(): TestOptions {
  const source = savedSource();
  return { threads: savedNumber("mmwx-speedtest-threads", threadOptions, 1), buf_size: savedNumber("mmwx-speedtest-bufsize", bufferOptions, 1) * 1024 * 1024, ...(source !== "master" && Number(source) > 0 ? { tester_id: Number(source) } : {}) };
}

export function useNodeSpeedTests(token: string, onNotice: Notice, enabled = true) {
  const [latest, setLatest] = useState<Map<number, SpeedTestResult>>(() => new Map());
  const [error, setError] = useState("");
  const [now, setNow] = useState(Date.now());
  const attempts = useRef(new Map<number, number>());
  const optimistic = useRef(new Map<number, SpeedTestResult>());
  const refreshSequence = useRef(0);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const refresh = useCallback(async () => {
    const sequence = ++refreshSequence.current;
    let response;
    try { response = await fetchSpeedTestResults(token, undefined, true); }
    catch (reason) { if (mounted.current && sequence === refreshSequence.current) throw reason; return; }
    if (!mounted.current || sequence !== refreshSequence.current) return;
    const next = speedTestLatest(response.results ?? []);
    for (const [id, pending] of optimistic.current) {
      const received = next.get(id);
      const receivedAt = new Date(received?.created_at || 0).getTime();
      const pendingAt = new Date(pending.created_at || 0).getTime();
      if (!received || receivedAt < pendingAt - 1000) next.set(id, pending);
      else optimistic.current.delete(id);
    }
    setLatest(next); setError(""); setNow(Date.now());
  }, [token]);
  useEffect(() => {
    if (!enabled) return;
    optimistic.current.clear(); attempts.current.clear(); setLatest(new Map()); setError("");
    void refresh().catch((reason) => { if (mounted.current) setError(speedTestError(reason)); });
  }, [enabled, refresh]);
  const running = [...latest.values()].some((result) => result.status === "running");
  useEffect(() => {
    if (!enabled || !running || error === SPEED_TEST_PRO_REQUIRED) return;
    let pending = false;
    const timer = window.setInterval(() => {
      setNow(Date.now());
      if (pending) return;
      pending = true;
      void refresh().catch((reason) => { if (mounted.current) setError(speedTestError(reason)); }).finally(() => { pending = false; });
    }, 1500);
    return () => window.clearInterval(timer);
  }, [enabled, error, refresh, running]);
  const start = async (nodes: XrayNode[], latencyOnly = false, options = defaultOptions()) => {
    if (!nodes.length) return;
    if (error === SPEED_TEST_PRO_REQUIRED) { onNotice("error", error); return; }
    refreshSequence.current++;
    const started = Date.now();
    const entries = nodes.map((node) => {
      const result: SpeedTestResult = { node_id: node.id, node_name: node.node_name, status: "running", created_at: new Date(started).toISOString() };
      optimistic.current.set(node.id, result); attempts.current.set(node.id, started); return [node.id, result] as const;
    });
    setLatest((current) => new Map([...current, ...entries])); setNow(started);
    const responses = await Promise.allSettled(nodes.map(async (node) => {
      try {
        const response = await runSpeedTest(token, { node_id: node.id, ...options, ...(latencyOnly ? { latency_only: true } : {}) });
        if (mounted.current && response.result && attempts.current.get(node.id) === started) {
          refreshSequence.current++;
          optimistic.current.delete(node.id);
          setLatest((current) => new Map(current).set(node.id, response.result!));
        }
      } catch (reason) {
        const message = speedTestError(reason);
        if (mounted.current && attempts.current.get(node.id) === started) {
          refreshSequence.current++;
          optimistic.current.delete(node.id);
          setLatest((current) => new Map(current).set(node.id, { node_id: node.id, status: "failed", error: message, created_at: new Date(started).toISOString() }));
          if (message === SPEED_TEST_PRO_REQUIRED) setError(message);
        }
        throw reason;
      }
    }));
    if (!mounted.current) return;
    const failed = responses.find((result) => result.status === "rejected");
    if (failed?.status === "rejected") onNotice("error", speedTestError(failed.reason));
    const count = responses.filter((result) => result.status === "fulfilled").length;
    if (count) {
      onNotice("success", nodes.length === 1 ? latencyOnly ? `已开始测试 ${nodes[0].node_name} 的延迟` : `已开始测速 ${nodes[0].node_name},结果稍后在节点行显示` : latencyOnly ? `已开始测试 ${count} 个节点的延迟,结果将陆续显示` : `已开始测速 ${count} 个节点,结果将陆续显示`);
      void refresh().catch((reason) => { if (mounted.current) setError(speedTestError(reason)); });
    }
  };
  return { latest, now, error, start, refresh };
}

export type NodeSpeedTestController = ReturnType<typeof useNodeSpeedTests>;

function SpeedResult({ result, now }: { result?: SpeedTestResult; now: number }) {
  const state = speedTestState(result, now);
  if (state === "running") return <span><Loader2 className="spin" /> 测速中</span>;
  if (state === "timeout") return <span title="15 秒未返回结果,点击重测">超时</span>;
  if (state === "failed") return <span title={result?.error || "连接延迟测试失败"}>失败</span>;
  return <span>{state === "ok" ? `↓ ${Number(result?.down_mbps || 0).toFixed(1)} Mbps` : "—"}</span>;
}

export function NodeSpeedTestActions({ node, controller, onHistory, options }: { node: XrayNode; controller: NodeSpeedTestController; onHistory: () => void; options?: TestOptions }) {
  const result = controller.latest.get(node.id);
  const state = speedTestState(result, controller.now);
  const busy = state === "running";
  const title = state === "timeout" ? "15 秒未返回结果,点击重测" : state === "failed" ? result?.error || "连接延迟测试失败" : "点击重新测速";
  const latency = state === "timeout" ? "超时" : state === "running" || state === "idle" ? "测延迟" : speedTestLatency(result);
  return <div className="node-action-row" onClick={(event) => event.stopPropagation()}>
    <button type="button" disabled={busy} title={title} onClick={() => void controller.start([node], false, options)}><Gauge />{state === "idle" ? "测速" : <SpeedResult result={result} now={controller.now} />}</button>
    <button type="button" disabled={busy} title={state === "timeout" ? title : "只测真连接延迟(Cloudflare 204 多采样)"} onClick={() => void controller.start([node], true, options)}>{busy ? <Loader2 className="spin" /> : <Play />}{latency}</button>
    <button type="button" title="测速历史" onClick={onHistory}><History />测速历史</button>
  </div>;
}

function SpeedDialog({ title, subtitle, onClose, children }: { title: string; subtitle: string; onClose: () => void; children: React.ReactNode }) {
  return <div className="node-dialog-layer" role="presentation" onClick={onClose}><section className="node-dialog node-tool-dialog" role="dialog" aria-modal="true" aria-label={title} onClick={(event) => event.stopPropagation()}><header><div><h2>{title}</h2><p>{subtitle}</p></div><button type="button" onClick={onClose} aria-label="关闭"><X /></button></header><div className="node-dialog-body">{children}</div></section></div>;
}

export function SpeedTestHistoryDialog({ token, nodes, node, onClose }: { token: string; nodes: XrayNode[]; node?: XrayNode; onClose: () => void; onNotice?: Notice }) {
  const [results, setResults] = useState<SpeedTestResult[]>([]);
  const [sort, setSort] = useState<"time" | "speed" | "latency">("time");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [now, setNow] = useState(Date.now());
  const load = useCallback(async () => {
    setLoading(true);
    try { const response = await fetchSpeedTestResults(token, node?.id, false, 100); setResults(response.results ?? []); setError(""); setNow(Date.now()); }
    catch (reason) { setError(speedTestError(reason)); }
    finally { setLoading(false); }
  }, [node?.id, token]);
  useEffect(() => { void load(); }, [load]);
  const running = results.some((result) => result.status === "running");
  useEffect(() => {
    if (!running || loading || error) return;
    const timer = window.setTimeout(() => void load(), 4000);
    return () => window.clearTimeout(timer);
  }, [error, load, loading, running]);
  const sorted = useMemo(() => sortSpeedTestResults(results, sort), [results, sort]);
  const names = useMemo(() => new Map(nodes.map((item) => [item.id, item.node_name])), [nodes]);
  return <SpeedDialog title={node ? `测速历史 · ${node.node_name}` : "测速结果"} subtitle="结果保存在服务端,刷新或切页后仍可查看;测速进行中会自动刷新。" onClose={onClose}>
    <div className="node-dialog-actions"><select aria-label="测速历史排序" value={sort} onChange={(event) => setSort(event.target.value as typeof sort)}><option value="time">按时间</option><option value="speed">按速度</option><option value="latency">按延迟</option></select><button type="button" disabled={loading} onClick={() => void load()}><RefreshCw className={loading ? "spin" : ""} />刷新</button></div>
    {error ? <p role="alert">{error}</p> : !results.length ? <div className="node-empty">{loading ? "正在读取" : "暂无测速记录"}</div> : <div style={{ overflowX: "auto" }}><table className="node-speedtest-table"><thead><tr>{!node && <th>节点</th>}<th>下行速度</th><th>延迟</th><th>出口 IP</th><th>来源</th><th>时间</th></tr></thead><tbody>{sorted.map((result, index) => <tr key={result.id ?? index}>{!node && <td>{result.node_name || names.get(result.node_id || 0) || `#${result.node_id}`}</td>}<td><SpeedResult result={result} now={now} /></td><td>{speedTestLatency(result)}</td><td>{result.egress_ip || "—"}</td><td>{result.source === "home_tester" ? "家用" : "主控"}</td><td>{result.created_at ? new Date(result.created_at).toLocaleString() : "—"}</td></tr>)}</tbody></table></div>}
  </SpeedDialog>;
}

export function SpeedTestPanel({ token, nodes, onNotice, controller: shared }: { token: string; nodes: XrayNode[]; onNotice: Notice; controller?: NodeSpeedTestController }) {
  const local = useNodeSpeedTests(token, onNotice, !shared);
  const controller = shared ?? local;
  const [testers, setTesters] = useState<SpeedTester[]>([]);
  const [tester, setTester] = useState(savedSource);
  const [threads, setThreads] = useState(() => savedNumber("mmwx-speedtest-threads", threadOptions, 1));
  const [buffer, setBuffer] = useState(() => savedNumber("mmwx-speedtest-bufsize", bufferOptions, 1));
  const [selected, setSelected] = useState<Set<number>>(() => new Set());
  const [protocols, setProtocols] = useState<Set<string>>(() => new Set());
  const [tags, setTags] = useState<Set<string>>(() => new Set());
  const [error, setError] = useState("");
  const [history, setHistory] = useState<XrayNode | "all" | null>(null);
  const [manager, setManager] = useState<{ autoRotateId?: number } | null>(null);
  const loadTesters = useCallback(() => fetchSpeedTesters(token).then((response) => { setTesters(response.testers ?? []); setError(""); }).catch((reason) => setError(speedTestError(reason))), [token]);
  useEffect(() => { void loadTesters(); }, [loadTesters]);
  useEffect(() => {
    try { localStorage.setItem("mmwx-speedtest-source", tester); localStorage.setItem("mmwx-speedtest-threads", String(threads)); localStorage.setItem("mmwx-speedtest-bufsize", String(buffer)); } catch { /* Storage may be unavailable in private browser sessions. */ }
  }, [buffer, tester, threads]);
  const visible = useMemo(() => filterSpeedTestNodes(nodes, protocols, tags), [nodes, protocols, tags]);
  const allProtocols = useMemo(() => [...new Set(nodes.map((node) => node.protocol || ""))].sort(), [nodes]);
  const allTags = useMemo(() => [...new Set(nodes.flatMap(speedTestNodeTags))].sort(), [nodes]);
  const allSelected = visible.length > 0 && visible.every((node) => selected.has(node.id));
  const options = { threads, buf_size: buffer * 1024 * 1024, ...(tester === "master" ? {} : { tester_id: Number(tester) }) };
  const toggle = (values: Set<string>, value: string) => { const next = new Set(values); next.has(value) ? next.delete(value) : next.add(value); return next; };
  const selectedNodes = nodes.filter((node) => selected.has(node.id));
  const busy = selectedNodes.some((node) => speedTestState(controller.latest.get(node.id), controller.now) === "running");
  return <>
    {(controller.error || error) && <p role="alert">{controller.error || error}</p>}
    <div className="node-form-grid"><label><span>测速来源</span><select value={tester} onChange={(event) => { const value = event.target.value; const item = testers.find((entry) => String(entry.id) === value); if (item?.online === false) setManager({ autoRotateId: item.id }); else setTester(value); }}><option value="master">主控</option>{testers.map((item) => <option key={item.id} value={item.id}>{item.name}{item.online ? "" : "（离线，点击重装）"}</option>)}</select></label><label><span>线程</span><select value={threads} onChange={(event) => setThreads(Number(event.target.value))}>{threadOptions.map((value) => <option key={value} value={value}>{value === 1 ? "单线程" : value}</option>)}</select></label><label title="每次收发的包大小;多线程比加大包更能跑满带宽"><span>包大小</span><select value={buffer} onChange={(event) => setBuffer(Number(event.target.value))}>{bufferOptions.map((value) => <option key={value} value={value}>{value}M</option>)}</select></label></div>
    <div className="node-dialog-actions"><button type="button" onClick={() => setHistory("all")}><History />测速结果</button><button type="button" onClick={() => setManager({})}>管理测速端</button></div>
    <div className="node-dialog-actions"><span>按协议筛选</span>{allProtocols.map((value) => <button type="button" key={value} aria-pressed={protocols.has(value)} className={protocols.has(value) ? "primary" : ""} onClick={() => setProtocols(toggle(protocols, value))}>{value.toUpperCase() || "NODE"}</button>)}{protocols.size > 0 && <button type="button" onClick={() => setProtocols(new Set())}>清除</button>}</div>
    {allTags.length > 0 && <details className="node-tag-filter"><summary>按标签筛选{tags.size ? `（${tags.size}）` : ""}</summary>{allTags.map((value) => <label className="node-check" key={value}><input type="checkbox" checked={tags.has(value)} onChange={() => setTags(toggle(tags, value))} />{value}</label>)}{tags.size > 0 && <button type="button" onClick={() => setTags(new Set())}>清除</button>}</details>}
    <div className="node-dialog-actions"><span>可见 {visible.length} / {nodes.length} 条</span><button type="button" onClick={() => setSelected(toggleVisibleSpeedTestNodes(selected, visible.map((node) => node.id)))}>{allSelected ? "取消全选" : "全选可见"}</button>{selected.size > 0 && <button type="button" onClick={() => setSelected(new Set())}>清空选择（{selected.size}）</button>}<button type="button" disabled={busy || !selected.size} onClick={() => void controller.start(selectedNodes, true, options)}><Play />批量测试延迟</button><button type="button" className="primary" disabled={busy || !selected.size} onClick={() => void controller.start(selectedNodes, false, options)}><Gauge />批量测速</button></div>
    {!nodes.length ? <div className="node-empty">暂无可测速的节点</div> : <div className="node-tool-list">{visible.map((node) => <article key={node.id}><div><label className="node-check"><input type="checkbox" checked={selected.has(node.id)} onChange={() => setSelected((current) => { const next = new Set(current); next.has(node.id) ? next.delete(node.id) : next.add(node.id); return next; })} /><strong>{node.node_name}</strong></label><p>{node.protocol?.toUpperCase()} · 出口 IP：{controller.latest.get(node.id)?.egress_ip || "—"}</p><NodeSpeedTestActions node={node} controller={controller} options={options} onHistory={() => setHistory(node)} /></div></article>)}</div>}
    {history && <SpeedTestHistoryDialog token={token} nodes={nodes} node={history === "all" ? undefined : history} onClose={() => setHistory(null)} />}
    {manager && <SpeedTesterManagerDialog token={token} autoRotateId={manager.autoRotateId} onNotice={onNotice} onClose={() => { setManager(null); void loadTesters(); }} />}
  </>;
}

const updateStatus: Record<string, string> = { pending: "等待更新", success: "更新成功", failed: "更新失败", offline: "离线，已跳过", unsupported: "需手动升级", latest: "已是最新" };

export function SpeedTesterManagerDialog({ token, onClose, onNotice, autoRotateId }: { token: string; onClose: () => void; onNotice: Notice; autoRotateId?: number }) {
  const [testers, setTesters] = useState<SpeedTester[]>([]);
  const [info, setInfo] = useState<{ has_update?: boolean; latest_version?: string; outdated_count?: number; testers?: SpeedTesterUpdate[] }>({});
  const [name, setName] = useState("");
  const [credential, setCredential] = useState<{ token: string; name: string } | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [updateOpen, setUpdateOpen] = useState(false);
  const [updates, setUpdates] = useState<SpeedTesterUpdate[] | null>(null);
  const rotated = useRef<number | undefined>(undefined);
  const load = useCallback(async () => { const response = await fetchSpeedTesters(token); setTesters(response.testers ?? []); }, [token]);
  const loadInfo = useCallback(async () => { setInfo(await fetchSpeedTesterUpdateInfo(token)); }, [token]);
  useEffect(() => {
    let fetching = false;
    const refresh = async () => { if (fetching) return; fetching = true; try { await load(); } catch (reason) { setError(speedTestError(reason)); } finally { fetching = false; } };
    void refresh(); const timer = window.setInterval(() => void refresh(), 5000); return () => window.clearInterval(timer);
  }, [load]);
  useEffect(() => {
    if (autoRotateId) return;
    let fetching = false;
    const refresh = async () => { if (fetching) return; fetching = true; try { await loadInfo(); } catch (reason) { setError(speedTestError(reason)); } finally { fetching = false; } };
    void refresh(); const timer = window.setInterval(() => void refresh(), 60_000); return () => window.clearInterval(timer);
  }, [autoRotateId, loadInfo]);
  const mutate = async (kind: string, action: () => Promise<void>) => {
    setBusy(kind); setError("");
    try { await action(); await load(); } catch (reason) { const message = speedTestError(reason); setError(message); onNotice("error", message); } finally { setBusy(""); }
  };
  const rotate = (tester: SpeedTester) => mutate("rotate", async () => {
    const response = await rotateSpeedTesterToken(token, tester.id); setCredential({ token: response.token, name: tester.name || `tester-${tester.id}` }); onNotice("success", "已生成新令牌,请重新部署测速端");
  });
  useEffect(() => {
    const tester = testers.find((item) => item.id === autoRotateId);
    if (!tester || !autoRotateId || rotated.current === autoRotateId) return;
    rotated.current = autoRotateId;
    if (!tester.online) void rotate(tester);
  }, [autoRotateId, testers]);
  const copy = async (value: string) => { try { await navigator.clipboard.writeText(value); onNotice("success", "已复制"); } catch { onNotice("error", "复制失败"); } };
  const rows = updates ?? info.testers ?? [];
  return <SpeedDialog title="管理测速端" subtitle="家用测速端部署在你家里的服务器/电脑,反向连入主控,从家庭网络视角测节点速度。" onClose={onClose}>
    {error && <p role="alert">{error}</p>}
    <a href="https://github.com/mmwx-group/mmwX-plugins/releases/latest" target="_blank" rel="noreferrer">在此下载测速端程序</a>
    {!autoRotateId && <><div className="node-form-grid"><label><span>名称</span><input value={name} onChange={(event) => setName(event.target.value)} placeholder="mmwx-speedtester" /></label></div><div className="node-dialog-actions"><button type="button" disabled={!!busy} onClick={() => void mutate("create", async () => { const testerName = name.trim() || "mmwx-speedtester"; const response = await createSpeedTester(token, testerName); setCredential({ token: response.token, name: testerName }); setName(""); onNotice("success", "已创建,请复制令牌(仅显示一次)"); })}>新建</button>{info.has_update && <button type="button" disabled={!!busy} onClick={() => { setUpdates(null); setUpdateOpen(true); }}>一键更新（{info.outdated_count || 0}）</button>}</div></>}
    {credential && <div className="node-form-grid"><label className="wide"><span>配对令牌(仅显示一次,请立即复制) · {credential.name}</span><div className="node-inline-input"><input readOnly value={credential.token} /><button type="button" aria-label="复制配对令牌" onClick={() => void copy(credential.token)}><Copy /></button></div></label>{speedTesterCommands(window.location.origin, credential.token, credential.name).map((item) => <label className="wide" key={item.label}><span>{item.label}</span><div className="node-inline-input"><input readOnly value={item.command} /><button type="button" aria-label={`复制${item.label}`} onClick={() => void copy(item.command)}><Copy /></button></div></label>)}<p className="wide">复制命令到家里的服务器/电脑终端执行,自动下载对应平台测速端并反向连入主控。</p></div>}
    {!autoRotateId && <><h3>已配对测速端</h3><div className="node-tool-list">{!testers.length && <p>暂无测速端。</p>}{testers.map((tester) => <article key={tester.id}><div><strong>{tester.name || `#${tester.id}`}</strong><p>{tester.online ? "在线" : "离线"}{tester.version ? ` · v${tester.version.replace(/^v/, "")}` : ""}</p></div><div className="node-action-row">{!tester.online && <button type="button" disabled={!!busy} title="重新生成令牌并展示安装命令(原令牌立即失效)" onClick={() => void rotate(tester)}><RefreshCw />重装</button>}<button type="button" disabled={!!busy} title="吊销" aria-label={`吊销 ${tester.name}`} onClick={() => void mutate("revoke", async () => { await revokeSpeedTester(token, tester.id); setCredential(null); onNotice("success", "已吊销"); })}><Trash2 /></button></div></article>)}</div></>}
    {updateOpen && <section><h3>更新所有测速端</h3><p>将 {info.outdated_count || 0} 个测速端更新到 v{info.latest_version?.replace(/^v/, "") || "-"}。离线或不支持远程更新的旧版测速端会跳过。</p><div className="node-tool-list">{rows.map((tester) => { const status = updates ? tester.status || "failed" : tester.update_available ? tester.online ? "pending" : "offline" : tester.update_supported ? "latest" : "unsupported"; return <article key={tester.id}><div><strong>{tester.name || `#${tester.id}`}</strong>{tester.error && <p>{tester.error}</p>}</div><span>{busy === "update" && status === "pending" ? "更新中" : updateStatus[status] || status}</span></article>; })}</div><div className="node-dialog-actions"><button type="button" disabled={busy === "update"} onClick={() => setUpdateOpen(false)}>关闭</button>{!updates && <button type="button" disabled={!!busy} onClick={() => void mutate("update", async () => { const response = await updateAllSpeedTesters(token); const results = response.results ?? []; setUpdates(results); const failed = results.filter((item) => item.status === "failed").length; onNotice(failed ? "error" : "success", failed ? `有 ${failed} 个测速端更新失败` : "测速端更新完成"); await loadInfo(); })}>{busy === "update" ? "更新中" : "开始更新"}</button>}</div></section>}
  </SpeedDialog>;
}
