// Fixture-only check for 本机 DNS 缓存 on 服务管理 (helper-dns-cache). No backend, credentials or remote requests.
// Card: ⚠ only on the fresh server whose cache is broken. Helper dialog: the DNS 缓存 row + ensure line.
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
const evidence = process.env.DNS_EVIDENCE || path.join(root, "runs/helper-dns-cache/shots");
mkdirSync(evidence, { recursive: true });
const server = await serveFixtureBuild(dist);
const base = `http://127.0.0.1:${server.address().port}`;
const errors = [], checks = [];
const { servers, groups } = fixture;
const ensured = { helper_version: "v0.7.25", started_at: fixture.now, finished_at: fixture.now, exit_code: 0, changed: false };
// 15 broken (⚠), 9 broken but its Helper is offline (no ⚠), 10 minimal, 14 / 17 resolved, the rest dnsmasq.
const dnsCache = (s) => s.id === 15 ? { mode: "none", ok: false, reason: "未启用本机 DNS 缓存（只有 1 个解析器）", checked_at: fixture.now, ensure: { ...ensured, exit_code: 1, changed: true, summary: "DNS 缓存：精简模式（apt 安装 dnsmasq-base 失败），解析器 10.0.0.2，超时 1 秒" } }
  : s.id === 9 ? { mode: "dnsmasq", ok: false, reason: "本机缓存 flvx-dnscache 未运行", checked_at: fixture.now }
  : s.id === 10 ? { mode: "minimal", ok: true, reason: "可用内存 40 MB（低于 60 MB）", checked_at: fixture.now, ensure: { ...ensured, summary: "DNS 缓存：精简模式（可用内存 40 MB（低于 60 MB）），解析器 1.1.1.1 8.8.8.8，超时 1 秒" } }
  : [14, 17].includes(s.id) ? { mode: "resolved", ok: true, checked_at: fixture.now, ensure: { ...ensured, summary: "DNS 缓存：systemd-resolved（127.0.0.53，上游 1.1.1.1 8.8.8.8）无需改动" } }
  : { mode: "dnsmasq", ok: true, checked_at: fixture.now, ensure: { ...ensured, summary: "DNS 缓存：本机 dnsmasq（127.0.0.1，上游 1.1.1.1 8.8.8.8）无需改动" } };
const coreMode = (s) => ({ desired_mode: s.xray_mode, current_mode: s.xray_mode, can_switch: true, fresh: s.id !== 9, configured: true, repair_status: "healthy", dns_cache: dnsCache(s) });
const agentStatus = (s) => ({ reported_at: fixture.now, helper: { installed: true, version: "v0.7.25", active: true, ready: true }, core: { installed: true, version: "Xray 2bf03c9", ready: true }, core_mode: s.xray_mode, official_agent: "connected", takeover: { status: "completed" }, architecture: "amd64", dns_cache: dnsCache(s) });
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
    let response = { success: true };
    const byPath = servers.find((item) => endpoint.includes(`/servers/${item.id}/`));
    if (endpoint === "/api/v3") response = body.op === "034e094d05aa3f83" ? { servers } : {};
    else if (endpoint === "/api/custom/ui/service-groups") response = { success: true, exists: true, groups, revision: 1 };
    else if (endpoint === "/api/custom/agent/metrics") response = {
      metrics: Object.fromEntries(servers.map((s, i) => [s.id, { server_id: String(s.id), available: s.id !== 9, tcp_count: 100 + i * 20, udp_count: 23, connection_count: 123 + i * 20, helper_version: "v0.7.25", updated_at: fixture.now }])),
      core_modes: Object.fromEntries(servers.map((s) => [s.id, coreMode(s)])),
    };
    else if (byPath && endpoint.endsWith("/agent")) response = { success: true, status: agentStatus(byPath), pending: [], results: [] };
    else if (byPath && endpoint.endsWith("/core-mode")) response = { success: true, configured: true, controller_mode: byPath.xray_mode, current_mode: byPath.xray_mode, intent: { desired_core_mode: byPath.xray_mode, repair_status: "healthy" }, agent_status: agentStatus(byPath) };
    else if (endpoint === "/api/custom/releases") response = { success: true, current_custom_version: "v1.4.91", cached: true, release: { latest_helper_version: "v0.7.25", latest_core_version: "2bf03c9" } };
    else if (endpoint === "/api/custom/flvx/admin") response = { shares: [], servers, integration: {} };
    else if (endpoint.startsWith("/api/custom/")) response = { success: true, metrics: {}, connections: {}, groups: [], plans: [] };
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(response) });
  });
  const page = await context.newPage();
  await page.clock.install({ time: new Date(fixture.now) });
  await page.clock.pauseAt(new Date(fixture.now));
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto(base);
  await page.getByRole("button", { name: "打开菜单", exact: true }).click();
  await page.getByRole("button", { name: "服务管理", exact: true }).click();
  await page.locator(".service-server-card").first().waitFor();
  await page.waitForTimeout(600);
  const shot = (name, locator) => (locator || page).screenshot({ path: path.join(evidence, `${name}.png`) });

  const alerts = await page.locator(".dns-cache-alert").evaluateAll((els) => els.map((el) => ({ text: el.textContent, card: el.closest(".service-server-card")?.querySelector("h2")?.textContent, title: el.getAttribute("title"), size: getComputedStyle(el).fontSize })));
  checks.push({ alerts });
  assert.equal(alerts.length, 1, `exactly one ⚠ badge, got ${JSON.stringify(alerts)}`);
  assert.equal(alerts[0].card, servers.find((s) => s.id === 15).name);
  assert.equal(alerts[0].text, "⚠ DNS 缓存未启用");
  assert.equal(alerts[0].size, "13px");
  const card15 = page.locator(".service-server-card", { has: page.locator(".dns-cache-alert") });
  await card15.scrollIntoViewIfNeeded();
  for (const theme of ["light", "dark"]) {
    await page.evaluate((theme) => { document.documentElement.dataset.theme = theme; }, theme);
    await shot(`390-${theme}-card-alert`, card15);
  }
  await page.evaluate(() => { document.documentElement.dataset.theme = "light"; });
  const healthyCard = page.locator(".service-server-card").filter({ hasText: servers.find((s) => s.id === 2).name }).first();
  await shot("390-light-card-healthy-no-alert", healthyCard);

  for (const [id, label] of [[15, "未启用"], [10, "精简模式"], [2, "正常"]]) {
    const name = servers.find((s) => s.id === id).name;
    await page.getByRole("button", { name: `${name} 更多操作`, exact: true }).click();
    await page.getByRole("button", { name: "生成安装命令", exact: true }).click();
    const row = page.locator(".dns-cache-row");
    await row.waitFor();
    const text = await row.textContent();
    checks.push({ id, text });
    assert.ok(text.includes(label), `server ${id} dialog row: ${text}`);
    assert.ok(text.includes("安装 / 升级"), `server ${id} ensure line missing: ${text}`);
    const small = await smallText(page, ".dns-cache-row");
    assert.deepEqual(small, [], `text under 13px in the DNS row: ${JSON.stringify(small)}`);
    for (const theme of id === 15 ? ["light", "dark"] : ["light"]) {
      await page.evaluate((theme) => { document.documentElement.dataset.theme = theme; }, theme);
      await row.scrollIntoViewIfNeeded();
      await shot(`390-${theme}-helper-dialog-s${id}`);
    }
    await page.evaluate(() => { document.documentElement.dataset.theme = "light"; });
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: /^(关闭面板|关闭)$/ }).first().click({ timeout: 2000 }).catch(() => undefined);
    await page.locator(".dns-cache-row").waitFor({ state: "detached", timeout: 5000 });
  }
  assert.deepEqual(errors, []);
  console.log(JSON.stringify({ ok: true, checks }, null, 2));
} finally {
  writeFileSync(path.join(evidence, "report.json"), JSON.stringify({ checks, errors }, null, 2));
  await browser?.close();
  server.close();
}
