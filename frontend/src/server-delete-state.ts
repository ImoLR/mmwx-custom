export type ServerDeletionPlan = {
  operation_id?: string;
  server_id: number;
  server_name: string;
  fingerprint: string;
  state: "preview" | "deleting" | "delete_partial" | "deleted";
  uninstall_agent: boolean;
  nodes: Array<{ id: number; name: string; server_name: string; reason: string; direct: boolean }>;
  packages: Array<{ id: number; name: string; users: string[]; node_ids: number[]; deleted_node_ids: number[]; delete: boolean }>;
  groups: Array<{ id: number; package_id: number; name: string; node_ids: number[]; limit_bytes: number; delete: boolean }>;
  connections: Record<string, number>;
  attention: string[];
  machine: { helper: string; official_agent: string; outstanding: string[]; manual_command: string };
  steps: Array<{ kind: string; name: string; id?: number; status: "pending" | "completed" | "failed"; attempts: number; last_error?: string }>;
  updated_at?: string;
};

export function groupServerDeletion(plan: ServerDeletionPlan) {
  return {
    direct: plan.nodes.filter((node) => node.direct),
    dependants: plan.nodes.filter((node) => !node.direct),
    deletedPackages: plan.packages.filter((pkg) => pkg.delete),
    trimmedPackages: plan.packages.filter((pkg) => !pkg.delete),
    completed: plan.steps.filter((step) => step.status === "completed").length,
    failed: plan.steps.filter((step) => step.status === "failed"),
  };
}

export function canConfirmServerDeletion(plan: ServerDeletionPlan | null, confirmation: string, running: boolean) {
  return Boolean(plan && plan.state !== "deleted" && !running && confirmation === plan.server_name);
}
