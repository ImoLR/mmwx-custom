export type FLVXHelper = { installed: boolean; version: string; architecture: string; flvx_ready: boolean; upgrading: boolean };

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
