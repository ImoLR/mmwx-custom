import assert from "node:assert/strict";
import test from "node:test";
import { deriveUpgradeProgress, progressForComponent, upgradeDuration, upgradeRunning } from "../../frontend/src/upgrade-progress-state.ts";

const now = Date.parse("2026-10-15T16:00:00Z");
const at = (seconds: number) => new Date(now - seconds * 1000).toISOString();
const base = { component: "helper" as const, phase: "downloading" as const, started_at: at(83), updated_at: at(8) };

test("upgrade steps and download progress", () => {
  const phases = ["dispatching", "downloading", "verifying", "installing", "restarting", "reconnecting", "success"] as const;
  phases.forEach((phase, index) => {
    const state = deriveUpgradeProgress({ ...base, phase }, at(2), now);
    assert.equal(state.step, index);
    assert.equal(state.running, index < 6);
  });
  const state = deriveUpgradeProgress({ ...base, percent: 42, bytes_done: 42, bytes_total: 100 }, at(2), now);
  assert.equal(state.percent, 42);
  assert.equal(state.elapsed, 83);
  assert.equal(state.lastAge, 8);
  assert.equal(upgradeDuration(83), "1分23秒");
  assert.equal(upgradeDuration(8), "8秒");
});

test("phase-specific stalls distinguish Helper silence from fresh reports", () => {
  for (const phase of ["downloading", "reconnecting", "installing"] as const) {
    const threshold = phase === "installing" ? 90 : 180;
    assert.equal(deriveUpgradeProgress({ ...base, phase, updated_at: at(threshold) }, at(1), now).stalled, false);
    const working = deriveUpgradeProgress({ ...base, phase, updated_at: at(threshold + 1) }, at(1), now);
    assert.equal(working.stalled, true);
    assert.equal(working.offline, false);
    assert.equal(deriveUpgradeProgress({ ...base, phase, updated_at: at(200) }, at(31), now).offline, true);
  }
  assert.equal(deriveUpgradeProgress({ ...base, updated_at: at(3600) }, undefined, now).visible, true);
});

test("terminal state freezes elapsed and uses the failing step", () => {
  for (const phase of ["failed", "rolled_back"] as const) {
    const state = deriveUpgradeProgress({ ...base, phase, log: [{ at: at(10), phase: "restarting", text: "重启" }, { at: at(8), phase, text: "失败" }] }, at(31), now);
    assert.equal(state.failed, true);
    assert.equal(state.step, 4);
    assert.equal(state.stalled, false);
    assert.equal(state.elapsed, 75);
  }
  assert.equal(deriveUpgradeProgress({ ...base, phase: "success", updated_at: at(599) }, at(1), now).visible, true);
  assert.equal(deriveUpgradeProgress({ ...base, phase: "success", updated_at: at(600) }, at(1), now).visible, false);
});

test("old Helper phase-only payload has an indeterminate bar and a useful log", () => {
  const state = deriveUpgradeProgress({ component: "core", phase: "verifying", updated_at: at(12) }, at(4), now);
  assert.equal(state.step, 2);
  assert.equal(state.percent, null);
  assert.equal(state.approximateStart, true);
  assert.equal(state.log[0].text, "正在校验");
  assert.equal(deriveUpgradeProgress(undefined, undefined, now).visible, false);
  assert.equal(upgradeRunning("offline"), false);
  assert.equal(progressForComponent({ success: true, pending: [{ id: "test", action: "helper.update", created_at: at(1) }] }, "helper", true, now)?.phase, "dispatching");
  assert.equal(progressForComponent({ success: true, results: [{ action: "helper.update", success: true, completed_at: at(1) }] }, "helper", true, now)?.phase, "reconnecting");
});
