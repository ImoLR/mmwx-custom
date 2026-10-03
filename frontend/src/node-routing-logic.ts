export type NodeRoutingRule = Record<string, unknown>;
export type IndexedNodeRule = { rule: NodeRoutingRule; originalIndex: number };

function list(rule: NodeRoutingRule, key: string) {
  return Array.isArray(rule[key]) ? rule[key] as unknown[] : [];
}

export function isNodeCatchAllRule(rule: NodeRoutingRule) {
  return !["domain", "ip", "protocol", "source", "user"].some((key) => list(rule, key).length) && !rule.port && !rule.network;
}

export function splitNodeRoutingRules(rules: NodeRoutingRule[], inboundTag: string, routedOutboundTag = "") {
  const dedicatedRules: IndexedNodeRule[] = [];
  const globalRules: IndexedNodeRule[] = [];
  const routedIndex = routedOutboundTag ? rules.findIndex((rule) => rule.outboundTag === routedOutboundTag) : -1;
  rules.forEach((rule, originalIndex) => {
    if (rule.outboundTag === "api" || list(rule, "inboundTag").includes("api")) return;
    const userRoute = list(rule, "user").length > 0 && Boolean(rule.outboundTag) && !["block", "direct"].includes(String(rule.outboundTag));
    const dedicated = Boolean(inboundTag) && list(rule, "inboundTag").includes(inboundTag);
    if (routedOutboundTag) {
      if (rule.outboundTag === routedOutboundTag || (routedIndex >= 0 && originalIndex < routedIndex && dedicated)) dedicatedRules.push({ rule, originalIndex });
      else if (!userRoute) globalRules.push({ rule, originalIndex });
    } else if (!userRoute) {
      if (dedicated) dedicatedRules.push({ rule, originalIndex });
      else if (!list(rule, "inboundTag").length) globalRules.push({ rule, originalIndex });
    }
  });
  const catchAll = dedicatedRules.find(({ rule }) => isNodeCatchAllRule(rule));
  return { dedicatedRules, globalRules, catchAll };
}

export function routingAppliedHot(response: { hot_applied?: unknown }) {
  return response.hot_applied === true;
}

export function withRoutingTarget(rule: NodeRoutingRule, target: string): NodeRoutingRule {
  const next = { ...rule };
  delete next.outboundTag;
  delete next.balancerTag;
  if (target.startsWith("balancer:")) next.balancerTag = target.slice(9);
  else next.outboundTag = target;
  return next;
}

export function routedLabelFor(name: string, random = Math.random()) {
  const clean = name.replace(/[^a-zA-Z0-9-]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 32);
  const suffix = random.toString(36).slice(2, 6) || "r1zz";
  return `${(clean ? `rout-${clean}` : "rout-node").slice(0, 32 - suffix.length - 1)}-${suffix}`;
}

export const nodeRoutingQuickRules: Array<{ name: string; rule: NodeRoutingRule; needOutbound?: boolean }> = [
  { name: "禁止 BT", rule: { type: "field", protocol: ["bittorrent"], marktag: "ban_bt", outboundTag: "block" } },
  { name: "禁止访问大陆 IP", rule: { type: "field", ip: ["geoip:cn"], marktag: "ban_geoip_cn", outboundTag: "block" } },
  { name: "OpenAI 直连", rule: { type: "field", domain: ["geosite:openai"], marktag: "fix_openai", outboundTag: "direct" } },
  { name: "禁止内网访问", rule: { type: "field", ip: ["geoip:private"], marktag: "ban_private", outboundTag: "block" } },
  { name: "RFC EMBY (需选择出站)", rule: { type: "field", domain: ["rfc.uhdnow.com"], network: "tcp", marktag: "rfc_emby" }, needOutbound: true },
  { name: "抖音解锁 (需选择出站)", rule: { type: "field", domain: ["geosite:tiktok"], marktag: "tiktok_unlock" }, needOutbound: true },
  { name: "AI 分流 (需选择出站)", rule: { type: "field", domain: ["geosite:category-ai-!cn", "domain:openai.com", "domain:chatgpt.com", "domain:oaistatic.com", "domain:oaiusercontent.com", "domain:sora.com", "domain:anthropic.com", "domain:claude.ai", "domain:claude.com", "domain:claudeusercontent.com", "domain:gemini.google.com", "domain:bard.google.com", "domain:aistudio.google.com", "domain:generativelanguage.googleapis.com", "domain:gemini.gstatic.com", "domain:grok.com", "domain:x.ai", "domain:perplexity.ai", "domain:pplx.ai", "domain:copilot.microsoft.com", "domain:githubcopilot.com"], marktag: "ai_route" }, needOutbound: true },
  { name: "防止送中 (走 WARP)", rule: { type: "field", domain: ["geosite:google", "geosite:meta"], marktag: "warp_anti_china", outboundTag: "warp-v4" } },
];

export function nodeRuleName(rule: NodeRoutingRule) {
  if (String(rule.marktag || "").startsWith("mmwx-reality-guard-")) return "Reality 防盗";
  return nodeRoutingQuickRules.find((item) => item.rule.marktag === rule.marktag)?.name.replace(" (需选择出站)", "") || String(rule.marktag || "");
}
