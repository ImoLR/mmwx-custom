import type { ConnectionMetric, MachineConnections, MachinePortSources, MachineProtectionSettings, MachineSummary } from "./types";

/** User decision 2026-10-20: 1000 whole-machine connections, TIME_WAIT 500, CLOSE_WAIT 10 s. */
export const machineProtectionDefaults = { maxTotal: 1000, twBuckets: 500, closeWaitSeconds: 10 } as const;

export const machineWarningRatio = 0.8;

export type MachineBadge = {
  text: string;
  tone: "success" | "warning" | "danger";
  protecting: boolean;
  outdated: boolean;
  label: string;
};

/** The server card badge: the same whole-machine number, plus 「/上限」 once protection is on. */
export function machineBadge(metric?: ConnectionMetric): MachineBadge {
  const count = metric?.available && typeof metric.connection_count === "number" ? metric.connection_count : null;
  const countText = count == null ? "--" : count.toLocaleString();
  const machine = metric?.machine;
  if (count == null || !machine) {
    return { text: countText, tone: "success", protecting: false, outdated: count != null && !machine, label: `整机连接 ${countText}` };
  }
  const max = machine.enabled ? machine.max_total : null;
  if (!max) return { text: countText, tone: "success", protecting: false, outdated: false, label: `整机连接 ${countText}，未设上限` };
  const text = `${countText}/${max.toLocaleString()}`;
  if (machine.blocking) return { text, tone: "danger", protecting: true, outdated: false, label: `整机连接 ${text}，保护中` };
  const tone = count >= max * machineWarningRatio ? "warning" : "success";
  return { text, tone, protecting: false, outdated: false, label: `整机连接 ${text}` };
}

export function machineBreakdownRows(connections?: MachineConnections, dropped = 0): Array<[string, number | undefined]> {
  return [
    ["ESTABLISHED", connections?.established],
    ["TIME_WAIT", connections?.time_wait],
    ["CLOSE_WAIT", connections?.close_wait],
    ["其它 TCP", connections?.other_tcp],
    ["UDP", connections?.udp],
    ["已丢弃新连接", dropped],
  ];
}

/** Whether FLVX ports (and, on Embedded servers, the official inbound ports) are inside the guard, in plain words. */
export function machineFLVXText(sources?: MachinePortSources) {
  if (!sources) return "FLVX 端口：未知";
  const parts: string[] = [];
  if (sources.flvx_file === "fresh") parts.push(`已含 FLVX 业务端口（TCP ${sources.flvx_file_tcp} / UDP ${sources.flvx_file_udp}）`);
  else if (sources.flvx_file === "stale") parts.push("FLVX 端口文件超过 10 分钟未更新，暂未计入");
  else if (sources.flvx_file === "malformed") parts.push("FLVX 端口文件格式不对，未计入");
  if (sources.flvx_shared_core > 0) parts.push(`已含 FLVX 共享 Core 端口 ${sources.flvx_shared_core} 个`);
  const flvx = parts.length ? parts.join("；") : "本机没有 FLVX 端口";
  return sources.official_embedded ? `已含官方 Embedded 端口 ${sources.official_embedded} 个；${flvx}` : flvx;
}

export function machineStateText(machine?: Pick<MachineSummary, "enabled" | "blocking" | "warming_up" | "release_below" | "idle_reason" | "last_error"> & { max_total?: number | null; connections?: MachineConnections }) {
  if (!machine) return "助手版本过旧，未启用整机保护";
  if (!machine.enabled) return "未启用";
  if (machine.last_error) return `异常：${machine.last_error}`;
  if (machine.blocking) return machine.release_below ? `保护中：拒绝业务端口的新连接，降到 ${machine.release_below.toLocaleString()} 以下恢复` : "保护中：拒绝业务端口的新连接";
  if (machine.warming_up) return "即将保护（等待连接跟踪就绪，约 5 秒）";
  if (machine.idle_reason === "no_business_ports") return "正常（本机没有可保护的业务端口）";
  const total = machine.connections?.total, max = machine.max_total;
  if (total != null && max && total >= max * machineWarningRatio) return `接近上限：到 ${max.toLocaleString()} 时开始拒绝业务端口的新连接`;
  return "正常";
}

/** Settings form values: saved values, or the approved defaults when unset. */
export function machineSettingsForm(saved?: MachineProtectionSettings | null): MachineProtectionSettings {
  return {
    enabled: saved?.enabled ?? false,
    max_active: saved?.max_active ?? null,
    max_total: saved?.max_total ?? machineProtectionDefaults.maxTotal,
    tw_buckets: saved?.tw_buckets ?? machineProtectionDefaults.twBuckets,
    close_wait_seconds: saved?.close_wait_seconds ?? machineProtectionDefaults.closeWaitSeconds,
  };
}

/** Older Helpers get only the fields they understand. */
export function machineSettingsForSave(settings: MachineProtectionSettings, helperSupportsV2: boolean): MachineProtectionSettings {
  if (helperSupportsV2) return { ...settings };
  const { enabled, max_active, max_total } = settings;
  return { enabled, max_active, max_total };
}
