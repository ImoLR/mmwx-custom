// Fixture-only acceptance for the inbound machine-port check and the Reality key race.
// No official backend, credentials or remote requests; every API answer is local.
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
const dist = process.env.PORT_CHECK_DIST || path.join(root, "frontend/dist");
const evidence = process.env.PORT_CHECK_EVIDENCE || path.join(root, "runs/inbound-port-check/evidence");
mkdirSync(evidence, { recursive: true });
const server = await serveFixtureBuild(dist);
const base = `http://127.0.0.1:${server.address().port}`;
const requests = [], errors = [], fonts = [], checks = [];
const { servers, groups, nodes } = fixture;
// 「Boil HKT 99u」(2) inbounds and listeners as on 2026-10-28; 「Boil Hinet 158」(12) still runs Helper v0.7.13.
const inbounds = [
  { tag: "api", port: 46736, protocol: "dokodemo-door", listen: "127.0.0.1", settings: { address: "127.0.0.1" } },
  { tag: "wings ss", port: 10015, protocol: "shadowsocks", settings: { method: "2022-blake3-aes-128-gcm", network: "tcp,udp", clients: [] } },
  { tag: "自己用 ss-10016", port: 10016, protocol: "shadowsocks", settings: { method: "2022-blake3-aes-128-gcm", network: "tcp,udp", clients: [] } },
  { tag: "king ss-10017", port: 10018, protocol: "shadowsocks", settings: { method: "2022-blake3-aes-128-gcm", network: "tcp,udp", clients: [] } },
];
const config = { ...structuredClone(fixture.config), inbounds };
const core = (port, tag, protocol = "tcp") => ({ port, protocol, address: "*", pid: 231078, process: "xray", owner: "core", inbound_tag: tag });
const listening = {
  success: true, available: true, supported: true, helper_version: "v0.7.14",
  listening: { scanned_at: fixture.now, ports: [
    { port: 22, protocol: "tcp", address: "*", pid: 521, process: "sshd" },
    core(10015, "wings ss"), core(10015, "wings ss", "udp"), core(10016, "自己用 ss-10016"), core(10016, "自己用 ss-10016", "udp"),
    { port: 10017, protocol: "tcp", address: "*", pid: 212883, process: "lkbk-core" }, { port: 10017, protocol: "udp", address: "*", pid: 212883, process: "lkbk-core" },
    core(10018, "king ss-10017"), core(10018, "king ss-10017", "udp"),
    { port: 23889, protocol: "tcp", address: "127.0.0.1", pid: 475, process: "mmw-agent", owner: "agent" },
    { port: 38889, protocol: "tcp", address: "127.0.0.1", pid: 231078, process: "xray", owner: "core" },
    { ...core(46736, "api"), address: "127.0.0.1" },
  ] },
};
const busy = new Set(listening.listening.ports.map((entry) => entry.port));
let browser;
const saveReport = () => writeFileSync(path.join(evidence, "report.json"), JSON.stringify({ fonts, checks, errors, requests }, null, 2));
const check = (name, ok, detail = "") => { checks.push({ name, ok, detail }); saveReport(); assert.ok(ok, `${name} ${detail}`); };

try {
  const available = Number(readFileSync("/proc/meminfo", "utf8").match(/MemAvailable:\s+(\d+)/)[1]) / 1024;
  assert.ok(available >= 900, `Wait for 900 MB available before Playwright (currently ${Math.floor(available)})`);
  browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE, args: ["--disable-dev-shm-usage"] });
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, timezoneId: "Asia/Taipei" });
  await context.addInitScript(() => {
    localStorage.setItem("mmwx-session", JSON.stringify({ token: "local-fixture", username: "admin", role: "admin", isAdmin: true, expiresAt: "2099-01-01T00:00:00Z" }));
  });
  await context.route("**/*", async (route) => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== base) { errors.push(`blocked remote request: ${url.origin}`); return route.abort(); }
    if (!url.pathname.startsWith("/api/")) return route.continue();
    const body = request.postDataJSON() || {}, method = request.method(), endpoint = url.pathname;
    requests.push({ endpoint, method, query: url.search, body });
    let response = { success: true };
    if (endpoint === "/api/v3") response = body.op === "034e094d05aa3f83" ? { servers } : body.op === "c87c168b92b5f22d" ? { nodes } : body.op === "ac1c61a9bca33068" ? { users: [{ username: "king", email: "king" }] } : {};
    else if (endpoint.endsWith("/agent/version-info")) response = { current: "v0.5.5", latest: "v0.5.5" };
    else if (endpoint === "/api/custom/ui/service-groups") response = { success: true, exists: true, groups, revision: 1 };
    else if (endpoint === "/api/custom/agent/metrics") response = { metrics: Object.fromEntries(servers.map((s) => [s.id, { server_id: String(s.id), available: true, tcp_count: 100, udp_count: 20, connection_count: 120, helper_version: s.id === 12 ? "v0.7.13" : "v0.7.14", updated_at: fixture.now }])), core_modes: {} };
    else if (/\/api\/custom\/servers\/\d+\/listening-ports$/.test(endpoint)) response = endpoint.includes("/servers/2/") ? listening : { success: true, available: true, supported: false, helper_version: "v0.7.13", listening: null };
    else if (endpoint === "/api/admin/xray/generate-x25519") {
      // The key answer arrives late, after the protocol switch below.
      await new Promise((resolve) => setTimeout(resolve, 1500));
      response = { privateKey: "fixture-private-key", publicKey: "fixture-public-key" };
    }
    else if (endpoint.endsWith("/services/status")) response = { xray: { running: true, version: "Xray 26.10.21" }, nginx: { installed: false, running: false } };
    else if (endpoint.endsWith("/xray/config")) response = { config: JSON.stringify(config, null, 2), path: "/usr/local/etc/xray/config.json" };
    else if (endpoint.endsWith("/xray/system-config")) response = { metrics_enabled: true, stats_enabled: true, grpc_enabled: true, grpc_port: 46736, metrics_listen: "127.0.0.1:38889" };
    else if (endpoint.endsWith("/remote/inbounds")) response = method === "GET" ? { inbounds } : { success: false, message: "fixture: writes are not expected" };
    else if (endpoint.endsWith("/outbounds")) response = { success: true, outbounds: config.outbounds };
    else if (endpoint.endsWith("/routing")) response = { success: true, routing: config.routing };
    else if (endpoint === "/api/custom/ui/routing-presets") response = { success: true, presets: [] };
    else if (endpoint.endsWith("/warp/status")) response = { installed: false };
    else if (endpoint === "/api/admin/certificates/valid") response = { certificates: [] };
    else if (endpoint.endsWith("/nginx/servers-list")) response = { domains: [] };
    else if (endpoint === "/api/admin/server-nics") response = { nics: [] };
    else if (endpoint === "/api/custom/flvx/admin") response = { shares: [], servers, integration: {} };
    else if (endpoint.startsWith("/api/custom/")) response = { success: true, metrics: {}, connections: {}, groups: [], plans: [] };
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(response) });
  });
  const page = await context.newPage();
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("dialog", (dialog) => dialog.dismiss());
  const top = () => page.getByRole("dialog").last();
  const editor = () => page.getByRole("dialog", { name: "Xray 配置编辑", exact: true });
  const shot = async (name) => {
    await page.mouse.move(0, 0);
    const issues = await smallText(page, ".xray-object-editor");
    fonts.push({ name, issues }); saveReport();
    await page.screenshot({ path: path.join(evidence, `${name}.png`) });
    assert.deepEqual(issues, [], `${name}: visible text below ${fontFloor}px`);
  };
  const openEditor = async (name) => {
    await page.goto(base);
    await page.getByRole("button", { name: "打开菜单", exact: true }).click();
    await page.getByRole("button", { name: "服务管理", exact: true }).click();
    await page.getByRole("button", { name: `${name} 更多操作`, exact: true }).click();
    await top().getByRole("button", { name: "配置 / 入站 / 出站 / 路由", exact: true }).click();
    const xray = page.getByRole("dialog", { name: "Xray 管理", exact: true });
    await xray.locator(".xray-json-editor").waitFor();
    await xray.getByRole("button", { name: /^入站 \d+$/ }).click();
    await xray.locator(".xray-loading").waitFor({ state: "hidden" });
    await xray.getByRole("button", { name: "添加入站", exact: true }).click();
    await editor().waitFor();
  };
  const portInput = () => editor().locator("label").filter({ hasText: "端口 *" }).locator("input");
  const warning = () => editor().locator(".xray-inline-warning");
  const typePort = async (port) => { await portInput().fill(String(port)); await page.waitForTimeout(700); };
  const scrollToPort = () => portInput().evaluate((el) => el.scrollIntoView({ block: "center" }));

  for (const width of [390, 1440]) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 });
    await openEditor("Boil HKT 99u");
    // Switch VLESS-Reality → SS-2022 at once, before the X25519 answer (1.5 s).
    await editor().locator("label").filter({ hasText: "协议 *" }).locator("select").selectOption("shadowsocks2022");
    await page.waitForTimeout(2200);
    await editor().getByRole("button", { name: "专家模式", exact: true }).click();
    await editor().getByRole("button", { name: "预览", exact: true }).click();
    const preview = await editor().locator(".xray-live-preview pre").innerText();
    const json = JSON.parse(preview);
    check(`${width} SS-2022 after late X25519 has no Reality leftovers`, json.protocol === "shadowsocks" && !/reality|fixture-private-key/i.test(preview) && !("reality_guard" in json), preview.slice(0, 200));
    await editor().locator(".xray-live-preview").evaluate((el) => el.scrollIntoView({ block: "start" }));
    await shot(`${width}-ss2022-preview-no-reality`);
    await editor().getByRole("button", { name: "关闭预览", exact: true }).click();

    await scrollToPort();
    await typePort(10017);
    check(`${width} 10017 names lkbk-core while typing`, (await warning().allInnerTexts()).some((text) => text.includes("端口 10017 已被本机进程 lkbk-core (PID 212883) 占用，请换一个端口")));
    await shot(`${width}-port-10017-lkbk-core`);
    await typePort(10016);
    check(`${width} 10016 is another inbound`, (await warning().allInnerTexts()).some((text) => text.includes("端口 10016 已被当前服务器的其他入站占用")));
    await shot(`${width}-port-10016-other-inbound`);
    await typePort(20000);
    check(`${width} 20000 has no warning`, (await warning().count()) === 0);
    await shot(`${width}-port-20000-free`);
    for (let draw = 0; draw < 5; draw++) {
      await editor().getByRole("button", { name: "随机", exact: true }).click();
      const port = Number(await portInput().inputValue());
      check(`${width} 随机 skips busy ports (${port})`, port >= 10000 && port <= 65535 && !busy.has(port));
    }
    // Saving re-reads the report and blocks with the same message; nothing is written.
    await typePort(10017);
    const before = requests.length;
    await editor().getByRole("button", { name: "保存", exact: true }).click();
    await editor().locator(".xray-field-error").waitFor();
    check(`${width} save blocked with the message`, (await editor().locator(".xray-field-error").innerText()) === "端口 10017 已被本机进程 lkbk-core (PID 212883) 占用");
    const after = requests.slice(before);
    check(`${width} save re-fetched the report`, after.some((item) => item.endpoint === "/api/custom/servers/2/listening-ports"));
    check(`${width} save wrote nothing`, !after.some((item) => item.method !== "GET" && item.endpoint !== "/api/v3"), JSON.stringify(after.map((item) => `${item.method} ${item.endpoint}`)));
    await editor().locator(".xray-field-error").evaluate((el) => el.scrollIntoView({ block: "center" }));
    await shot(`${width}-save-blocked`);
    await editor().getByRole("button", { name: "取消", exact: true }).click();

    // A Helper that does not report yet: neutral note, saving stays possible.
    await openEditor("Boil Hinet 158");
    await editor().getByRole("button", { name: "专家模式", exact: true }).click();
    await scrollToPort();
    await typePort(10017);
    check(`${width} old Helper shows the neutral note`, (await editor().locator(".xray-assistant-note").allInnerTexts()).includes("无法检测本机端口占用（Helper 未上报/需升级）") && (await warning().count()) === 0);
    await shot(`${width}-helper-not-reporting`);
    await editor().getByRole("button", { name: "取消", exact: true }).click();
  }
  check("no page errors", errors.length === 0, errors.join("; "));
  check("no write requests at all", !requests.some((item) => item.method !== "GET" && !["/api/v3", "/api/admin/xray/generate-x25519"].includes(item.endpoint)), JSON.stringify(requests.filter((item) => item.method !== "GET").map((item) => item.endpoint)));
  saveReport();
  console.log(`ok: ${checks.length} checks, evidence in ${evidence}`);
} catch (error) {
  await browser?.contexts()[0]?.pages()[0]?.screenshot({ path: path.join(evidence, "failure.png") }).catch(() => undefined);
  throw error;
} finally {
  saveReport();
  await browser?.close();
  server.close();
}
