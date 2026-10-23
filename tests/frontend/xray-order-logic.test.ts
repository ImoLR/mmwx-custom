import assert from "node:assert/strict";
import test from "node:test";
import { beginOrderDraft, defaultOutboundChange, isProtectedRule, moveToPosition, orderChanged, orderNeedsRestart, orderPayload, ruleMatcherSummary } from "../../frontend/src/xray-order-logic.ts";

test("moves to top, bottom and adjacent positions without mutating input", () => {
  const items = Object.freeze(["a", "b", "c", "d"]);
  assert.deepEqual(moveToPosition(items, 2, "top"), ["c", "a", "b", "d"]);
  assert.deepEqual(moveToPosition(items, 1, "bottom"), ["a", "c", "d", "b"]);
  assert.deepEqual(moveToPosition(items, 2, -1), ["a", "c", "b", "d"]);
  assert.deepEqual(moveToPosition(items, 1, 1), ["a", "c", "b", "d"]);
  assert.deepEqual(items, ["a", "b", "c", "d"]);
});

test("first, last, empty, single and invalid positions stay safe", () => {
  for (const position of ["top", -1] as const) assert.deepEqual(moveToPosition([1, 2], 0, position), [1, 2]);
  for (const position of ["bottom", 1] as const) assert.deepEqual(moveToPosition([1, 2], 1, position), [1, 2]);
  for (const position of ["top", "bottom", -1, 1] as const) {
    assert.deepEqual(moveToPosition([], 0, position), []);
    assert.deepEqual(moveToPosition([1], 0, position), [1]);
    for (const index of [-1, 2, 0.5, NaN]) assert.deepEqual(moveToPosition([1, 2], index, position), [1, 2]);
  }
});

test("protected rule prefix stays untouched when visible rules move", () => {
  const protectedRules = Object.freeze([{ outboundTag: "api" }, { inboundTag: ["tunnel-in"] }]);
  const rules = [{ marktag: "a" }, { marktag: "b" }, { marktag: "c" }];
  const payload = [...protectedRules, ...moveToPosition(rules, 2, "top")];
  assert.deepEqual(payload, [...protectedRules, rules[2], rules[0], rules[1]]);
  assert.equal(payload[0], protectedRules[0]);
  assert.equal(payload[1], protectedRules[1]);
});

test("rule drafts save the whole routing object with protected prefix, and cancel leaves source intact", () => {
  const rules = [{ marktag: "first", domain: ["geosite:openai"] }, { inboundTag: ["tunnel-in"], outboundTag: "direct" }, { outboundTag: "api" }, { marktag: "last", outboundTag: "block" }];
  const routing = { domainStrategy: "AsIs", rules, balancers: [{ tag: "pool" }], domainMatcher: "hybrid" };
  const draft = beginOrderDraft("rule", rules, routing);
  assert.equal(orderChanged(draft), false);
  draft.items = moveToPosition(draft.items, 1, "top");
  assert.equal(orderChanged(draft), true);
  assert.deepEqual(orderPayload(draft), { action: "set", routing: { ...routing, rules: [rules[1], rules[2], rules[3], rules[0]] } });
  assert.equal(defaultOutboundChange(draft), null);
  // Cancelling discards the draft; beginning again reads the unchanged source.
  assert.deepEqual(beginOrderDraft("rule", rules, routing).items, [rules[0], rules[3]]);
  assert.deepEqual(routing.rules, rules);
  assert.equal(isProtectedRule({ inboundTag: ["api"] }), true);
  assert.equal(isProtectedRule({ inboundTag: ["tunnel-user"] }), false);
});

test("outbound draft detects default changes and restores clean state after moving back", () => {
  const items = [{ tag: "direct" }, { tag: "warp-v4" }, { tag: "block" }];
  const draft = beginOrderDraft("outbound", items);
  draft.items = moveToPosition(draft.items, 2, -1);
  assert.equal(defaultOutboundChange(draft), null);
  assert.equal(orderNeedsRestart(draft), false, "non-default reorder keeps Xray running");
  draft.items = moveToPosition(draft.items, 2, "top");
  assert.equal(defaultOutboundChange(draft), "warp-v4");
  assert.equal(orderNeedsRestart(draft), true, "a new default outbound only applies after a restart");
  assert.deepEqual(orderPayload(draft), { action: "reorder", tags: ["warp-v4", "direct", "block"] });
  draft.items = moveToPosition(draft.items, 0, 1);
  assert.equal(orderChanged(draft), false);
  assert.equal(defaultOutboundChange(draft), null);
  assert.deepEqual(items.map((item) => item.tag), ["direct", "warp-v4", "block"]);
});

test("rule matcher shows actual values and additional counts across every supported field", () => {
  assert.equal(ruleMatcherSummary({ domain: ["geosite:openai", "a.test", "b.test"], protocol: ["bittorrent"] }), "域名 geosite:openai +2 · 协议 bittorrent");
  assert.equal(ruleMatcherSummary({ ip: ["geoip:cn"], port: "80,443", network: "tcp", inboundTag: ["vless"], user: ["alice"], source: ["192.0.2.0/24"], sourcePort: "1000-2000", attrs: { key: "value" } }), 'IP geoip:cn · 端口 80,443 · 网络 tcp · 入站 vless · 用户 alice · 来源 192.0.2.0/24 · 来源端口 1000-2000 · 属性 {"key":"value"}');
  assert.equal(ruleMatcherSummary({ domain: [], port: "", type: "field", outboundTag: "direct" }), "全部流量");
});

test("successive draft moves compose and only the final order is saved", () => {
  const rules = [{ outboundTag: "api", inboundTag: ["api"] }, ...["a", "b", "c", "d", "e"].map((marktag) => ({ marktag, outboundTag: "direct" }))];
  const draft = beginOrderDraft("rule", rules, { rules });
  for (const [index, position] of [[4, "top"], [0, "bottom"], [2, -1], [0, 1]] as const) draft.items = moveToPosition(draft.items, index, position);
  assert.deepEqual(draft.items.map((rule) => rule.marktag), ["c", "a", "b", "d", "e"]);
  const payload = orderPayload(draft);
  assert.equal(payload.action, "set");
  assert.deepEqual(payload.action === "set" && payload.routing.rules[0], rules[0]);
  assert.equal(payload.action === "set" && payload.routing.rules.length, rules.length);
});
