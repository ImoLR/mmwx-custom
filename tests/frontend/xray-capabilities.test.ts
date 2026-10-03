import assert from "node:assert/strict";
import test from "node:test";
import { supportsCustomCoreFeatures } from "../../frontend/src/xray-capabilities.ts";
import type { CoreModeResponse } from "../../frontend/src/types.ts";

const now = Date.parse("2026-10-03T12:00:00Z");
const external = { xray_mode: "external" };
const embedded = { xray_mode: "embedded" };

function ownedCore(): CoreModeResponse {
  return {
    success: true, configured: true, controller_mode: "external", controller_status: "connected", current_mode: "external",
    intent: { custom_core_owned: true, desired_core_mode: "external" },
    agent_status: {
      core_mode: "external", reported_at: new Date(now).toISOString(),
      external_ownership: { enabled: true, service_owned: true, runtime_owned: true, single_core: true, service_active: true, core_ready: true },
    },
  };
}

test("Embedded WARP retains its availability without Helper data", () => {
  assert.equal(supportsCustomCoreFeatures(embedded, undefined, now), true);
});

test("confirmed running Custom Core enables WARP", () => {
  assert.equal(supportsCustomCoreFeatures(external, ownedCore(), now), true);
});

test("unknown External status and installed or intended ownership alone cannot enable features", () => {
  const mode = ownedCore();
  mode.agent_status = { core: { installed: true, ready: true }, reported_at: new Date(now).toISOString() };
  assert.equal(supportsCustomCoreFeatures(external, undefined, now), false);
  assert.equal(supportsCustomCoreFeatures(external, mode, now), false);
});

test("every runtime ownership condition is required", () => {
  for (const field of ["enabled", "service_owned", "runtime_owned", "single_core", "service_active", "core_ready"] as const) {
    for (const value of [false, undefined]) {
      const mode = ownedCore();
      mode.agent_status!.external_ownership![field] = value;
      assert.equal(supportsCustomCoreFeatures(external, mode, now), false, field);
    }
  }
});

test("disconnected or mismatched controller and Agent modes cannot enable features", () => {
  for (const patch of [{ success: false }, { controller_status: "disconnected" }, { controller_mode: "embedded" }, { current_mode: "embedded" }]) {
    assert.equal(supportsCustomCoreFeatures(external, { ...ownedCore(), ...patch }, now), false);
  }
  const mode = ownedCore();
  mode.agent_status!.core_mode = "embedded";
  assert.equal(supportsCustomCoreFeatures(external, mode, now), false);
});

test("stale, missing, malformed, and future reports are disabled", () => {
  for (const timestamp of [undefined, "invalid", new Date(now - 15_001).toISOString(), new Date(now + 1).toISOString()]) {
    const mode = ownedCore();
    mode.agent_status!.reported_at = timestamp;
    assert.equal(supportsCustomCoreFeatures(external, mode, now), false);
  }
  assert.equal(supportsCustomCoreFeatures(external, ownedCore(), now + 15_000), true);
  assert.equal(supportsCustomCoreFeatures(external, ownedCore(), now + 15_001), false);
});
