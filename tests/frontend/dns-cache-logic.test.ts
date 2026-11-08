import assert from "node:assert/strict";
import test from "node:test";
import { dnsCacheEnsureText, dnsCacheView } from "../../frontend/src/dns-cache-logic.ts";

test("⚠ only when the Helper reports the cache missing or broken", () => {
  assert.equal(dnsCacheView({ mode: "dnsmasq", ok: true }, true).attention, false);
  assert.equal(dnsCacheView({ mode: "dnsmasq", ok: true }, true).label, "正常");
  assert.match(dnsCacheView({ mode: "resolved", ok: true }, true).detail, /systemd-resolved/);
  const minimal = dnsCacheView({ mode: "minimal", ok: true, reason: "可用内存 40 MB（低于 60 MB）" }, true);
  assert.deepEqual([minimal.label, minimal.attention, minimal.tone], ["精简模式", false, "neutral"]);
  assert.match(minimal.detail, /可用内存 40 MB/);
  const down = dnsCacheView({ mode: "dnsmasq", ok: false, reason: "本机缓存 flvx-dnscache 未运行" }, true);
  assert.deepEqual([down.label, down.attention, down.tone], ["未运行", true, "warning"]);
  assert.match(down.detail, /^本机缓存 flvx-dnscache 未运行。/);
  const none = dnsCacheView({ mode: "none", ok: false, reason: "未启用本机 DNS 缓存（只有 1 个解析器）" }, true);
  assert.deepEqual([none.label, none.attention], ["未启用", true]);
});

test("offline or old Helpers are 未知, never ⚠", () => {
  assert.deepEqual([dnsCacheView(undefined, true).label, dnsCacheView(undefined, true).attention], ["未知", false]);
  assert.match(dnsCacheView(undefined, true).detail, /升级 Helper/);
  const stale = dnsCacheView({ mode: "none", ok: false, reason: "x" }, false);
  assert.deepEqual([stale.label, stale.attention], ["未知", false]);
});

test("install / upgrade ensure line", () => {
  const time = (value: string) => `T(${value})`;
  assert.equal(dnsCacheEnsureText(undefined, time), "");
  assert.equal(dnsCacheEnsureText({ mode: "dnsmasq", ok: true }, time), "");
  assert.match(dnsCacheEnsureText({ mode: "dnsmasq", ok: true, ensure: { helper_version: "v0.7.25", exit_code: 0 } }, time), /正在执行/);
  assert.equal(
    dnsCacheEnsureText({ mode: "dnsmasq", ok: true, ensure: { helper_version: "v0.7.25", finished_at: "2026-11-09T00:00:00Z", exit_code: 0, summary: "DNS 缓存：本机 dnsmasq（127.0.0.1，上游 1.1.1.1 8.8.8.8）无需改动" } }, time),
    "Helper v0.7.25 安装 / 升级时检查过（T(2026-11-09T00:00:00Z)）：DNS 缓存：本机 dnsmasq（127.0.0.1，上游 1.1.1.1 8.8.8.8）无需改动",
  );
  assert.match(
    dnsCacheEnsureText({ mode: "none", ok: false, ensure: { helper_version: "v0.7.25", finished_at: "2026-11-09T00:00:00Z", exit_code: 1, summary: "DNS 缓存：精简模式（apt 安装 dnsmasq-base 失败）" } }, time),
    /没有配置完成.*精简模式.*不影响 Helper 使用/,
  );
});
