import test from "node:test";
import assert from "node:assert/strict";
import { machineBadge, machineBreakdownRows, machineFLVXText, machineSettingsForSave, machineSettingsForm, machineStateText } from "../../frontend/src/machine-protection-logic.ts";

const summary = { version: 2, enabled: true, effective: true, max_total: 1000, release_below: 950, blocking: false, dropped_total: 0, tw_buckets: 500, close_wait_seconds: 10, tcp_ports: 3, udp_ports: 1 };
const metric = (connection_count: number, machine?: object) => ({ server_id: "5", tcp_count: connection_count - 20, udp_count: 20, connection_count, available: true, helper_version: machine ? "v0.7.9" : "v0.7.8", machine } as never);

test("card badge shows N/1000 and turns yellow at 80 % and red while protecting", () => {
  assert.deepEqual(machineBadge(metric(823, summary)), { text: "823/1,000", tone: "warning", protecting: false, outdated: false, label: "整机连接 823/1,000" });
  assert.equal(machineBadge(metric(799, summary)).tone, "success");
  assert.equal(machineBadge(metric(800, summary)).tone, "warning");
  const red = machineBadge(metric(1012, { ...summary, blocking: true }));
  assert.equal(red.tone, "danger");
  assert.equal(red.protecting, true);
  assert.match(red.label, /保护中/);
  // Hysteresis: still protecting below the cap until 950.
  assert.equal(machineBadge(metric(960, { ...summary, blocking: true })).tone, "danger");
});

test("old Helpers keep the plain number and are flagged; offline stays --", () => {
  assert.deepEqual(machineBadge(metric(396)), { text: "396", tone: "success", protecting: false, outdated: true, label: "整机连接 396" });
  assert.equal(machineBadge({ ...metric(5), available: false } as never).text, "--");
  assert.equal(machineBadge(undefined).outdated, false);
  assert.equal(machineBadge(metric(500, { ...summary, enabled: false })).text, "500");
  assert.equal(machineStateText(undefined), "助手版本过旧，未启用整机保护");
});

test("breakdown lists every state, UDP and drops, and says whether FLVX ports are included", () => {
  assert.deepEqual(machineBreakdownRows({ total: 846, tcp: 790, udp: 56, established: 214, time_wait: 500, close_wait: 41, other_tcp: 35 }, 12).map(([label, value]) => `${label}=${value}`),
    ["ESTABLISHED=214", "TIME_WAIT=500", "CLOSE_WAIT=41", "其它 TCP=35", "UDP=56", "已丢弃新连接=12"]);
  assert.equal(machineFLVXText({ core: 2, flvx_shared_core: 0, flvx_file: "fresh", flvx_file_tcp: 3, flvx_file_udp: 1 }), "已含 FLVX 业务端口（TCP 3 / UDP 1）");
  assert.equal(machineFLVXText({ core: 2, flvx_shared_core: 1, flvx_file: "absent", flvx_file_tcp: 0, flvx_file_udp: 0 }), "已含 FLVX 共享 Core 端口 1 个");
  assert.equal(machineFLVXText({ core: 2, flvx_shared_core: 0, flvx_file: "absent", flvx_file_tcp: 0, flvx_file_udp: 0 }), "本机没有 FLVX 端口");
  assert.match(machineFLVXText({ core: 2, flvx_shared_core: 0, flvx_file: "stale", flvx_file_tcp: 0, flvx_file_udp: 0 }), /暂未计入/);
  assert.match(machineFLVXText({ core: 2, flvx_shared_core: 0, flvx_file: "malformed", flvx_file_tcp: 0, flvx_file_udp: 0 }), /格式不对/);
  assert.match(machineStateText({ ...summary, blocking: true }), /降到 950 以下恢复/);
  assert.match(machineStateText({ ...summary, warming_up: true }), /即将保护/);
  assert.match(machineStateText({ ...summary, idle_reason: "no_business_ports" }), /没有可保护的业务端口/);
  assert.match(machineStateText({ ...summary, connections: { total: 846, tcp: 790, udp: 56, established: 214, time_wait: 500, close_wait: 41, other_tcp: 35 } }), /接近上限：到 1,000 时/);
  assert.equal(machineStateText({ ...summary, connections: { total: 500, tcp: 490, udp: 10, established: 214, time_wait: 200, close_wait: 41, other_tcp: 35 } }), "正常");
});

test("settings form prefills 1000 / 500 / 10 and old Helpers never get the new fields", () => {
  assert.deepEqual(machineSettingsForm(null), { enabled: false, max_active: null, max_total: 1000, tw_buckets: 500, close_wait_seconds: 10 });
  assert.deepEqual(machineSettingsForm({ enabled: true, max_active: null, max_total: 1500, tw_buckets: 0, close_wait_seconds: 0 }), { enabled: true, max_active: null, max_total: 1500, tw_buckets: 0, close_wait_seconds: 0 });
  const form = machineSettingsForm({ enabled: true, max_active: null, max_total: 1000 });
  assert.deepEqual(Object.keys(machineSettingsForSave(form, false)).sort(), ["enabled", "max_active", "max_total"]);
  assert.deepEqual(machineSettingsForSave(form, true), form);
});
