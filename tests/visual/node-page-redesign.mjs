// Local-only evidence: serves the production build, intercepts every API call,
// and never forwards a request to the official backend or a remote server.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { readFileSync, writeFileSync, mkdirSync, existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const require = createRequire(path.join(root, "frontend/package.json"));
const { chromium } = require("playwright");
const evidence = process.env.NODE_REDESIGN_EVIDENCE || path.join(root, "runs/node-page-redesign/evidence");
mkdirSync(evidence, { recursive: true });
const texts = JSON.parse(readFileSync(new URL("./node-page-fixture.json", import.meta.url), "utf8"));
const servers = [];
const whole = new Map();
const memberships = {};
const packages = [];
const owners = {};
const nodes = texts.map((text, i) => {
  const parts = text.split(" | ");
  const addressIndex = parts.findIndex((part) => part.includes(" · "));
  const name = parts.slice(0, addressIndex).join(" | ");
  const address = parts[addressIndex].split(" · ");
  const original_server = address[0];
  const hostPort = address[1];
  const port = Number(hostPort.slice(hostPort.lastIndexOf(":") + 1));
  const host = hostPort.slice(0, hostPort.lastIndexOf(":"));
  let server = servers.find((item) => item.name === original_server);
  if (!server) { server = { id: servers.length + 1, name: original_server, ip_address: `192.0.2.${servers.length + 1}`, domain: host, status: "connected", xray_mode: "external", xray_running: true }; servers.push(server); }
  const protocol = parts[addressIndex + 1].toLowerCase();
  const tag = parts.find((part) => part.startsWith("入站 "))?.slice(3) || "";
  const relay = parts.find((part) => part.startsWith("中转原服务器 "))?.slice(7);
  const config = { name, type: protocol, server: host, port, ...(protocol === "vless" ? { uuid: `fixture-${i}`, flow: "xtls-rprx-vision" } : { password: `fixture-${i}` }), ...(parts.includes("Reality") ? { tls: true, "reality-opts": { "public-key": "fixture" } } : {}) };
  const out = parts.find((part) => part.startsWith("整个节点出站: "))?.slice(8);
  if (out) whole.set(`${server.id}:${tag}`, out);
  const pkgName = parts.find((part) => /(?:车|@)/.test(part) && !part.startsWith("入站"));
  if (pkgName) {
    let pkg = packages.find((item) => item.package_name === pkgName);
    if (!pkg) { pkg = { package_id: packages.length + 1, package_name: pkgName, all_nodes: false }; packages.push(pkg); }
    memberships[i + 1] = [pkg];
  }
  const match = name.match(/wings|usb|max|soga|khalilgao|king|Riczzoe/i)?.[0];
  const user = match ? (/riczzoe/i.test(match) ? "Riczzoe" : match.toLowerCase()) : original_server.includes("马年") ? "khalilgao" : "admin";
  owners[i + 1] = { users: [user], admin_only: user === "admin", shared: false, source: "credential", inbound_backed: true };
  return { id: i + 1, node_name: name, protocol, original_server, inbound_tag: tag, enabled: i !== 20, tag: `远程:${original_server}`, clash_config: JSON.stringify(config), parsed_config: JSON.stringify(config), ...(relay ? { relay_orig_server: relay.slice(0, relay.lastIndexOf(":")), relay_orig_port: Number(relay.slice(relay.lastIndexOf(":") + 1)) } : {}) };
});
for (const node of nodes.filter((node) => node.relay_orig_server)) {
  const parent = nodes.find((candidate) => !candidate.relay_orig_server && candidate.original_server === node.original_server && candidate.inbound_tag === node.inbound_tag);
  if (parent) owners[node.id] = { ...owners[parent.id], parent_node_id: parent.id };
}
// Add the edge cases that the original 49-node screenshot doesn't contain.
const spare = nodes.filter((node) => !node.relay_orig_server && owners[node.id].admin_only && !nodes.some((child) => owners[child.id].parent_node_id === node.id));
for (const [index, user] of ["king", "Riczzoe"].entries()) if (spare[index]) owners[spare[index].id] = { ...owners[spare[index].id], users: [user], admin_only: false };
if (spare[2]) owners[spare[2].id] = { ...owners[spare[2].id], users: ["usb", "wings"], admin_only: false, shared: true };
if (spare[3]) { spare[3].original_server = ""; spare[3].inbound_tag = ""; spare[3].probe_enabled = true; owners[spare[3].id] = { users: [], source: "none", shared: false, admin_only: false, inbound_backed: false }; }
if (spare[4]) owners[spare[4].id] = { ...owners[spare[4].id], users: [], admin_only: false, source: "none" };
const wings = nodes.find((node) => node.inbound_tag === "wings ss 10018" && !node.relay_orig_server);
const wingServer = servers.find((server) => server.name === wings.original_server);
const wingConfig = JSON.parse(wings.clash_config);
const tunnels = [{ kind: "inbound", server_id: servers[0].id, server_name: servers[0].name, tag: "fixture-tunnel", listen_port: 13068, target_address: wingConfig.server, target_port: wingConfig.port }];
let order = nodes.map((node) => node.id);
const requests = [];
const errors = [];
const dist = path.join(root, "frontend/dist");
assert.ok(existsSync(path.join(dist, "index.html")), "Build the UI before running this harness");
const server = createServer((req, res) => {
  const pathname = new URL(req.url, "http://127.0.0.1").pathname;
  const file = pathname === "/" ? path.join(dist, "index.html") : path.resolve(dist, `.${pathname}`);
  if (!file.startsWith(dist + "/") || !existsSync(file)) { res.writeHead(404); res.end(); return; }
  const mime = { ".html": "text/html", ".js": "application/javascript", ".css": "text/css", ".png": "image/png", ".svg": "image/svg+xml", ".wasm": "application/wasm" }[path.extname(file)] || "application/octet-stream";
  res.writeHead(200, { "Content-Type": mime }); res.end(readFileSync(file));
});
server.on("upgrade", (_request, socket) => socket.destroy());
await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
const base = `http://127.0.0.1:${server.address().port}`;
let browser;
const checks = [];
const metrics = [];
try {
  browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE, args: ["--disable-dev-shm-usage"] });
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, permissions: ["clipboard-read", "clipboard-write"] });
  await context.addInitScript(() => localStorage.setItem("mmwx-session", JSON.stringify({ token: "local-fixture", username: "admin", role: "admin", isAdmin: true, expiresAt: "2099-01-01T00:00:00Z" })));
  await context.route("**/*", async (route) => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== base) { errors.push(`blocked remote request: ${url.origin}`); return route.abort(); }
    if (!url.pathname.startsWith("/api/")) return route.continue();
    const body = request.postDataJSON?.() || {};
    requests.push({ method: request.method(), path: url.pathname, body });
    let result = { success: true };
    const serverId = Number(url.searchParams.get("server_id"));
    if (url.pathname === "/api/v3") {
      result = body.op === "034e094d05aa3f83" ? { servers } : body.op === "c87c168b92b5f22d" ? { nodes } : body.op === "edc667caa2f10498" ? { node_ids: [spare[3]?.id].filter(Boolean) } : {};
    } else if (url.pathname === "/api/custom/nodes/owners") result = { owners };
    else if (url.pathname === "/api/user/config") { if (request.method() === "PUT") order = body.node_order; result = { node_order: order }; }
    else if (url.pathname === "/api/admin/nodes/tags") result = { tags: [...new Set(nodes.map((node) => node.tag))] };
    else if (url.pathname === "/api/admin/nodes/package-membership") result = { memberships, packages };
    else if (url.pathname === "/api/admin/node-probe") result = { enabled: true, states: spare[3] ? { fixture: { node_id: spare[3].id, fail_streak: 2, samples: [{ at: "2026-10-08T00:00:00Z", ok: false, latency_ms: 0 }] } } : {} };
    else if (url.pathname === "/api/admin/tunnels") result = { tunnels, chains: [] };
    else if (url.pathname === "/api/admin/node-unlocks") result = { nodes: { [wings.id]: { unlocked: 4, total: 5 } } };
    else if (url.pathname === "/api/admin/remote/routing") result = { routing: { rules: nodes.filter((node) => node.original_server === servers.find((item) => item.id === serverId)?.name && whole.has(`${serverId}:${node.inbound_tag}`)).map((node) => ({ type: "field", inboundTag: [node.inbound_tag], outboundTag: "direct-ipv4" })) } };
    else if (url.pathname === "/api/admin/remote/outbounds") result = { outbounds: [{ tag: "direct-ipv4", protocol: "freedom", settings: { domainStrategy: "UseIPv4" } }] };
    else if (url.pathname === "/api/admin/remote/inbounds") result = { inbounds: nodes.filter((node) => node.original_server === servers.find((item) => item.id === serverId)?.name).map((node) => ({ tag: node.inbound_tag, protocol: node.protocol, settings: { clients: [{ id: JSON.parse(node.clash_config).uuid, flow: "xtls-rprx-vision" }] } })) };
    else if (url.pathname === "/api/admin/speedtest/results") result = { results: nodes.filter((node) => node.id % 3 === 0).map((node) => ({ node_id: node.id, status: "ok", latency_ms: 30 + node.id, down_mbps: 72, created_at: "2026-10-08T00:00:00Z" })) };
    else if (url.pathname === "/api/admin/speedtest/run") result = { success: true, result: { node_id: body.node_id, status: "ok", latency_ms: 35, down_mbps: 80, created_at: new Date().toISOString() } };
    else if (url.pathname === "/api/admin/tcping") result = { success: true, latency: 38 };
    else if (url.pathname === "/api/admin/tcping/batch") result = (Array.isArray(body) ? body : body.targets || []).map(() => ({ success: true, latency: 38 }));
    else if (/\/nodes\/\d+\/uri$/.test(url.pathname)) result = { uri: "ss://fixture@example.test:443#fixture" };
    else if (/\/nodes\/\d+\/related-inbounds$/.test(url.pathname)) result = { inbounds: [] };
    else if (url.pathname === "/api/admin/temp-subscription") result = { url: "/t/local-fixture" };
    else if (url.pathname === "/api/traffic/summary") result = { total_uplink: 0, total_downlink: 0, total_traffic: 0 };
    else if (url.pathname.includes("/traffic/")) result = { items: [], servers: [], connections: {} };
    else if (url.pathname.startsWith("/api/custom/")) result = { success: true, metrics: {}, connections: {}, groups: [] };
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(result) });
  });
  const page = await context.newPage();
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto(base);
  await page.getByRole("button", { name: "打开菜单", exact: true }).click();
  await page.getByRole("button", { name: "节点管理", exact: true }).click();
  await page.waitForSelector(".node-compact-row");
  await page.waitForFunction(() => document.querySelector(".node-manager")?.getAttribute("aria-busy") === "false");
  await page.locator(".node-group-label strong").filter({ hasText: "自用" }).waitFor();
  await page.locator(".node-row-tag.out").first().waitFor();
  const screenshot = async (name, fullPage = false) => page.screenshot({ path: path.join(evidence, `${name}.png`), fullPage });
  const measure = async (label) => {
    const data = await page.evaluate(() => {
      const rows = [...document.querySelectorAll(".node-compact-row")].map((element) => ({ id: element.dataset.nodeId, rect: element.getBoundingClientRect() }));
      return { width: innerWidth, height: innerHeight, pageHeight: document.documentElement.scrollHeight, rowHeights: [...new Set(rows.map((row) => row.rect.height))], firstScreenNodes: new Set(rows.filter((row) => row.rect.top >= 0 && row.rect.bottom <= innerHeight).map((row) => row.id)).size, visibleRows: rows.length, uniqueNodes: new Set(rows.map((row) => row.id)).size, overflow: document.documentElement.scrollWidth > innerWidth, overflowPixels: Math.max(0, document.documentElement.scrollWidth - innerWidth) };
    });
    assert.equal(data.overflow, false, `${label}: horizontal overflow`);
    assert.ok(Math.max(...data.rowHeights) <= 60, `${label}: rows too tall`);
    metrics.push({ label, ...data });
  };
  for (const theme of ["light", "dark"]) {
    if (theme === "dark") { await page.getByRole("button", { name: "打开菜单", exact: true }).click(); await page.getByRole("button", { name: "切换主题" }).click(); await page.getByRole("button", { name: "节点管理", exact: true }).click(); }
    await page.setViewportSize({ width: 390, height: 844 });
    await page.evaluate(() => scrollTo(0, 0));
    await measure(`390-${theme}`);
    await screenshot(`390-${theme}-grouped`);
    await screenshot(`390-${theme}-full`, true);
    await page.locator(`.node-compact-row[data-node-id="${wings.id}"] .node-row-more`).first().click();
    await screenshot(`390-${theme}-action`);
    const action = page.getByRole("dialog").last();
    for (const submenu of ["节点路由 · 链式出站 · 中转组", "测速历史 · 探测与状态", "查看配置 · 临时订阅 · 更多"]) await action.getByText(submenu, { exact: true }).click();
    for (const text of ["复制 URI", "测延迟", "测速", "编辑", "节点路由", "链式出站", "中转组", "新增落地节点 / 整个节点出站", "取消整个节点出站", "编辑名称 / 中转配置", "查看配置", "临时订阅", "解析 IP", "恢复域名", "地区 emoji", "删除节点", "测速历史"]) assert.ok(await action.getByRole("button", { name: text, exact: true }).count(), `missing action ${text}`);
    await action.getByRole("button", { name: "关闭面板" }).click();
    await page.getByRole("button", { name: "筛选", exact: true }).click();
    await screenshot(`390-${theme}-filter`);
    await page.getByRole("button", { name: "关闭面板" }).click();
    await page.getByRole("button", { name: "节点管理菜单" }).click();
    await page.getByRole("menuitem", { name: "选择节点" }).click();
    await page.locator('.node-group-head > input[type="checkbox"]').nth(2).check();
    await page.evaluate(() => scrollTo(0, 0));
    await screenshot(`390-${theme}-selection`);
    await page.getByRole("button", { name: "完成", exact: true }).click();
    for (const width of [360, 375, 412, 1440]) {
      await page.setViewportSize({ width, height: width === 1440 ? 1000 : 844 });
      await measure(`${width}-${theme}`);
      if (width === 1440) await screenshot(`1440-${theme}-grouped`);
    }
  }
  checks.push("light/dark grouped, action, filter, selection; 360/375/390/412/1440 no overflow; every old action entry");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "导入", exact: true }).click();
  for (const tab of ["手动输入", "订阅导入", "SOCKS5"]) { await page.getByRole("button", { name: tab, exact: true }).click(); assert.ok(await page.getByRole("button", { name: "解析节点", exact: true }).count()); }
  await page.getByRole("button", { name: "手动输入", exact: true }).click();
  await page.getByText("开启中转", { exact: true }).click();
  await page.getByPlaceholder("中转服务器 IP 或域名").waitFor();
  await page.getByRole("button", { name: "关闭面板" }).click();
  checks.push("all three import modes and relay options reachable");
  await page.locator(`.node-compact-row[data-node-id="${wings.id}"] .node-row-more`).first().click();
  let actionPanel = page.getByRole("dialog").last();
  await actionPanel.getByRole("button", { name: "复制 URI", exact: true }).click();
  await page.waitForFunction(async () => (await navigator.clipboard.readText()).startsWith("ss://fixture"));
  await actionPanel.getByRole("button", { name: "测延迟", exact: true }).click();
  await actionPanel.getByText("TCPing：38 ms", { exact: true }).waitFor();
  await actionPanel.getByRole("button", { name: "测速", exact: true }).click();
  await actionPanel.getByText("测速历史 · 探测与状态", { exact: true }).click();
  await actionPanel.locator('button[title="只测真连接延迟(Cloudflare 204 多采样)"]').click();
  assert.ok(requests.some((request) => request.path === "/api/admin/speedtest/run" && request.body.latency_only === true));
  assert.ok(requests.some((request) => request.path === "/api/admin/speedtest/run" && request.body.latency_only === undefined));
  await actionPanel.locator(".node-package-chip").click();
  await page.getByRole("dialog", { name: "该节点所属套餐", exact: true }).getByRole("button", { name: "关闭", exact: true }).click();
  await actionPanel.getByText("查看配置 · 临时订阅 · 更多", { exact: true }).click();
  await actionPanel.getByRole("button", { name: "编辑名称 / 中转配置", exact: true }).click();
  const editor = page.getByRole("dialog", { name: "编辑节点", exact: true });
  for (const label of ["保存基础信息", "更新地址", "恢复原始域名", "设置/修改中转", "复制为中转节点", "取消中转", "保存配置"]) assert.ok(await editor.getByRole("button", { name: label, exact: true }).count(), label);
  await editor.getByRole("button", { name: "关闭", exact: true }).click();
  actionPanel = page.getByRole("dialog").last();
  await actionPanel.getByText("查看配置 · 临时订阅 · 更多", { exact: true }).click();
  await actionPanel.getByRole("button", { name: "临时订阅", exact: true }).click();
  await page.getByRole("button", { name: "生成并复制", exact: true }).click();
  await page.waitForFunction(() => [...document.querySelectorAll('input[readonly]')].some((input) => input.value.endsWith("/t/local-fixture")));
  await page.getByRole("dialog", { name: "节点详情", exact: true }).getByRole("button", { name: "关闭", exact: true }).first().click();
  await page.getByRole("button", { name: "关闭面板", exact: true }).click();
  checks.push("URI, TCPing, throughput and real-latency API callbacks; package dialog; edit subactions; local-origin temporary subscription");
  const search = page.getByRole("textbox", { name: "搜索节点、用户、端口" });
  await search.fill("wings");
  assert.ok(await page.locator(".node-compact-row").count());
  await search.fill("");
  await page.getByRole("button", { name: "筛选", exact: true }).click();
  const filterPanel = page.getByRole("dialog", { name: "筛选节点" });
  await filterPanel.locator("select").first().selectOption("ss");
  await filterPanel.getByRole("combobox", { name: "状态筛选" }).selectOption("enabled");
  await filterPanel.getByRole("button", { name: /显示 .* 个节点/ }).click();
  assert.equal(await page.locator(".node-active-filters button").count(), 2);
  await page.locator(".node-active-filters button").first().click();
  assert.equal(await page.locator(".node-active-filters button").count(), 1);
  await page.locator(".node-active-filters button").first().click();
  await page.getByRole("button", { name: "按套餐", exact: true }).click();
  assert.equal(await page.evaluate(() => localStorage.getItem("mmwx-node-grouping")), "package");
  await page.getByRole("button", { name: "按用户", exact: true }).click();
  await page.locator(".node-group-toggle").first().click();
  assert.ok((await page.evaluate(() => JSON.parse(localStorage.getItem("mmwx-node-collapsed")))).length);
  await page.locator(".node-group-toggle").first().click();
  const expand = page.getByRole("button", { name: /再显示 \d+ 个中转/ }).first();
  const before = await page.locator(".node-compact-row").count();
  await expand.click();
  assert.ok(await page.locator(".node-compact-row").count() > before);
  checks.push("owner search, combined filters and removable chips, package grouping, collapse storage and relay expansion");
  await page.getByRole("button", { name: "不分组", exact: true }).click();
  assert.equal(await page.locator(".node-compact-row").count(), 49);
  await page.getByRole("button", { name: "节点管理菜单" }).click();
  await page.getByRole("menuitem", { name: "排序模式", exact: true }).click();
  const handles = page.getByRole("button", { name: "拖动排序", exact: true });
  await handles.first().scrollIntoViewIfNeeded();
  const from = await handles.nth(0).boundingBox(), to = await handles.nth(2).boundingBox();
  await page.mouse.move(from.x + 12, from.y + 14); await page.mouse.down();
  await page.mouse.move(to.x + 12, to.y + 26, { steps: 15 }); await page.mouse.up();
  await page.waitForFunction(() => document.querySelector(".node-manager")?.getAttribute("aria-busy") === "false");
  assert.notDeepEqual(order, nodes.map((node) => node.id), "drag must persist node_order");
  const configWrites = requests.filter((request) => request.path === "/api/user/config" && request.method === "PUT");
  assert.equal(configWrites.length, 1, "group/collapse preferences must not write user_config");
  checks.push("ungrouped drag persists node_order once; grouping/collapse UI preferences never write user_config");
  assert.equal(errors.length, 0, errors.join("\n"));
  assert.ok(metrics.filter((item) => item.label.startsWith("390-")).every((item) => item.firstScreenNodes >= 7));
  writeFileSync(path.join(evidence, "metrics.json"), JSON.stringify({ fixtureNodes: nodes.length, metrics, checks, requests, errors }, null, 2));
  console.log(JSON.stringify({ fixtureNodes: nodes.length, metrics, checks }, null, 2));
} finally {
  await browser?.close();
  await new Promise((resolve) => server.close(resolve));
}
