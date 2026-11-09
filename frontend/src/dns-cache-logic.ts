import type { DNSCacheStatus } from "./types";

// 本机 DNS 缓存 (FLVX flvx-dns-cache, shipped in Helper v0.7.25+). The Helper re-checks it locally at most
// every 10 minutes; the card shows ⚠ only when the cache is missing or broken. Same wording as FLVX's
// node card (plan 240) so both panels read alike.

export type DNSCacheView = {
  tone: "ok" | "neutral" | "warning";
  label: string;
  detail: string;
  attention: boolean;
};

export function dnsCacheView(status: DNSCacheStatus | undefined, fresh: boolean): DNSCacheView {
  if (!fresh) return { tone: "neutral", label: "未知", detail: "Helper 离线，上线后显示本机 DNS 缓存状态。", attention: false };
  if (!status) return { tone: "neutral", label: "未知", detail: "Helper 版本过旧，不会上报 DNS 缓存状态，升级 Helper 后显示。", attention: false };
  if (status.ok && status.mode === "minimal") {
    return { tone: "neutral", label: "精简模式", detail: `没有装本机缓存，只用了备用解析器和 1 秒超时。原因：${status.reason || "未知"}`, attention: false };
  }
  if (status.ok) {
    const how = status.mode === "resolved"
      ? "系统自带的 systemd-resolved 缓存（127.0.0.53）"
      : "本机 dnsmasq 缓存（127.0.0.1，同时问 2–3 个上游，谁先答用谁）";
    return { tone: "ok", label: "正常", detail: `正常：${how}。`, attention: false };
  }
  const reason = status.reason || "本机 DNS 缓存未启用";
  return { tone: "warning", label: status.mode === "none" ? "未启用" : "未运行", detail: `${reason}。丢一个 DNS 包可能卡 1–5 秒。`, attention: true };
}

/** The once-per-Helper-version ensure run (install / upgrade), for the Helper dialog. */
export function dnsCacheEnsureText(status: DNSCacheStatus | undefined, formatTime: (value: string) => string): string {
  const ensure = status?.ensure;
  if (!ensure?.helper_version) return "";
  const when = dnsCacheTimeSet(ensure.finished_at) ? `（${formatTime(ensure.finished_at)}）` : "";
  if (!dnsCacheTimeSet(ensure.finished_at)) return `Helper ${ensure.helper_version} 安装 / 升级后的 DNS 缓存检查正在执行。`;
  const outcome = ensure.summary || (ensure.exit_code === 0 ? "完成" : "未完成");
  return ensure.exit_code === 0
    ? `Helper ${ensure.helper_version} 安装 / 升级时检查过${when}：${outcome}`
    : `Helper ${ensure.helper_version} 安装 / 升级时没有配置完成${when}：${outcome}（不影响 Helper 使用）`;
}

// 「修复 DNS 缓存」 (Helper v0.7.26+, same button as FLVX's node card): Custom sends dns.cache.ensure, the
// Helper runs `ensure` once in the background and reports the record in dns_cache.repair.

export const DNS_CACHE_REPAIR_ACTION = "dns.cache.ensure";
export const DNS_CACHE_REPAIR_COOLDOWN_MS = 60_000;
/** The page stops waiting for a result after this (the Helper bounds one run at 260 s). */
export const DNS_CACHE_REPAIR_WAIT_MS = 5 * 60_000;

export type DNSCacheRepairRecord = NonNullable<DNSCacheStatus["repair"]>;

/** A Go zero time ("0001-01-01T00:00:00Z") means not set. */
export function dnsCacheTimeSet(value: string | undefined): value is string {
  return Boolean(value && !value.startsWith("0001-") && Number.isFinite(Date.parse(value)));
}

export function dnsCacheRepairRunning(repair: DNSCacheRepairRecord | undefined, now: number): boolean {
  return Boolean(repair && dnsCacheTimeSet(repair.started_at) && !dnsCacheTimeSet(repair.finished_at) && now - Date.parse(repair.started_at) < DNS_CACHE_REPAIR_WAIT_MS);
}

export type DNSCacheRepairButton = { enabled: boolean; running: boolean; hint: string };

export function dnsCacheRepairButton(input: { fresh: boolean; capabilities?: string[]; status?: DNSCacheStatus; busy: boolean; lastClickAt?: number; now: number }): DNSCacheRepairButton {
  if (input.busy || dnsCacheRepairRunning(input.status?.repair, input.now)) return { enabled: false, running: true, hint: "一般几秒，最长约 3 分钟" };
  if (!input.fresh) return { enabled: false, running: false, hint: "Helper 离线" };
  if (!input.capabilities?.includes(DNS_CACHE_REPAIR_ACTION)) return { enabled: false, running: false, hint: "需要先升级 Helper" };
  const startedAt = input.status?.repair?.started_at;
  const last = Math.max(input.lastClickAt ?? 0, dnsCacheTimeSet(startedAt) ? Date.parse(startedAt) : 0);
  const wait = DNS_CACHE_REPAIR_COOLDOWN_MS - (input.now - last);
  if (wait > 0) return { enabled: false, running: false, hint: `${Math.ceil(wait / 1000)} 秒后可再试` };
  return { enabled: true, running: false, hint: "" };
}

export type DNSCacheRepairOutcome = { tone: "ok" | "warning"; text: string };

/** The result line: 无需改动 / 已安装 / 失败 + reason. */
export function dnsCacheRepairOutcome(repair: DNSCacheRepairRecord | undefined, formatTime: (value: string) => string): DNSCacheRepairOutcome | null {
  if (!repair || !dnsCacheTimeSet(repair.finished_at)) return null;
  const when = formatTime(repair.finished_at);
  const summary = (repair.summary || "").replace(/^DNS 缓存：/, "");
  if (repair.exit_code !== 0) return { tone: "warning", text: `上次修复（${when}）：失败。${summary || "未知原因"}` };
  // 精简模式 (no cache possible, e.g. low memory) carries its own reason; dnsmasq / resolved end in 无需改动 / 已配置.
  if (summary.startsWith("精简模式")) return { tone: "ok", text: `上次修复（${when}）：${summary}` };
  const detail = summary.replace(/(无需改动|已配置)$/, "");
  return { tone: "ok", text: `上次修复（${when}）：${repair.changed ? "已安装并启用" : "无需改动"}${detail ? `。${detail}` : ""}` };
}

/** Polling after a click: the Helper refused (busy, switched off …) or the record of this command is finished. */
export function dnsCacheRepairProgress(
  agent: { status?: { dns_cache?: DNSCacheStatus }; results?: Array<{ command_id?: string; action: string; success: boolean; message?: string }> },
  commandID: string,
): { done: boolean; error?: string; repair?: DNSCacheRepairRecord } {
  const refused = agent.results?.find((result) => result.command_id === commandID && !result.success);
  if (refused) return { done: true, error: refused.message || "Helper 拒绝执行" };
  const repair = agent.status?.dns_cache?.repair;
  if (repair?.command_id === commandID && dnsCacheTimeSet(repair.finished_at)) return { done: true, repair };
  return { done: false };
}
