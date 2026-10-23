// Fixture-only acceptance for 服务管理 + Xray 管理. No official backend, credentials or remote requests.
// SERVER_PHASE=before (main build) | part-a (rule 置顶/置底 commit) | after (this branch).
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
const phase = process.env.SERVER_PHASE || "after";
assert.ok(["before", "part-a", "after", "xray-only"].includes(phase), `unknown SERVER_PHASE ${phase}`);
// xray-only = released v1.4.64 shape: today's server page, redesigned Xray 管理 dialog.
const pageNew = phase === "after";
const redesigned = phase === "after" || phase === "xray-only";
const fixture = JSON.parse(readFileSync(new URL("./server-page-fixture.json", import.meta.url), "utf8"));
const dist = process.env.SERVER_DIST || path.join(root, "frontend/dist");
const evidence = process.env.SERVER_EVIDENCE || path.join(root, "runs/server-page-redesign/evidence", phase);
mkdirSync(evidence, { recursive: true });
const server = await serveFixtureBuild(dist);
const base = `http://127.0.0.1:${server.address().port}`;
const requests = [], errors = [], metrics = [], fonts = [], checks = [], fontScale = {};
const { servers, groups, nodes } = fixture;
let config = structuredClone(fixture.config);
let presets = structuredClone(fixture.presets);
let browser;
const saveReport = () => writeFileSync(path.join(evidence, "report.json"), JSON.stringify({ phase, metrics, fontScale, fonts, checks, errors, requests }, null, 2));

// Server 9 is offline with a stale Helper; server 11 has a Core switch in flight that the official record doesn't show yet.
const coreMode = (s) => ({ desired_mode: s.id === 11 ? "external" : s.xray_mode, current_mode: s.xray_mode, can_switch: s.id !== 9, fresh: s.id !== 9, configured: true, repair_status: s.id === 11 ? "drift_detected" : "healthy", pending_change: s.id === 11 });
const helperVersion = (s) => s.id === 17 ? "v0.7.4" : "v0.7.5";

try {
  const available = Number(readFileSync("/proc/meminfo", "utf8").match(/MemAvailable:\s+(\d+)/)[1]) / 1024;
  assert.ok(available >= 900, `Wait for 900 MB available before Playwright (currently ${Math.floor(available)})`);
  browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE, args: ["--disable-dev-shm-usage"] });
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, timezoneId: "Asia/Taipei" });
  await context.addInitScript(() => {
    localStorage.setItem("mmwx-session", JSON.stringify({ token: "local-fixture", username: "admin", role: "admin", isAdmin: true, expiresAt: "2099-01-01T00:00:00Z" }));
    // A stored grid/list preference from the old page must not break the new list.
    localStorage.setItem("services-view-mode", "list");
  });
  await context.route("**/*", async (route) => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== base) { errors.push(`blocked remote request: ${url.origin}`); return route.abort(); }
    if (!url.pathname.startsWith("/api/")) return route.continue();
    const body = request.postDataJSON() || {}, method = request.method(), endpoint = url.pathname;
    requests.push({ endpoint, method, body });
    let response = { success: true };
    if (endpoint === "/api/v3") response = body.op === "034e094d05aa3f83" ? { servers } : body.op === "c87c168b92b5f22d" ? { nodes } : {};
    else if (endpoint.endsWith("/agent/version-info")) response = { current: "v0.5.5", latest: "v0.5.5" };
    else if (endpoint === "/api/custom/ui/service-groups") response = { success: true, exists: true, groups, revision: 1 };
    else if (endpoint === "/api/custom/agent/metrics") response = {
      metrics: Object.fromEntries(servers.map((s, i) => [s.id, { server_id: String(s.id), available: s.id !== 9, tcp_count: 100 + i * 20, udp_count: 23, connection_count: 123 + i * 20, helper_version: helperVersion(s), updated_at: fixture.now }])),
      core_modes: Object.fromEntries(servers.map((s) => [s.id, coreMode(s)])),
    };
    else if (endpoint.endsWith("/core-mode")) {
      const s = servers.find((item) => endpoint.includes(`/${item.id}/`)) || servers[0];
      response = { success: true, configured: true, controller_mode: s.xray_mode, current_mode: s.xray_mode, controller_status: "connected", intent: { desired_core_mode: coreMode(s).desired_mode, repair_status: "healthy" }, agent_status: { reported_at: fixture.now, core_mode: s.xray_mode, core: { installed: true, version: "Xray 8f5b614", ready: true }, external_ownership: { enabled: true, service_owned: true, runtime_owned: true, single_core: true, service_active: true, core_ready: true } } };
    }
    else if (endpoint === "/api/custom/helper-batch") response = { success: true, version: "v0.7.5", preview: servers.map((s) => ({ server_id: String(s.id), name: s.name, version: helperVersion(s), phase: s.id === 17 ? "queued" : "skipped", reason: s.id === 17 ? "" : "已是最新" })) };
    else if (endpoint === "/api/custom/geo/lookup") response = { success: true, ...(url.searchParams.get("host") === "server-10.example.test" ? { country_code: "HK", country: "Hong Kong" } : {}) };
    else if (endpoint === "/api/custom/server-deletions") response = { plans: [{ operation_id: "fixture-deleted", server_id: 99, server_name: "本地已删除示例", state: "deleted", machine: { outstanding: [] } }] };
    else if (endpoint === "/api/custom/flvx/admin") response = { shares: fixture.flvx_shared.map((id) => ({ server_id: id, status: "active" })), servers, integration: {} };
    else if (endpoint.endsWith("/services/status")) response = { xray: { running: true, version: "Xray 26.9.23" } };
    else if (endpoint.endsWith("/xray/config")) response = { config: JSON.stringify(config, null, 2), path: "/etc/xray/config.json" };
    else if (endpoint.endsWith("/xray/system-config")) response = { metrics_enabled: true, stats_enabled: true, grpc_enabled: true, grpc_port: 46736, metrics_listen: "127.0.0.1:38889" };
    else if (endpoint.endsWith("/inbounds")) response = { inbounds: config.inbounds };
    else if (endpoint.endsWith("/outbounds")) {
      if (method === "POST" && body.action === "reorder") config.outbounds = body.tags.map((tag) => config.outbounds.find((item) => item.tag === tag));
      response = { success: true, outbounds: config.outbounds };
    }
    else if (endpoint.endsWith("/routing")) {
      if (method === "POST" && body.action === "set") config.routing = body.routing;
      if (method === "POST" && body.action === "add_rule") config.routing.rules.push(body.rule);
      response = { success: true, routing: config.routing };
    }
    else if (endpoint === "/api/custom/ui/routing-presets") {
      if (method === "DELETE") presets = presets.filter((p) => p.id !== Number(url.searchParams.get("id")));
      response = { success: true, presets };
    }
    else if (endpoint.endsWith("/warp/status")) response = { installed: true, addr_v4: "192.0.2.99", addr_v6: "2001:db8::99", license_active: false };
    else if (endpoint === "/api/admin/server-nics") response = { nics: [] };
    // Minimal node-page data, only used to read the node page's font scale for comparison.
    else if (endpoint === "/api/custom/nodes/owners") response = { owners: Object.fromEntries(nodes.map((node) => [node.id, { users: ["admin"], admin_only: true, shared: false, source: "credential", inbound_backed: node.node_type !== "routed" }])) };
    else if (endpoint === "/api/user/config") response = { node_order: nodes.map((node) => node.id) };
    else if (endpoint === "/api/admin/nodes/tags") response = { tags: [...new Set(nodes.map((node) => node.tag).filter(Boolean))] };
    else if (endpoint === "/api/admin/nodes/package-membership") response = { memberships: {}, packages: [] };
    else if (endpoint === "/api/admin/node-probe") response = { enabled: false, states: {} };
    else if (endpoint === "/api/admin/tunnels") response = { tunnels: [], chains: [] };
    else if (endpoint === "/api/admin/node-unlocks") response = { nodes: {} };
    else if (endpoint === "/api/admin/speedtest/results") response = { results: [] };
    else if (endpoint === "/api/traffic/summary") response = { total_uplink: 0, total_downlink: 0, total_traffic: 0 };
    else if (endpoint.includes("/traffic/")) response = { items: [], servers: [], connections: {} };
    else if (endpoint.startsWith("/api/custom/")) response = { success: true, metrics: {}, connections: {}, groups: [], plans: [] };
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(response) });
  });
  const page = await context.newPage();
  await page.clock.install({ time: new Date(fixture.now) });
  await page.clock.pauseAt(new Date(fixture.now));
  page.on("pageerror", (error) => errors.push(error.message));
  let acceptConfirm = false;
  const confirmations = [];
  page.on("dialog", async (dialog) => { confirmations.push(dialog.message()); await (acceptConfirm ? dialog.accept() : dialog.dismiss()); });

  const row = pageNew ? ".server-row" : ".service-server-card";
  const xrayRow = redesigned ? ".xray-row" : ".xray-item";
  const fontScope = pageNew ? ".server-compact, .cx-sheet, .package-dialog-layer" : phase === "xray-only" ? ".cx-sheet" : ".service-page, .service-action-sheet, .service-dialog";
  const mainDialog = () => page.getByRole("dialog", { name: "Xray 管理", exact: true });
  const top = () => page.getByRole("dialog").last();
  const navigate = async () => {
    await page.goto(base);
    await page.getByRole("button", { name: "打开菜单", exact: true }).click();
    await page.getByRole("button", { name: "服务管理", exact: true }).click();
    await page.locator(row).first().waitFor();
    await page.getByRole("button", { name: /已共享给 FLVX/ }).first().waitFor();
    await page.evaluate(() => scrollTo(0, 0));
  };
  const shot = async (name, fullPage = false) => {
    await page.mouse.move(0, 0);
    // Closing a sheet returns focus to its trigger; keep that ring out of the evidence.
    await page.evaluate(() => { if (!document.activeElement?.closest('[role="dialog"], [role="alertdialog"]')) document.activeElement?.blur(); });
    const issues = await smallText(page, fontScope);
    fonts.push({ name, issues }); saveReport();
    await page.screenshot({ path: path.join(evidence, `${name}.png`), fullPage });
    // before / part-a are today's UI: report their violations, gate only the redesign.
    if (redesigned) assert.deepEqual(issues, [], `${name}: visible text below ${fontFloor}px`);
  };
  const measure = async (name) => {
    const result = await page.evaluate((selector) => {
      const rows = [...document.querySelectorAll(selector)].map((el) => el.getBoundingClientRect());
      return { width: innerWidth, height: innerHeight, pageHeight: document.documentElement.scrollHeight, rows: rows.length, rowHeights: [...new Set(rows.map((r) => Math.round(r.height)))], firstScreenRows: rows.filter((r) => r.top >= 0 && r.bottom <= innerHeight).length, overflow: document.documentElement.scrollWidth > innerWidth, overflowPixels: Math.max(0, document.documentElement.scrollWidth - innerWidth) };
    }, row);
    metrics.push({ name, ...result }); saveReport();
    if (pageNew) {
      assert.equal(result.overflow, false, `${name}: horizontal overflow`);
      if (result.width <= 412) assert.ok(result.rowHeights.every((h) => h <= 110), `${name}: row over 110px`);
      if (result.width === 390) assert.ok(result.firstScreenRows >= 5, `${name}: fewer than five servers on first screen`);
    }
  };
  const openSheet = async (name = "Boil HKT") => {
    await page.getByRole("button", { name: `${name} 更多操作`, exact: true }).click();
    await page.getByRole("dialog", { name: pageNew ? name : `${name} 更多操作`, exact: true }).waitFor();
  };
  const closeTop = async () => {
    const count = await page.getByRole("dialog").count();
    await top().getByRole("button", { name: /^(关闭面板|关闭)$/ }).first().click();
    await page.waitForFunction((count) => document.querySelectorAll('[role="dialog"]').length < count, count);
  };
  const openXray = async (name = "Boil HKT") => {
    if (pageNew) { await openSheet(name); await top().getByRole("button", { name: "Xray 配置", exact: true }).click(); }
    else await page.locator(row).first().getByRole("button", { name: "Xray 配置", exact: true }).click();
    await mainDialog().waitFor();
    await mainDialog().locator(".xray-json-editor").waitFor();
  };
  const tab = async (label) => {
    await mainDialog().getByRole("button", { name: redesigned && label !== "配置" ? new RegExp(`^${label} \\d+$`) : label, exact: !redesigned || label === "配置" }).click();
    await mainDialog().locator(".xray-loading").waitFor({ state: "hidden" });
    await mainDialog().locator(redesigned ? ".cx-dialog-body" : ".service-dialog-body").first().evaluate((el) => { el.scrollTop = 0; });
  };
  // Leave room for the old dialog's sticky tab bar above the target.
  const scrollDialogTo = (locator) => locator.first().evaluate((el) => { el.scrollIntoView({ block: "start" }); const body = el.closest(".service-dialog-body, .cx-dialog-body"); if (body) body.scrollTop -= 64; });
  const computed = (selectors) => page.evaluate((selectors) => Object.fromEntries(Object.entries(selectors).map(([key, selector]) => {
    const element = document.querySelector(selector);
    if (!element) return [key, null];
    const style = getComputedStyle(element);
    return [key, { size: style.fontSize, weight: style.fontWeight, lineHeight: style.lineHeight, family: style.fontFamily.split(",")[0].replace(/"/g, "") }];
  })), selectors);

  await navigate();
  for (const theme of ["light", "dark"]) {
    await page.evaluate((theme) => { document.documentElement.dataset.theme = theme; }, theme);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.evaluate(() => scrollTo(0, 0));
    if (!["part-a", "xray-only"].includes(phase)) {
      await shot(`390-${theme}-page-top`); await shot(`390-${theme}-page-full`, true); await measure(`390-${theme}`);
      await openSheet(); await shot(`390-${theme}-server-sheet`);
      if (redesigned) { await top().locator(".cx-dialog-body").evaluate((el) => { el.scrollTop = el.scrollHeight; }); await shot(`390-${theme}-server-sheet-bottom`); }
      await closeTop();
    }
    await openXray();
    if (phase !== "part-a") for (const label of ["配置", "入站", "出站", "路由"]) { await tab(label); await shot(`390-${theme}-xray-${({ 配置: "config", 入站: "inbounds", 出站: "outbounds", 路由: "routing" })[label]}`); }
    else await tab("路由");
    if (redesigned) {
      await mainDialog().getByRole("button", { name: "排序", exact: true }).click();
      await mainDialog().locator(xrayRow).nth(2).getByRole("button", { name: "置顶", exact: true }).click();
      await mainDialog().locator(xrayRow).nth(5).getByRole("button", { name: "置底", exact: true }).click();
      await shot(`390-${theme}-routing-draft`);
      await mainDialog().getByRole("button", { name: "取消", exact: true }).click();
      await mainDialog().getByRole("button", { name: "添加规则", exact: true }).click();
      await page.getByRole("dialog", { name: "添加规则", exact: true }).waitFor();
      await shot(`390-${theme}-add-rules`); await closeTop();
    } else {
      await scrollDialogTo(mainDialog().locator(".xray-list-head").filter({ hasText: "路由规则" }));
      await shot(`390-${theme}-rules-list`);
      if (phase === "before") { await scrollDialogTo(mainDialog().locator(".xray-quick-block")); await shot(`390-${theme}-add-rules-inline`); }
    }
    await closeTop();
    if (phase === "xray-only") {
      for (const width of [360, 375, 412]) {
        await page.setViewportSize({ width, height: 844 });
        await openXray();
        for (const label of ["配置", "入站", "出站", "路由"]) {
          await tab(label);
          const overflow = await mainDialog().evaluate((el) => el.scrollWidth > el.clientWidth + 1 || document.documentElement.scrollWidth > innerWidth);
          assert.equal(overflow, false, `${width}-${theme}-${label}: horizontal overflow`);
        }
        await closeTop();
      }
      await page.setViewportSize({ width: 1440, height: 1000 });
      await shot(`1440-${theme}-page`); await openXray(); await tab("路由"); await shot(`1440-${theme}-xray-routing`); await closeTop();
      await page.setViewportSize({ width: 390, height: 844 });
    } else if (phase !== "part-a") {
      for (const width of [360, 375, 412, 1440]) {
        await page.setViewportSize({ width, height: width === 1440 ? 1000 : 844 });
        await page.evaluate(() => scrollTo(0, 0)); await measure(`${width}-${theme}`);
        if (width === 1440) {
          await shot(`1440-${theme}-page`); await openSheet(); await shot(`1440-${theme}-server-sheet`); await closeTop();
          await openXray(); await tab("路由"); await shot(`1440-${theme}-xray-routing`); await closeTop();
        }
      }
    }
  }

  if (phase === "part-a") {
    // Part A on today's UI: 置顶 hidden on the first rule, 置底 hidden on the last, one write + one restart per move.
    await page.setViewportSize({ width: 390, height: 844 });
    await openXray(); await tab("路由");
    const ruleItems = mainDialog().locator(".xray-item").filter({ has: page.locator(".xray-chip-row") });
    const count = await ruleItems.count();
    assert.equal(count, fixture.config.routing.rules.length - 1, "api rule stays hidden");
    assert.equal(await ruleItems.first().getByRole("button", { name: "置顶", exact: true }).count(), 0);
    assert.equal(await ruleItems.last().getByRole("button", { name: "置底", exact: true }).count(), 0);
    assert.equal(await mainDialog().getByRole("button", { name: "置顶", exact: true }).count(), count - 1);
    assert.equal(await mainDialog().getByRole("button", { name: "置底", exact: true }).count(), count - 1);
    const start = requests.length;
    await ruleItems.nth(4).getByRole("button", { name: "置顶", exact: true }).click();
    await mainDialog().getByText("顺序已保存", { exact: true }).waitFor();
    const writes = requests.slice(start).filter((r) => r.method !== "GET");
    assert.equal(writes.filter((r) => r.endpoint.endsWith("/routing")).length, 1);
    assert.equal(writes.filter((r) => r.endpoint.endsWith("/services/control")).length, 1);
    const saved = writes.find((r) => r.endpoint.endsWith("/routing")).body.routing.rules;
    assert.deepEqual(saved[0], fixture.config.routing.rules[0], "protected api rule first and untouched");
    assert.equal(saved[1].marktag, fixture.config.routing.rules[5].marktag);
    checks.push("Part A：首条无置顶、末条无置底；置顶一次 = 1 次 set + 1 次重启 + 重读；api 规则原样置前");
    config = structuredClone(fixture.config);
    await closeTop();
  }

  if (pageNew) {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.evaluate(() => { document.documentElement.dataset.theme = "light"; scrollTo(0, 0); });
    // Font scale: the server page must use the node page's sizes.
    const serverPage = await computed({ h1: ".server-compact .cx-hero h1", h1Count: ".server-compact .cx-hero h1 small", search: ".server-compact .cx-search input", segments: ".server-compact .cx-segments button", rowName: ".server-row .cx-row-name", line2: ".server-row .server-row-live", line3: ".server-row .server-row-meta", tag: ".server-row .cx-tag" });
    await openSheet();
    Object.assign(serverPage, await computed({ sheetTitle: ".server-sheet header h2", sheetSubtitle: ".server-sheet header p", sheetItems: ".server-sheet .cx-sheet-items button", sheetSection: ".server-sheet .cx-sheet-section", quickAction: ".server-sheet .cx-quick-actions button" }));
    await closeTop();
    await openXray(); await tab("路由");
    const xray = await computed({ sheetTitle: ".xray-compact header h2", segments: ".xray-compact .cx-segments button", rowName: ".xray-row .cx-row-name", line2: ".xray-row .node-row-address", tag: ".xray-row .cx-tag", listHead: ".xray-list-head h4", select: ".xray-routing-base select" });
    await mainDialog().getByRole("button", { name: "排序", exact: true }).click();
    Object.assign(xray, await computed({ orderBar: ".xray-order-bar > span", orderButton: ".xray-order-actions button" }));
    await mainDialog().getByRole("button", { name: "取消", exact: true }).click();
    await mainDialog().getByRole("button", { name: "添加规则", exact: true }).click();
    Object.assign(xray, await computed({ addRulesItems: ".xray-add-rules .cx-sheet-items button" }));
    await top().getByRole("button", { name: "禁止 BT", exact: true }).click();
    await page.getByRole("dialog", { name: "Xray 配置编辑" }).waitFor();
    Object.assign(xray, await computed({ editorTitle: ".xray-object-editor h4", editorLabel: ".xray-fields label > span", editorInput: ".xray-fields input", editorSelect: ".xray-fields select", editorTextarea: ".xray-fields textarea" }));
    await page.getByRole("dialog", { name: "Xray 配置编辑" }).getByRole("button", { name: "关闭", exact: true }).click();
    await tab("配置");
    Object.assign(xray, await computed({ jsonEditor: ".xray-compact .xray-json-editor", switchLabel: ".xray-compact .xray-switch", controlButton: ".xray-compact .xray-control-row button" }));
    await closeTop();
    await page.getByRole("button", { name: "打开菜单", exact: true }).click();
    await page.getByRole("button", { name: "节点管理", exact: true }).click();
    await page.locator(".node-compact-row").first().waitFor();
    const nodePage = await computed({ h1: ".node-hero h1", h1Count: ".node-hero h1 small", search: ".node-search input", segments: ".node-grouping button", rowName: ".node-compact-row .cx-row-name", line2: ".node-compact-row .node-row-address", line3: ".node-compact-row .node-row-meta", tag: ".node-compact-row .cx-tag" });
    await page.locator(".node-compact-row .cx-row-name").first().click();
    await page.locator(".node-action-sheet").waitFor();
    Object.assign(nodePage, await computed({ sheetTitle: ".node-action-sheet header h2", sheetSubtitle: ".node-action-sheet header p", sheetItems: ".node-action-sheet .cx-sheet-items button", sheetSection: ".node-action-sheet .cx-sheet-section", quickAction: ".node-action-sheet .cx-quick-actions button" }));
    await closeTop();
    Object.assign(fontScale, { server: serverPage, node: nodePage, xray });
    const table = ["h1", "h1Count", "search", "segments", "rowName", "line2", "line3", "tag", "sheetTitle", "sheetSubtitle", "sheetItems", "sheetSection", "quickAction"].map((key) => ({ item: key, server: serverPage[key] && `${serverPage[key].size}/${serverPage[key].weight}/${serverPage[key].lineHeight}`, node: nodePage[key] && `${nodePage[key].size}/${nodePage[key].weight}/${nodePage[key].lineHeight}` }));
    console.table(table);
    for (const key of ["h1", "h1Count", "search", "segments", "rowName", "line2", "line3", "sheetTitle", "sheetSubtitle", "sheetItems", "sheetSection", "quickAction"]) {
      assert.ok(serverPage[key] && nodePage[key], `font scale: ${key} missing`);
      assert.equal(serverPage[key].size, nodePage[key].size, `font scale: ${key} differs from the node page`);
    }
    for (const key of ["rowName", "line2", "line3"]) assert.equal(serverPage[key].lineHeight, nodePage[key].lineHeight, `font scale: ${key} line height`);
    assert.equal(serverPage.rowName.weight, nodePage.rowName.weight);
    assert.deepEqual([xray.rowName.size, xray.rowName.weight, xray.rowName.lineHeight, xray.line2.size, xray.line2.family, xray.tag.size, xray.sheetTitle.size], [nodePage.rowName.size, nodePage.rowName.weight, nodePage.rowName.lineHeight, nodePage.line2.size, nodePage.line2.family, nodePage.tag?.size || "13px", "18px"]);
    for (const key of ["editorInput", "editorSelect", "select", "addRulesItems"]) assert.ok(parseFloat(xray[key].size) >= 15, `xray ${key} below 15px`);
    for (const key of ["editorLabel", "editorTextarea", "jsonEditor", "orderBar"]) assert.ok(parseFloat(xray[key].size) >= 14, `xray ${key} below 14px`);
    assert.match(xray.jsonEditor.family + xray.editorTextarea.family, /mono|Menlo|Consolas|SFMono/i);
    checks.push("字号阶梯：服务器页 h1/搜索/分段/行名/第二行/第三行/面板标题/面板项 与节点页一致；Xray 行与节点页一致，输入框 ≥15px、标签/JSON ≥14px");
    saveReport();

    await page.getByRole("button", { name: "打开菜单", exact: true }).click();
    await page.getByRole("button", { name: "服务管理", exact: true }).click();
    await page.locator(row).first().waitFor();
    const search = page.getByRole("textbox", { name: "搜索服务器" });
    const visibleNames = () => page.locator(`${row} .cx-row-name`).allTextContents();
    await search.fill("boil hkt"); assert.deepEqual(await visibleNames(), ["Boil HKT", "Boil HKT 99u"]);
    await search.fill("192.0.2.16"); assert.deepEqual(await visibleNames(), ["PQS TW"]);
    await search.fill("台湾"); assert.deepEqual(await visibleNames(), servers.filter((s) => s.region === "台湾").map((s) => s.name));
    await search.fill("dmit 香港"); assert.deepEqual(await visibleNames(), ["Dmit T1"], "region from the geo lookup is searchable");
    await page.getByRole("button", { name: "美日 3", exact: true }).click(); assert.equal(await page.locator(row).count(), 0);
    assert.equal(await page.locator(".server-rows .empty-text").textContent(), "没有匹配的服务器");
    await search.fill(""); assert.deepEqual(await visibleNames(), ["Breadcloud US", "Breadcloud US 年付", "Breadcloud JP"]);
    await page.getByRole("button", { name: "全部 13", exact: true }).click();
    await search.fill("no-such-server"); await shot("390-light-search-empty"); await search.fill("");
    checks.push("名称 / IP / 地区（含 geo 查询结果）搜索与分组交集；空结果文案；旧 grid/list 偏好不影响新列表");
    const rowOf = (name) => page.locator(row).filter({ has: page.getByRole("button", { name, exact: true }) });
    assert.equal(await page.locator(`${row} .cx-tag`, { hasText: /^Helper / }).count(), 1, "only the stale server gets a Helper tag");
    assert.equal(await rowOf("Nobrand KFC 1cm").locator(".cx-tag", { hasText: "Helper 离线 / 数据过期" }).count(), 1);
    assert.equal(await rowOf("Breadcloud JP").locator(".server-row-mode").textContent(), "切换到 External…");
    assert.equal(await rowOf("Boil HKT 99u").locator(".server-row-mode").textContent(), "External");
    assert.equal(await rowOf("Boil HKT").locator(".server-row-mode").textContent(), "Embedded");
    assert.equal(await rowOf("Nobrand KFC 1cm").locator(".server-row-mode").textContent(), "Embedded · Helper 离线");
    checks.push("行内模式 pill 与 CoreModeBadge 同源（切换中显示「切换到 External…」）；Helper 标签只出现在离线 / 过期的服务器上");
    await page.getByRole("button", { name: "Helper 批量升级", exact: false }).click();
    await page.getByRole("button", { name: "一键升级全部 Helper", exact: true }).waitFor();
    await page.locator(".server-delete-history summary").click();
    await page.getByText("本地已删除示例", { exact: true }).waitFor();
    await shot("390-light-idle-lines-expanded");
    await page.getByRole("button", { name: "Helper 批量升级", exact: false }).click();
    await page.locator(".server-delete-history summary").click();
    await page.getByRole("button", { name: "页面更多", exact: true }).click();
    for (const name of ["接入分享服务器", "批量升级 Agent"]) assert.ok(await top().getByRole("button", { name, exact: true }).count(), `page action ${name}`);
    await shot("390-light-page-actions"); await closeTop();
    checks.push("Helper 批量升级 / 删除记录空闲时各一行，可展开；页面 ⋯ 含接入分享服务器、批量升级 Agent");

    await openSheet();
    const sheet = top();
    for (const name of ["Xray 配置", "Agent 管理", "连接数", "编辑", "编辑服务器", "同步节点", "同步节点地址", "配置历史", "下发默认配置", "添加网站", "配置 / 入站 / 出站 / 路由", "指标 / 流量统计 / gRPC", "升级 Agent", "具体连接数与限制", "生成安装命令", "停止 Xray", "重启 Xray", "卸载 Agent", "删除服务器"]) assert.ok(await sheet.getByRole("button", { name, exact: true }).count(), `missing action ${name}`);
    assert.ok(await sheet.getByRole("button", { name: /^Helper 在线 v0\.7\.5/ }).count(), "Helper status row");
    for (const label of ["分享 / 接入服务器", "扫描远程服务"]) assert.ok(await sheet.getByRole("button", { name: new RegExp(label) }).isDisabled());
    await sheet.locator(".core-mode-badge").click();
    await page.getByRole("alertdialog").waitFor(); await shot("390-light-core-mode-confirm");
    await page.getByRole("alertdialog").getByRole("button", { name: "取消", exact: true }).click();
    await closeTop(); checks.push("服务器面板含全部旧菜单项 + 两个「暂未迁移」禁用项；模式切换确认可取消");

    await openXray(); await tab("路由");
    const names = () => mainDialog().locator(`${xrayRow} .cx-row-name`).allTextContents();
    const ruleRows = () => mainDialog().locator(".xray-rows").first().locator(xrayRow);
    const original = await ruleRows().locator(".cx-row-name").allTextContents();
    assert.equal(original.length, fixture.config.routing.rules.length - 1); assert.ok(!original.includes("api"));
    assert.equal(await ruleRows().filter({ hasText: "路由节点 · 香港" }).locator(".node-row-address").textContent(), "用户 fixture-user");
    assert.equal(await ruleRows().filter({ hasText: "OpenAI 直连" }).locator(".node-row-address").textContent(), "域名 geosite:openai +2");
    const writes = () => requests.filter((r) => r.method !== "GET" && r.endpoint !== "/api/v3");
    let start = writes().length;
    await mainDialog().getByRole("button", { name: "排序", exact: true }).click();
    assert.equal(await ruleRows().first().getByRole("button", { name: "置顶", exact: true }).isDisabled(), true);
    assert.equal(await ruleRows().last().getByRole("button", { name: "置底", exact: true }).isDisabled(), true);
    await ruleRows().nth(2).getByRole("button", { name: "置顶", exact: true }).click();
    await ruleRows().nth(1).getByRole("button", { name: "置底", exact: true }).click();
    await ruleRows().nth(2).getByRole("button", { name: "上移", exact: true }).click();
    await ruleRows().nth(0).getByRole("button", { name: "下移", exact: true }).click();
    assert.equal(writes().length, start, "draft moves must not write");
    const moved = await ruleRows().locator(".cx-row-name").allTextContents();
    assert.notDeepEqual(moved, original);
    await mainDialog().getByRole("button", { name: "配置", exact: true }).click();
    assert.ok(confirmations.at(-1).includes("顺序尚未保存"));
    assert.deepEqual(await ruleRows().locator(".cx-row-name").allTextContents(), moved, "dismissing the tab-change confirmation keeps the draft");
    await mainDialog().getByRole("button", { name: "关闭面板", exact: true }).click();
    assert.ok(await mainDialog().isVisible(), "dismissing the close confirmation keeps the dialog");
    await mainDialog().getByRole("button", { name: "保存顺序", exact: true }).click();
    await mainDialog().getByText("顺序已保存", { exact: true }).waitFor();
    const changes = writes().slice(start);
    assert.equal(changes.filter((r) => r.endpoint.endsWith("/routing")).length, 1);
    assert.equal(changes.filter((r) => r.endpoint.endsWith("/services/control")).length, 1);
    assert.deepEqual(changes[0].body.routing.rules[0], fixture.config.routing.rules[0]);
    assert.equal(changes[0].body.routing.rules.length, fixture.config.routing.rules.length);
    assert.deepEqual(await names().then((list) => list.slice(0, moved.length)), moved);
    checks.push("规则 4 次移动零写入；拒绝换页 / 关闭确认均保留草稿；保存 = 1 次 set + 1 次重启 + 重读；api 规则原样置前");
    start = writes().length;
    await mainDialog().getByRole("button", { name: "排序", exact: true }).click();
    await ruleRows().nth(2).getByRole("button", { name: "置顶", exact: true }).click();
    await mainDialog().getByRole("button", { name: "取消", exact: true }).click();
    assert.equal(writes().length, start); assert.deepEqual((await names()).slice(0, moved.length), moved);
    await tab("出站");
    await mainDialog().getByRole("button", { name: "排序", exact: true }).click();
    await mainDialog().locator(xrayRow).nth(2).getByRole("button", { name: "置顶", exact: true }).click();
    await mainDialog().getByText("默认出站将变为 warp-v4 · 保存时会重启 Xray", { exact: true }).waitFor();
    await shot("390-light-outbound-draft");
    await mainDialog().getByRole("button", { name: "保存顺序", exact: true }).click(); assert.equal(writes().length, start, "declined default change writes nothing");
    acceptConfirm = true;
    await mainDialog().getByRole("button", { name: "保存顺序", exact: true }).click();
    await mainDialog().getByText("顺序已保存，Xray 已重启", { exact: true }).waitFor(); acceptConfirm = false;
    assert.equal(writes().slice(start).filter((r) => r.endpoint.endsWith("/outbounds")).length, 1);
    assert.equal(writes().slice(start).filter((r) => r.endpoint.endsWith("/services/control")).length, 1, "default change restarts Xray once");
    assert.equal(config.outbounds[0].tag, "warp-v4");
    assert.ok(confirmations.some((text) => text.includes("默认出站将变为 warp-v4")));
    checks.push("取消排序零写入；出站草稿改变默认项时底栏提示并需确认；保存 = 1 次 reorder + 1 次重启 Xray（不重启时运行中的 Core 仍用旧默认出站）");
    const outboundRow = (name) => mainDialog().locator(xrayRow).filter({ has: page.getByRole("button", { name, exact: true }) });
    assert.deepEqual(await outboundRow("warp-v4").locator(".xray-row-line .cx-tag").allTextContents(), ["默认"]);
    assert.deepEqual(await outboundRow("routed-hk").locator(".xray-row-line .cx-tag").allTextContents(), ["路由出站"]);
    assert.deepEqual(await outboundRow("tunnel-forward").locator(".xray-row-line .cx-tag").allTextContents(), ["端口转发"]);
    await page.getByRole("button", { name: "tunnel-forward 更多操作", exact: true }).click();
    assert.deepEqual(await top().locator(".cx-sheet-items button").allTextContents(), ["查看 JSON"]); await closeTop();
    await page.getByRole("button", { name: "jp-backup 更多操作", exact: true }).click();
    assert.deepEqual(await top().locator(".cx-sheet-items button").allTextContents(), ["查看 JSON", "编辑", "删除"]); await closeTop();
    await page.getByRole("button", { name: "direct 更多操作", exact: true }).click();
    assert.deepEqual(await top().locator(".cx-sheet-items button").allTextContents(), ["查看 JSON", "编辑"]); await closeTop();
    await tab("路由");
    await page.getByRole("button", { name: "路由节点 · 香港 更多操作", exact: true }).click();
    assert.deepEqual(await top().locator(".cx-sheet-items button").allTextContents(), ["查看 JSON"]);
    await shot("390-light-managed-rule-actions"); await closeTop();
    await page.getByRole("button", { name: "日本备用 更多操作", exact: true }).click();
    assert.deepEqual(await top().locator(".cx-sheet-items button").allTextContents(), ["查看 JSON", "编辑", "删除"]);
    await top().getByRole("button", { name: "查看 JSON", exact: true }).click();
    await page.getByRole("dialog", { name: "Xray 配置编辑" }).waitFor(); await shot("390-light-view-json");
    await page.getByRole("dialog", { name: "Xray 配置编辑" }).getByRole("button", { name: "关闭", exact: true }).click();
    checks.push("⋯ 面板：端口转发 / 路由节点只可查看，直连不可删，普通项 查看 JSON / 编辑 / 删除；出站默认 / 路由出站 / 端口转发标签");
    await mainDialog().getByRole("button", { name: "添加规则", exact: true }).click();
    await top().getByRole("button", { name: "禁止 BT", exact: true }).click();
    await page.getByRole("dialog", { name: "Xray 配置编辑" }).waitFor();
    await shot("390-light-rule-editor");
    await page.getByRole("dialog", { name: "Xray 配置编辑" }).getByRole("button", { name: "关闭", exact: true }).click();
    await mainDialog().getByRole("button", { name: "添加规则", exact: true }).click();
    start = writes().length;
    await page.getByRole("button", { name: "删除快捷规则 OpenAI 分流", exact: true }).click(); assert.equal(writes().length, start);
    acceptConfirm = true; await page.getByRole("button", { name: "删除快捷规则 OpenAI 分流", exact: true }).click(); acceptConfirm = false;
    await page.getByRole("button", { name: "删除快捷规则 OpenAI 分流", exact: true }).waitFor({ state: "hidden" });
    assert.equal(writes().slice(start).filter((r) => r.method === "DELETE").length, 1);
    await closeTop();
    checks.push("「＋ 添加规则」面板：内置快捷打开同一编辑器；最近使用删除仍需确认");
    for (const width of [360, 375, 390, 412]) {
      await page.setViewportSize({ width, height: 844 });
      for (const label of ["配置", "入站", "出站", "路由"]) {
        await tab(label);
        const overflow = await mainDialog().evaluate((el) => [el, ...el.querySelectorAll(".cx-dialog-body, .xray-rows, .xray-row")].some((node) => node.scrollWidth > node.clientWidth + 1));
        assert.equal(overflow, false, `${width} ${label} dialog overflow`);
      }
    }
    await closeTop(); await openXray("Boil HKT 99u");
    await tab("出站"); await mainDialog().getByRole("button", { name: "Cloudflare WARP", exact: true }).click();
    await page.getByRole("dialog", { name: "Cloudflare WARP 管理" }).getByText("已安装", { exact: true }).waitFor(); await shot("412-light-warp");
    await page.getByRole("dialog", { name: "Cloudflare WARP 管理" }).getByRole("button", { name: "关闭", exact: true }).first().click();
    await closeTop();
    checks.push("360 / 375 / 390 / 412 下 Xray 四个页签无横向溢出；WARP 面板");
  }
  assert.deepEqual(errors, [], "browser errors");
  saveReport();
  console.log(JSON.stringify({ phase, screenshots: fonts.length, fontViolations: fonts.reduce((n, f) => n + f.issues.length, 0), metrics: metrics.map(({ name, rowHeights, firstScreenRows, pageHeight, overflow }) => ({ name, rowHeights, firstScreenRows, pageHeight, overflow })), checks }, null, 2));
} finally {
  saveReport(); await browser?.close(); await new Promise((resolve) => server.close(resolve));
}
