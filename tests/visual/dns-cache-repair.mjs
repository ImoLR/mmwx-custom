// Fixture-only check for 「修复 DNS 缓存」 on 服务管理 (custom-dns-cache-repair). No backend, credentials or
// remote requests. The card's ⚠ opens the Connections Helper dialog; the DNS 缓存 row carries the button:
// confirm → dns.cache.ensure → running → result. Disabled for old / offline Helpers and inside the minute.
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { serveFixtureBuild } from "./local-fixture-server.mjs";
import { smallText } from "./font-floor.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const require = createRequire(path.join(root, "frontend/package.json"));
const { chromium } = require("playwright");
const fixture = JSON.parse(readFileSync(new URL("./server-page-fixture.json", import.meta.url), "utf8"));
const dist = process.env.DNS_DIST || path.join(root, "frontend/dist");
const evidence = process.env.DNS_EVIDENCE || path.join(root, "runs/custom-dns-cache-repair/shots");
mkdirSync(evidence, { recursive: true });
const server = await serveFixtureBuild(dist);
const base = `http://127.0.0.1:${server.address().port}`;
const errors = [], checks = [], posts = [];
const { servers, groups } = fixture;
const now = Date.parse(fixture.now);
const iso = (offset) => new Date(now + offset).toISOString();
const zero = "0001-01-01T00:00:00Z";
const ensured = { helper_version: "v0.7.26", started_at: iso(-3600_000), finished_at: iso(-3600_000), exit_code: 0, changed: false };
const noop = "DNS 缓存：本机 dnsmasq（127.0.0.1，上游 1.1.1.1 8.8.8.8）无需改动";
// 15 broken (⚠, repaired in the run), 10 minimal (its repair fails), 2 repaired 30 s ago (cooldown),
// 12 on Helper v0.7.25 (no repair command), 9 offline, the rest dnsmasq.
const caps = ["core.status", "dns.cache.ensure"];
const state = Object.fromEntries(servers.map((s) => [s.id, {
  version: s.id === 12 ? "v0.7.25" : "v0.7.26",
  caps: s.id === 12 ? ["core.status"] : caps,
  dns: s.id === 15 ? { mode: "none", ok: false, reason: "未启用本机 DNS 缓存（只有 1 个解析器）", checked_at: iso(-60_000), ensure: { ...ensured, exit_code: 1, changed: true, summary: "DNS 缓存：精简模式（apt 安装 dnsmasq-base 失败），解析器 10.0.0.2，超时 1 秒" } }
    : s.id === 10 ? { mode: "minimal", ok: true, reason: "可用内存 40 MB（低于 60 MB）", checked_at: iso(-60_000), ensure: { ...ensured, summary: "DNS 缓存：精简模式（可用内存 40 MB（低于 60 MB）），解析器 1.1.1.1 8.8.8.8，超时 1 秒" } }
    : s.id === 2 ? { mode: "dnsmasq", ok: true, checked_at: iso(-30_000), ensure: { ...ensured, summary: noop }, repair: { command_id: "earlier", helper_version: "v0.7.26", started_at: iso(-30_000), finished_at: iso(-29_700), exit_code: 0, changed: false, summary: noop } }
    : { mode: "dnsmasq", ok: true, checked_at: iso(-60_000), ensure: { ...ensured, summary: noop } },
  results: [],
}]));
// What the Helper reports after the click, poll by poll.
const scripted = {
  15: [(id) => ({ dns: { ...state[15].dns, repair: { command_id: id, helper_version: "v0.7.26", started_at: iso(0), finished_at: zero, exit_code: 0 } } }),
    (id) => ({ dns: { mode: "dnsmasq", ok: true, checked_at: iso(4_000), ensure: state[15].dns.ensure, repair: { command_id: id, helper_version: "v0.7.26", started_at: iso(0), finished_at: iso(3_900), exit_code: 0, changed: true, summary: "DNS 缓存：本机 dnsmasq（127.0.0.1，上游 1.1.1.1 8.8.8.8）已配置" } } })],
  10: [(id) => ({ dns: { ...state[10].dns, checked_at: iso(2_000), repair: { command_id: id, helper_version: "v0.7.26", started_at: iso(0), finished_at: iso(1_500), exit_code: 1, changed: true, summary: "DNS 缓存：精简模式（apt 安装 dnsmasq-base 失败），解析器 10.0.0.2，超时 1 秒" } } })],
};
const pendingScript = {};
const fresh = (s) => s.id !== 9;
const coreMode = (s) => ({ desired_mode: s.xray_mode, current_mode: s.xray_mode, can_switch: true, fresh: fresh(s), configured: true, repair_status: "healthy", dns_cache: state[s.id].dns });
const agentStatus = (s) => ({ reported_at: fresh(s) ? fixture.now : iso(-3600_000), capabilities: state[s.id].caps, helper: { installed: true, version: state[s.id].version, active: true, ready: true }, core: { installed: true, version: "Xray 2bf03c9", ready: true }, core_mode: s.xray_mode, official_agent: "connected", takeover: { status: "completed" }, architecture: "amd64", dns_cache: state[s.id].dns });
let browser;
try {
  const available = Number(readFileSync("/proc/meminfo", "utf8").match(/MemAvailable:\s+(\d+)/)[1]) / 1024;
  assert.ok(available >= 900, `Wait for 900 MB available before Playwright (currently ${Math.floor(available)})`);
  browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE, args: ["--disable-dev-shm-usage"] });
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, timezoneId: "Asia/Taipei" });
  await context.addInitScript(() => {
    localStorage.setItem("mmwx-session", JSON.stringify({ token: "local-fixture", username: "admin", role: "admin", isAdmin: true, expiresAt: "2099-01-01T00:00:00Z" }));
    localStorage.setItem("services-view-mode", "grid");
  });
  await context.route("**/*", async (route) => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== base) { errors.push(`blocked remote request: ${url.origin}`); return route.abort(); }
    if (!url.pathname.startsWith("/api/")) return route.continue();
    const body = request.postDataJSON() || {}, endpoint = url.pathname;
    let response = { success: true }, status = 200;
    const byPath = servers.find((item) => endpoint.includes(`/servers/${item.id}/`));
    if (endpoint === "/api/v3") response = body.op === "034e094d05aa3f83" ? { servers } : {};
    else if (endpoint === "/api/custom/ui/service-groups") response = { success: true, exists: true, groups, revision: 1 };
    else if (endpoint === "/api/custom/agent/metrics") response = {
      metrics: Object.fromEntries(servers.map((s, i) => [s.id, { server_id: String(s.id), available: fresh(s), tcp_count: 100 + i * 20, udp_count: 23, connection_count: 123 + i * 20, helper_version: state[s.id].version, updated_at: fixture.now }])),
      core_modes: Object.fromEntries(servers.map((s) => [s.id, coreMode(s)])),
    };
    else if (byPath && endpoint.endsWith("/agent") && request.method() === "POST") {
      posts.push({ server: byPath.id, body });
      const id = `cmd-${byPath.id}-${posts.length}`;
      pendingScript[byPath.id] = { id, steps: [...(scripted[byPath.id] || [])] };
      response = { success: true, command: { id, action: body.action } };
      status = 202;
    } else if (byPath && endpoint.endsWith("/agent")) {
      const run = pendingScript[byPath.id];
      const step = run?.steps.shift();
      if (step) Object.assign(state[byPath.id], step(run.id));
      response = { success: true, status: agentStatus(byPath), pending: [], results: state[byPath.id].results };
    } else if (byPath && endpoint.endsWith("/core-mode")) response = { success: true, configured: true, controller_mode: byPath.xray_mode, current_mode: byPath.xray_mode, intent: { desired_core_mode: byPath.xray_mode, repair_status: "healthy" }, agent_status: agentStatus(byPath) };
    else if (endpoint === "/api/custom/releases") response = { success: true, current_custom_version: "v1.4.92", cached: true, release: { latest_helper_version: "v0.7.26", latest_core_version: "2bf03c9" } };
    else if (endpoint === "/api/custom/flvx/admin") response = { shares: [], servers, integration: {} };
    else if (endpoint.startsWith("/api/custom/")) response = { success: true, metrics: {}, connections: {}, groups: [], plans: [] };
    await route.fulfill({ status, contentType: "application/json", body: JSON.stringify(response) });
  });
  const page = await context.newPage();
  await page.clock.install({ time: new Date(fixture.now) });
  await page.clock.pauseAt(new Date(fixture.now));
  page.on("pageerror", (error) => errors.push(error.message));
  const confirms = [];
  page.on("dialog", (dialog) => { confirms.push(dialog.message()); void dialog.accept(); });
  await page.goto(base);
  await page.getByRole("button", { name: "打开菜单", exact: true }).click();
  await page.getByRole("button", { name: "服务管理", exact: true }).click();
  await page.locator(".service-server-card").first().waitFor();
  await page.waitForTimeout(600);
  const shot = (name, locator) => (locator || page).screenshot({ path: path.join(evidence, `${name}.png`) });
  const row = page.locator(".dns-cache-row");
  const button = page.locator(".dns-cache-repair-button");
  const closeDialog = async () => {
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: /^(关闭面板|关闭)$/ }).first().click({ timeout: 2000 }).catch(() => undefined);
    await row.waitFor({ state: "detached", timeout: 5000 });
  };
  const openHelper = async (id) => {
    const name = servers.find((s) => s.id === id).name;
    await page.getByRole("button", { name: `${name} 更多操作`, exact: true }).click();
    await page.getByRole("button", { name: "生成安装命令", exact: true }).click();
    await row.waitFor();
  };
  const tick = async (ms) => { await page.clock.runFor(ms); await page.waitForTimeout(150); };

  // The ⚠ on the card is a button now, same look, and opens the Helper dialog.
  const alert = page.locator(".dns-cache-alert");
  assert.equal(await alert.count(), 1);
  const look = await alert.evaluate((el) => { const c = getComputedStyle(el); return { tag: el.tagName, size: c.fontSize, border: c.borderTopWidth, color: c.color, background: c.backgroundColor, cursor: c.cursor, font: c.fontFamily === getComputedStyle(document.body).fontFamily }; });
  checks.push({ alertLook: look });
  assert.deepEqual([look.tag, look.size, look.border, look.cursor, look.font], ["BUTTON", "13px", "0px", "pointer", true]);
  const card15 = page.locator(".service-server-card", { has: alert });
  await card15.scrollIntoViewIfNeeded();
  await shot("390-light-card-alert-button", card15);
  await alert.click();
  await row.waitFor();
  assert.equal(await page.locator(".service-dialog h3").textContent(), "Connections Helper");
  assert.ok(await button.isEnabled(), "repair button enabled on a broken, fresh, v0.7.26 Helper");
  assert.equal(await button.textContent(), "修复 DNS 缓存");
  await row.scrollIntoViewIfNeeded();
  await shot("390-light-s15-ready");
  assert.deepEqual(await smallText(page, ".dns-cache-row"), []);
  // Click → confirm → POST dns.cache.ensure (no payload) → running → result, row refreshed.
  await button.click();
  await page.waitForTimeout(150);
  assert.equal(confirms.length, 1);
  assert.match(confirms[0], /Nobrand KFC 5cm\n修复本机 DNS 缓存？/);
  assert.deepEqual(posts.at(-1), { server: 15, body: { action: "dns.cache.ensure" } });
  assert.equal(await button.textContent(), "正在修复…");
  assert.ok(await button.isDisabled());
  await tick(2000);
  assert.equal(await button.textContent(), "正在修复…");
  await row.scrollIntoViewIfNeeded();
  await shot("390-light-s15-running");
  await tick(2000);
  const result = await page.locator(".dns-cache-repair-result").textContent();
  checks.push({ s15: await row.textContent() });
  assert.match(result, /^上次修复（.*）：已安装并启用。本机 dnsmasq（127.0.0.1，上游 1.1.1.1 8.8.8.8）$/);
  assert.match(await row.locator("strong").textContent(), /^正常$/);
  assert.match(await page.locator(".dns-cache-repair small").textContent(), /^\d+ 秒后可再试$/);
  assert.ok(await button.isDisabled(), "cooldown after a run");
  assert.deepEqual(await smallText(page, ".dns-cache-row"), []);
  await row.scrollIntoViewIfNeeded();
  await shot("390-light-s15-result");
  await page.evaluate(() => { document.documentElement.dataset.theme = "dark"; });
  await shot("390-dark-s15-result");
  await page.evaluate(() => { document.documentElement.dataset.theme = "light"; });
  await closeDialog();

  // A failed run shows 失败 + the reason, no retry.
  await openHelper(10);
  await button.click();
  await tick(2000);
  const failed = await page.locator(".dns-cache-repair-result").textContent();
  checks.push({ s10: failed });
  assert.match(failed, /失败。精简模式（apt 安装 dnsmasq-base 失败）/);
  assert.equal(posts.filter((p) => p.server === 10).length, 1);
  await tick(10_000);
  assert.equal(posts.filter((p) => p.server === 10).length, 1, "never retried by the page");
  await row.scrollIntoViewIfNeeded();
  await shot("390-light-s10-failed");
  await closeDialog();

  // Repaired 30 s ago (other tab / earlier): last result shown, button waits out the minute.
  await openHelper(2);
  assert.ok(await button.isDisabled());
  assert.match(await page.locator(".dns-cache-repair small").textContent(), /^\d+ 秒后可再试$/);
  assert.match(await page.locator(".dns-cache-repair-result").textContent(), /：无需改动。本机 dnsmasq/);
  await row.scrollIntoViewIfNeeded();
  await shot("390-light-s2-cooldown");
  await closeDialog();

  // Helper v0.7.25: shown, but 「需要先升级 Helper」. Offline Helper: 「Helper 离线」.
  for (const [id, hint, name] of [[12, "需要先升级 Helper", "old-helper"], [9, "Helper 离线", "offline"]]) {
    await openHelper(id);
    assert.ok(await button.isDisabled(), `server ${id} button disabled`);
    assert.equal(await page.locator(".dns-cache-repair small").textContent(), hint);
    await row.scrollIntoViewIfNeeded();
    await shot(`390-light-s${id}-${name}`);
    await closeDialog();
  }
  assert.equal(posts.length, 2, `only the two clicks went out: ${JSON.stringify(posts)}`);

  // Desktop width.
  await page.setViewportSize({ width: 1280, height: 900 });
  await openHelper(15);
  await row.scrollIntoViewIfNeeded();
  await shot("1280-light-s15-dialog");
  await closeDialog();
  assert.deepEqual(errors, []);
  console.log(JSON.stringify({ ok: true, checks }, null, 2));
} finally {
  writeFileSync(path.join(evidence, "report.json"), JSON.stringify({ checks, errors, posts }, null, 2));
  await browser?.close();
  server.close();
}
