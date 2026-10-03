import assert from "node:assert/strict";
import test from "node:test";
import { membershipNeedsExpandWarning, nodeProbeStatesById, nodeProbeSummary, probeResyncMinutes } from "../../frontend/src/node-auxiliary-logic.ts";

test("removing an all-nodes package membership requires its expansion warning", () => {
  assert.equal(membershipNeedsExpandWarning({ all_nodes: true }, false), true);
  assert.equal(membershipNeedsExpandWarning({ all_nodes: true }, true), false);
  assert.equal(membershipNeedsExpandWarning({ all_nodes: false }, false), false);
  assert.equal(membershipNeedsExpandWarning({ all_nodes: false }, true), false);
});

test("probe auto-resync matches official rounding, bounds and invalid-value behavior", () => {
  for (const [input, expected] of [["", 0], ["NaN", 0], ["Infinity", 0], ["-1", 0], ["0", 0], ["1.9", 1], ["1440", 1440], ["2000", 1440]] as const) {
    assert.equal(probeResyncMinutes(input), expected);
  }
});

test("probe samples distinguish waiting, timeout, availability and two consecutive failures", () => {
  assert.deepEqual(nodeProbeSummary(), { last: undefined, availability: null, failStreak: 0, down: false });
  const samples = [{ at: "a", ok: true, latency_ms: 100 }, { at: "b", ok: true, latency_ms: 210 }, { at: "c", ok: false, latency_ms: 0 }];
  assert.deepEqual(nodeProbeSummary({ node_id: 7, samples, fail_streak: 1 }), { last: samples[2], availability: 67, failStreak: 1, down: false });
  assert.equal(nodeProbeSummary({ node_id: 7, samples, fail_streak: 2 }).down, true);
  const state = { node_id: 7, samples };
  assert.equal(nodeProbeStatesById({ states: { unrelatedKey: state } }).get(7), state);
  assert.equal(nodeProbeStatesById(null).size, 0);
});
