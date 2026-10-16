import type { RemoteServer, RemoteServerUpdateRequest } from "./types";

// Same rule as officialServerUpdatePayload: preserve the fresh readback, but
// never echo redacted credentials or turn a computed total into a counter edit.
export function remoteServerUpdatePayload(current: RemoteServer, changes: Partial<RemoteServerUpdateRequest>, pullToken: string) {
  const payload: Record<string, unknown> = { ...current };
  delete payload.token;
  delete payload.agent_token;
  delete payload.traffic_used;
  if (typeof payload.expires_at === "string") payload.expires_at = payload.expires_at.split("T", 1)[0];
  return { ...payload, ...changes, id: current.id, pull_token: pullToken };
}

export function remoteServerEditChanges(current: RemoteServerUpdateRequest, baseline: RemoteServerUpdateRequest): Partial<RemoteServerUpdateRequest> & { id: number } {
  return Object.fromEntries(Object.entries(current).filter(([key, value]) => key === "id" || value !== baseline[key as keyof RemoteServerUpdateRequest])) as Partial<RemoteServerUpdateRequest> & { id: number };
}

// Same as externalconfig.Statistics. Agent startup otherwise replaces the
// entire policy when statistics are missing, losing timeouts and user levels.
export function modeSwitchConfig(raw: string): string {
  const config = JSON.parse(raw);
  const before = JSON.stringify(config);
  config.stats ??= {};
  config.policy ??= {};
  config.policy.levels ??= {};
  config.policy.levels["0"] ??= {};
  for (const level of Object.values(config.policy.levels) as Array<Record<string, unknown>>) {
    level.statsUserUplink = true;
    level.statsUserDownlink = true;
  }
  config.policy.system ??= {};
  for (const key of ["statsInboundUplink", "statsInboundDownlink", "statsOutboundUplink", "statsOutboundDownlink"]) config.policy.system[key] = true;
  const prepared = JSON.stringify(config);
  return before === prepared ? raw : prepared;
}
