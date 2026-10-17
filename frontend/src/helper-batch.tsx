import React, { useCallback, useEffect, useRef, useState } from "react";
import { changeHelperBatch, fetchHelperBatch } from "./api";
import type { HelperBatchJob, HelperBatchResponse, HelperBatchRow } from "./types";
import { helperBatchCounts, showHelperBatchButton } from "./helper-batch-state";
import { componentUpgradeLabel, upgradeDismissKey, upgradeDuration } from "./upgrade-progress-state";
import { useUpgradePresentation } from "./upgrade-progress";

function BatchRows({ rows }: { rows: HelperBatchRow[] }) {
  return <div className="helper-batch-rows">{rows.map((row) => <div key={row.server_id} className={`helper-batch-row${["failed", "rolled_back"].includes(row.phase) ? " is-failed" : ["skipped", "cancelled"].includes(row.phase) ? " is-muted" : ""}`}>
    <div><strong>{row.name}</strong><small>{row.reason || `${row.version || "版本未知"}${row.first_upgrade ? " · 首次升级" : ""}`}</small></div>
    <span>{({ queued: "排队中", skipped: "跳过", cancelled: "已停止" } as Record<string, string>)[row.phase] || componentUpgradeLabel(row.phase)}</span>
  </div>)}</div>;
}

export function HelperBatchPanel({ job, busy, onStop }: { job: HelperBatchJob; busy: boolean; onStop: () => void }) {
  const running = job.phase === "running";
  const { now, dismissed, dismiss } = useUpgradePresentation(upgradeDismissKey("all", "helper-batch", job.started_at), running, job.finished_at, job.phase === "success");
  const counts = helperBatchCounts(job);
  if (dismissed) return null;
  const finished = Date.parse(job.finished_at || job.updated_at);
  const elapsed = Math.max(0, Math.floor(((running ? now : finished) - Date.parse(job.started_at)) / 1000));
  const collapsed = job.phase === "success" && now - finished >= 60_000;
  return <section className={`helper-batch-panel upgrade-progress${job.phase === "failed" ? " is-failed" : ""}`} aria-label="批量 Helper 升级进度">
    <div className="helper-batch-head"><strong>{running ? "批量升级 Helper" : job.stopped ? "批量升级已停止" : job.phase === "failed" ? "批量升级完成，有失败" : "批量升级成功"} · 已完成 {counts.done}/{counts.total} · 失败 {counts.failed}{counts.cancelled ? ` · 已停止 ${counts.cancelled}` : ""}</strong>
      {running ? <button type="button" onClick={onStop} disabled={busy || job.stopped || !job.rows.some((row) => row.phase === "queued")}>{job.stopped ? "已停止剩余" : "停止剩余"}</button> : <div className="upgrade-finished"><button type="button" aria-label="关闭升级进度" onClick={dismiss}>×</button></div>}
    </div>
    <p className="upgrade-timing">{running ? "已用时" : "用时"} {upgradeDuration(elapsed)}{!running && ` · 完成于 ${new Date(finished).toLocaleTimeString("en-GB", { timeZone: "Asia/Taipei", hour12: false })}`}</p>
    {!collapsed && <><div className="upgrade-meter" role="progressbar" aria-label="批量升级进度" aria-valuemin={0} aria-valuemax={100} aria-valuenow={counts.percent}><span style={{ width: `${counts.percent}%`, background: "#168061" }} /></div><BatchRows rows={job.rows} /></>}
  </section>;
}

export function HelperBatchUpgrade({ token }: { token: string }) {
  const [data, setData] = useState<HelperBatchResponse | null>(null);
  const [dialog, setDialog] = useState<HelperBatchResponse | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submitting = useRef(false);
  const generation = useRef(0);
  const load = useCallback(async () => {
    const current = generation.current;
    try { const response = await fetchHelperBatch(token); if (current === generation.current) setData(response); }
    catch (reason) { if (current === generation.current) setError(reason instanceof Error ? reason.message : "读取批量升级状态失败"); }
  }, [token]);
  useEffect(() => { void load(); const timer = window.setInterval(() => void load(), 5000); return () => window.clearInterval(timer); }, [load]);
  async function confirm() {
    if (submitting.current || data?.job?.phase === "running") return;
    submitting.current = true; setBusy(true); setError("");
    try { const response = await fetchHelperBatch(token); setData(response); if (response.job?.phase !== "running") setDialog(response); }
    catch (reason) { setError(reason instanceof Error ? reason.message : "读取升级列表失败"); }
    finally { submitting.current = false; setBusy(false); }
  }
  async function change(action: "start" | "stop") {
    if (submitting.current) return;
    submitting.current = true; generation.current++; setBusy(true); setError("");
    try {
      const response = await changeHelperBatch(token, action === "start"
        ? { action, version: dialog?.version, server_ids: dialog?.preview.filter((row) => row.phase === "queued").map((row) => row.server_id) }
        : { action, job_id: data?.job?.id });
      generation.current++; setData(response); setDialog(null);
    } catch (reason) { setError(reason instanceof Error ? reason.message : "批量升级操作失败"); await load(); }
    finally { submitting.current = false; setBusy(false); }
  }
  const eligible = dialog?.preview.filter((row) => row.phase === "queued") || [];
  const skipped = dialog?.preview.filter((row) => row.phase === "skipped") || [];
  return <section className="helper-batch" aria-label="Helper 批量升级">
    {showHelperBatchButton(data) && <button type="button" className="service-primary-button" disabled={busy || data?.job?.phase === "running"} onClick={() => void confirm()}>{data?.job?.phase === "running" ? "批量升级进行中" : "一键升级全部 Helper"}</button>}
    {error && <div className="agent-notice error">{error}</div>}
    {data?.job && <HelperBatchPanel job={data.job} busy={busy} onStop={() => void change("stop")} />}
    {dialog && <div className="traffic-list-layer" role="presentation" onClick={() => !busy && setDialog(null)}><section className="traffic-list-sheet helper-batch-dialog" role="dialog" aria-modal="true" aria-label="一键升级全部 Helper" onClick={(event) => event.stopPropagation()}>
      <header className="traffic-list-dialog-head"><div><h2>一键升级全部 Helper</h2><p>升级至 {dialog.version} · 同时最多 2 台</p></div><button type="button" aria-label="关闭" disabled={busy} onClick={() => setDialog(null)}>×</button></header>
      <div className="traffic-list-dialog-body"><p>任务在后台执行，关闭页面后会继续。每台最多等待 5 分钟；失败会继续处理其它服务器。</p><h3>将升级 {eligible.length} 台</h3><BatchRows rows={eligible} /><h3>跳过 {skipped.length} 台</h3><BatchRows rows={skipped} /><button className="service-primary-button helper-batch-confirm" type="button" disabled={busy || !eligible.length} onClick={() => void change("start")}>{busy ? "正在下发…" : `确认升级 ${eligible.length} 台 Helper`}</button></div>
    </section></div>}
  </section>;
}
