import assert from "node:assert/strict";
import test from "node:test";
import { isNodeCatchAllRule, nodeRoutingQuickRules, nodeRuleName, routedLabelFor, routingAppliedHot, splitNodeRoutingRules, withRoutingTarget } from "../../frontend/src/node-routing-logic.ts";

test("dedicated rules match inbound entries exactly and retain original delete indexes", () => {
  const rules = [{ inboundTag: ["api"], outboundTag: "api" }, { inboundTag: ["node-a", "node-b"], ip: ["geoip:private"], outboundTag: "block" }, { inboundTag: ["node-ab"], outboundTag: "direct" }, { domain: ["geosite:openai"], outboundTag: "direct" }, { inboundTag: ["node-a"], user: ["routed-user"], outboundTag: "routed:p1:hk" }, { inboundTag: ["node-a"], balancerTag: "hk" }];
  const split = splitNodeRoutingRules(rules, "node-a");
  assert.deepEqual(split.dedicatedRules.map((item) => item.originalIndex), [1, 5]);
  assert.deepEqual(split.globalRules.map((item) => item.originalIndex), [3]);
  assert.equal(split.catchAll?.originalIndex, 5);
  assert.equal(isNodeCatchAllRule({ inboundTag: ["node-a"], network: "tcp", outboundTag: "hk" }), false);
  assert.equal(isNodeCatchAllRule({ inboundTag: ["node-a"], user: ["a"], outboundTag: "hk" }), false);
});

test("routed children keep guards before their dedicated user route", () => {
  const rules = [{ inboundTag: ["a"], protocol: ["bittorrent"], outboundTag: "block" }, { inboundTag: ["a"], user: ["u"], outboundTag: "routed:p1:hk" }, { inboundTag: ["a"], outboundTag: "direct" }, { user: ["other"], outboundTag: "routed:p1:us" }];
  const split = splitNodeRoutingRules(rules, "a", "routed:p1:hk");
  assert.deepEqual(split.dedicatedRules.map((item) => item.originalIndex), [0, 1]);
  assert.deepEqual(split.globalRules.map((item) => item.originalIndex), [2]);
  assert.equal(split.catchAll, undefined);
});

test("only official hot_applied true skips restart and routing targets are exclusive", () => {
  assert.equal(routingAppliedHot({ hot_applied: true }), true);
  for (const value of [undefined, false, "true", 1]) assert.equal(routingAppliedHot({ hot_applied: value }), false);
  assert.deepEqual(withRoutingTarget({ type: "field", outboundTag: "direct" }, "balancer:hk"), { type: "field", balancerTag: "hk" });
  assert.deepEqual(withRoutingTarget({ type: "field", balancerTag: "hk" }, "direct"), { type: "field", outboundTag: "direct" });
});

test("routed labels follow official clean-name, random suffix and length limit", () => {
  const random = 0.123456789;
  const suffix = random.toString(36).slice(2, 6);
  assert.equal(routedLabelFor("🇭🇰 HK #1", random), `rout-HK-1-${suffix}`);
  assert.equal(routedLabelFor("香港", random), `rout-node-${suffix}`);
  assert.equal(routedLabelFor("A".repeat(100), random).length, 32);
  assert.match(routedLabelFor("A".repeat(100), random), /^[a-zA-Z0-9-]{2,32}$/);
  assert.equal(routedLabelFor("HK", 0), "rout-HK-r1zz");
  assert.notEqual(routedLabelFor("HK", 0.1), routedLabelFor("HK", 0.2));
});

test("official quick rules include AI domains and recognize Reality guards without fabricating them", () => {
  assert.equal(nodeRuleName({ marktag: "mmwx-reality-guard-vless" }), "Reality 防盗");
  assert.equal(nodeRuleName({ marktag: "ban_bt" }), "禁止 BT");
  assert.equal(nodeRoutingQuickRules.length, 8);
  assert.deepEqual(nodeRoutingQuickRules.find((item) => item.rule.marktag === "ai_route")?.rule.domain, ["geosite:category-ai-!cn", "domain:openai.com", "domain:chatgpt.com", "domain:oaistatic.com", "domain:oaiusercontent.com", "domain:sora.com", "domain:anthropic.com", "domain:claude.ai", "domain:claude.com", "domain:claudeusercontent.com", "domain:gemini.google.com", "domain:bard.google.com", "domain:aistudio.google.com", "domain:generativelanguage.googleapis.com", "domain:gemini.gstatic.com", "domain:grok.com", "domain:x.ai", "domain:perplexity.ai", "domain:pplx.ai", "domain:copilot.microsoft.com", "domain:githubcopilot.com"]);
});
