// Fixture-only screenshots for machine protection v2 (server card badge, breakdown, settings).
// No official backend, credentials or remote requests. MACHINE_EVIDENCE = output dir.
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { serveFixtureBuild } from "./local-fixture-server.mjs";
import { smallText, fontFloor } from "./font-floor.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const require = createRequire(path.join(root, "frontend/package.json"));
const { chromium } = require("playwright");
const fixture = JSON.parse(readFileSync(new URL("./server-page-fixture.json", import.meta.url), "utf8"));
const dist = process.env.MACHINE_DIST || path.join(root, "frontend/dist");
const evidence = process.env.MACHINE_EVIDENCE || path.join(root, "runs/machine-protect/screens");
mkdirSync(evidence, { recursive: true });
const server = await serveFixtureBuild(dist);
const base = `http://127.0.0.1:${server.address().port}`;
const errors = [], fonts = [], shots = [];
const { servers, groups, nodes } = fixture;

const conns = (total, tw, cw, udp) => {
  const tcp = total - udp, established = Math.round((tcp - tw - cw) * 0.8);
  return { total, tcp, udp, established, time_wait: tw, close_wait: cw, other_tcp: tcp - established - tw - cw };
};
const sources = (file, tcp = 0, udp = 0, shared = 0) => ({ core: 3, flvx_shared_core: shared, flvx_file: file, flvx_file_tcp: tcp, flvx_file_udp: udp, flvx_file_updated_at: file === "fresh" ? fixture.now : undefined });
// Per server: whole-machine counts, protection state and port sources.
const machines = {
  1: { connections: conns(25, 3, 0, 4), blocking: false, dropped_total: 0, port_sources: sources("absent"), idle_reason: "no_business_ports" },
  2: { connections: conns(312, 96, 4, 18), blocking: false, dropped_total: 0, port_sources: sources("absent") },
  3: { connections: conns(188, 40, 1, 9), blocking: false, dropped_total: 0, port_sources: sources("absent") },
  5: { connections: conns(846, 500, 41, 56), blocking: false, dropped_total: 0, port_sources: sources("fresh", 2, 1, 1) },
  7: { connections: conns(1012, 500, 63, 71), blocking: true, dropped_total: 37, port_sources: sources("fresh", 4, 2, 2) },
  10: { connections: conns(97, 12, 0, 6), blocking: false, dropped_total: 0, port_sources: sources("absent") },
  11: { connections: conns(431, 180, 2, 22), blocking: false, dropped_total: 0, port_sources: sources("fresh", 6, 3) },
  14: { connections: conns(387, 304, 1, 12), blocking: false, dropped_total: 0, port_sources: sources("fresh", 1, 1) },
  15: { connections: conns(64, 8, 0, 5), blocking: false, dropped_total: 0, port_sources: sources("absent") },
  16: { connections: conns(143, 37, 0, 10), blocking: false, dropped_total: 0, port_sources: sources("absent") },
  17: { connections: conns(220, 61, 3, 15), blocking: false, dropped_total: 0, port_sources: sources("absent") },
};
const oldHelper = new Set([12]);
const counts = { 9: 51, 12: 404 };
const summary = (id) => {
  const m = machines[id];
  return { version: 2, enabled: true, effective: true, max_total: 1000, release_below: 950, blocking: m.blocking, dropped_total: m.dropped_total, connections: m.connections, tw_buckets: 500, close_wait_seconds: 10, tcp_ports: 5, udp_ports: 3, port_sources: m.port_sources, idle_reason: m.idle_reason };
};
const metric = (s) => {
  const total = machines[s.id]?.connections.total ?? counts[s.id] ?? 100;
  const udp = machines[s.id]?.connections.udp ?? 23;
  return { server_id: String(s.id), available: s.id !== 9, tcp_count: total - udp, udp_count: udp, connection_count: total, helper_version: oldHelper.has(s.id) ? "v0.7.8" : "v0.7.9", updated_at: fixture.now, ...(machines[s.id] && !oldHelper.has(s.id) ? { machine: summary(s.id) } : {}) };
};
const tcpStates = (c) => ({ tcp_total: c.tcp, established: c.established, syn_sent: 1, syn_recv: 2, fin_wait_1: 0, fin_wait_2: 3, time_wait: c.time_wait, close_wait: c.close_wait, last_ack: 0, closing: 0, close: 0, listen: c.other_tcp - 6, unknown: 0 });
const settingsFor = (id) => ({ machine_protection: oldHelper.has(id) ? null : { enabled: true, max_active: null, max_total: 1000, tw_buckets: 500, close_wait_seconds: 10 }, default_close_wait_timeout_seconds: null, online_ip_grace_period_seconds: 30, global_total_limit_enabled: false, max_global_total_connections: null, max_global_inbound_connections: null, users: [], ports: [], management_users: [] });
const detailed = (id) => {
  const s = servers.find((item) => item.id === id);
  const m = machines[id];
  const c = m?.connections ?? conns(404, 304, 1, 12);
  const protection = oldHelper.has(id)
    ? { supported: true, configured: { enabled: false, max_active: null, max_total: null }, effective: true, blocking: false, active: 80, total: c.tcp, controlled_ports: [10021, 10022, 10023] }
    : { version: 2, owner: "mmwx", supported: true, configured: settingsFor(id).machine_protection, effective: true, blocking: m.blocking, threshold_reason: m.blocking ? "total" : undefined, active: c.established + c.close_wait, total: c.total, controlled_ports: [10016, 10017, 20001, 30000], tcp_ports: [10016, 10017, 20001, 20003, 30000], udp_ports: [10016, 20001, 30000], connections: c, max_total: 1000, release_below: 950, port_sources: m.port_sources, idle_reason: m.idle_reason, dropped_total: m.dropped_total, tw_buckets: { target: 500, current: 500, original: 262144 }, close_wait_seconds: 10 };
  return { success: true, available: true, stale_timeout_seconds: 30, settings: settingsFor(id), ownership_error: "",
    record: { server_id: String(id), helper_version: oldHelper.has(id) ? "v0.7.8" : "v0.7.9", updated_at: fixture.now, snapshot: { system: tcpStates(c), inbounds: [], proxy_users: [], management_groups: [], global: { current_total: 0, max_total: null, rejected_global_total_limit: 0, current_inbound: 0, max_inbound: null, rejected_global_inbound_limit: 0 }, core: { available: true, interface_version: 7 }, machine_protection: protection, sampled_at: fixture.now } },
    management: { users: [], unassigned_ports: [], assignable_users: [], warnings: [] }, core_accounting: { total: 0, attributed: 0, unattributed: 0, inbound_current: 0, outbound_active: 0, outbound_pending: 0, tracker_total: 0, reconciliation_delta: 0, balanced: true }, name: s?.name };
};

let browser;
const saveReport = () => writeFileSync(path.join(evidence, "report.json"), JSON.stringify({ shots, fonts, errors }, null, 2));
try {
  const available = Number(readFileSync("/proc/meminfo", "utf8").match(/MemAvailable:\s+(\d+)/)[1]) / 1024;
  assert.ok(available >= 900, `Wait for 900 MB available before Playwright (currently ${Math.floor(available)})`);
  browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE, args: ["--disable-dev-shm-usage"] });
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, timezoneId: "Asia/Taipei" });
  await context.addInitScript(() => {
    localStorage.setItem("mmwx-session", JSON.stringify({ token: "local-fixture", username: "admin", role: "admin", isAdmin: true, expiresAt: "2099-01-01T00:00:00Z" }));
  });
  await context.route("**/*", async (route) => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== base) { errors.push(`blocked remote request: ${url.origin}`); return route.abort(); }
    if (!url.pathname.startsWith("/api/")) return route.continue();
    const body = request.postDataJSON() || {}, endpoint = url.pathname;
    let response = { success: true };
    const connectionMatch = endpoint.match(/^\/api\/custom\/servers\/(\d+)\/connections$/);
    if (endpoint === "/api/v3") response = body.op === "034e094d05aa3f83" ? { servers } : body.op === "c87c168b92b5f22d" ? { nodes } : {};
    else if (connectionMatch) response = detailed(Number(connectionMatch[1]));
    else if (endpoint.endsWith("/agent/version-info")) response = { current: "v0.5.5", latest: "v0.5.5" };
    else if (endpoint === "/api/custom/ui/service-groups") response = { success: true, exists: true, groups, revision: 1 };
    else if (endpoint === "/api/custom/agent/metrics") response = {
      metrics: Object.fromEntries(servers.map((s) => [s.id, metric(s)])),
      core_modes: Object.fromEntries(servers.map((s) => [s.id, { desired_mode: s.xray_mode, current_mode: s.xray_mode, can_switch: s.id !== 9, fresh: s.id !== 9, configured: true, repair_status: "healthy", pending_change: false }])),
    };
    else if (endpoint === "/api/custom/flvx/admin") response = { shares: fixture.flvx_shared.map((id) => ({ server_id: id, status: "active" })), servers, integration: {} };
    else if (endpoint === "/api/custom/server-deletions") response = { plans: [] };
    else if (endpoint === "/api/custom/geo/lookup") response = { success: true };
    else if (endpoint === "/api/traffic/summary") response = { total_uplink: 0, total_downlink: 0, total_traffic: 0 };
    else if (endpoint.includes("/traffic/")) response = { items: [], servers: [], connections: {} };
    else if (endpoint.startsWith("/api/custom/")) response = { success: true, metrics: {}, connections: {}, groups: [], plans: [] };
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(response) });
  });
  const page = await context.newPage();
  await page.clock.install({ time: new Date(fixture.now) });
  await page.clock.pauseAt(new Date(fixture.now));
  page.on("pageerror", (error) => errors.push(error.message));

  const card = (name) => page.locator(".service-server-card").filter({ has: page.getByRole("heading", { name, exact: true }) });
  const badge = (name) => card(name).locator(".connection-tag");
  const navigate = async () => {
    await page.goto(base);
    await page.getByRole("button", { name: "打开菜单", exact: true }).click();
    await page.getByRole("button", { name: "服务管理", exact: true }).click();
    await page.locator(".service-server-card").first().waitFor();
    await badge("Boil Hinet 马年").waitFor();
    await page.evaluate(() => scrollTo(0, 0));
  };
  const newScope = ".machine-breakdown.open, .connection-protection-block, .connection-tag-protecting, .connection-tag-value";
  const shot = async (name, options = {}) => {
    const issues = await smallText(page, newScope);
    fonts.push({ name, issues });
    await page.screenshot({ path: path.join(evidence, `${name}.png`), ...options });
    shots.push(name); saveReport();
    assert.deepEqual(issues, [], `${name}: new text below ${fontFloor}px`);
  };
  const hover = async (name) => {
    await card(name).evaluate((el) => { el.scrollIntoView({ block: "start" }); scrollBy(0, -16); });
    await page.waitForTimeout(300);
    await badge(name).hover();
    await page.locator(".machine-breakdown.open").waitFor();
    await page.waitForTimeout(200);
    await page.locator(".machine-breakdown.open").waitFor();
  };
  const unhover = async () => { await page.mouse.move(0, 0); await page.locator(".machine-breakdown.open").waitFor({ state: "hidden" }); };
  const dialog = () => page.getByRole("dialog").last();
  const openConnections = async (name) => {
    await badge(name).click();
    await dialog().locator(".connection-protection-block").waitFor();
    await dialog().locator(".connection-protection-block").evaluate((el) => { el.scrollIntoView({ block: "start" }); const body = el.closest(".service-dialog-body"); if (body) body.scrollTop -= 12; });
  };
  const closeDialog = async () => { await page.keyboard.press("Escape"); if (await page.getByRole("dialog").count()) await dialog().getByRole("button", { name: /关闭/ }).first().click(); await page.waitForFunction(() => !document.querySelector(".service-dialog")); };

  // Badge text checks before any screenshot.
  await navigate();
  const badgeText = async (name) => (await badge(name).innerText()).replace(/\s+/g, " ").trim();
  assert.match(await badgeText("Boil HKT 99u"), /312\/1,000/);
  assert.match(await badgeText("Boil Hinet 马年"), /846\/1,000/);
  assert.match(await badgeText("Breadcloud US 年付"), /1,012\/1,000 保护中/);
  assert.match(await badgeText("Boil Hinet 158"), /^🔌 404$/);
  assert.ok((await badge("Boil HKT 99u").getAttribute("class")).includes("success"));
  assert.ok((await badge("Boil Hinet 马年").getAttribute("class")).includes("warning"));
  assert.ok((await badge("Breadcloud US 年付").getAttribute("class")).includes("danger"));

  for (const viewport of [{ label: "desktop", width: 1280, height: 900 }, { label: "phone", width: 390, height: 844 }]) {
    await page.setViewportSize({ width: viewport.width, height: viewport.height });
    for (const theme of viewport.label === "desktop" ? ["light", "dark"] : ["light", "dark"]) {
      await page.evaluate((theme) => { document.documentElement.dataset.theme = theme; }, theme);
      await page.evaluate(() => scrollTo(0, 0));
      await unhover().catch(() => {});
      const prefix = `${viewport.label}-${theme}`;
      await shot(`${prefix}-cards`, { fullPage: viewport.label === "phone" ? false : false });
      if (viewport.label === "phone") {
        // Phones have no hover: the breakdown also shows on keyboard focus; the dialog carries it too.
        for (const [name, slug] of [["Boil Hinet 马年", "yellow"], ["Breadcloud US 年付", "red"]]) {
          await card(name).evaluate((el) => { el.scrollIntoView({ block: "start" }); scrollBy(0, -16); });
          await page.waitForTimeout(400);
          await badge(name).evaluate((el) => el.focus({ preventScroll: true }));
          await page.locator(".machine-breakdown.open").waitFor();
          await page.waitForTimeout(200);
          await page.locator(".machine-breakdown.open").waitFor();
          await shot(`${prefix}-breakdown-${slug}`);
          await badge(name).blur();
        }
      } else {
        for (const [name, slug] of [["Boil HKT 99u", "normal"], ["Boil Hinet 马年", "yellow"], ["Breadcloud US 年付", "red"], ["Boil Hinet 158", "old-helper"], ["Boil HKT", "no-ports"]]) {
          await hover(name);
          await shot(`${prefix}-breakdown-${slug}`);
          await unhover();
        }
      }
      for (const [name, slug] of [["Boil Hinet 马年", "settings-yellow"], ["Breadcloud US 年付", "settings-red"], ["Boil Hinet 158", "settings-old-helper"]]) {
        await openConnections(name);
        await shot(`${prefix}-${slug}`);
        if (slug === "settings-yellow") {
          await dialog().locator(".machine-limit-grid").evaluate((el) => el.scrollIntoView({ block: "center" }));
          await shot(`${prefix}-${slug}-fields`);
        }
        await closeDialog();
      }
    }
  }
  assert.deepEqual(errors, [], "page errors or remote requests");
  saveReport();
  console.log(`machine-protection screenshots: ${shots.length} in ${evidence}`);
} finally {
  await browser?.close();
  server.close();
}
