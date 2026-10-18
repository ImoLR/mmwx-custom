// Static local builds and synthetic API responses only; all remote requests fail.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { readFileSync, writeFileSync, existsSync, mkdirSync } from "node:fs";
import { createHash } from "node:crypto";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const require = createRequire(path.join(root, "frontend/package.json"));
const { chromium } = require("playwright");
const { PNG } = require("pngjs");
const run = process.env.FLVX_EVIDENCE || "/root/mmwx-workers/runs/flvx-share";
const screens = path.join(run, "screens"); mkdirSync(screens, { recursive: true });
const now = Date.parse("2026-10-18T12:00:00Z"), GB = 2 ** 30;
const serverNames = ["US 年付 · 共享演示", "香港 · 备用共享", "东京 · 待共享", "新加坡 · 双装演示", "日本 · 原生落地"];
const servers = serverNames.map((name, i) => ({ id: i + 7, name, ip_address: `203.0.113.${i + 7}`, status: "connected", xray_mode: "external", xray_running: true, xray_version: "8f5b614", agent_version: "v0.5.5", country_code: ["US", "HK", "JP", "SG", "JP"][i], flag: ["🇺🇸", "🇭🇰", "🇯🇵", "🇸🇬", "🇯🇵"][i], traffic_limit: 2 * 1024 * GB, traffic_used: 320 * GB, current_upload_speed: 123456, current_download_speed: 234567, last_heartbeat: new Date(now).toISOString(), boot_time: new Date(now - 86400_000).toISOString(), traffic_reset_day: 1 }));
const available = servers.map((s, i) => ({ server_id: s.id, name: s.name, hosts: { ipv4: s.ip_address, ipv6: "", domain: "" }, official_ports: i === 2 ? [30000, 30001] : [10001, 10002], ports_known: true, dual_install: i === 0 || i === 3, native_landing: i === 4, detection_error: false }));
const cfg = { port_min: 30000, port_max: 30999, traffic_limit_bytes: 500 * GB, reset_day: 1, max_connections: 100, share_until: null, memory_mb: 256, cpu_percent: 50 };
const node = (id, owner, protocol, port, used, state = "active") => ({ id, owner: { id: owner === "小林" ? "21" : "22", name: owner }, name: `${owner} · ${protocol === "ss2022" ? "高速 SS" : "Reality 专线"}`, protocol, port, max_connections: 20, expires_at: Date.parse("2026-11-18T15:59:59Z"), status: { id, state, up_bytes: used * GB * .2, down_bytes: used * GB * .8, connections: state === "active" ? 4 : 0 } });
const fixture = () => ({ shares: [
  { server_id: 7, name: servers[0].name, hosts: available[0].hosts, status: "active", config: { ...cfg }, traffic_used_bytes: 186.4 * GB, traffic_limit_bytes: 500 * GB, connections: 12, max_connections: 100, core: { state: "running", version: "8f5b614" }, dual_install: true, nodes: [node("101", "小林", "ss2022", 30001, 68.2), node("102", "小林", "vless_reality_vision", 30002, 45.3), node("103", "Alex", "ss2022", 30003, 72.9)], report: { collected_at: now, applied_revision: 12 } },
  { server_id: 8, name: servers[1].name, hosts: available[1].hosts, status: "paused", config: { ...cfg, share_until: Date.parse("2026-12-31T15:59:59Z"), traffic_limit_bytes: 200 * GB, max_connections: 50 }, traffic_used_bytes: 42.6 * GB, traffic_limit_bytes: 200 * GB, connections: 0, max_connections: 50, core: { state: "running", version: "8f5b614" }, dual_install: false, nodes: [node("104", "Alex", "vless_reality_vision", 30001, 42.6, "removed")], report: { collected_at: now, applied_revision: 5 } },
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
const baseline = await serve(path.join(run, "baseline-dist")), current = await serve(path.join(root, "frontend/dist"));
let browser;
const captures = [], errors = [], checks = [], mutations = [];
const chromiumPath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE || (existsSync("/root/.cache/ms-playwright/chromium-1124/chrome-linux/chrome") ? "/root/.cache/ms-playwright/chromium-1124/chrome-linux/chrome" : undefined);

async function contextFor(origin, width) {
  const context = await browser.newContext({ viewport: { width, height: width > 800 ? 1000 : 844 }, timezoneId: "Asia/Taipei", permissions: ["clipboard-read", "clipboard-write"] });
  await context.addInitScript(({ now }) => {
    localStorage.setItem("mmwx-session", JSON.stringify({ token: "local-fixture", username: "admin", role: "admin", isAdmin: true, expiresAt: "2099-01-01T00:00:00Z" }));
    const Original = Date;
    class Fixed extends Original { constructor(...args) { super(...(args.length ? args : [now])); } static now() { return now; } }
    window.Date = Fixed;
  }, { now });
  let data = fixture();
  await context.route("**/*", async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.origin !== origin) { errors.push(`blocked external ${url.origin}`); return route.abort(); }
    if (!url.pathname.startsWith("/api/")) return route.continue();
    const body = req.postDataJSON() || {}; let response = { success: true };
    if (url.pathname.startsWith("/api/custom/flvx/admin")) {
      if (req.method() !== "GET") mutations.push({ method: req.method(), path: url.pathname, body });
      if (url.pathname.endsWith("/token")) { data.integration.token_configured = req.method() === "POST"; response = { token: req.method() === "POST" ? "flvx_local_preview_token_shown_once_only_0123456789" : "" }; }
      else if (req.method() === "PUT") { const id = Number(url.pathname.split("/").pop()); const share = data.shares.find((s) => s.server_id === id); if (share) { share.status = body.status; if (body.port_min) share.config = { ...body }; } response = { success: true }; }
      else response = data;
    } else if (url.pathname === "/api/v3") response = body.op === "034e094d05aa3f83" ? { servers } : { nodes: [], users: [], packages: [] };
    else if (url.pathname === "/api/traffic/summary") response = { metrics: { total_limit_gb: 10240, total_used_gb: 1600, total_remaining_gb: 8640, usage_percentage: 15.625, unlimited_used_gb: 0 }, history: [{ date: "2026-10-17", used_gb: 1510 }, { date: "2026-10-18", used_gb: 1600 }] };
    else if (url.pathname === "/api/admin/traffic/period") response = { items: [], servers: [], range: "today", total_traffic: 0 };
    else if (url.pathname === "/api/custom/dashboard/system") response = { cpu_pct: 12, cpu_cores: 2, mem_used: 512 * 2 ** 20, mem_total: 2 * GB, disk_used: 8 * GB, disk_total: 40 * GB, has_cpu: true, has_mem: true, has_disk: true, loadavg: "0.12 0.10 0.08" };
    else if (url.pathname.includes("agent/version-info")) response = { current: "v0.5.5", latest: "v0.5.5" };
    else if (url.pathname.includes("services/status")) response = { xray_running: true, xray_version: "8f5b614" };
    else if (url.pathname === "/api/custom/helper-batch") response = { preview: [], version: "v0.7.4", job: null };
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

async function shot(page, name) {
  await page.waitForTimeout(120);
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `${name}: horizontal overflow`);
  const file = `${name}.png`; await page.screenshot({ path: path.join(screens, file), fullPage: true, animations: "disabled" }); captures.push(file);
}
async function openPage(page, name) { await page.getByRole("button", { name: "打开菜单", exact: true }).click(); await page.getByRole("button", { name, exact: true }).click(); }

try {
  browser = await chromium.launch({ headless: true, executablePath: chromiumPath, args: ["--disable-dev-shm-usage"] });
  for (const width of [1440, 390]) {
    const b = await contextFor(baseline.origin, width); await shot(b.page, `overview-before-${width}`); await b.context.close();
    const { context, page } = await contextFor(current.origin, width);
    await shot(page, `overview-after-${width}`);
    const before = PNG.sync.read(readFileSync(path.join(screens, `overview-before-${width}.png`))), after = PNG.sync.read(readFileSync(path.join(screens, `overview-after-${width}.png`)));
    assert.equal(before.width, after.width); assert.equal(before.height, after.height); assert.ok(before.data.equals(after.data), `overview ${width} changed pixels`);
    checks.push(`overview ${width}: pixel-identical before/after`);
    await page.getByRole("button", { name: "打开菜单", exact: true }).click();
    const nav = await page.locator(".menu-item").allTextContents(); assert.equal(nav[nav.indexOf("转发管理") + 1], "FLVX 共享");
    await shot(page, `navigation-${width}`); await page.getByRole("button", { name: "FLVX 共享", exact: true }).click();
    await page.getByRole("heading", { name: /US 年付/ }).waitFor(); await shot(page, `servers-${width}`);
    await page.locator(".flvx-nodes > summary").first().click(); await shot(page, `nodes-expanded-${width}`);
    await page.getByRole("button", { name: "按用户", exact: true }).click(); await shot(page, `users-${width}`);
    await page.getByRole("button", { name: "按服务器", exact: true }).click();
    await page.getByRole("button", { name: "共享一台服务器", exact: true }).click(); await shot(page, `share-dialog-${width}`);
    await page.getByRole("dialog").locator("select").first().selectOption("9");
    assert.equal(await page.getByLabel("端口起始", { exact: true }).inputValue(), "31000");
    await page.getByText("高级 · 资源上限", { exact: true }).click(); await shot(page, `share-advanced-${width}`);
    await page.getByRole("dialog").locator("select").first().selectOption("10"); await shot(page, `share-dual-warning-${width}`);
    assert.equal(await page.getByRole("button", { name: "确认共享", exact: true }).isEnabled(), true);
    await page.getByRole("dialog").locator("select").first().selectOption("11"); await shot(page, `share-native-refused-${width}`);
    assert.equal(await page.getByRole("button", { name: "确认共享", exact: true }).isDisabled(), true);
    await page.getByRole("button", { name: "关闭面板", exact: true }).click();
    await page.getByRole("button", { name: "设置", exact: true }).first().click(); await page.getByText("高级 · 资源上限", { exact: true }).click(); await shot(page, `settings-${width}`); await page.getByRole("button", { name: "关闭面板", exact: true }).click();
    await page.getByRole("button", { name: "取消共享", exact: true }).first().click();
    assert.equal(await page.getByRole("button", { name: "确认取消共享", exact: true }).isDisabled(), true);
    await shot(page, `cancel-confirmation-${width}`); await page.getByLabel("确认服务器名称").fill(servers[0].name);
    assert.equal(await page.getByRole("button", { name: "确认取消共享", exact: true }).isEnabled(), true);
    await shot(page, `cancel-name-gate-${width}`); await page.getByRole("button", { name: "保留共享", exact: true }).click();
    await page.getByRole("button", { name: "接入令牌", exact: true }).click(); await shot(page, `token-management-${width}`);
    await page.getByRole("button", { name: "重新生成令牌", exact: true }).click(); await page.locator(".flvx-token code").waitFor(); await shot(page, `token-once-${width}`);
    await page.getByRole("button", { name: "关闭面板", exact: true }).click(); await page.getByRole("button", { name: "接入令牌", exact: true }).click(); assert.equal(await page.locator(".flvx-token code").count(), 0); await page.getByRole("button", { name: "关闭面板", exact: true }).click();
    await openPage(page, "服务管理"); await page.getByRole("button", { name: "已共享给 FLVX", exact: true }).first().waitFor(); await shot(page, `service-badge-${width}`);
    await page.getByRole("button", { name: "已共享给 FLVX", exact: true }).first().click(); await page.locator(".flvx-page").waitFor();
    await page.getByRole("button", { name: "打开菜单", exact: true }).click(); await page.getByRole("button", { name: "切换主题", exact: true }).click(); await page.getByRole("button", { name: "FLVX 共享", exact: true }).click(); await shot(page, `dark-${width}`);
    checks.push(`${width}: nav order, auto-avoid official ports, dual warning/native refusal, cancel name gate, token shown once, badge navigation, no overflow`);
    await context.close();
  }
  assert.deepEqual(errors, []);
  assert.ok(mutations.every((r) => r.path === "/api/custom/flvx/admin/token" && r.method === "POST"));
} finally {
  await browser?.close(); await new Promise((r) => baseline.server.close(r)); await new Promise((r) => current.server.close(r));
  writeFileSync(path.join(screens, "manifest.json"), JSON.stringify({ captures, errors, checks, mutations: mutations.map(({ method, path }) => ({ method, path })), hashes: Object.fromEntries(captures.map((f) => [f, createHash("sha256").update(readFileSync(path.join(screens, f))).digest("hex")])) }, null, 2));
}
writeFileSync(path.join(screens, "index.html"), `<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>FLVX 共享 · 截图预览</title><style>body{font:15px system-ui;background:#edf0f4;color:#253247;margin:24px}main{display:grid;grid-template-columns:repeat(auto-fit,minmax(300px,1fr));gap:20px}article{background:white;padding:16px;border-radius:14px}img{width:100%;height:auto;border:1px solid #e0e5ed}a{color:#215daa}</style><h1>FLVX 共享 · 桌面与手机预览</h1><p>本机合成数据，未连接生产或真实服务器。概览前后像素完全一致。令牌仅为无效演示文字。</p><main>${captures.map((f) => `<article><h2>${f.replace(".png", "")}</h2><a href="${f}"><img src="${f}" loading="lazy"></a></article>`).join("")}</main></html>`);
console.log(`${captures.length} screenshots; ${checks.join("; ")}`);
