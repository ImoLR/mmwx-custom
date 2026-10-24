import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import vm from "node:vm";
import test from "node:test";
import ts from "typescript";
import React from "react";

const require = createRequire(import.meta.url);
const source = readFileSync(new URL("../src/connections-manager.tsx", import.meta.url), "utf8");
function machineLogic() {
  const logic = readFileSync(new URL("../src/machine-protection-logic.ts", import.meta.url), "utf8");
  const module = { exports: {} as any };
  vm.runInNewContext(ts.transpileModule(logic, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText, { module, exports: module.exports });
  return module.exports;
}
const tcp = (total: number) => ({ tcp_total: total, established: 3, time_wait: total - 3, close_wait: 0 });
function fixture() {
  const aggregate = { current_total: 38, inbound_current: 6, inbound_tcp: tcp(17), outbound_tcp: tcp(21), inbound_online_ips: [], outbound_new_rate: 2, rejected_user_inbound_limit: 7, rejected_port_inbound_limit: 8, rejected_global_inbound_limit: 9 };
  return {
    settings: { global_total_limit_enabled: false, max_global_total_connections: null, max_global_inbound_connections: 40, management_users: [{ username: "alice", max_inbound_connections: 20, max_total_connections: null }], ports: [{ inbound_tag: "in-a", max_inbound_connections: 10, max_total_connections: null }], users: [] },
    management: { users: [{ username: "alice", source: "binding", aggregate, ports: [{ inbound_tag: "in-a", port: 10016, protocol_identities: [], aggregate, limits: {} }] }], assignable_users: [], unassigned_ports: [] },
    core_accounting: { inbound_current: 6, total: 38, attributed: 38, unattributed: 0, balanced: true },
    record: { snapshot: { system: { ...tcp(304), established: 10, time_wait: 279, close_wait: 2 }, global: { current_inbound: 6, current_total: 38, rejected_global_inbound_limit: 9 }, core: {}, proxy_users: [{ ...aggregate, inbound_tag: "in-a", user: "proto-a" }], management_groups: [] } },
  };
}
function dialog(data: any) {
  const states: any[] = [data, structuredClone(data.settings)];
  let cursor = 0;
  let saved: any;
  const module = { exports: {} as any };
  const code = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 } }).outputText;
  vm.runInNewContext(code, { module, exports: module.exports, require: (id: string) => {
    if (id === "react") return { ...React, useEffect() {}, useCallback: (fn: any) => fn, useRef: (current: any) => ({ current }), useState: (initial: any) => {
      const index = cursor++;
      if (!(index in states)) states[index] = initial;
      return [states[index], (value: any) => { states[index] = typeof value === "function" ? value(states[index]) : value; }];
    } };
    if (id === "./api") return { updateDetailedConnectionSettings: async (_token: any, _id: any, settings: any) => { saved = structuredClone(settings); return { ...data, settings }; } };
    if (id === "lucide-react") return new Proxy({}, { get: () => () => null });
    if (id === "./machine-protection-logic") return machineLogic();
    return require(id);
  } });
  const render = () => { cursor = 0; return expand(module.exports.ConnectionsManager({ server: { id: 5, name: "Boil Hinet 马年" }, token: "test" })); };
  return { render, settings: () => states[1], saved: () => saved };
}
function expand(node: any): any {
  if (Array.isArray(node)) return node.map(expand);
  if (!node || typeof node !== "object") return node;
  if (typeof node.type === "function") return expand(node.type(node.props));
  return { type: node.type, props: { ...node.props, children: expand(node.props.children) } };
}
function nodes(node: any): any[] {
  if (Array.isArray(node)) return node.flatMap(nodes);
  if (!node || typeof node !== "object") return [];
  return [node, ...nodes(node.props.children)];
}
function text(node: any): string {
  if (Array.isArray(node)) return node.map(text).join("");
  if (node == null || typeof node === "boolean") return "";
  if (typeof node === "object") return text(node.props.children);
  return String(node);
}
function section(tree: any, className: string) { return nodes(tree).find(n => n.props.className?.split(" ").includes(className)); }
function input(tree: any, label: string) {
  const field = nodes(tree).find(n => n.type === "label" && text(n).startsWith(label));
  assert.ok(field, label);
  return nodes(field).find(n => n.type === "input");
}

test("Core, management user and port select inbound counts, keeping full socket diagnostics", () => {
  const data = fixture();
  data.core_accounting.inbound_current = 9;
  data.record.snapshot.global.current_inbound = 9;
  data.management.users[0].ports[0].aggregate = { ...data.management.users[0].aggregate, inbound_current: 2 };
  const tree = dialog(data).render();
  assert.match(text(section(tree, "connection-global-settings")), /用户连接 9/);
  assert.match(text(tree), /TCP 套接字 38（入站 17 \+ 出站 21，含 TIME_WAIT\/CLOSE_WAIT）/);
  assert.match(text(section(tree, "connection-user-fold")), /连接 6\/20/);
  assert.match(text(section(tree, "connection-port-fold")), /连接 2\/10/);
  assert.match(text(tree), /TCP 套接字 38 = 入站 17 \+ 出站 21/);
  assert.match(text(tree), /用户连接 7.*端口连接 8.*全局连接 9/);
  assert.match(text(tree), /入站 TCP 套接字.*出站 TCP 套接字/);
  const zero = fixture(); zero.core_accounting.inbound_current = 0; zero.management.users[0].aggregate.inbound_current = 0;
  assert.match(text(dialog(zero).render()), /用户连接 0/);
  assert.match(text(dialog(zero).render()), /连接 0\/20/);
  const fallback = fixture(); delete (fallback as any).core_accounting;
  assert.match(text(dialog(fallback).render()), /用户连接 6/);
});

test("Level 1 keeps the original machine TCP fields, all-state total and text", () => {
  const machine = section(dialog(fixture()).render(), "connection-machine-section");
  assert.match(text(machine), /TCP 总数304ESTABLISHED10TIME_WAIT279CLOSE_WAIT2/);
  assert.match(text(machine), /Machine Total = \/proc\/net\/tcp \+ tcp6 全部 TCP 行（含 LISTEN）。各状态仅用于解释 Total，不另造第二种总连接口径。/);
  assert.match(text(machine), /Linux 整机所有进程/);
});

test("server card badge still shows the available connection_count, now as N/上限 once machine protection v2 is on", () => {
  const main = readFileSync(new URL("../src/main.tsx", import.meta.url), "utf8");
  assert.ok(main.includes('<MachineConnectionTag metric={connectionMetric} onOpen={() => onOpenDialog("connections")} />'));
  const { machineBadge } = machineLogic();
  assert.equal(machineBadge({ available: true, connection_count: 396, tcp_count: 392, udp_count: 4 }).text, "396");
  assert.equal(machineBadge({ available: true, connection_count: 0 }).text, "0");
  assert.equal(machineBadge({ available: false, connection_count: 396 }).text, "--");
  assert.equal(machineBadge().text, "--");
  const machine = { version: 2, enabled: true, max_total: 1000, blocking: false };
  assert.equal(machineBadge({ available: true, connection_count: 396, machine }).text, "396/1,000");
  assert.ok(source.includes('className={`service-badge ${badge.tone} connection-tag'));
  assert.ok(source.includes('<span className="connection-tag-value">{badge.text}</span>'));
});

test("machine protection v2 fields prefill 1000 / 200 / 10, old Helpers see 助手版本过旧 and send only old fields", async () => {
  const data: any = fixture();
  data.record.snapshot.machine_protection = { supported: true, configured: { enabled: false, max_active: null, max_total: null }, effective: true, blocking: false, active: 1, total: 10, controlled_ports: [443] };
  let view = dialog(data);
  let tree = view.render();
  assert.match(text(section(tree, "connection-protection-block")), /助手版本过旧/);
  assert.equal(input(tree, "TIME_WAIT 上限").props.disabled, true);
  await nodes(tree).find(n => n.type === "button" && n.props.className === "connection-save").props.onClick();
  assert.equal(view.saved().machine_protection, undefined);
  data.settings.machine_protection = { enabled: true, max_active: null, max_total: 1000, tw_buckets: 500, close_wait_seconds: 10 };
  view = dialog(data);
  await nodes(view.render()).find(n => n.type === "button" && n.props.className === "connection-save").props.onClick();
  assert.deepEqual(Object.keys(view.saved().machine_protection).sort(), ["enabled", "max_active", "max_total"]);

  data.settings.machine_protection = null;
  data.record.snapshot.machine_protection = { version: 2, owner: "mmwx", supported: true, configured: { enabled: true, max_active: null, max_total: 1000 }, effective: true, blocking: true, active: 300, total: 1012, controlled_ports: [443, 20001], tcp_ports: [443, 20001], udp_ports: [20001], connections: { total: 1012, tcp: 950, udp: 62, established: 300, time_wait: 500, close_wait: 41, other_tcp: 109 }, max_total: 1000, release_below: 950, port_sources: { core: 1, flvx_shared_core: 0, flvx_file: "fresh", flvx_file_tcp: 1, flvx_file_udp: 1 }, dropped_total: 12, tw_buckets: { target: 500, current: 500, original: 262144 }, close_wait_seconds: 10 };
  view = dialog(data);
  tree = view.render();
  const block = text(section(tree, "connection-protection-block"));
  assert.match(block, /整机连接 1,012 \/ 1,000/);
  assert.match(block, /当前动作 保护中/);
  assert.match(block, /已丢弃新连接 12/);
  assert.match(block, /ESTABLISHED300TIME_WAIT500CLOSE_WAIT41其它 TCP109UDP62/);
  assert.match(block, /降到 950 以下恢复/);
  assert.match(block, /已含 FLVX 业务端口（TCP 1 \/ UDP 1）/);
  assert.match(block, /原值 262,144/);
  assert.equal(input(tree, "整机连接上限（TCP+UDP）").props.value, 1000);
  assert.equal(input(tree, "TIME_WAIT 上限").props.value, 200);
  assert.equal(input(tree, "CLOSE_WAIT 超时（秒）").props.value, 10);
  input(tree, "TIME_WAIT 上限").props.onChange({ target: { value: "800" } });
  tree = view.render();
  await nodes(tree).find(n => n.type === "button" && n.props.className === "connection-save").props.onClick();
  assert.deepEqual(view.saved().machine_protection, { enabled: false, max_active: null, max_total: 1000, tw_buckets: 800, close_wait_seconds: 10 });
  // Level 1 「机器 TCP」 is unchanged next to the new whole-machine numbers.
  assert.match(text(section(tree, "connection-machine-section")), /TCP 总数304ESTABLISHED10TIME_WAIT279CLOSE_WAIT2/);
});

test("one server CLOSE_WAIT input: the machine protection one; Core shows what the Helper applies", async () => {
  const data: any = fixture();
  data.settings.machine_protection = { enabled: true, max_active: null, max_total: 1000, tw_buckets: 500, close_wait_seconds: 20 };
  data.settings.default_close_wait_timeout_seconds = null;
  data.record.snapshot.machine_protection = { version: 2, owner: "mmwx", supported: true, configured: data.settings.machine_protection, effective: true, blocking: false, active: 10, total: 304, controlled_ports: [443], tcp_ports: [443], udp_ports: [443], connections: { total: 304, tcp: 300, udp: 4, established: 10, time_wait: 279, close_wait: 2, other_tcp: 9 }, max_total: 1000, release_below: 950, dropped_total: 0, tw_buckets: { target: 500, current: 500, original: 4096 }, close_wait_seconds: 20 };
  const view = dialog(data);
  const tree = view.render();
  const closeWaitInputs = nodes(tree).filter(n => n.type === "label" && /CLOSE_WAIT/.test(text(n)) && nodes(n).some(child => child.type === "input"));
  assert.deepEqual(closeWaitInputs.map(n => text(n)), ["CLOSE_WAIT 超时（秒）"]);
  assert.equal(input(tree, "CLOSE_WAIT 超时（秒）").props.value, 20);
  assert.equal(nodes(tree).filter(n => n.props.className?.includes("machine-field-notes")).length, 1);
  assert.match(text(section(tree, "machine-field-notes")), /CLOSE_WAIT 超时作用于妙妙屋 Core 和 FLVX 共享 Core，默认 10 秒，关闭保护时不生效。0 表示该项不启用。/);
  assert.doesNotMatch(text(tree), /CLOSE_WAIT 超时关闭|以那里为准/);
  assert.match(text(section(tree, "connection-global-settings")), /当前 CLOSE_WAIT \d+自动关闭秒数 20/);
  input(tree, "CLOSE_WAIT 超时（秒）").props.onChange({ target: { value: "0" } });
  await nodes(view.render()).find(n => n.type === "button" && n.props.className === "connection-save").props.onClick();
  assert.equal(view.saved().machine_protection.close_wait_seconds, 0);
  assert.equal(view.saved().default_close_wait_timeout_seconds ?? null, null);
});

test("inbound inputs save their own fields while legacy limits stay collapsed and round-trip", async () => {
  const data = fixture();
  assert.equal(section(dialog(data).render(), "connection-legacy-limits"), undefined);
  Object.assign(data.settings, { global_total_limit_enabled: true, max_global_total_connections: 500 });
  Object.assign(data.settings.management_users[0], { max_total_connections: 300 });
  Object.assign(data.settings.ports[0], { max_total_connections: 200 });
  Object.assign(data.record.snapshot.proxy_users[0], { rejected_user_combined_limit: 12 });
  const view = dialog(data);
  let tree = view.render();
  const legacy = section(tree, "connection-legacy-limits");
  assert.equal(legacy.props.open, undefined);
  assert.match(text(legacy), /旧的 TCP 套接字上限（入站\+出站）/);
  assert.match(text(legacy), /Core（已启用） 500.*用户 alice 300.*端口 in-a 200/);
  assert.match(text(legacy), /用户套接字拒绝 12/);
  assert.doesNotMatch(text(legacy), /端口套接字拒绝/);
  for (const [label, value] of [["Core 连接上限", "41"], ["用户连接上限", "21"], ["端口连接上限", "11"]]) input(tree, label).props.onChange({ target: { value } });
  tree = view.render();
  await nodes(tree).find(n => n.type === "button" && n.props.className === "connection-save").props.onClick();
  assert.equal(view.saved().max_global_inbound_connections, 41);
  assert.equal(view.saved().management_users[0].max_inbound_connections, 21);
  assert.equal(view.saved().ports[0].max_inbound_connections, 11);
  assert.equal(view.saved().max_global_total_connections, 500);
  assert.equal(view.saved().global_total_limit_enabled, true);
  assert.equal(view.saved().management_users[0].max_total_connections, 300);
  assert.equal(view.saved().ports[0].max_total_connections, 200);
  for (const button of nodes(section(view.render(), "connection-legacy-limits")).filter(n => n.type === "button" && text(n).startsWith("清除 "))) button.props.onClick();
  assert.equal(view.settings().max_global_total_connections, null);
  assert.equal(view.settings().global_total_limit_enabled, false);
  assert.equal(view.settings().management_users[0].max_total_connections, null);
  assert.equal(view.settings().ports[0].max_total_connections, null);
  assert.equal(view.settings().max_global_inbound_connections, 41);
});

test("disabled legacy values and non-zero legacy rejections remain visible without changing saved values", async () => {
  const data = fixture();
  Object.assign(data.settings, { max_global_total_connections: 500 });
  Object.assign(data.settings.management_users[0], { max_total_connections: 0 });
  Object.assign(data.record.snapshot.proxy_users[0], { rejected_port_total_limit: 3, rejected_global_total_limit: 4 });
  const view = dialog(data);
  const tree = view.render();
  const legacy = section(tree, "connection-legacy-limits");
  assert.match(text(legacy), /Core（未启用） 500.*用户 alice 0/);
  assert.match(text(legacy), /端口旧总数拒绝 3.*全局套接字拒绝 4/);
  assert.doesNotMatch(text(section(tree, "connection-port-fold")), /旧总数拒绝|套接字拒绝/);
  await nodes(tree).find(n => n.type === "button" && n.props.className === "connection-save").props.onClick();
  assert.deepEqual(view.saved(), data.settings);
});
