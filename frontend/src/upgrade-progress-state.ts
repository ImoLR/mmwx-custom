import type { CustomAgentStatusResponse } from "./types.ts";

export type UpgradeProgress = NonNullable<NonNullable<CustomAgentStatusResponse["status"]>["update"]>;

export const upgradeSteps = ["下发", "下载", "校验", "安装", "重启", "重连", "完成"];
const phases = ["dispatching", "downloading", "verifying", "installing", "restarting", "reconnecting", "success"];

export function upgradeRunning(phase?: string) {
  return !!phase && phases.slice(0, 6).includes(phase);
}

export function componentUpgradeLabel(value: string) {
  return ({
    idle: "已是最新", update_available: "有更新", dispatching: "正在下发", downloading: "正在下载", verifying: "正在校验",
    installing: "正在安装", restarting: "正在重启", reconnecting: "正在重连", success: "升级成功", failed: "升级失败",
    rolled_back: "升级失败，已回滚", offline: "Helper 离线",
  } as Record<string, string>)[value] || value;
}

export function deriveUpgradeProgress(progress: UpgradeProgress | undefined, reportedAt?: string, now = Date.now()) {
  const age = (at?: string) => at && Number.isFinite(Date.parse(at)) ? Math.max(0, Math.floor((now - Date.parse(at)) / 1000)) : null;
  const running = upgradeRunning(progress?.phase);
  const failed = progress?.phase === "failed" || progress?.phase === "rolled_back";
  const lastAge = age(progress?.updated_at);
  const helperAge = age(reportedAt);
  const threshold = progress?.phase === "downloading" || progress?.phase === "reconnecting" ? 180 : 90;
  const lastStep = [...(progress?.log || [])].reverse().find((line) => upgradeRunning(line.phase));
  const step = failed ? phases.indexOf(lastStep?.phase || "dispatching") : phases.indexOf(progress?.phase || "");
  const startedAt = progress?.started_at || progress?.log?.[0]?.at || progress?.updated_at;
  const end = running ? now : Date.parse(progress?.updated_at || "");
  const elapsed = startedAt && Number.isFinite(Date.parse(startedAt)) && Number.isFinite(end) ? Math.max(0, Math.floor((end - Date.parse(startedAt)) / 1000)) : 0;
  const percent = progress?.phase === "downloading" && typeof progress.percent === "number" && Number.isFinite(progress.percent) ? Math.max(0, Math.min(100, progress.percent)) : null;
  const log = progress?.log?.length ? progress.log : progress ? [{ at: progress.updated_at, phase: progress.phase, text: progress.message || componentUpgradeLabel(progress.phase) }] : [];
  return { visible: !!progress && (running || (lastAge !== null && lastAge < 600)), running, failed, step, elapsed, lastAge, helperAge, offline: helperAge === null || helperAge > 30, threshold, stalled: running && lastAge !== null && lastAge > threshold, percent, log, approximateStart: !progress?.started_at };
}

export function upgradeDuration(seconds: number) {
  return seconds >= 60 ? `${Math.floor(seconds / 60)}分${seconds % 60}秒` : `${seconds}秒`;
}

export function progressForComponent(agent: CustomAgentStatusResponse | null, component: "helper" | "core", updateAvailable: boolean, now = Date.now()): UpgradeProgress | undefined {
  const progress = agent?.status?.update;
  if (progress?.component === component) return progress;
  const pending = agent?.pending?.find((item) => item.action === `${component}.update`);
  if (pending) return { component, phase: "dispatching", started_at: pending.created_at, updated_at: pending.created_at };
  const result = [...(agent?.results || [])].reverse().find((item) => item.action === `${component}.update`);
  if (!result || now - Date.parse(result.completed_at) >= 600_000) return undefined;
  return { component, phase: !result.success ? (/rollback|restored/i.test(result.message || "") ? "rolled_back" : "failed") : updateAvailable ? "reconnecting" : "success", updated_at: result.completed_at, message: result.message };
}
