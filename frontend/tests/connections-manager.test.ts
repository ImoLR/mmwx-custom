import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import vm from "node:vm";
import test from "node:test";
import ts from "typescript";
import React from "react";

const require = createRequire(import.meta.url);
const source = readFileSync(new URL("../src/connections-manager.tsx", import.meta.url), "utf8");
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

test("server card formatting still uses available connection_count, including its original badge", () => {
  const main = readFileSync(new URL("../src/main.tsx", import.meta.url), "utf8");
  const ast = ts.createSourceFile("main.tsx", main, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const fn = ast.statements.find(s => ts.isFunctionDeclaration(s) && s.name?.text === "formatConnectionCount")!;
  const code = ts.transpileModule(fn.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText;
  const format = vm.runInNewContext(`${code}; formatConnectionCount`);
  assert.equal(format({ available: true, connection_count: 396, tcp_count: 392, udp_count: 4 }), "396");
  assert.equal(format({ available: true, connection_count: 0 }), "0");
  assert.equal(format({ available: false, connection_count: 396 }), "--");
  assert.equal(format(), "--");
  assert.ok(main.includes('className="service-badge success connection-tag"'));
  assert.ok(main.includes('title="具体连接数"'));
  assert.ok(main.includes('<span className="connection-tag-value">{formatConnectionCount(connectionMetric)}</span>'));
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
