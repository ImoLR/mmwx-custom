export type MovePosition = "top" | "bottom" | -1 | 1;

export function moveToPosition<T>(items: readonly T[], index: number, position: MovePosition): T[] {
  const next = [...items];
  if (!Number.isInteger(index) || index < 0 || index >= items.length) return next;
  const target = position === "top" ? 0 : position === "bottom" ? items.length - 1 : Math.max(0, Math.min(items.length - 1, index + position));
  const [item] = next.splice(index, 1);
  next.splice(target, 0, item);
  return next;
}

// Keep the hidden control-plane rules intact at the front of every saved draft.
type XrayItem = Record<string, unknown>;
export function isProtectedRule(rule: XrayItem) {
  return rule.outboundTag === "api" || (Array.isArray(rule.inboundTag) && rule.inboundTag.some((tag) => tag === "api" || tag === "tunnel-in"));
}

export type OrderDraft = {
  kind: "rule" | "outbound";
  original: XrayItem[];
  items: XrayItem[];
  routing: XrayItem;
  protectedRules: XrayItem[];
};

export function beginOrderDraft(kind: OrderDraft["kind"], items: XrayItem[], routing: XrayItem = {}): OrderDraft {
  const original = kind === "rule" ? items.filter((item) => !isProtectedRule(item)) : [...items];
  return { kind, original, items: [...original], routing, protectedRules: kind === "rule" ? items.filter(isProtectedRule) : [] };
}

export function orderChanged(draft: OrderDraft) {
  return draft.items.some((item, index) => item !== draft.original[index]);
}

export function defaultOutboundChange(draft: OrderDraft) {
  return draft.kind === "outbound" && draft.items[0]?.tag !== draft.original[0]?.tag ? String(draft.items[0]?.tag || "") : null;
}

// A running Xray only picks up rule order and its default outbound (the first
// one) on restart; reordering other outbounds changes nothing at runtime.
export function orderNeedsRestart(draft: OrderDraft) {
  return orderChanged(draft) && (draft.kind === "rule" || defaultOutboundChange(draft) !== null);
}

export function orderPayload(draft: OrderDraft) {
  return draft.kind === "rule"
    ? { action: "set" as const, routing: { ...draft.routing, rules: [...draft.protectedRules, ...draft.items] } }
    : { action: "reorder" as const, tags: draft.items.map((item) => String(item.tag || "")) };
}

export function ruleMatcherSummary(rule: XrayItem) {
  const labels: Record<string, string> = { domain: "域名", ip: "IP", port: "端口", protocol: "协议", network: "网络", inboundTag: "入站", user: "用户", source: "来源", sourcePort: "来源端口", attrs: "属性" };
  return Object.entries(labels).flatMap(([key, label]) => {
    const value = rule[key];
    const values = (Array.isArray(value) ? value : value == null || value === "" ? [] : [value]).map((entry) => typeof entry === "object" ? JSON.stringify(entry) : String(entry));
    return values.length ? [`${label} ${values[0]}${values.length > 1 ? ` +${values.length - 1}` : ""}`] : [];
  }).join(" · ") || "全部流量";
}
