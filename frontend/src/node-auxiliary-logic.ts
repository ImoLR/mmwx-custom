export type NodePackageMembership = { package_id: number; package_name: string; all_nodes: boolean };
export type NodePackageMemberships = {
  success?: boolean;
  memberships?: Record<string, NodePackageMembership[]>;
  packages?: NodePackageMembership[];
};

export type NodeProbeSample = { at: string; ok: boolean; latency_ms: number };
export type NodeProbeState = { node_id: number; source?: string; fail_streak?: number; samples?: NodeProbeSample[] };
export type NodeProbeSettings = { enabled?: boolean; tester_id?: number; resync_minutes?: number };
export type NodeProbeStatus = NodeProbeSettings & {
  success?: boolean;
  enabled_count?: number;
  interval_sec?: number;
  states?: Record<string, NodeProbeState>;
};

export type RelayCredentialRepairReport = {
  dry_run: boolean;
  servers_scanned: number;
  outbounds_scanned: number;
  updated: number;
  already_ok: number;
  skipped: number;
  failed: number;
  details?: Array<{ status: string; server?: string; outbound?: string; node_id?: number; node_name?: string; message?: string }>;
};

export function membershipNeedsExpandWarning(packageItem: Pick<NodePackageMembership, "all_nodes">, member: boolean) {
  return !member && packageItem.all_nodes;
}

export function probeResyncMinutes(value: string) {
  const minutes = Number(value);
  return Number.isFinite(minutes) && minutes >= 0 ? Math.min(Math.floor(minutes), 1440) : 0;
}

export function nodeProbeSummary(state?: NodeProbeState) {
  const samples = state?.samples ?? [];
  return {
    last: samples[samples.length - 1],
    availability: samples.length ? Math.round(samples.filter((sample) => sample.ok).length / samples.length * 100) : null,
    failStreak: state?.fail_streak ?? 0,
    down: (state?.fail_streak ?? 0) >= 2,
  };
}

export function nodeProbeStatesById(status: NodeProbeStatus | null) {
  return new Map(Object.values(status?.states ?? {}).map((state) => [state.node_id, state]));
}
