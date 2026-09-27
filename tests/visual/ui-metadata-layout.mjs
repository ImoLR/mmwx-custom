import { createRequire } from "node:module";
import { existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const frontendDir = path.resolve(here, "../../frontend");
const requireFromFrontend = createRequire(path.join(frontendDir, "package.json"));
const { chromium } = requireFromFrontend("playwright");
const baseURL = process.env.UI_METADATA_BASE_URL || "http://127.0.0.1:4179";
const executablePath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE
  || (existsSync("/root/.cache/ms-playwright/chromium-1124/chrome-linux/chrome")
    ? "/root/.cache/ms-playwright/chromium-1124/chrome-linux/chrome"
    : undefined);
const widths = [360, 375, 390, 412];

const servers = [
  { id: 15, name: "Nobrand KFC 5cm", status: "connected", ws_connected: true, xray_mode: "external", xray_running: true, xray_version: "v26.8.8", agent_version: "v0.8.8", ip_address: "192.0.2.15", current_upload_speed: 1200, current_download_speed: 800, traffic_used: 1000, traffic_limit: 100000 },
  { id: 5, name: "Boil Hinet", status: "connected", ws_connected: true, xray_mode: "external", xray_running: true, xray_version: "v26.8.8", agent_version: "v0.8.8", ip_address: "192.0.2.5", current_upload_speed: 700, current_download_speed: 600, traffic_used: 2000, traffic_limit: 100000 },
];
const inbounds = [
  { tag: "shadowsocks2022-10015-demo", protocol: "shadowsocks", listen: "0.0.0.0", port: 10015, settings: { clients: [{ email: "alice__ss-10015", password: "hidden" }, { email: "bob__ss-10015", password: "hidden" }] } },
  { tag: "vless-443-demo", protocol: "vless", listen: "0.0.0.0", port: 443, settings: { clients: [{ email: "carol__vless-443", id: "00000000-0000-0000-0000-000000000000" }] } },
];
const routing = {
  domainStrategy: "AsIs",
  rules: [{ type: "field", inboundTag: ["missing-inbound"], user: ["missing__identity"], domain: ["domain:example.com"], outboundTag: "direct", marktag: "legacy-rule", customAdvancedField: { keep: true } }],
  balancers: [],
};
const presets = [{ id: 7, name: "跨设备历史规则", rule: { ...routing.rules[0], inboundTag: ["shadowsocks2022-10015-demo"] }, created_at: "2026-09-27T00:00:00Z", updated_at: "2026-09-27T00:00:00Z" }];

let groupDocument = null;
let groupWrites = 0;

function json(route, value, status = 200) {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify(value) });
}

async function installMocks(page) {
  await page.addInitScript(() => {
    localStorage.setItem("mmwx-session", JSON.stringify({ token: "visual-admin-session", username: "admin", role: "admin", isAdmin: true, expiresAt: "2099-01-01T00:00:00Z" }));
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const pathname = url.pathname;
    if (pathname === "/api/v3") {
      const body = request.postDataJSON();
      if (body.op === "bc48d95b4c587a37") return json(route, servers);
      if (body.op === "ba429330247847b1") return json(route, { success: true, nodes: [] });
      return json(route, { success: true });
    }
    if (pathname === "/api/custom/ui/service-groups") {
      if (request.method() === "PUT") {
        const body = request.postDataJSON();
        if (body.only_if_empty && groupDocument) return json(route, { success: false, message: "service groups already exist" }, 409);
        groupDocument = body.groups;
        groupWrites += 1;
      }
      return json(route, { success: true, exists: groupDocument !== null, groups: groupDocument || [], revision: groupWrites });
    }
    if (pathname === "/api/custom/ui/routing-presets") return json(route, { success: true, presets });
    if (pathname === "/api/traffic/summary") return json(route, { metrics: { total_limit_gb: 0, total_used_gb: 0, total_remaining_gb: 0, usage_percentage: 0, unlimited_used_gb: 0 }, history: [] });
    if (pathname === "/api/admin/traffic/period") return json(route, { items: [], range: "today", range_start: "2026-09-27", range_end: "2026-09-27", timezone: "Asia/Shanghai", complete: true });
    if (pathname === "/api/admin/traffic/user-connections" || pathname === "/api/admin/traffic/node-connections") return json(route, { connections: {} });
    if (pathname === "/api/admin/traffic/servers") return json(route, { servers: [] });
    if (pathname === "/api/custom/agent/metrics") return json(route, { metrics: {} });
    if (pathname === "/api/custom/dashboard/system") return json(route, { cpu_pct: 1, cpu_cores: 2, mem_used: 100, mem_total: 1000, swap_used: 0, swap_total: 0, disk_used: 100, disk_total: 1000, has_cpu: true, has_mem: true, has_disk: true });
    if (pathname === "/api/admin/remote/user-speeds") return json(route, { user_speeds: {} });
    if (pathname === "/api/admin/remote/agent/version-info") return json(route, { current: "v0.8.8" });
    if (pathname === "/api/admin/remote/services/status") return json(route, { xray: { running: true, version: "v26.8.8" } });
    if (pathname === "/api/admin/remote/xray/config") return json(route, { path: "/usr/local/etc/xray/config.json", config: "{}" });
    if (pathname === "/api/admin/remote/xray/system-config") return json(route, { config: { metrics_enabled: false, stats_enabled: true, grpc_enabled: false } });
    if (pathname === "/api/admin/remote/inbounds") return json(route, { inbounds });
    if (pathname === "/api/admin/remote/outbounds") return json(route, { outbounds: [{ tag: "direct", protocol: "freedom", settings: {} }, { tag: "block", protocol: "blackhole", settings: {} }] });
    if (pathname === "/api/admin/remote/routing") return json(route, { routing });
    if (pathname === "/api/geo/lookup" || pathname === "/api/custom/geo/lookup") return json(route, { success: true, country_code: "JP", country: "日本", flag: "🇯🇵" });
    return json(route, { success: true });
  });
}

async function openServices(page) {
  await page.goto(`${baseURL}/?ui-metadata-test=${Date.now()}`, { waitUntil: "domcontentloaded", timeout: 30000 });
  await page.getByRole("button", { name: "打开菜单" }).click();
  await page.getByRole("button", { name: "服务管理" }).click();
  await page.waitForSelector(".service-server-card", { timeout: 15000 });
}

async function assertNoOverflow(page, label) {
  const dimensions = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, client: document.documentElement.clientWidth, body: document.body.scrollWidth, bodyClient: document.body.clientWidth }));
  if (dimensions.doc !== dimensions.client || dimensions.body !== dimensions.bodyClient) throw new Error(`${label} horizontal overflow: ${JSON.stringify(dimensions)}`);
}

async function verifyRouting(page, width) {
  await openServices(page);
  await page.getByRole("button", { name: "Xray 配置" }).first().click();
  await page.getByRole("button", { name: "路由" }).click();
  await page.getByText("内置快捷规则", { exact: true }).waitFor();
  await page.getByText("跨设备历史规则", { exact: true }).waitFor();
  await page.getByRole("button", { name: "编辑" }).first().click();
  const fields = page.locator(".xray-multi-field");
  await fields.nth(0).getByText("missing-inbound", { exact: true }).waitFor();
  await fields.nth(1).getByText("missing__identity", { exact: true }).waitFor();
  await fields.nth(0).getByRole("button", { name: /选择/ }).click();
  await page.locator(".xray-picker-option").filter({ hasText: "10015 · shadowsocks2022-10015-demo" }).click();
  await page.getByRole("button", { name: "完成" }).click();
  await fields.nth(1).getByRole("button", { name: /选择/ }).click();
  await page.locator(".xray-picker-option").filter({ hasText: "alice" }).click();
  await page.getByRole("button", { name: "完成" }).click();
  await page.getByRole("button", { name: "高级 JSON" }).click();
  const editor = page.locator(".xray-json-editor.object");
  const parsed = JSON.parse(await editor.inputValue());
  if (!parsed.inboundTag.includes("missing-inbound") || !parsed.inboundTag.includes("shadowsocks2022-10015-demo")) throw new Error("inbound multi-select lost values");
  if (!parsed.user.includes("missing__identity") || !parsed.user.includes("alice__ss-10015")) throw new Error("routing user multi-select lost identities");
  if (parsed.customAdvancedField?.keep !== true) throw new Error("advanced routing fields were lost");
  await assertNoOverflow(page, `routing editor ${width}px`);
}

async function verifyGroupSync(browser) {
  groupDocument = null;
  groupWrites = 0;
  const contextA = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const pageA = await contextA.newPage();
  await installMocks(pageA);
  await openServices(pageA);
  await pageA.getByRole("button", { name: "分组" }).click();
  await pageA.getByPlaceholder("输入组名，例如 香港节点").fill("手机同步组");
  await pageA.getByRole("button", { name: "新增" }).click();
  await pageA.getByText("手机同步组", { exact: true }).first().waitFor();
  await pageA.getByText("Nobrand KFC 5cm", { exact: true }).last().click();
  await pageA.waitForFunction(() => true);
  if (!groupDocument?.some((group) => group.name === "手机同步组" && group.server_ids.includes(15))) throw new Error("group membership was not persisted");

  const contextB = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const pageB = await contextB.newPage();
  await installMocks(pageB);
  await openServices(pageB);
  await pageB.getByText("手机同步组", { exact: true }).first().waitFor();
  await assertNoOverflow(pageB, "second storage context service groups");
  await contextA.close();
  await contextB.close();
}

async function verifyLegacyMigration(browser) {
  groupDocument = null;
  groupWrites = 0;
  const context = await browser.newContext({ viewport: { width: 375, height: 812 } });
  const page = await context.newPage();
  await installMocks(page);
  await page.addInitScript(() => localStorage.setItem("mmwxc-service-groups:admin", JSON.stringify([{ id: "legacy", name: "旧手机分组", serverIds: [5, 15] }])));
  await openServices(page);
  await page.getByText("旧手机分组", { exact: true }).waitFor();
  if (groupWrites !== 1 || groupDocument?.[0]?.server_ids.join(",") !== "5,15") throw new Error("legacy localStorage migration did not preserve order");
  const removed = await page.evaluate(() => localStorage.getItem("mmwxc-service-groups:admin"));
  if (removed !== null) throw new Error("legacy localStorage was not removed after server save");
  await context.close();

  const writesBefore = groupWrites;
  const staleContext = await browser.newContext({ viewport: { width: 375, height: 812 } });
  const stalePage = await staleContext.newPage();
  await installMocks(stalePage);
  await stalePage.addInitScript(() => localStorage.setItem("mmwxc-service-groups:admin", JSON.stringify([{ id: "stale", name: "不得覆盖", serverIds: [15] }])));
  await openServices(stalePage);
  await stalePage.getByText("旧手机分组", { exact: true }).waitFor();
  if (groupWrites !== writesBefore || groupDocument?.[0]?.name !== "旧手机分组") throw new Error("stale device overwrote authoritative server groups");
  await staleContext.close();
}

const browser = await chromium.launch({ headless: true, executablePath, args: ["--no-sandbox"] });
try {
  for (const width of widths) {
    const context = await browser.newContext({ viewport: { width, height: 844 } });
    const page = await context.newPage();
    await installMocks(page);
    await verifyRouting(page, width);
    await context.close();
  }
  await verifyGroupSync(browser);
  await verifyLegacyMigration(browser);
  process.stdout.write("ui metadata routing/grouping mobile checks passed\n");
} finally {
  await browser.close();
}
