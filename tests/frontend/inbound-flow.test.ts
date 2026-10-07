import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";
import { inboundFlowWarning, inboundSecurityMode, nodeInboundFlow, normalizeInboundFlow, relayPortChangeWarning } from "../../frontend/src/inbound-flow.ts";
import type { XrayNode, XrayObject } from "../../frontend/src/types.ts";

const vision = "xtls-rprx-vision";
const inbound: XrayObject = { tag: "usb vless", protocol: "vless", port: 10020, settings: { decryption: "none", clients: [{ id: "ADMIN-UUID", email: "admin", level: 0 }, { id: "package-uuid", email: "usb", level: 1, flow: vision }] }, streamSettings: { network: "tcp", security: "reality", realitySettings: { privateKey: "keep-key" } } };
const node: XrayNode = { id: 104, node_name: "usb vless", original_server: "Boil", inbound_tag: "usb vless", protocol: "vless", parsed_config: JSON.stringify({ type: "vless", uuid: "admin-uuid", server: "origin.example", port: 10020, flow: vision }) };
const relay: XrayNode = { ...node, id: 105, node_name: "usb vless-relay", relay_orig_server: "origin.example", relay_orig_port: 10020, parsed_config: JSON.stringify({ type: "vless", uuid: "ADMIN-UUID", server: "relay.example", port: 57776, flow: vision }) };

test("mixed client flows are inconsistent instead of silently selecting Vision", () => {
  for (const protocol of ["vless", "trojan"]) {
    const item = { ...inbound, protocol };
    assert.match(inboundFlowWarning(item), /流控不一致：部分用户带 Vision/);
    assert.equal(inboundSecurityMode(item), "inconsistent");
    assert.throws(() => normalizeInboundFlow(item), /请先选择/);
    assert.equal(inboundSecurityMode({ ...item, _wizard_security: "REALITY" }), "REALITY");
    assert.match(inboundFlowWarning({ ...item, _wizard_security: "REALITY" }), /流控不一致/);
  }
  assert.equal(inboundFlowWarning({ ...inbound, protocol: "vmess" }), "");
  assert.equal(inboundFlowWarning({ ...inbound, settings: { clients: [{ id: "a" }, { id: "b", flow: "" }] } }), "");
  assert.equal(inboundSecurityMode(normalizeInboundFlow(inbound, "XTLS-Vision-REALITY")), "XTLS-Vision-REALITY");
  assert.equal(inboundSecurityMode(normalizeInboundFlow(inbound, "REALITY")), "REALITY");
  assert.equal(inboundSecurityMode({ ...normalizeInboundFlow(inbound, "XTLS-Vision"), streamSettings: { security: "tls" } }), "XTLS-Vision");
});

test("normalization preserves every identity and unrelated option without mutating the input", () => {
  const original = JSON.stringify(inbound);
  for (const protocol of ["vless", "trojan"]) {
    for (const mode of ["XTLS-Vision", "XTLS-Vision-REALITY", "REALITY", "TLS", "None", "Encryption"]) {
      const result = normalizeInboundFlow({ ...inbound, protocol, _wizard_security: mode });
      const values = (result.settings as XrayObject).clients as XrayObject[];
      assert.deepEqual(values.map(({ flow, ...client }) => client), [{ id: "ADMIN-UUID", email: "admin", level: 0 }, { id: "package-uuid", email: "usb", level: 1 }]);
      assert.ok(values.every((client) => mode.includes("Vision") ? client.flow === vision : !("flow" in client)));
      assert.deepEqual(result.streamSettings, inbound.streamSettings);
    }
  }
  assert.equal(JSON.stringify(inbound), original);
  const vmess = { ...inbound, protocol: "vmess" };
  assert.equal(normalizeInboundFlow(vmess, "TLS"), vmess);
});

test("node flow checks use the matching identity for direct and relay links", () => {
  for (const value of [node, relay]) {
    const result = nodeInboundFlow(value, [inbound]);
    assert.equal(result?.matched, true);
    assert.equal(result?.mismatch, true);
    assert.match(result?.warning || "", /流控不一致/);
  }
  const packageNode = { ...node, parsed_config: JSON.stringify({ type: "vless", uuid: "package-uuid", flow: vision }) };
  assert.equal(nodeInboundFlow(packageNode, [inbound])?.mismatch, false);
  assert.match(nodeInboundFlow(packageNode, [inbound])?.mixed || "", /部分用户带 Vision/);
  assert.equal(nodeInboundFlow(node, [normalizeInboundFlow(inbound, "XTLS-Vision-REALITY")])?.warning, "");
  const plain = { ...node, parsed_config: JSON.stringify({ type: "vless", uuid: "ADMIN-UUID" }) };
  assert.equal(nodeInboundFlow(plain, [normalizeInboundFlow(inbound, "REALITY")])?.warning, "");
  assert.equal(nodeInboundFlow({ ...plain, inbound_tag: "other" }, [inbound]), null);
  assert.equal(nodeInboundFlow({ ...plain, node_type: "routed" }, [inbound]), null);
});

test("missing or unrelated credentials do not prove mismatch; duplicate matching clients are all checked", () => {
  const missing = { ...node, parsed_config: JSON.stringify({ type: "vless", uuid: "missing", flow: vision }) };
  assert.equal(nodeInboundFlow(missing, [normalizeInboundFlow(inbound, "REALITY")])?.mismatch, false);
  assert.equal(nodeInboundFlow(missing, [inbound])?.matched, false);
  const duplicate = { ...inbound, settings: { clients: [{ id: "ADMIN-UUID", flow: vision }, { id: "admin-uuid" }] } };
  assert.equal(nodeInboundFlow(node, [duplicate])?.mismatch, true);
  const trojan = { ...inbound, protocol: "trojan", settings: { clients: [{ password: "SECRET" }] } };
  const trojanNode = { ...node, protocol: "trojan", parsed_config: JSON.stringify({ type: "trojan", password: "SECRET", flow: vision }) };
  assert.equal(nodeInboundFlow(trojanNode, [trojan])?.mismatch, true);
  assert.equal(nodeInboundFlow({ ...trojanNode, parsed_config: JSON.stringify({ type: "trojan", password: "secret", flow: vision }) }, [trojan])?.matched, false);
  assert.equal(nodeInboundFlow({ ...node, clash_config: JSON.stringify({ type: "vless", uuid: "admin-uuid" }) }, [normalizeInboundFlow(inbound, "REALITY")])?.mismatch, false);
});

test("port warning is scoped to the same managed inbound and ignores the relay entry port", () => {
  const changed = { ...inbound, port: 10021 };
  assert.match(relayPortChangeWarning(inbound, changed, [node, relay], { name: "Boil" }), /10020 改为 10021.*usb vless-relay.*外部中转转发规则必须手动更新/);
  assert.equal(relayPortChangeWarning(inbound, inbound, [relay], { name: "Boil" }), "");
  assert.equal(relayPortChangeWarning(inbound, changed, [node], { name: "Boil" }), "");
  assert.equal(relayPortChangeWarning(inbound, changed, [{ ...relay, inbound_tag: "other" }], { name: "Boil" }), "");
  assert.equal(relayPortChangeWarning(inbound, changed, [relay], { name: "Other" }), "");
  assert.equal(relayPortChangeWarning({}, changed, [relay], { name: "Boil" }), "");
});

const require = createRequire(new URL("../../frontend/package.json", import.meta.url));
const ts = require("typescript");
const source = readFileSync(new URL("../../frontend/src/xray-manager.tsx", import.meta.url), "utf8");
const file = ts.createSourceFile("xray-manager.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
function find(scope: any, predicate: (value: any) => boolean): any {
  if (predicate(scope)) return scope;
  return ts.forEachChild(scope, (child: any) => find(child, predicate));
}
function compileSave(component: string, name: string, dependencies: Record<string, unknown>) {
  const scope = component ? file.statements.find((value: any) => ts.isFunctionDeclaration(value) && value.name?.text === component) : file;
  const handler = find(scope, (value) => (ts.isFunctionDeclaration(value) || ts.isVariableDeclaration(value)) && value.name?.text === name);
  assert.ok(handler, `${component} ${name} handler exists`);
  const helpers = file.statements.filter((value: any) => ts.isFunctionDeclaration(value) && ["asString", "asNumber", "asObject", "sanitizeInbound", "managementAssignments", "getError"].includes(value.name?.text));
  const code = ts.transpileModule(`${helpers.map((value: any) => value.getText(file)).join("\n")}\n${ts.isVariableDeclaration(handler) ? "const " : ""}${handler.getText(file)}`, { compilerOptions: { target: ts.ScriptTarget.ES2020 } }).outputText;
  const deps = { inboundSecurityMode, normalizeInboundFlow, nodeInboundFlow, ...dependencies };
  return new Function(...Object.keys(deps), `${code}\nreturn ${name};`)(...Object.values(deps));
}

test("all three actual inbound save paths normalize every client, including unchanged loaded settings", async () => {
  for (const protocol of ["vless", "trojan"]) for (const mode of ["REALITY", "XTLS-Vision-REALITY"]) for (const path of ["inbound-add", "inbound-update", "node-add", "node-edit"]) {
    const payloads: any[] = [], errors: string[] = [];
    const server = { id: 12, name: "Boil", xray_mode: "external" };
    const item = { ...inbound, protocol, _wizard_security: mode };
    const noop = () => undefined;
    const dependencies = {
      server, token: "test", node, editor: { kind: "inbound", item, ...(path === "inbound-update" ? { originalTag: inbound.tag } : {}) },
      state: { server, item, originalTag: inbound.tag }, selectedServers: [server],
      setBusy: noop, setEditor: noop, setError: (value: string) => { if (value) errors.push(value); },
      setNotice: (value: any) => { if (value?.kind === "error") errors.push(value.text); },
      mutateXrayInbound: async (_token: string, id: number, body: any) => { assert.equal(id, 12); payloads.push(body); return { success: true }; },
      assignConnectionPort: noop, refreshInbounds: noop, checkServers: noop, ipSelection: () => ({ v4: true, v6: false }),
      onCreated: noop, onInboundCreated: undefined, onSaved: noop, onClose: noop,
    };
    const save = path.startsWith("inbound-") ? compileSave("", "saveEditor", dependencies) : compileSave(path === "node-add" ? "ManagedNodeCreateDialog" : "ManagedNodeEditDialog", "save", dependencies);
    await save(item);
    assert.deepEqual(errors, [], `${path} ${protocol} ${mode}`);
    assert.equal(payloads.length, 1);
    assert.equal(payloads[0].action, path.endsWith("add") ? "add" : "update");
    const values = payloads[0].inbound.settings.clients;
    assert.equal(values.length, 2);
    assert.ok(values.every((client: XrayObject) => mode.includes("Vision") ? client.flow === vision : !("flow" in client)), `${path} ${protocol} ${mode}`);
    assert.equal("_wizard_security" in payloads[0].inbound, false);
  }
});

test("repair re-reads the inbound, preserves newly added clients and verifies official output", async () => {
  const newer = { ...inbound, settings: { ...(inbound.settings as XrayObject), clients: [...((inbound.settings as XrayObject).clients as XrayObject[]), { id: "new-user", email: "new-user" }] } };
  const payloads: any[] = [], errors: string[] = [];
  let reads = 0, refreshed = 0, closed = 0;
  const noop = () => undefined;
  const deps = {
    server: { id: 12 }, token: "test", node, state: nodeInboundFlow(node, [inbound]), vision: true,
    setBusy: noop, setError: (value: string) => { if (value) errors.push(value); },
    fetchXrayInbounds: async () => ({ success: true, inbounds: [++reads === 1 ? newer : payloads[0].inbound] }),
    fetchXrayNodes: async () => ({ success: true, nodes: [node] }),
    mutateXrayInbound: async (_token: string, _id: number, body: any) => { payloads.push(body); return { success: true }; },
    onSaved: async () => { refreshed++; }, onClose: () => { closed++; },
  };
  await compileSave("NodeFlowRepairDialog", "save", deps)();
  assert.deepEqual(errors, []);
  assert.equal(payloads[0].action, "update");
  assert.equal(payloads[0].inbound.settings.clients.length, 3);
  assert.ok(payloads[0].inbound.settings.clients.every((client: XrayObject) => client.flow === vision));
  assert.equal(reads, 2); assert.equal(refreshed, 1); assert.equal(closed, 1);
  reads = 0; payloads.length = 0; errors.length = 0; closed = 0;
  await compileSave("NodeFlowRepairDialog", "save", { ...deps, fetchXrayInbounds: async () => ({ success: true, inbounds: [inbound] }) })();
  assert.equal(closed, 0);
  assert.match(errors[0], /尚未一致.*官方侧修复/);
  payloads.length = 0; errors.length = 0;
  await compileSave("NodeFlowRepairDialog", "save", { ...deps, fetchXrayInbounds: async () => ({ success: false, inbounds: [inbound] }) })();
  assert.equal(payloads.length, 0);
  assert.match(errors[0], /读取服务器入站失败/);
  payloads.length = 0; errors.length = 0;
  await compileSave("NodeFlowRepairDialog", "save", { ...deps, fetchXrayInbounds: async () => ({ success: true, inbounds: [{ ...inbound, streamSettings: { security: "none" } }] }) })();
  assert.equal(payloads.length, 0);
  assert.match(errors[0], /Vision 需要 TLS 或 REALITY/);
});

test("repair defaults to the selected node link's Vision choice", async () => {
  for (const flow of [vision, ""]) {
    let choice: boolean | undefined;
    const value = { ...node, parsed_config: JSON.stringify({ type: "vless", uuid: "ADMIN-UUID", flow }) };
    await compileSave("NodeFlowRepairDialog", "load", {
      active: true, server: { id: 12 }, token: "test", node: value,
      fetchXrayInbounds: async () => ({ success: true, inbounds: [inbound] }),
      setState: () => undefined, setVision: (value: boolean) => { choice = value; },
      setError: (value: string) => assert.fail(value),
    })();
    assert.equal(choice, flow === vision);
  }
});

test("the real editor displays both mixed-flow and relay port warnings, and card badges distinguish healthy nodes", () => {
  const React = require("react"), { renderToStaticMarkup } = require("react-dom/server");
  const helpers = file.statements.filter((value: any) => ts.isFunctionDeclaration(value) && ["asString", "asNumber", "asObject", "cloneObject", "ObjectEditor"].includes(value.name?.text));
  const code = ts.transpileModule(helpers.map((value: any) => value.getText(file)).join("\n"), { compilerOptions: { target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React } }).outputText;
  let stateIndex = 0;
  const noop = () => null;
  const deps = {
    React, useCallback: React.useCallback,
    useState: (initial: unknown) => React.useState(stateIndex++ === 0 ? { ...inbound, port: 10021 } : initial),
    inboundFlowWarning, relayPortChangeWarning,
    InboundStructuredEditor: noop, X: noop, Eye: noop, Save: noop, Braces: noop,
  };
  const Editor = new Function(...Object.keys(deps), `${code}\nreturn ObjectEditor;`)(...Object.values(deps));
  const html = renderToStaticMarkup(React.createElement(Editor, { editor: { kind: "inbound", item: inbound, originalTag: inbound.tag }, server: { name: "Boil" }, nodes: [node, relay], token: "test", username: "admin", inbounds: [inbound], outbounds: [], balancers: [], usedPorts: [10020], pending: false, onCancel: noop, onSave: noop }));
  assert.match(html, /role="alert">流控不一致：部分用户带 Vision/);
  assert.match(html, /外部中转转发规则必须手动更新/);
  const cardFile = ts.createSourceFile("node-card-extras.tsx", readFileSync(new URL("../../frontend/src/node-card-extras.tsx", import.meta.url), "utf8"), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const badge = cardFile.statements.find((value: any) => ts.isFunctionDeclaration(value) && value.name?.text === "NodeCardBadges");
  const badgeCode = ts.transpileModule(badge.getText(cardFile).replace(/^export /, ""), { compilerOptions: { target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React } }).outputText;
  const Badges = new Function("React", `${badgeCode}\nreturn NodeCardBadges;`)(React);
  const renderBadge = (item: XrayObject) => renderToStaticMarkup(React.createElement(Badges, { node, state: { flow: nodeInboundFlow(node, [item]), tunnels: [] }, servers: [] }));
  assert.match(renderBadge(inbound), /class="bad".*流控不一致/);
  assert.doesNotMatch(renderBadge(normalizeInboundFlow(inbound, "XTLS-Vision-REALITY")), /流控不一致/);
});
