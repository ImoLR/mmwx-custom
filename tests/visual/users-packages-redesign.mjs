// Only loopback static files are served. All API calls are fulfilled in memory.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { createRequire } from "node:module";
import { readFileSync, writeFileSync, mkdirSync, existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const require = createRequire(path.join(root, "frontend/package.json"));
const { chromium } = require("playwright");
const fixture = JSON.parse(readFileSync(new URL("./users-packages-fixture.json", import.meta.url), "utf8"));
const { users, packages, nodes, servers, lifecycles, expired_packages, groups } = fixture;
const baseline = process.env.REDESIGN_BASELINE === "1";
const evidence = process.env.REDESIGN_EVIDENCE || path.join(root, "runs/users-packages-redesign/evidence");
const dist = process.env.REDESIGN_DIST || path.join(root, "frontend/dist");
mkdirSync(evidence, { recursive: true });
const server = createServer((req, res) => {
  const pathname = new URL(req.url, "http://127.0.0.1").pathname;
  const file = pathname === "/" ? path.join(dist, "index.html") : path.resolve(dist, `.${pathname}`);
  if (!file.startsWith(`${dist}/`) || !existsSync(file)) { res.writeHead(404); res.end(); return; }
  res.writeHead(200, { "Content-Type": ({ ".html": "text/html", ".js": "application/javascript", ".css": "text/css", ".png": "image/png", ".svg": "image/svg+xml", ".wasm": "application/wasm" })[path.extname(file)] || "application/octet-stream" });
  res.end(readFileSync(file));
});
server.on("upgrade", (_req, socket) => socket.destroy());
await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
const base = `http://127.0.0.1:${server.address().port}`;
const errors = [], requests = [], metrics = [], checks = [];
let browser;
try {
  browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE, args: ["--disable-dev-shm-usage"] });
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, timezoneId: "Asia/Taipei" });
  await context.addInitScript(() => {
    localStorage.setItem("mmwx-session", JSON.stringify({ token: "local-fixture", username: "admin", role: "admin", isAdmin: true, expiresAt: "2099-01-01T00:00:00Z" }));
    localStorage.setItem("packages-view-mode", "list"); // The new page must ignore old preferences.
  });
  const operations = {
    "034e094d05aa3f83": { success: true, servers }, "c87c168b92b5f22d": { nodes }, "088e20ddf2bfcab2": { packages }, "ac1c61a9bca33068": { users },
    "a121f6567062f1a8": { templates: ["yaml", "conf", "lcf"].map((ext) => ({ filename: `fixture.${ext}`, name: `本地模板 ${ext}` })) }, "e252f32ba95311fe": { chains: [] },
    "27880a5bdf786b59": { accounts: [], inbound: [], routed: [] }, "05bcee4d21e786ec": { username: "atlas", bound: false },
  };
  await context.route("**/*", async (route) => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== base) { errors.push(`blocked remote: ${url.origin}`); return route.abort(); }
    if (!url.pathname.startsWith("/api/")) return route.continue();
    const body = request.postDataJSON?.() || {};
    requests.push({ method: request.method(), path: url.pathname, body });
    let result = { success: true };
    if (url.pathname === "/api/v3" || url.pathname === "/api/v3u") {
      result = operations[body.op];
      if (!result) { errors.push(`unexpected operation ${body.op}`); return route.fulfill({ status: 400, body: '{}' }); }
    } else if (url.pathname === "/api/admin/nodes") result = { nodes };
    else if (url.pathname === "/api/custom/user-lifecycle") result = { success: true, users: lifecycles, expired_packages };
    else if (url.pathname === "/api/admin/traffic/user-connections") result = { success: true, connections: { atlas: 3, coral: 8 }, ips: { atlas: { "192.0.2.10": 3 } }, geo: { "192.0.2.10": { country: "台湾", city: "台北", isp: "本地测试" } }, excluded_server_names: servers.map((server) => server.name), connection_count_ready: true };
    else if (url.pathname === "/api/custom/agent/user-connections") result = { success: true, connections: { atlas: 12, birch: 5 }, available_server_ids: servers.map((server) => server.id), user_rates: { atlas: { rate_fresh: true, upload_bytes_per_second: 128000, download_bytes_per_second: 2400000 }, coral: { rate_fresh: false, upload_bytes_per_second: 1000, download_bytes_per_second: 2000 } } };
    else if (/\/traffic-groups\/usage$/.test(url.pathname)) result = { success: true, usage: [], nodes: [] };
    else if (/\/traffic-groups$/.test(url.pathname)) result = { success: true, groups: groups[url.pathname.split("/")[4]] || [] };
    else if (url.pathname === "/api/user/config") result = { node_order: nodes.map((node) => node.id) };
    else if (url.pathname === "/api/admin/package-assignments") result = { assignments: [], packages };
    else if (/\/available-packages$/.test(url.pathname)) result = { package_ids: packages.map((pkg) => pkg.id) };
    else if (/\/access$/.test(url.pathname)) result = { success: true, access: lifecycles.dusk.access };
    else if (/\/deletion-preview$/.test(url.pathname)) result = { success: true, preview: { username: url.pathname.split("/")[4], inbound_plan: [], package_bindings: 1, subscriptions: 1, sessions_and_tokens: 0, telegram_bindings: 0, subaccounts: 0, inbound_bindings: 0, private_nodes: 0, routed_relations: 0, user_limits: 0, custom_assignments: 0, traffic_records: 0, other_private: 0 } };
    else if (/\/imported-nodes$/.test(url.pathname)) result = { nodes: [] };
    else if (url.pathname === "/api/admin/node-uris") result = { items: [] };
    else if (url.pathname === "/api/traffic/summary") result = { total_uplink: 0, total_downlink: 0, total_traffic: 0 };
    else if (url.pathname.includes("/traffic/")) result = { items: [], servers: [], connections: {} };
    else if (url.pathname.startsWith("/api/custom/")) result = { success: true, metrics: {}, connections: {}, groups: [] };
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(result) });
  });
  const page = await context.newPage();
  await page.clock.install({ time: new Date(fixture.now) });
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("dialog", (dialog) => dialog.dismiss());
  await page.goto(base);
  const navigate = async (name) => {
    await page.getByRole("button", { name: "打开菜单", exact: true }).click();
    await page.getByRole("button", { name, exact: true }).click();
    await page.locator(name === "用户管理" ? (baseline ? ".user-card" : ".user-compact-row") : (baseline ? ".package-card" : ".package-compact-row")).first().waitFor();
    if (!baseline) await page.waitForFunction(() => document.querySelector(".cx-manager")?.getAttribute("aria-busy") === "false");
    await page.evaluate(() => scrollTo(0, 0));
  };
  const shot = async (name, fullPage = false) => { await page.mouse.move(0, 0); return page.screenshot({ path: path.join(evidence, `${name}.png`), fullPage }); };
  const measure = async (label, kind) => {
    const selector = baseline ? `.${kind}-card` : `.${kind}-compact-row`;
    const data = await page.evaluate(({ selector, kind }) => {
      const rows = [...document.querySelectorAll(selector)].map((element) => ({ id: element.dataset.username || element.dataset.packageId, top: element.getBoundingClientRect().top, bottom: element.getBoundingClientRect().bottom, height: element.getBoundingClientRect().height }));
      const lines = [...document.querySelectorAll(kind === "user" ? ".user-row-package, .user-row-metrics" : ".package-row-cycle, .package-row-meta")];
      return { width: innerWidth, pageHeight: document.documentElement.scrollHeight, rowHeights: [...new Set(rows.map((row) => row.height))], firstScreenRows: rows.filter((row) => row.top >= 0 && row.bottom <= innerHeight).length, firstScreenUnique: new Set(rows.filter((row) => row.top >= 0 && row.bottom <= innerHeight).map((row) => row.id)).size, rows: rows.length, rowDetails: rows, overflow: document.documentElement.scrollWidth > innerWidth, lineOverflow: lines.some((line) => line.scrollWidth > line.clientWidth + 1) };
    }, { selector, kind });
    metrics.push({ label, ...data });
    writeFileSync(path.join(evidence, "metrics.json"), JSON.stringify({ baseline, metrics, checks, requests, errors }, null, 2));

    if (!baseline) {
      assert.equal(data.overflow, false, `${label}: page overflow`);
      assert.equal(data.lineOverflow, false, `${label}: row line overflow`);
      assert.ok(data.rowHeights.every((height) => height <= 88), `${label}: rows too tall`);
      if (data.width === 390) assert.ok(data.firstScreenUnique >= 7, `${label}: only ${data.firstScreenUnique} distinct rows on first screen`);
    }
  };
  const close = async () => { await page.keyboard.press("Escape"); await page.getByRole("dialog").waitFor({ state: "hidden" }); };
  const userSheet = async (username = "atlas") => { await page.getByRole("button", { name: `操作 ${username}`, exact: true }).click(); return page.getByRole("dialog", { name: username, exact: true }); };
  const packageSheet = async () => { await page.locator('.package-compact-row[data-package-id="1"] .cx-row-more').first().click(); return page.getByRole("dialog", { name: packages[0].name, exact: true }); };
  for (const theme of ["light", "dark"]) {
    await page.evaluate((value) => document.documentElement.dataset.theme = value, theme);
    for (const [kind, name] of [["user", "用户管理"], ["package", "套餐管理"]]) {
      await page.setViewportSize({ width: 390, height: 844 });
      await navigate(name);
      const prefix = `${kind}s-390-${theme}`;
      await measure(prefix, kind);
      await shot(`${prefix}-top`);
      await shot(`${prefix}-full`, true);
      if (baseline) continue;
      if (kind === "user") {
        await userSheet();
        assert.ok(await page.locator(".user-action-sheet .user-facts").evaluateAll((items) => items.every((item) => item.getBoundingClientRect().height > 80 && item.scrollHeight <= item.clientHeight + 1)), "user detail facts must not collapse or clip");
        await shot(`${prefix}-sheet`); await close();
        await page.getByRole("button", { name: "筛选", exact: true }).click(); await shot(`${prefix}-filter`); await close();
      } else {
        await packageSheet(); await shot(`${prefix}-sheet`); await close();
        await page.getByRole("button", { name: "按服务器", exact: true }).click(); await page.evaluate(() => scrollTo(0, 0)); await shot(`${prefix}-server`);
        await page.getByRole("button", { name: "按用户", exact: true }).click();
      }
      for (const width of [360, 375, 412, 1440]) {
        await page.setViewportSize({ width, height: width === 1440 ? 1000 : 844 });
        await page.evaluate(() => scrollTo(0, 0)); await measure(`${kind}s-${width}-${theme}`, kind);
        if (width !== 1440) {
          if (kind === "user") await userSheet(); else await packageSheet();
          assert.equal(await page.locator(".cx-sheet, .cx-dialog-body").evaluateAll((items) => items.some((item) => item.scrollWidth > item.clientWidth + 1)), false, `${kind} sheet overflow at ${width}`);
          await close();
        }
        if (width === 1440) {
          await shot(`${kind}s-1440-${theme}-list`);
          if (kind === "user") await userSheet(); else await packageSheet();
          await shot(`${kind}s-1440-${theme}-sheet`); await close();
        }
      }
    }
  }
  if (!baseline) {
    await page.setViewportSize({ width: 390, height: 844 });
    await navigate("用户管理");
    assert.equal(await page.locator(".user-compact-row").count(), 9);
    for (const [name, count] of [["快到期 2", 2], ["已过期 1", 1], ["已禁用 1", 1], ["全部 9", 9]]) { await page.getByRole("button", { name, exact: true }).click(); assert.equal(await page.locator(".user-compact-row").count(), count); }
    await page.getByRole("textbox", { name: "搜索用户" }).fill("atlas@example.test"); assert.equal(await page.locator(".user-compact-row").count(), 1);
    await page.getByRole("textbox", { name: "搜索用户" }).fill("");
    await page.getByRole("button", { name: "筛选", exact: true }).click();
    await page.getByRole("combobox", { name: "套餐筛选" }).selectOption("3");
    await page.getByRole("combobox", { name: "到期筛选" }).selectOption("d30");
    await page.getByRole("button", { name: /显示 .* 个用户/ }).click();
    assert.equal(await page.locator(".cx-active-filters button").count(), 2);
    await page.getByRole("button", { name: "移除套餐筛选" }).click(); await page.getByRole("button", { name: "移除到期筛选" }).click();
    await page.getByRole("combobox", { name: "用户排序" }).selectOption("username"); assert.equal(await page.evaluate(() => localStorage.getItem("mmwx-user-sort")), "username");
    await page.getByRole("combobox", { name: "用户排序" }).selectOption("expiry");
    await page.locator(".user-coverage summary").click(); assert.match(await page.locator(".user-coverage").innerText(), /IP／地理位置仍仅显示官方覆盖的数据/); await page.locator(".user-coverage summary").click();
    checks.push("user quick filters/counts, search, combined filter sheet/chips, sort storage, full Helper notice");
    let sheet = await userSheet();
    for (const action of ["订阅", "节点 URI", "续期", "资料", "套餐", "+30 天", "+90 天", "+365 天", "自定义续期", "重置流量", "限制", "子账户", "导入节点", "Telegram", "重置密码", "禁用", "删除"]) assert.ok(await sheet.getByRole("button", { name: action, exact: true }).count(), action);
    await page.keyboard.press("Tab"); assert.equal(await page.evaluate(() => document.activeElement?.getAttribute("aria-label")), "关闭面板");
    await page.keyboard.press("Shift+Tab"); assert.equal(await page.evaluate(() => document.activeElement?.textContent), "删除"); await close();
    const dialogs = [["订阅", "复制订阅"], ["节点 URI", "用户节点 URI"], ["续期", "自定义续期"], ["资料", "编辑用户资料"], ["套餐", "分配套餐"], ["限制", "用户限制"], ["子账户", "子账户"], ["导入节点", "用户导入节点"], ["Telegram", "绑定 Telegram"], ["重置密码", "重置密码"], ["禁用", "禁用用户 atlas？"], ["删除", "删除用户 atlas？"]];
    for (const [action] of dialogs) {
      sheet = await userSheet(); await sheet.getByRole("button", { name: action, exact: true }).first().click();
      await page.getByRole("dialog").waitFor(); assert.notEqual(await page.getByRole("dialog").getAttribute("aria-label"), "atlas", action);
      await close();
    }
    await page.getByRole("button", { name: "新增", exact: true }).click(); await page.getByRole("dialog", { name: "新增用户", exact: true }).waitFor(); await close();
    sheet = await userSheet("dusk"); assert.ok(await sheet.getByRole("button", { name: "启用 (1)", exact: true }).count()); assert.ok(await sheet.getByRole("button", { name: "重试禁用", exact: true }).count()); assert.ok(await sheet.getByLabel("禁用状态", { exact: true }).count()); await close();
    sheet = await userSheet("ember"); assert.equal(await sheet.getByRole("button", { name: "禁用", exact: true }).count(), 0); assert.match(await sheet.innerText(), /删除未完成，还有 2 个项目待清理/); assert.equal(await sheet.getByRole("button", { name: "+30 天", exact: true }).isDisabled(), true); await close();
    sheet = await userSheet("glade"); assert.equal(await sheet.getByRole("button", { name: "重新绑定上次套餐并续期", exact: true }).isDisabled(), true); assert.match(await sheet.innerText(), /上次套餐已绑定其他用户/); await close();
    for (const action of ["更换订阅凭据", "修复自己节点凭据"]) { sheet = await userSheet("admin"); assert.equal(await sheet.getByRole("button", { name: "删除", exact: true }).count(), 0); await sheet.getByRole("button", { name: action, exact: true }).click(); await page.getByRole("dialog").waitFor(); await close(); }
    checks.push("all user action entries/dialogs, focus trap/Escape, pending disable/delete, unavailable rebind and current-admin actions");
    await navigate("套餐管理");
    await page.getByRole("textbox", { name: "搜索套餐" }).fill("T01"); assert.equal(await page.locator(".package-compact-row").count(), 1); await page.getByRole("textbox", { name: "搜索套餐" }).fill("");
    assert.equal(await page.locator('.package-compact-row[data-package-id="3"]').count(), 2);
    await page.locator(".cx-group-toggle").first().click(); assert.ok(JSON.parse(await page.evaluate(() => localStorage.getItem("mmwx-package-collapsed"))).length); await page.locator(".cx-group-toggle").first().click();
    await page.getByRole("button", { name: "按服务器", exact: true }).click(); assert.equal(await page.evaluate(() => localStorage.getItem("mmwx-package-grouping")), "server"); assert.ok(await page.getByText("多服务器", { exact: true }).count()); assert.ok(await page.getByText("全部节点", { exact: true }).count());
    await page.getByRole("button", { name: "不分组", exact: true }).click(); assert.equal(await page.locator(".package-compact-row").count(), 12);
    await page.getByRole("button", { name: "按用户", exact: true }).click();
    for (const action of ["编辑", "共享组用量"]) { sheet = await packageSheet(); await sheet.getByRole("button", { name: action, exact: true }).click(); await page.getByRole("dialog").waitFor(); await close(); }
    sheet = await packageSheet(); await sheet.getByText("节点限速配置", { exact: true }).click(); assert.match(await sheet.innerText(), /80 Mbps/); await sheet.getByRole("button", { name: "删除", exact: true }).click(); await page.getByRole("alertdialog").waitFor(); await page.getByRole("button", { name: "取消", exact: true }).click();
    await page.getByRole("button", { name: "创建", exact: true }).click(); await page.getByRole("dialog", { name: "创建套餐模板", exact: true }).waitFor(); await close();
    checks.push("package shared/multi-server/all-nodes groups, search, collapse and grouping storage; editor/usage/delete/create entries");
    assert.equal(requests.filter((request) => request.path === "/api/user/config" && request.method !== "GET").length, 0);
    assert.deepEqual(requests.filter((request) => request.method !== "GET" && request.path !== "/api/v3" && request.path !== "/api/v3u"), [], "preview must be read-only");
  }
  assert.deepEqual(errors, []);
  writeFileSync(path.join(evidence, "metrics.json"), JSON.stringify({ baseline, metrics, checks, requests, errors }, null, 2));
  console.log(JSON.stringify({ metrics, checks }, null, 2));
} finally {
  await browser?.close();
  await new Promise((resolve) => server.close(resolve));
}
