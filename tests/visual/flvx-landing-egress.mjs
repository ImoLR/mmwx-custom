// FLVX 共享 · 出站与路由 (landing egress) screenshots and checks.
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
const run = process.env.FLVX_EVIDENCE || "/root/mmwx-workers/runs/landing-egress-mmwx";
const screens = process.env.FLVX_SCREENS || path.join(run, "screens"); mkdirSync(screens, { recursive: true });
const now = Date.parse("2026-10-19T12:00:00Z"), GB = 2 ** 30;
const names = ["US 年付 · 共享演示", "香港 · 备用共享", "东京 · 规则异常", "新加坡 · 旧版 Helper", "日本 · 默认直连"];
const servers = names.map((name, i) => ({ id: i + 7, name, ip_address: `203.0.113.${i + 7}`, status: "connected", xray_mode: "external", xray_running: true, xray_version: "8f5b614", agent_version: "v0.5.5", country_code: ["US", "HK", "JP", "SG", "JP"][i], flag: ["🇺🇸", "🇭🇰", "🇯🇵", "🇸🇬", "🇯🇵"][i], traffic_limit: 2 * 1024 * GB, traffic_used: 320 * GB, current_upload_speed: 123456, current_download_speed: 234567, last_heartbeat: new Date(now).toISOString(), boot_time: new Date(now - 86400_000).toISOString(), traffic_reset_day: 1 }));
const available = servers.map((s) => ({ server_id: s.id, name: s.name, hosts: { ipv4: s.ip_address, ipv6: "", domain: "" }, official_ports: [10001, 10002], ports_known: true, dual_install: false, native_landing: false, detection_error: false }));
const cfg = { port_min: 30000, port_max: 30999, traffic_limit_bytes: 500 * GB, reset_day: 1, max_connections: 100, share_until: null, memory_mb: 256, cpu_percent: 50 };
const node = (id, owner, protocol, port, used, state = "active") => ({ id, owner: { id: owner === "小林" ? "21" : "22", name: owner }, name: `${owner} · ${protocol === "ss2022" ? "高速 SS" : "Reality 专线"}`, protocol, port, max_connections: 20, expires_at: Date.parse("2026-11-18T15:59:59Z"), status: { id, state, up_bytes: used * GB * .2, down_bytes: used * GB * .8, connections: state === "active" ? 4 : 0 } });
const capability = { versions: [1], allowed: true, geoip: true, geosite: true };
const share = (i, extra) => ({ server_id: i + 7, name: names[i], hosts: available[i].hosts, status: "active", config: { ...cfg }, traffic_used_bytes: 186.4 * GB, traffic_limit_bytes: 500 * GB, connections: 12, max_connections: 100, core: { state: "running", version: "8f5b614" }, dual_install: false, nodes: [node(`${i}01`, "小林", "ss2022", 30001, 68.2), node(`${i}02`, "Alex", "vless_reality_vision", 30002, 45.3)], report: { collected_at: now, applied_revision: 12 }, allow_egress: true, egress: { ...capability }, egress_plan: { active: false, outbounds: 0, outbound_types: [], rules: 0, covered_nodes: 0 }, ...extra });
const fixture = () => ({ shares: [
  share(0, { egress_plan: { active: true, outbounds: 2, outbound_types: ["wireguard", "socks"], rules: 5, covered_nodes: 2 }, report: { collected_at: now, applied_revision: 57, egress: { state: "applied", error: "", applied_revision: 57, warnings: ["flvxo-3: 服务器没有可用的 IPv6 地址，强制 IPv6 的连接会失败"] } } }),
  share(1, { status: "paused", allow_egress: false, egress: { ...capability, allowed: false }, egress_plan: { active: true, outbounds: 1, outbound_types: ["wireguard"], rules: 2, covered_nodes: 1 }, report: { collected_at: now, applied_revision: 5, egress: { state: "none", error: "", applied_revision: 5, warnings: [] } } }),
  share(2, { egress_plan: { active: true, outbounds: 1, outbound_types: ["vless"], rules: 3, covered_nodes: 2 }, report: { collected_at: now, applied_revision: 9, egress: { state: "error", error: "geo 数据 geosite.dat 下载或校验失败，2 分钟后重试", applied_revision: 8, warnings: [] } } }),
  share(3, { egress: undefined, report: { collected_at: now, applied_revision: 3 } }),
  share(4, { report: { collected_at: now, applied_revision: 4, egress: { state: "none", error: "", applied_revision: 4, warnings: [] } } }),
], servers: structuredClone(available), integration: { token_configured: true, last_api_at: now - 20_000 } });

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
const captures = [], errors = [], checks = [], mutations = [], fontIssues = [];
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
    const body = req.postDataJSON() || {}; let response = { success: true };
    if (url.pathname.startsWith("/api/custom/flvx/admin")) {
      if (req.method() !== "GET") mutations.push({ method: req.method(), path: url.pathname, body });
      if (req.method() === "PUT") {
        const s = data.shares.find((v) => v.server_id === Number(url.pathname.split("/").pop()));
        if (s) { s.status = body.status; if (body.port_min) { const { status, allow_egress, ...config } = body; s.config = config; } if (typeof body.allow_egress === "boolean") { s.allow_egress = body.allow_egress; if (s.egress) s.egress.allowed = body.allow_egress; } }
      } else response = data;
    } else if (url.pathname === "/api/v3") response = body.op === "034e094d05aa3f83" ? { servers } : { nodes: [], users: [], packages: [] };
    else if (url.pathname === "/api/traffic/summary") response = { metrics: { total_limit_gb: 10240, total_used_gb: 1600, total_remaining_gb: 8640, usage_percentage: 15.625, unlimited_used_gb: 0 }, history: [{ date: "2026-10-18", used_gb: 1510 }, { date: "2026-10-19", used_gb: 1600 }] };
    else if (url.pathname === "/api/admin/traffic/period") response = { items: [], servers: [], range: "today", total_traffic: 0 };
    else if (url.pathname === "/api/custom/dashboard/system") response = { cpu_pct: 12, cpu_cores: 2, mem_used: 512 * 2 ** 20, mem_total: 2 * GB, disk_used: 8 * GB, disk_total: 40 * GB, has_cpu: true, has_mem: true, has_disk: true, loadavg: "0.12 0.10 0.08" };
    else if (url.pathname.includes("agent/version-info")) response = { current: "v0.5.5", latest: "v0.5.5" };
    else if (url.pathname.includes("services/status")) response = { xray_running: true, xray_version: "8f5b614" };
    else if (url.pathname === "/api/custom/helper-batch") response = { preview: [], version: "v0.7.6", job: null };
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

async function shot(page, name, { element, dialog } = {}) {
  await page.waitForTimeout(150);
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `${name}: horizontal overflow`);
  const small = await smallText(page, ".flvx-page, .flvx-dialog");
  if (small.length) fontIssues.push({ name, small });
  if (enforceFontFloor) assert.deepEqual(small, [], `${name}: text below ${fontFloor}px`);
  const file = `${name}.png`;
  if (dialog) await page.locator(".flvx-switch, .flvx-dialog .flvx-advanced").first().scrollIntoViewIfNeeded();
  if (element) await element.screenshot({ path: path.join(screens, file), animations: "disabled" });
  else if (dialog) await page.screenshot({ path: path.join(screens, file), animations: "disabled" });
  else await page.screenshot({ path: path.join(screens, file), fullPage: true, animations: "disabled" });
  captures.push(file);
}
async function openFLVX(page) { await page.getByRole("button", { name: "打开菜单", exact: true }).click(); await page.getByRole("button", { name: "FLVX 共享", exact: true }).click(); await page.getByRole("heading", { name: /US 年付/ }).waitFor(); }
const card = (page, i) => page.locator(".flvx-card").nth(i);

try {
  browser = await chromium.launch({ headless: true, executablePath: chromiumPath, args: ["--disable-dev-shm-usage"] });
  for (const width of [1440, 390]) {
    const b = await contextFor(baseline.origin, width); await shot(b.page, `overview-before-${width}`); await openFLVX(b.page); await shot(b.page, `share-page-before-${width}`);
    await b.page.getByRole("button", { name: "设置", exact: true }).first().click(); await shot(b.page, `settings-before-${width}`, { dialog: true }); await b.context.close();
    const { context, page } = await contextFor(current.origin, width);
    await shot(page, `overview-after-${width}`);
    const before = PNG.sync.read(readFileSync(path.join(screens, `overview-before-${width}.png`))), after = PNG.sync.read(readFileSync(path.join(screens, `overview-after-${width}.png`)));
    assert.ok(before.width === after.width && before.height === after.height && before.data.equals(after.data), `overview ${width} changed pixels`);
    checks.push(`overview ${width}: pixel-identical before/after`);
    await openFLVX(page); await shot(page, `share-page-after-${width}`);
    const lines = await page.locator(".flvx-egress strong").allTextContents();
    assert.deepEqual(lines, ["FLVX 自定义 · WARP、SOCKS5 · 5 条规则 · 2 个节点", "未允许 · FLVX 节点一律从本机直连", "FLVX 自定义 · VLESS Reality · 3 条规则 · 2 个节点", "升级 Helper 后可用 · 目前一律直连", "默认直连"]);
    assert.equal(await card(page, 0).locator(".flvx-egress .flvx-status").textContent(), "已生效");
    assert.equal(await card(page, 2).locator(".flvx-egress .flvx-status").textContent(), "应用失败");
    assert.match(await card(page, 2).locator(".flvx-egress small").textContent(), /geosite\.dat/);
    assert.equal(await page.locator(".flvx-egress").getByText(/proxy|password|198\.51/).count(), 0);
    checks.push(`${width}: five egress states (applied+warning, not allowed, error, old Helper, default) without upstream details`);
    for (const [i, label] of [[0, "applied"], [1, "not-allowed"], [2, "error"], [3, "old-helper"], [4, "default"]]) await shot(page, `card-${label}-${width}`, { element: card(page, i) });
    await card(page, 0).getByRole("button", { name: "设置", exact: true }).click();
    const toggle = page.getByRole("switch", { name: /允许自定义出站与路由/ });
    assert.equal(await toggle.isChecked(), true);
    await shot(page, `settings-egress-on-${width}`, { dialog: true });
    await toggle.click(); assert.equal(await toggle.isChecked(), false);
    await shot(page, `settings-egress-off-${width}`, { dialog: true });
    await page.getByRole("button", { name: "保存设置", exact: true }).click(); await page.getByRole("dialog").waitFor({ state: "detached" });
    const saved = mutations.at(-1);
    assert.ok(saved.path === "/api/custom/flvx/admin/shares/7" && saved.body.allow_egress === false && saved.body.status === "active" && saved.body.port_min === 30000);
    await page.getByRole("button", { name: "刷新共享状态" }).click(); await page.waitForTimeout(400);
    assert.equal(await card(page, 0).locator(".flvx-egress strong").textContent(), "未允许 · FLVX 节点一律从本机直连");
    await shot(page, `card-after-disable-${width}`, { element: card(page, 0) });
    // Status-only edits never send the policy.
    await card(page, 0).getByRole("button", { name: "暂停共享", exact: true }).click(); await page.waitForTimeout(300);
    assert.deepEqual(mutations.at(-1).body, { status: "paused" });
    await card(page, 1).getByRole("button", { name: "设置", exact: true }).click();
    assert.equal(await page.getByRole("switch", { name: /允许自定义出站与路由/ }).isChecked(), false);
    await page.getByRole("button", { name: "关闭面板", exact: true }).click();
    await page.getByRole("button", { name: "共享一台服务器", exact: true }).click();
    assert.equal(await page.getByRole("switch", { name: /允许自定义出站与路由/ }).isChecked(), true);
    await shot(page, `new-share-default-on-${width}`, { dialog: true });
    await page.getByRole("button", { name: "关闭面板", exact: true }).click();
    checks.push(`${width}: toggle default on, saves allow_egress:false with the config, status-only edits omit it, new shares default on`);
    await page.getByRole("button", { name: "打开菜单", exact: true }).click(); await page.getByRole("button", { name: "切换主题", exact: true }).click(); await page.getByRole("button", { name: "FLVX 共享", exact: true }).click();
    await shot(page, `dark-share-page-${width}`);
    await card(page, 2).getByRole("button", { name: "设置", exact: true }).click(); await shot(page, `dark-settings-${width}`, { dialog: true });
    await context.close();
  }
  assert.deepEqual(errors, []);
} finally {
  await browser?.close(); await new Promise((r) => baseline.server.close(r)); await new Promise((r) => current.server.close(r));
  writeFileSync(path.join(screens, "manifest.json"), JSON.stringify({ captures, errors, checks, fontIssues, mutations: mutations.map(({ method, path, body }) => ({ method, path, body })), hashes: Object.fromEntries(captures.map((f) => [f, createHash("sha256").update(readFileSync(path.join(screens, f))).digest("hex")])) }, null, 2));
}
const group = (prefix) => captures.filter((f) => f.startsWith(prefix));
const section = (title, files) => `<h2>${title}</h2><main>${files.map((f) => `<article><h3>${f.replace(".png", "")}</h3><a href="${f}"><img src="${f}" loading="lazy"></a></article>`).join("")}</main>`;
writeFileSync(path.join(screens, "index.html"), `<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>FLVX 共享 · 出站与路由 · 截图预览</title><style>body{font:15px system-ui;background:#edf0f4;color:#253247;margin:24px}main{display:grid;grid-template-columns:repeat(auto-fit,minmax(300px,1fr));gap:20px}article{background:white;padding:16px;border-radius:14px}h3{font-size:14px}img{width:100%;height:auto;border:1px solid #e0e5ed}a{color:#215daa}</style><h1>FLVX 共享 · 出站与路由（电脑 1440 + 手机 390）</h1><p>本机合成数据，未连接生产或真实服务器。服务器卡片新增「出站与路由」一行，共享设置新增「允许自定义出站与路由」开关（默认开）；其余界面不变，概览页前后像素完全一致。</p>${section("改动前后对比", [...group("share-page-"), ...group("settings-before"), ...group("settings-egress-on")])}${section("五种状态的服务器卡片", group("card-"))}${section("开关", [...group("settings-egress-off"), ...group("new-share")])}${section("暗色", group("dark-"))}${section("概览页（不变）", group("overview-"))}</html>`);
console.log(`${captures.length} screenshots; ${checks.join("; ")}`);
