import React, { useEffect, useRef, useState } from "react";
import { componentUpgradeLabel, deriveUpgradeProgress, upgradeDuration, upgradeSteps, type UpgradeProgress } from "./upgrade-progress-state";

export function UpgradeProgressView({ progress, reportedAt }: { progress?: UpgradeProgress; reportedAt?: string }) {
  const [now, setNow] = useState(Date.now);
  const view = deriveUpgradeProgress(progress, reportedAt, now);
  const [open, setOpen] = useState(view.running);
  const logBox = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!progress) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [progress?.started_at, progress?.component]);
  useEffect(() => { setOpen(view.running); }, [progress?.started_at, progress?.component]);
  const newest = view.log[view.log.length - 1];
  useEffect(() => {
    if (open && logBox.current) logBox.current.scrollTop = logBox.current.scrollHeight;
  }, [open, newest?.at, newest?.text]);
  if (!progress || !view.visible) return null;
  const ageText = (age: number | null) => age === null ? "未知" : `${age} 秒前`;
  const bytes = (value?: number) => ((value || 0) / 1024 / 1024).toFixed(1);
  return (
    <section className={`upgrade-progress${view.failed ? " is-failed" : ""}`} aria-label={`${progress.component === "helper" ? "Helper" : "Core"} 升级进度`}>
      <ol className="upgrade-steps" aria-label="升级步骤">
        {upgradeSteps.map((step, index) => <li key={step} className={`${index < view.step ? "is-done" : ""}${index === view.step ? " is-current" : ""}`} aria-current={index === view.step ? "step" : undefined}><i aria-hidden="true">{index < view.step ? "✓" : index + 1}</i><span>{step}</span></li>)}
      </ol>
      {view.failed && <p className="upgrade-failure">{componentUpgradeLabel(progress.phase)}{!progress.log?.some((line) => !["success", "failed", "rolled_back"].includes(line.phase)) ? " · 失败阶段未上报" : ""}</p>}
      <div className={`upgrade-meter${view.running && view.percent === null ? " is-indeterminate" : ""}`} role="progressbar" aria-label="升级进度" aria-valuemin={0} aria-valuemax={100} aria-valuenow={view.percent ?? (progress.phase === "success" ? 100 : undefined)}>
        <span style={{ width: view.percent !== null ? `${view.percent}%` : progress.phase === "success" ? "100%" : undefined }} />
      </div>
      {view.percent !== null && <p className="upgrade-download">{view.percent}%{progress.bytes_total ? ` · ${bytes(progress.bytes_done)}/${bytes(progress.bytes_total)} MB` : ""}</p>}
      <p className="upgrade-timing"><span>{view.approximateStart ? "本阶段已用时" : "已用时"} {upgradeDuration(view.elapsed)}</span><span>最后更新 {ageText(view.lastAge)}</span></p>
      {view.stalled && <p className="upgrade-stall" role="status">超过 {view.threshold} 秒没有新进度，可能卡住了；Helper 最后上报 {ageText(view.helperAge)}。{view.offline ? "Helper 离线，等待恢复上报。" : "Helper 仍在上报，升级步骤尚未更新。"}</p>}
      <details className="upgrade-log" open={open} onToggle={(event) => setOpen(event.currentTarget.open)}>
        <summary>步骤日志 <span>{view.log.length} 条</span></summary>
        <div ref={logBox} className="upgrade-log-lines" tabIndex={0} aria-label="升级步骤日志">
          {view.log.map((line, index) => <div key={`${line.at}-${index}`} className={line.phase === "failed" || line.phase === "rolled_back" ? "is-error" : ""}><time>{Number.isFinite(Date.parse(line.at)) ? new Date(line.at).toLocaleTimeString("en-GB", { hour12: false }) : "--:--:--"}</time><span>{line.text}</span></div>)}
        </div>
      </details>
    </section>
  );
}
