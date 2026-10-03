import type { PackageTrafficGroupInput, PackageTrafficGroupNode } from "./types";

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
    if (limitBytes > packageLimitBytes) return `${label}的共享额度不能超过套餐总额度`;
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
