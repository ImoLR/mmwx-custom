import type { CoreModeResponse, RemoteServer } from "./types";

export function supportsCustomCoreFeatures(server: Pick<RemoteServer, "xray_mode">, mode?: CoreModeResponse, now = Date.now()): boolean {
  if (server.xray_mode !== "external") return true;
  const status = mode?.agent_status;
  const ownership = status?.external_ownership;
  const age = now - Date.parse(status?.reported_at || "");
  // Ownership of the running process matters; custom_core_owned is only intent.
  return mode?.success === true
    && mode.controller_mode === "external"
    && mode.controller_status === "connected"
    && mode.current_mode === "external"
    && status?.core_mode === "external"
    && age >= 0 && age <= 15_000
    && ownership?.enabled === true
    && ownership.service_owned === true
    && ownership.runtime_owned === true
    && ownership.single_core === true
    && ownership.service_active === true
    && ownership.core_ready === true;
}
