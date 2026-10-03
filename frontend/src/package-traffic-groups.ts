import type { PackageTrafficGroupInput, PackageTrafficGroupNode, PackageTrafficGroupUsage } from "./types";

export const TRAFFIC_GB = 1024 ** 3;

export type PackageTrafficGroupDraft = {
  id?: number;
  name: string;
  limit_gb: number;
  node_ids: number[];
};

export function trafficGroupDraft(group: PackageTrafficGroupInput): PackageTrafficGroupDraft {
  return { id: group.id, name: group.name, limit_gb: group.limit_bytes / TRAFFIC_GB, node_ids: [...group.node_ids] };
}

export function trafficGroupPayload(group: PackageTrafficGroupDraft): PackageTrafficGroupInput {
  return { id: group.id, name: group.name.trim(), limit_bytes: Math.floor(group.limit_gb * TRAFFIC_GB), node_ids: [...group.node_ids] };
}

export function retainTrafficGroupNodes(groups: PackageTrafficGroupDraft[], nodeIds: number[]): PackageTrafficGroupDraft[] {
  const allowed = new Set(nodeIds);
  return groups.map((group) => ({ ...group, node_ids: group.node_ids.filter((id) => allowed.has(id)) }));
}

export function validateTrafficGroups(groups: PackageTrafficGroupDraft[], packageLimitGB: number, nodeIds: number[], nodeLimits: Record<number, number>): string {
  const allowed = new Set(nodeIds);
  const assigned = new Set<number>();
  const packageLimitBytes = Math.floor(packageLimitGB * TRAFFIC_GB);
  for (const [index, group] of groups.entries()) {
    const label = group.name.trim() ? `共享组“${group.name.trim()}”` : `第 ${index + 1} 个共享组`;
    const limitBytes = Math.floor(group.limit_gb * TRAFFIC_GB);
    if (!group.name.trim()) return `${label}请输入名称`;
    if ([...group.name.trim()].length > 100) return "共享组名称不能超过 100 个字符";
    if (!Number.isFinite(group.limit_gb) || limitBytes < 1) return `${label}的共享额度必须大于 0 GB`;
    if (!Number.isSafeInteger(limitBytes)) return `${label}的共享额度过大`;
    if (packageLimitGB > 0 && limitBytes > packageLimitBytes) return `${label}的共享额度不能超过套餐总额度`;
    if (group.node_ids.length === 0) return `${label}至少需要一个节点`;
    for (const nodeId of group.node_ids) {
      if (!allowed.has(nodeId)) return `${label}包含未关联到套餐的节点 ${nodeId}`;
      if (assigned.has(nodeId)) return `节点 ${nodeId} 只能属于一个共享组`;
      if (Math.floor(Number(nodeLimits[nodeId]) * TRAFFIC_GB) > limitBytes) return `节点 ${nodeId} 的单节点流量额度不能超过${label}的共享额度`;
      assigned.add(nodeId);
    }
  }
  return "";
}

export function trafficGroupNodeWarning(node: PackageTrafficGroupNode): string {
  switch (node.status) {
    case "enforced": return "";
    case "embedded": return "该节点为 Embedded 模式，共享组额度无法强制执行";
    case "core_outdated": return "Core 版本过旧或能力未确认，共享组额度无法强制执行";
    case "helper_outdated": return "Helper 版本过旧或未连接，共享组额度无法强制执行";
    case "external_node": return "外部节点无法强制执行共享组额度";
    case "no_identity": return `无法安全识别独立用户身份，共享组额度无法强制执行${node.reason ? `：${node.reason}` : ""}`;
    default: return "共享组执行能力未知";
  }
}

export function trafficGroupUsageStatus(row: Pick<PackageTrafficGroupUsage, "used_bytes" | "limit_bytes" | "blocked" | "nodes">) {
  const overQuota = row.limit_bytes > 0 && row.used_bytes >= row.limit_bytes;
  if (!overQuota) return { overQuota: false, label: "", warning: "" };
  const unavailable = row.nodes.length === 0 || row.nodes.some((node) => node.status !== "enforced");
  return {
    overQuota: true,
    label: row.blocked ? "已超额 · 待执行确认" : "已超额 · 无法执行拦截",
    warning: row.blocked
      ? `${unavailable ? "部分节点无法执行拦截。" : ""}已生成拦截配置，实际执行结果需在服务器连接状态中确认。`
      : "当前没有可安全执行拦截的节点，超额后仍可能继续产生流量，请检查节点提醒。",
  };
}
