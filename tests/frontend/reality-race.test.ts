import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";
import { applyRealityResult, inboundSecurityMode, normalizeInboundFlow, realityTicket, stripRealityLeftovers } from "../../frontend/src/inbound-flow.ts";
import type { XrayObject } from "../../frontend/src/types.ts";

// The real InboundAssistant, templates and sanitizeInbound from xray-manager.tsx,
// driven by a minimal hook runtime so an X25519 response can arrive after the
// protocol was switched (the 「king ss-10017」 inbound of 2026-10-25).
const require = createRequire(new URL("../../frontend/package.json", import.meta.url));
const ts = require("typescript");
const source = readFileSync(new URL("../../frontend/src/xray-manager.tsx", import.meta.url), "utf8");
const file = ts.createSourceFile("xray-manager.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const names = ["asString", "asNumber", "asObject", "getError", "inboundProtocolMode", "inboundTransportMode", "defaultInbound", "defaultInboundForUser", "randomPassword", "randomBase64", "normalizeDomain", "sanitizeInbound", "InboundAssistant"];
const code = ts.transpileModule(file.statements.filter((value: any) => ts.isFunctionDeclaration(value) && names.includes(value.name?.text)).map((value: any) => value.getText(file)).join("\n"), { compilerOptions: { target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React } }).outputText;

type Deferred = { promise: Promise<XrayObject>; resolve: (value: XrayObject) => void };
function deferred(): Deferred {
  let resolve!: (value: XrayObject) => void;
  const promise = new Promise<XrayObject>((done) => { resolve = done; });
  return { promise, resolve };
}

function harness() {
  const slots: any[] = [];
  let index = 0;
  let queued: Array<() => void> = [];
  const keyRequests: Deferred[] = [];
  const hooks = {
    useState(initial: unknown) {
      const at = index++;
      if (!(at in slots)) slots[at] = typeof initial === "function" ? (initial as () => unknown)() : initial;
      return [slots[at], (value: unknown) => { slots[at] = typeof value === "function" ? (value as (old: unknown) => unknown)(slots[at]) : value; }];
    },
    useRef(initial: unknown) {
      const at = index++;
      if (!(at in slots)) slots[at] = { current: initial };
      return slots[at];
    },
    useEffect(effect: () => void | (() => void), deps?: unknown[]) {
      const at = index++;
      const previous = slots[at];
      if (previous && deps && deps.every((value, position) => Object.is(value, previous.deps[position]))) return;
      queued.push(() => { previous?.cleanup?.(); slots[at] = { deps, cleanup: effect() }; });
    },
  };
  const element = () => null;
  const icons = Object.fromEntries(["KeyRound", "Search", "ShieldCheck", "Copy", "RefreshCw", "Check", "Plus", "Trash2", "X", "Globe", "Lock", "Save", "Server", "Users", "Wand2"].map((name) => [name, element]));
  const api = {
    generateXrayX25519: () => { const request = deferred(); keyRequests.push(request); return request.promise; },
    fetchXrayUsers: async () => ({ users: [] }), fetchValidCertificates: async () => ({ certificates: [] }), fetchNginxServerDomains: async () => ({ domains: [] }), fetchXrayServiceStatus: async () => ({}),
    fetchRealityDomains: async () => ({ domains: [] }), probeCustomRealityDomain: async () => ({}), generateXrayProtocolKeys: async () => ({}), setupRemoteSSL: async () => ({}), updateRemoteServerDomain: async () => ({}), updateXrayUserEmail: async () => ({}),
  };
  const deps: Record<string, unknown> = { React: { createElement: element, Fragment: element }, ...hooks, ...icons, ...api, inboundSecurityMode, normalizeInboundFlow, applyRealityResult, realityTicket, stripRealityLeftovers };
  const scope = new Proxy(deps, { has: (target, key) => key in target });
  const compiled = new Function("scope", `with (scope) { ${code}\nreturn { InboundAssistant, defaultInboundForUser, sanitizeInbound, inboundProtocolMode }; }`)(scope);
  let item: XrayObject = compiled.defaultInboundForUser("vless", "king");
  const errors: string[] = [];
  const onChange = (update: XrayObject | ((current: XrayObject) => XrayObject)) => { item = typeof update === "function" ? update(item) : update; };
  const render = () => {
    index = 0;
    compiled.InboundAssistant({ server: { id: 2, name: "Boil HKT 99u", domain: "boilhkt99u.imgamer.top" }, token: "t", item, protocolMode: compiled.inboundProtocolMode(item), expert: true, editing: false, onChange, onError: (message: string) => { if (message) errors.push(message); } });
    const effects = queued;
    queued = [];
    for (const run of effects) run();
  };
  const settle = () => new Promise((done) => setTimeout(done, 0));
  return { compiled, keyRequests, errors, render, settle, get item() { return item; }, set item(value: XrayObject) { item = value; } };
}

const keys = { privateKey: "late-private-key", publicKey: "late-public-key" };

test("an X25519 response that arrives after switching VLESS-Reality → SS-2022 leaves the SS inbound untouched", async () => {
  const editor = harness();
  editor.render();
  assert.equal(editor.keyRequests.length, 1, "opening the VLESS-Reality template asks for keys");
  editor.item = editor.compiled.defaultInboundForUser("shadowsocks2022", "king");
  const switched = JSON.parse(JSON.stringify(editor.item));
  editor.render();
  editor.keyRequests[0].resolve(keys);
  await editor.settle();
  editor.render();
  assert.deepEqual(editor.item, switched, "the late keys must not touch the SS inbound");
  const saved = editor.compiled.sanitizeInbound({ ...editor.item, port: 10018 });
  assert.equal(saved.protocol, "shadowsocks");
  assert.equal(saved.streamSettings.security, undefined);
  assert.equal(saved.streamSettings.realitySettings, undefined);
  assert.equal("reality_guard" in saved, false);
  assert.deepEqual(editor.errors, []);
});

test("keys still land on the Reality inbound they were asked for, and only from its own session", async () => {
  const kept = harness();
  kept.render();
  kept.keyRequests[0].resolve(keys);
  await kept.settle();
  const reality = kept.item.streamSettings as XrayObject;
  assert.equal(reality.security, "reality");
  assert.deepEqual(reality.realitySettings, { dest: "www.lovelive-anime.jp:443", serverNames: ["www.lovelive-anime.jp"], shortIds: [""], ...keys });

  // VLESS-Reality → Trojan-Reality: the VLESS request is dropped, Trojan gets its own keys.
  const trojan = harness();
  trojan.render();
  trojan.item = trojan.compiled.defaultInboundForUser("trojan", "king");
  trojan.render();
  assert.equal(trojan.keyRequests.length, 2, "entering a new Reality protocol asks again");
  trojan.keyRequests[0].resolve({ privateKey: "vless-key", publicKey: "vless-pub" });
  await trojan.settle();
  assert.equal(((trojan.item.streamSettings as XrayObject).realitySettings as XrayObject).privateKey, undefined);
  trojan.keyRequests[1].resolve({ privateKey: "trojan-key", publicKey: "trojan-pub" });
  await trojan.settle();
  assert.equal(((trojan.item.streamSettings as XrayObject).realitySettings as XrayObject).privateKey, "trojan-key");

  // VLESS-Reality → SS → VLESS-Reality before the first answer: only the newest request counts.
  const back = harness();
  back.render();
  back.item = back.compiled.defaultInboundForUser("shadowsocks2022", "king");
  back.render();
  back.item = back.compiled.defaultInboundForUser("vless", "king");
  back.render();
  back.keyRequests[0].resolve({ privateKey: "stale", publicKey: "stale-pub" });
  await back.settle();
  assert.equal(((back.item.streamSettings as XrayObject).realitySettings as XrayObject).privateKey, undefined);
  back.keyRequests[1].resolve({ privateKey: "fresh", publicKey: "fresh-pub" });
  await back.settle();
  assert.equal(((back.item.streamSettings as XrayObject).realitySettings as XrayObject).privateKey, "fresh");
});

test("applyRealityResult and stripRealityLeftovers guard every save path", () => {
  const vless: XrayObject = { protocol: "vless", streamSettings: { network: "tcp", security: "reality", realitySettings: { dest: "a:443" } } };
  const ss: XrayObject = { protocol: "shadowsocks", settings: { method: "2022-blake3-aes-128-gcm" }, streamSettings: { network: "tcp" } };
  const ticket = realityTicket(vless, 3);
  assert.equal(applyRealityResult(ss, ticket, 3, keys), ss);
  assert.equal(applyRealityResult(vless, ticket, 4, keys), vless);
  assert.equal(applyRealityResult({ ...vless, protocol: "trojan" }, ticket, 3, keys).streamSettings, vless.streamSettings);
  assert.equal(applyRealityResult({ ...vless, streamSettings: { network: "tcp", security: "tls" } }, ticket, 3, keys).streamSettings.realitySettings, undefined);
  assert.deepEqual(applyRealityResult(vless, ticket, 3, keys).streamSettings, { network: "tcp", security: "reality", realitySettings: { dest: "a:443", ...keys } });

  // The saved 「king ss-10017」 of 2026-10-25.
  const broken: XrayObject = { tag: "king ss-10017", protocol: "shadowsocks", port: 10017, reality_guard: false, settings: { method: "2022-blake3-aes-128-gcm", network: "tcp,udp" }, streamSettings: { network: "tcp", security: "reality", realitySettings: { privateKey: "p", publicKey: "q" } } };
  assert.deepEqual(stripRealityLeftovers(broken), { tag: "king ss-10017", protocol: "shadowsocks", port: 10017, settings: { method: "2022-blake3-aes-128-gcm", network: "tcp,udp" }, streamSettings: { network: "tcp" } });
  assert.equal(broken.reality_guard, false, "the input is not mutated");
  // Leaving Reality for TLS drops the old Reality block; a Reality inbound keeps all of it.
  assert.deepEqual(stripRealityLeftovers({ protocol: "vless", reality_guard: true, streamSettings: { security: "tls", tlsSettings: {}, realitySettings: { dest: "a:443" } } }), { protocol: "vless", streamSettings: { security: "tls", tlsSettings: {} } });
  const guarded = { protocol: "vless", reality_guard: true, streamSettings: { security: "reality", realitySettings: { dest: "a:443" } } };
  assert.equal(stripRealityLeftovers(guarded), guarded);
  const plain = { protocol: "vmess", streamSettings: { security: "none" } };
  assert.equal(stripRealityLeftovers(plain), plain);
});

test("every async Reality path takes its ticket before awaiting", () => {
  const assistant = source.slice(source.indexOf("function InboundAssistant("), source.indexOf("function InboundAssistant(") + 12000);
  for (const name of ["loadRealityDomains", "probeDomain", "generateRealityKeys"]) {
    const body = assistant.slice(assistant.indexOf(`async function ${name}(`), assistant.indexOf(`async function ${name}(`) + 1500);
    assert.ok(body.indexOf("realityTicket(item, realitySession.current)") > 0 && body.indexOf("realityTicket(") < body.indexOf("await "), `${name} takes a ticket before its request`);
  }
  assert.doesNotMatch(assistant.slice(assistant.indexOf("function updateReality("), assistant.indexOf("function selectRealityDomain(")), /security: "reality"/);
  assert.match(source, /return normalizeInboundFlow\(stripRealityLeftovers\(inbound\), inboundSecurityMode\(item\)\);/);
});
