export type FLVXHelper = { installed: boolean; version: string; architecture: string; flvx_ready: boolean; upgrading: boolean; flvx_protocols?: boolean };
export type FLVXProtocolCaps = { versions: number[]; protocols: Record<string, { udp: boolean; versions?: number[]; obfs?: string[]; transports?: string[] }> };

const stateText: Record<string, string> = { active: "已启用", error: "异常", removed: "已移除", pending: "待应用", running: "运行中", stopped: "已停止", installing: "安装中", helper_outdated: "Helper 需升级" };

// A desired node the Helper has not reported yet is waiting, never removed.
export function flvxStateText(state: string, helper?: FLVXHelper) {
  if (state === "pending" && helper && !helper.flvx_ready) return "等待 Helper";
  return stateText[state] || state;
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

