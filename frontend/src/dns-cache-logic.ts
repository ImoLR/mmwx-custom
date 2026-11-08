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
  const when = ensure.finished_at ? `（${formatTime(ensure.finished_at)}）` : "";
  if (!ensure.finished_at) return `Helper ${ensure.helper_version} 安装 / 升级后的 DNS 缓存检查正在执行。`;
  const outcome = ensure.summary || (ensure.exit_code === 0 ? "完成" : "未完成");
  return ensure.exit_code === 0
    ? `Helper ${ensure.helper_version} 安装 / 升级时检查过${when}：${outcome}`
    : `Helper ${ensure.helper_version} 安装 / 升级时没有配置完成${when}：${outcome}（不影响 Helper 使用）`;
}
