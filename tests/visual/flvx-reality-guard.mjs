// FLVX 共享 · Reality 防盗 screenshots and checks.
// Static local builds and synthetic API responses only; all remote requests fail.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { readFileSync, writeFileSync, existsSync, mkdirSync } from "node:fs";
import { createHash } from "node:crypto";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { smallText, enforceFontFloor, fontFloor } from "./font-floor.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const require = createRequire(path.join(root, "frontend/package.json"));
const { chromium } = require("playwright");
const { PNG } = require("pngjs");
const run = process.env.FLVX_EVIDENCE || "/root/mmwx-workers/runs/shared-reality-guard";
const screens = process.env.FLVX_SCREENS || path.join(run, "screens"); mkdirSync(screens, { recursive: true });
const now = Date.parse("2026-10-23T04:00:00Z"), GB = 2 ** 30;
const names = ["US 年付 · 共享演示", "香港 · 部分生效", "东京 · FLVX 未开启", "大阪 · 规则异常", "首尔 · 待重启", "台北 · 刚开启", "新加坡 · 旧版 Helper"];
const servers = names.map((name, i) => ({ id: i + 7, name, ip_address: `203.0.113.${i + 7}`, status: "connected", xray_mode: "external", xray_running: true, xray_version: "b01adef", agent_version: "v0.5.5", country_code: "US", flag: "🇺🇸", traffic_limit: 2 * 1024 * GB, traffic_used: 320 * GB, current_upload_speed: 123456, current_download_speed: 234567, last_heartbeat: new Date(now).toISOString(), boot_time: new Date(now - 86400_000).toISOString(), traffic_reset_day: 1 }));
const available = servers.map((s) => ({ server_id: s.id, name: s.name, hosts: { ipv4: s.ip_address, ipv6: "", domain: "" }, official_ports: [10001, 10002], ports_known: true, dual_install: false, native_landing: false, detection_error: false }));
const cfg = { port_min: 30000, port_max: 30999, traffic_limit_bytes: 500 * GB, reset_day: 1, max_connections: 100, share_until: null, memory_mb: 256, cpu_percent: 50 };
const node = (id, owner, protocol, port, used) => ({ id, owner: { id: owner === "小林" ? "21" : "22", name: owner }, name: `${owner} · ${protocol === "ss2022" ? "高速 SS" : "Reality 专线"}`, protocol, port, max_connections: 20, expires_at: Date.parse("2026-11-18T15:59:59Z"), status: { id, state: "active", up_bytes: used * GB * .2, down_bytes: used * GB * .8, connections: 4 } });
const helper = { installed: true, version: "v0.7.13", architecture: "amd64", flvx_ready: true, upgrading: false, flvx_protocols: true, flvx_reality_guard: true };
const plan = (requested, extra) => ({ requested, reality_nodes: 2, ...extra });
const share = (i, extra) => ({ server_id: i + 7, name: names[i], hosts: available[i].hosts, status: "active", config: { ...cfg }, traffic_used_bytes: 186.4 * GB, traffic_limit_bytes: 500 * GB, connections: 12, max_connections: 100, core: { state: "running", version: "b01adef" }, dual_install: false,
  nodes: [node(`${i}01`, "小林", "ss2022", 30001, 68.2), node(`${i}02`, "Alex", "vless_reality_vision", 30002, 45.3), node(`${i}03`, "Alex", "vless_reality_vision", 30003, 12.1)],
  report: { collected_at: now, applied_revision: 12, egress: { state: "none", error: "", applied_revision: 12, warnings: [] }, reality_guard: { state: "on", nodes: 2 } }, allow_egress: true, egress: { versions: [1], allowed: true, geoip: true, geosite: true }, egress_plan: { active: false, outbounds: 0, outbound_types: [], rules: 0, covered_nodes: 0 },
  helper: { ...helper }, protocol_caps: { versions: [1], protocols: { anytls: { udp: true }, snell: { udp: true, versions: [4, 5], obfs: ["none", "http", "tls"] }, mieru: { udp: true, transports: ["TCP", "UDP"] } } }, core_pin: { pinned: "b01adef", latest: "b01adef", upgradable: false }, reality_guard: { versions: [1] }, reality_guard_plan: plan(true), ...extra });
const report = (guard) => ({ collected_at: now, applied_revision: 12, egress: { state: "none", error: "", applied_revision: 12, warnings: [] }, ...(guard ? { reality_guard: guard } : {}) });
const fixture = () => ({ shares: [
  share(0),
  share(1, { report: report({ state: "on", nodes: 1 }), reality_guard_plan: plan(true, { error: "Reality 防盗辅助入站（本机端口 41001）启动失败，相关节点暂按未开启处理" }) }),
  share(2, { report: report(null), reality_guard_plan: plan(false) }),
  share(3, { report: report({ state: "error", nodes: 0 }), reality_guard_plan: plan(true, { error: "Reality 防盗规则未生效：更新 FLVX Core 出站与路由失败" }) }),
  share(4, { report: report({ state: "restart_required", nodes: 0 }), reality_guard_plan: plan(true, { error: "FLVX Core 启动时未开启路由 API，重启 FLVX Core 后防盗生效；目前按未开启处理" }) }),
  share(5, { report: report(null) }),
  share(6, { report: report(null), reality_guard: undefined, reality_guard_plan: plan(false), helper: { ...helper, version: "v0.7.12", flvx_reality_guard: false } }),
], servers: structuredClone(available), integration: { token_configured: true, last_api_at: now - 20_000 } });
const expected = [
  ["已开启 · 2 个 Reality 节点受保护", "已生效"],
  ["已开启 · 1 个 Reality 节点受保护", "已生效"],
  ["FLVX 未开启", null],
  ["未生效 · 目前不拦截", "应用失败"],
  ["重启 FLVX Core 后生效 · 目前不拦截", "待重启"],
  ["已开启 · 2 个 Reality 节点", "等待 Helper 应用"],
  ["升级 Helper 后可用", null],
];

function serve(dist) {
  const server = createServer((req, res) => {
    const url = new URL(req.url, "http://127.0.0.1"); const file = path.resolve(dist, url.pathname === "/" ? "index.html" : `.${url.pathname}`);
    if (!file.startsWith(`${dist}/`) || !existsSync(file)) { res.writeHead(404); res.end(); return; }
    const type = { ".html": "text/html; charset=utf-8", ".js": "application/javascript", ".css": "text/css", ".png": "image/png", ".svg": "image/svg+xml", ".wasm": "application/wasm" }[path.extname(file)] || "application/octet-stream";
    res.writeHead(200, { "Content-Type": type }); res.end(readFileSync(file));
  });
  server.on("upgrade", (_req, socket) => socket.destroy());
  return new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve({ server, origin: `http://127.0.0.1:${server.address().port}` })));
}
const baseline = await serve(process.env.FLVX_BASELINE_DIST || path.join(run, "baseline-dist")), current = await serve(process.env.FLVX_DIST || path.join(root, "frontend/dist"));
let browser;
const captures = [], errors = [], checks = [], fontIssues = [];
const chromiumPath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE || (existsSync("/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome") ? "/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome" : undefined);

async function contextFor(origin, width) {
  const context = await browser.newContext({ viewport: { width, height: width > 800 ? 1000 : 844 }, timezoneId: "Asia/Taipei" });
  await context.addInitScript(({ now }) => {
    localStorage.setItem("mmwx-session", JSON.stringify({ token: "local-fixture", username: "admin", role: "admin", isAdmin: true, expiresAt: "2099-01-01T00:00:00Z" }));
    const Original = Date;
    class Fixed extends Original { constructor(...args) { super(...(args.length ? args : [now])); } static now() { return now; } }
    window.Date = Fixed;
  }, { now });
  const data = fixture();
  await context.route("**/*", async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.origin !== origin) { errors.push(`blocked external ${url.origin}`); return route.abort(); }
    if (!url.pathname.startsWith("/api/")) return route.continue();
    let response = { success: true };
    if (url.pathname.startsWith("/api/custom/flvx/admin")) { if (req.method() === "GET") response = data; }
    else if (url.pathname === "/api/v3") response = (req.postDataJSON() || {}).op === "034e094d05aa3f83" ? { servers } : { nodes: [], users: [], packages: [] };
    else if (url.pathname === "/api/traffic/summary") response = { metrics: { total_limit_gb: 10240, total_used_gb: 1600, total_remaining_gb: 8640, usage_percentage: 15.625, unlimited_used_gb: 0 }, history: [{ date: "2026-10-22", used_gb: 1510 }, { date: "2026-10-23", used_gb: 1600 }] };
    else if (url.pathname === "/api/admin/traffic/period") response = { items: [], servers: [], range: "today", total_traffic: 0 };
    else if (url.pathname === "/api/custom/dashboard/system") response = { cpu_pct: 12, cpu_cores: 2, mem_used: 512 * 2 ** 20, mem_total: 2 * GB, disk_used: 8 * GB, disk_total: 40 * GB, has_cpu: true, has_mem: true, has_disk: true, loadavg: "0.12 0.10 0.08" };
    else if (url.pathname.includes("agent/version-info")) response = { current: "v0.5.5", latest: "v0.5.5" };
    else if (url.pathname.includes("services/status")) response = { xray_running: true, xray_version: "b01adef" };
    else if (url.pathname === "/api/custom/helper-batch") response = { preview: [], version: "v0.7.13", job: null };
    else if (url.pathname === "/api/custom/geo/lookup") response = { success: true, country_code: "US", country: "美国", flag: "🇺🇸" };
    else if (url.pathname === "/api/custom/server-deletions") response = { plans: [] };
    else if (url.pathname === "/api/custom/ui/service-groups") response = { groups: [] };
    else if (url.pathname.includes("/traffic/servers")) response = { servers: [], nodes: [] };
    else if (url.pathname.startsWith("/api/custom/") || url.pathname.includes("connections")) response = { success: true, metrics: {}, connections: {}, core_modes: {}, groups: [] };
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(response) });
  });
  const page = await context.newPage(); page.on("pageerror", (err) => errors.push(err.message));
  await page.goto(origin); await page.locator(".system-status-card").waitFor(); await page.waitForTimeout(2200);
  return { context, page };
}

async function shot(page, name, { element } = {}) {
  await page.waitForTimeout(150);
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `${name}: horizontal overflow`);
  const small = await smallText(page, ".flvx-page, .flvx-dialog");
  if (small.length) fontIssues.push({ name, small });
  if (enforceFontFloor) assert.deepEqual(small, [], `${name}: text below ${fontFloor}px`);
  const file = `${name}.png`;
  if (element) await element.screenshot({ path: path.join(screens, file), animations: "disabled" });
  else await page.screenshot({ path: path.join(screens, file), fullPage: true, animations: "disabled" });
  captures.push(file);
}
async function openFLVX(page) { await page.getByRole("button", { name: "打开菜单", exact: true }).click(); await page.getByRole("button", { name: "FLVX 共享", exact: true }).click(); await page.getByRole("heading", { name: /US 年付/ }).waitFor(); }
const card = (page, i) => page.locator(".flvx-card").nth(i);
const guardLine = (page, i) => card(page, i).locator(".flvx-egress").filter({ hasText: "Reality 防盗" });

try {
  browser = await chromium.launch({ headless: true, executablePath: chromiumPath, args: ["--disable-dev-shm-usage"] });
  for (const width of [1440, 390]) {
    const b = await contextFor(baseline.origin, width); await shot(b.page, `overview-before-${width}`); await openFLVX(b.page); await shot(b.page, `share-page-before-${width}`);
    assert.equal(await b.page.getByText("Reality 防盗").count(), 0); await b.context.close();
    const { context, page } = await contextFor(current.origin, width);
    await shot(page, `overview-after-${width}`);
    const before = PNG.sync.read(readFileSync(path.join(screens, `overview-before-${width}.png`))), after = PNG.sync.read(readFileSync(path.join(screens, `overview-after-${width}.png`)));
    assert.ok(before.width === after.width && before.height === after.height && before.data.equals(after.data), `overview ${width} changed pixels`);
    checks.push(`overview ${width}: pixel-identical before/after`);
    await openFLVX(page); await shot(page, `share-page-after-${width}`);
    for (const [i, [text, chip]] of expected.entries()) {
      const line = guardLine(page, i);
      assert.equal(await line.locator("strong").textContent(), text, `card ${i}`);
      assert.equal(await line.locator(".flvx-status").count() ? await line.locator(".flvx-status").textContent() : null, chip, `card ${i} chip`);
    }
    assert.match(await guardLine(page, 1).locator("small").textContent(), /41001/);
    assert.match(await guardLine(page, 3).locator("small").textContent(), /规则未生效/);
    assert.equal(await guardLine(page, 4).locator("small").count(), 0, "restart reason is the line itself");
    assert.equal(await page.getByText(/127\.0\.0\.1|lovelive|flvxg-/).count(), 0);
    // Every other line of the card is unchanged: the guard line is the only addition.
    assert.equal(await page.locator(".flvx-egress").count(), expected.length * 3);
    checks.push(`${width}: seven guard states (on, partial, FLVX off, error, restart, waiting, old Helper); no loopback/decoy details`);
    for (const [i, label] of ["on", "partial", "flvx-off", "error", "restart", "waiting", "old-helper"].entries()) await shot(page, `card-${label}-${width}`, { element: card(page, i) });
    await page.getByRole("button", { name: "打开菜单", exact: true }).click(); await page.getByRole("button", { name: "切换主题", exact: true }).click(); await page.getByRole("button", { name: "FLVX 共享", exact: true }).click();
    await page.getByRole("heading", { name: /US 年付/ }).waitFor();
    await shot(page, `dark-share-page-${width}`);
    for (const [i, label] of [[0, "on"], [3, "error"]]) await shot(page, `dark-card-${label}-${width}`, { element: card(page, i) });
    await context.close();
  }
  assert.deepEqual(errors, []);
} finally {
  await browser?.close(); await new Promise((r) => baseline.server.close(r)); await new Promise((r) => current.server.close(r));
  writeFileSync(path.join(screens, "manifest.json"), JSON.stringify({ captures, errors, checks, fontIssues, hashes: Object.fromEntries(captures.map((f) => [f, createHash("sha256").update(readFileSync(path.join(screens, f))).digest("hex")])) }, null, 2));
}
const group = (prefix) => captures.filter((f) => f.startsWith(prefix));
const section = (title, files) => `<h2>${title}</h2><main>${files.map((f) => `<article><h3>${f.replace(".png", "")}</h3><a href="${f}"><img src="${f}" loading="lazy"></a></article>`).join("")}</main>`;
writeFileSync(path.join(screens, "index.html"), `<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>FLVX 共享 · Reality 防盗 · 截图预览</title><style>body{font:15px system-ui;background:#edf0f4;color:#253247;margin:24px}main{display:grid;grid-template-columns:repeat(auto-fit,minmax(300px,1fr));gap:20px}article{background:white;padding:16px;border-radius:14px}h3{font-size:14px}img{width:100%;height:auto;border:1px solid #e0e5ed}a{color:#215daa}</style><h1>FLVX 共享 · Reality 防盗（电脑 1440 + 手机 390）</h1><p>本机合成数据，未连接生产或真实服务器。每张服务器卡片在「出站与路由」下面新增一行「Reality 防盗」，其余界面不变；概览页前后像素完全一致。</p>${section("改动前后对比", group("share-page-"))}${section("七种状态的服务器卡片", group("card-"))}${section("暗色", group("dark-"))}${section("概览页（不变）", group("overview-"))}</html>`);
console.log(`${captures.length} screenshots; ${checks.join("; ")}`);
