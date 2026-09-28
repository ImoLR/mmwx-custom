import type { ManagedPackage, ManagedUser, ManagedUserStateResponse, XrayNode } from "./types";

export type UserManagementRefresh = {
  users?: ManagedUser[];
  packages?: ManagedPackage[];
  nodes?: XrayNode[];
  failures: string[];
};

function reasonText(reason: unknown) {
  return reason instanceof Error && reason.message ? reason.message : "未知错误";
}

export async function fetchUserManagementData(
  fetchUsers: () => Promise<{ users?: ManagedUser[] }>,
  fetchPackages: () => Promise<{ packages?: ManagedPackage[] }>,
  fetchNodes: () => Promise<{ nodes?: XrayNode[] }>,
): Promise<UserManagementRefresh> {
  const [userResult, packageResult, nodeResult] = await Promise.allSettled([fetchUsers(), fetchPackages(), fetchNodes()]);
  const failures: string[] = [];
  if (userResult.status === "rejected") failures.push(`用户列表：${reasonText(userResult.reason)}`);
  if (packageResult.status === "rejected") failures.push(`套餐列表：${reasonText(packageResult.reason)}`);
  if (nodeResult.status === "rejected") failures.push(`节点列表：${reasonText(nodeResult.reason)}`);
  return {
    users: userResult.status === "fulfilled" ? userResult.value.users ?? [] : undefined,
    packages: packageResult.status === "fulfilled" ? packageResult.value.packages ?? [] : undefined,
    nodes: nodeResult.status === "fulfilled" ? nodeResult.value.nodes ?? [] : undefined,
    failures,
  };
}

export type ManagedUserStatusOutcome =
  | { kind: "confirmed"; isActive: boolean }
  | { kind: "unknown"; reason: string };

export async function writeAndVerifyManagedUserStatus(
  username: string,
  expected: boolean,
  write: () => Promise<unknown>,
  verify: () => Promise<ManagedUserStateResponse>,
): Promise<ManagedUserStatusOutcome> {
  await write();
  try {
    const state = await verify();
    if (!state.user.exists || state.user.username !== username || state.user.is_active !== expected) {
      return { kind: "unknown", reason: "数据库中的用户状态与操作结果不一致" };
    }
    return { kind: "confirmed", isActive: state.user.is_active };
  } catch (error) {
    return { kind: "unknown", reason: reasonText(error) };
  }
}
