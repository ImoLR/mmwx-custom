import assert from "node:assert/strict";
import test from "node:test";
import {
  DNS_CACHE_REPAIR_WAIT_MS,
  dnsCacheEnsureText,
  dnsCacheRepairButton,
  dnsCacheRepairOutcome,
  dnsCacheRepairProgress,
  dnsCacheRepairRunning,
  dnsCacheView,
} from "../../frontend/src/dns-cache-logic.ts";

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

test("a Go zero finished_at is still running, not 0001-01-01", () => {
  const time = (value: string) => `T(${value})`;
  assert.match(dnsCacheEnsureText({ mode: "dnsmasq", ok: true, ensure: { helper_version: "v0.7.26", finished_at: "0001-01-01T00:00:00Z", exit_code: 0 } }, time), /正在执行/);
});

const caps = ["core.status", "dns.cache.ensure"];
const at = Date.parse("2026-11-09T04:00:00Z");

test("修复 DNS 缓存 button: offline / old Helper / running / cooldown", () => {
  const ok = { mode: "dnsmasq", ok: true };
  assert.deepEqual(dnsCacheRepairButton({ fresh: true, capabilities: caps, status: ok, busy: false, now: at }), { enabled: true, running: false, hint: "" });
  assert.equal(dnsCacheRepairButton({ fresh: false, capabilities: caps, status: ok, busy: false, now: at }).hint, "Helper 离线");
  // Helper v0.7.25 shows the cache but cannot repair it.
  const old = dnsCacheRepairButton({ fresh: true, capabilities: ["core.status"], status: ok, busy: false, now: at });
  assert.deepEqual([old.enabled, old.hint], [false, "需要先升级 Helper"]);
  assert.equal(dnsCacheRepairButton({ fresh: true, status: ok, busy: false, now: at }).hint, "需要先升级 Helper");
  const clicking = dnsCacheRepairButton({ fresh: true, capabilities: caps, status: ok, busy: true, now: at });
  assert.deepEqual([clicking.enabled, clicking.running], [false, true]);
  // Another tab (or the Helper) is still running one.
  const running = { ...ok, repair: { command_id: "c1", started_at: "2026-11-09T03:59:50Z", finished_at: "0001-01-01T00:00:00Z", exit_code: 0 } };
  assert.equal(dnsCacheRepairButton({ fresh: true, capabilities: caps, status: running, busy: false, now: at }).running, true);
  assert.equal(dnsCacheRepairRunning(running.repair, at + DNS_CACHE_REPAIR_WAIT_MS), false, "a run silent for 5 min no longer blocks");
  // One click a minute, counted from the Helper's start time or this tab's click.
  const finished = { ...ok, repair: { command_id: "c1", started_at: "2026-11-09T03:59:30Z", finished_at: "2026-11-09T03:59:31Z", exit_code: 0 } };
  assert.deepEqual(dnsCacheRepairButton({ fresh: true, capabilities: caps, status: finished, busy: false, now: at }), { enabled: false, running: false, hint: "30 秒后可再试" });
  assert.equal(dnsCacheRepairButton({ fresh: true, capabilities: caps, status: finished, busy: false, now: at + 30_000 }).enabled, true);
  assert.equal(dnsCacheRepairButton({ fresh: true, capabilities: caps, status: ok, busy: false, lastClickAt: at - 1_000, now: at }).hint, "59 秒后可再试");
});

test("修复结果: 无需改动 / 已安装并启用 / 精简模式 / 失败 + 原因", () => {
  const time = (value: string) => `T(${value})`;
  const base = { command_id: "c1", started_at: "2026-11-09T04:00:00Z", finished_at: "2026-11-09T04:00:01Z" };
  assert.equal(dnsCacheRepairOutcome(undefined, time), null);
  assert.equal(dnsCacheRepairOutcome({ ...base, finished_at: "0001-01-01T00:00:00Z", exit_code: 0 }, time), null, "still running");
  assert.deepEqual(dnsCacheRepairOutcome({ ...base, exit_code: 0, summary: "DNS 缓存：本机 dnsmasq（127.0.0.1，上游 1.1.1.1 8.8.8.8）无需改动" }, time), {
    tone: "ok", text: "上次修复（T(2026-11-09T04:00:01Z)）：无需改动。本机 dnsmasq（127.0.0.1，上游 1.1.1.1 8.8.8.8）",
  });
  assert.equal(dnsCacheRepairOutcome({ ...base, exit_code: 0, changed: true, summary: "DNS 缓存：本机 dnsmasq（127.0.0.1，上游 1.1.1.1 8.8.8.8）已配置" }, time)?.text,
    "上次修复（T(2026-11-09T04:00:01Z)）：已安装并启用。本机 dnsmasq（127.0.0.1，上游 1.1.1.1 8.8.8.8）");
  assert.match(dnsCacheRepairOutcome({ ...base, exit_code: 0, changed: true, summary: "DNS 缓存：精简模式（可用内存 40 MB（低于 60 MB）），解析器 10.0.0.2 1.1.1.1，超时 1 秒" }, time)?.text ?? "", /^上次修复（T\(2026-11-09T04:00:01Z\)）：精简模式（可用内存 40 MB（低于 60 MB）），解析器/);
  const failed = dnsCacheRepairOutcome({ ...base, exit_code: 1, changed: true, summary: "DNS 缓存：精简模式（apt 安装 dnsmasq-base 失败），解析器 10.0.0.2，超时 1 秒" }, time);
  assert.deepEqual([failed?.tone, /失败。精简模式（apt 安装 dnsmasq-base 失败）/.test(failed?.text ?? "")], ["warning", true]);
  assert.match(dnsCacheRepairOutcome({ ...base, exit_code: -1 }, time)?.text ?? "", /失败。未知原因$/);
});

test("waiting for the result of this click only", () => {
  assert.deepEqual(dnsCacheRepairProgress({}, "c2"), { done: false });
  const refused = { results: [{ command_id: "c2", action: "dns.cache.ensure", success: false, message: "刚刚执行过，请 12 秒后再试" }] };
  assert.deepEqual(dnsCacheRepairProgress(refused, "c2"), { done: true, error: "刚刚执行过，请 12 秒后再试" });
  const claimed = { results: [{ command_id: "c2", action: "dns.cache.ensure", success: true }], status: { dns_cache: { mode: "dnsmasq", ok: true, repair: { command_id: "c2", started_at: "2026-11-09T04:00:00Z", finished_at: "0001-01-01T00:00:00Z", exit_code: 0 } } } };
  assert.equal(dnsCacheRepairProgress(claimed, "c2").done, false);
  const previous = { status: { dns_cache: { mode: "dnsmasq", ok: true, repair: { command_id: "c1", started_at: "2026-11-09T03:00:00Z", finished_at: "2026-11-09T03:00:01Z", exit_code: 0 } } } };
  assert.equal(dnsCacheRepairProgress(previous, "c2").done, false, "an older repair is not this click's result");
  const done = { status: { dns_cache: { mode: "dnsmasq", ok: true, repair: { command_id: "c2", started_at: "2026-11-09T04:00:00Z", finished_at: "2026-11-09T04:00:01Z", exit_code: 0, summary: "x" } } } };
  assert.equal(dnsCacheRepairProgress(done, "c2").repair?.summary, "x");
});
