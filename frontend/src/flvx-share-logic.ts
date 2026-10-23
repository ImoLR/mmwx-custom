export type FLVXHelper = { installed: boolean; version: string; architecture: string; flvx_ready: boolean; upgrading: boolean; flvx_protocols?: boolean; flvx_reality_guard?: boolean };
export type FLVXGuardReport = { state: "on" | "restart_required" | "error"; nodes: number };
export type FLVXGuardPlan = { requested: boolean; reality_nodes: number; error?: string };
export type FLVXProtocolCaps = { versions: number[]; protocols: Record<string, { udp: boolean; versions?: number[]; obfs?: string[]; transports?: string[] }> };

const stateText: Record<string, string> = { active: "已启用", error: "异常", removed: "已移除", pending: "待应用", running: "运行中", stopped: "已停止", installing: "安装中", helper_outdated: "Helper 需升级" };

// A desired node the Helper has not reported yet is waiting, never removed.
export function flvxStateText(state: string, helper?: FLVXHelper) {
  if (state === "pending" && helper && !helper.flvx_ready) return "等待 Helper";
  return stateText[state] || state;
}

// The Helper's reason it cannot (re)start the FLVX Core, e.g. a parent
// directory the mmwxc-flvx account cannot enter; "" when there is none.
export function flvxCoreIssue(state: string, error?: string) {
  if (!error) return "";
  return state === "running" ? `FLVX Core 正在运行，但下次重启会失败：${error}` : `FLVX Core 未启动：${error}`;
}

// Why this server's Helper cannot run FLVX nodes; "" when it can. Only Helper
// v0.7.5+ reports the flvx.share capability.
export function flvxHelperIssue(helper?: FLVXHelper) {
  if (!helper || helper.flvx_ready) return "";
  if (!helper.installed) return "未安装 Helper";
  return `Helper 需升级：当前 ${helper.version || "版本未知"} 不支持 FLVX 共享`;
}

// Same rule as 服务管理: v0.1.x Helpers need the guided first upgrade there.
export function flvxHelperUpgradable(helper?: FLVXHelper) {
  return Boolean(helper?.installed && !helper.flvx_ready && helper.version && !/^v?0\.1(?:\.|$)/.test(helper.version));
}

const protocolNames: Record<string, string> = { ss2022: "SS2022", vless_reality_vision: "VLESS Reality Vision", anytls: "AnyTLS", snell: "Snell", mieru: "Mieru" };

export function flvxProtocolName(protocol: string) {
  return protocolNames[protocol] || protocol;
}

// 扩展协议 (landing-protocols-v1): what FLVX may create on this share beyond
// SS2022 / VLESS, or what is missing. ready = advertised to FLVX.
export function flvxExtraProtocols(caps: FLVXProtocolCaps | undefined, helper: FLVXHelper | undefined, coreState: string) {
  const names = ["anytls", "snell", "mieru"].filter((p) => caps?.protocols?.[p]);
  if (names.length) {
    const tcpOnly = names.filter((p) => !caps!.protocols[p].udp).map(flvxProtocolName);
    return { ready: true, text: `${names.map(flvxProtocolName).join(" · ")} 可由 FLVX 创建${tcpOnly.length ? ` · ${tcpOnly.join("、")} 仅 TCP` : ""}` };
  }
  if (!helper?.flvx_protocols) return { ready: false, text: "升级 Helper 后可用（AnyTLS / Snell / Mieru）" };
  if (coreState !== "running") return { ready: false, text: "等待 FLVX Core 运行后核对" };
  return { ready: false, text: "升级 FLVX Core 后可用（AnyTLS / Snell / Mieru）" };
}


// Reality 防盗 (landing-reality-guard-v1): FLVX switches it per server; the
// Helper closes unauthenticated Reality probes with a foreign or missing SNI
// instead of relaying them to the decoy site. capable = the share advertises
// the guard to FLVX. status / tone drive the chip; note is the Helper's reason.
export function flvxRealityGuard(capable: boolean, plan: FLVXGuardPlan | undefined, report: FLVXGuardReport | undefined) {
  const note = plan?.error || "";
  if (!capable) return { text: plan?.requested ? "升级 Helper 后生效 · 目前不拦截" : "升级 Helper 后可用", status: "", tone: "", note: "" };
  if (!plan?.requested) return { text: "FLVX 未开启", status: "", tone: "", note: "" };
  if (!plan.reality_nodes) return { text: "已开启 · 暂无 Reality 节点", status: "", tone: "", note: "" };
  if (!report) return { text: `已开启 · ${plan.reality_nodes} 个 Reality 节点`, status: "等待 Helper 应用", tone: "", note: "" };
  if (report.state === "restart_required") return { text: "重启 FLVX Core 后生效 · 目前不拦截", status: "待重启", tone: "", note: "" };
  if (report.state === "error") return { text: "未生效 · 目前不拦截", status: "应用失败", tone: "error", note };
  return { text: `已开启 · ${report.nodes} 个 Reality 节点受保护`, status: "已生效", tone: "online", note };
}
