import assert from "node:assert/strict";
import test from "node:test";
import { filterSpeedTestNodes, sortSpeedTestResults, speedTestError, speedTestLatest, speedTestLatency, speedTestState, speedTesterCommands, toggleVisibleSpeedTestNodes } from "../../frontend/src/node-speedtest-logic.ts";

const started = "2026-10-03T12:00:00Z";
const now = new Date(started).getTime();

test("speed tests expose running, 15 second timeout, completed and failed states", () => {
  assert.equal(speedTestState(), "idle");
  assert.equal(speedTestState({ status: "running", created_at: started }, now + 15_000), "running");
  assert.equal(speedTestState({ status: "running", created_at: started }, now + 15_001), "timeout");
  assert.equal(speedTestState({ status: "running" }, now), "running");
  assert.equal(speedTestState({ status: "running", created_at: "invalid" }, now), "running");
  assert.equal(speedTestState({ status: "failed", created_at: started }, now + 20_000), "failed");
  assert.equal(speedTestState({ status: "ok", latency_ms: 0 }, now), "ok");
});

test("latest results are per node and history sorts do not mutate server results", () => {
  const old = { node_id: 1, status: "ok", created_at: started, down_mbps: 5, latency_ms: 100 };
  const recent = { node_id: 1, status: "ok", created_at: "2026-10-03T12:00:01Z", down_mbps: 10, latency_ms: 0 };
  const running = { node_id: 2, status: "running", created_at: "2026-10-03T12:00:02Z" };
  const records = [recent, old, running];
  assert.equal(speedTestLatest(records).get(1), recent);
  assert.deepEqual(sortSpeedTestResults(records, "time"), [running, recent, old]);
  assert.deepEqual(sortSpeedTestResults(records, "speed"), [recent, old, running]);
  assert.deepEqual(sortSpeedTestResults(records, "latency"), [recent, old, running]);
  assert.deepEqual(records, [recent, old, running]);
});

test("visible selection preserves hidden selections and protocol/tag filters combine", () => {
  const nodes = [{ id: 1, node_name: "A", protocol: "vless", tags: ["EU", "fast"] }, { id: 2, node_name: "B", protocol: "trojan", tag: "EU" }, { id: 3, node_name: "C", protocol: "vless", tag: "US" }];
  assert.deepEqual(filterSpeedTestNodes(nodes, new Set(["vless"]), new Set(["EU"])).map((node) => node.id), [1]);
  assert.deepEqual(filterSpeedTestNodes(nodes, new Set(), new Set(["EU", "US"])), nodes);
  assert.deepEqual([...toggleVisibleSpeedTestNodes(new Set([3]), [1, 2])], [3, 1, 2]);
  assert.deepEqual([...toggleVisibleSpeedTestNodes(new Set([3, 1, 2]), [1, 2])], [3]);
  assert.deepEqual([...toggleVisibleSpeedTestNodes(new Set([3]), [])], [3]);
});

test("PRO gate uses official text and install commands quote user supplied names", () => {
  assert.equal(speedTestError(new Error("节点测速是 PRO 功能，请升级许可证")), "节点测速是 PRO 功能,请升级许可证");
  assert.equal(speedTestError(new Error("network failed")), "network failed");
  const commands = speedTesterCommands("https://panel.example", "a'b", "name with $(unsafe)");
  assert.equal(commands.length, 3);
  assert.ok(commands[0].command.includes("'a'\\''b'"));
  assert.ok(commands[1].command.includes("'a''b'"));
  assert.ok(commands[2].command.includes("'MMWX_SPEEDTEST_NAME=name with $(unsafe)'"));
});

// The official latency-only handler can finish with status ok and latency -1.
test("completed latency failures do not appear as zero throughput or negative milliseconds", () => {
  const failedLatency = { status: "ok", latency_ms: -1, down_mbps: 0 };
  assert.equal(speedTestState(failedLatency), "failed");
  assert.equal(speedTestLatency(failedLatency), "失败");
  assert.equal(speedTestState({ ...failedLatency, down_mbps: 12 }), "ok");
  assert.equal(speedTestLatency({ ...failedLatency, down_mbps: 12 }), "失败");
  assert.equal(speedTestState({ ...failedLatency, latency_ms: 0 }), "ok");
  assert.equal(speedTestLatency({ ...failedLatency, latency_ms: 0 }), "0 ms");
  assert.equal(speedTestLatency({ status: "running", latency_ms: 0 }), "—");
  assert.equal(speedTestLatency({ status: "failed", latency_ms: -1 }), "失败");
  assert.equal(speedTestLatency(), "—");
});
