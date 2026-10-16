// The real CoreControlPanel and progress component, with read-only local fixtures.
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { createServer } from "node:http";
import { readFileSync, readdirSync, writeFileSync, mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const frontend = path.join(root, "frontend");
const require = createRequire(path.join(frontend, "package.json"));
const { chromium } = require("playwright");
const ts = require("typescript");
const { build } = require("esbuild");
const output = process.env.UPGRADE_PROGRESS_EVIDENCE || path.join(root, "tests/visual/current/upgrade-progress");
mkdirSync(output, { recursive: true });
const source = readFileSync(path.join(frontend, "src/main.tsx"), "utf8");
const tree = ts.createSourceFile("main.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const names = ["CoreControlPanel", "InfoBlock", "coreVersionLabel", "componentUpgradeState", "formatDateTime"];
const functions = tree.statements.filter((node) => ts.isFunctionDeclaration(node) && names.includes(node.name?.text)).map((node) => node.getText(tree)).join("\n");
assert.equal(functions.match(/function /g)?.length, 6); // Includes the upgrade handler.
const bundle = await build({ stdin: { contents: `
import React, {useCallback,useEffect,useState,useRef} from 'react';
import {createRoot} from 'react-dom/client';
import {RefreshCw} from 'lucide-react';
import {UpgradeProgressView} from './src/upgrade-progress';
import {componentUpgradeLabel,upgradeRunning,progressForComponent,upgradeBlocked,officialReconnectText} from './src/upgrade-progress-state';
import {serverCoreVersion} from './src/xray-capabilities';
const fetchCoreMode = async () => ({configured:true,controller_mode:'external',current_mode:'external',intent:{repair_status:'healthy',desired_core_mode:'external'}});
const fetchCustomReleaseInfo = async () => ({release:{latest_helper_version:'v0.7.2',latest_core_version:'8f5b614'}});
const fetchCustomAgentStatus = async () => { window.polls.push(Date.now()); return structuredClone(window.fixture); };
const dispatchCustomAgentAction = async () => {throw Error('fixture forbids dispatch');};
${functions}
createRoot(document.getElementById('root')).render(<CoreControlPanel server={{id:5,name:'升级进度样本',xray_mode:'external'}} sessionToken="fixture"/>);
`, resolveDir: frontend, loader: "tsx" }, bundle: true, write: false, format: "iife" });
const cssFile = readdirSync(path.join(frontend, "dist/assets")).find((name) => name.endsWith(".css"));
const css = readFileSync(path.join(frontend, "dist/assets", cssFile));
const now = Date.parse("2026-10-15T16:00:00Z");
const at = (seconds) => new Date(now - seconds * 1000).toISOString();
const phaseNames = ["dispatching", "downloading", "verifying", "installing", "restarting", "reconnecting"];
const texts = ["已下发升级任务 → v0.7.2", "正在下载", "SHA-256 校验通过", "备份并替换文件", "停止旧进程 → 启动新进程", "等待健康检查 1/25"];
function fixture(phase, options = {}) {
  const component = options.component || "helper";
  const last = options.age ?? 8;
  const progress = { component, phase, target_version: component === "helper" ? "v0.7.2" : "8f5b614", started_at: at(Math.max(83, last + 30)), updated_at: at(last) };
  const count = options.step ?? (phaseNames.indexOf(phase) + 1 || 6);
  progress.log = phaseNames.slice(0, count).map((p, i) => ({ at: at(last + count - i), phase: p, text: texts[i] }));
  if (["failed", "rolled_back", "success"].includes(phase)) progress.log.push({ at: at(last), phase, text: phase === "success" ? "已确认新版本 v0.7.2" : "健康检查超时" + (phase === "rolled_back" ? "，已恢复上一版本" : "") });
  if (options.percent) Object.assign(progress, { percent: options.percent, bytes_done: 4.2 * 1024 * 1024, bytes_total: 10 * 1024 * 1024 });
  if (options.old) { delete progress.started_at; delete progress.log; }
  return { success: true, status: { helper: { installed: true, version: phase === "success" ? "v0.7.2" : "v0.7.1" }, core: { installed: true, version: "8f5b614" }, architecture: "amd64", reported_at: at(options.offline ? 220 : 3), update: progress }, pending: phaseNames.includes(phase) ? [{ id: "fixture", action: `${component}.update`, created_at: at(83) }] : [], results: [] };
}
const fixtures = {
  downloading: fixture("downloading", { percent: 42 }),
  installing: fixture("installing", { component: "core" }),
  stalled: fixture("downloading", { percent: 42, age: 190 }),
  "stalled-offline": fixture("reconnecting", { age: 220, offline: true }),
  failed: fixture("failed", { component: "core", step: 5 }),
  "rolled-back": fixture("rolled_back", { step: 6 }),
  "old-helper": fixture("downloading", { old: true }),
  success: fixture("success"),
};
const server = createServer((request, response) => {
  const url = new URL(request.url, "http://127.0.0.1");
  if (url.pathname === "/fixture.js") { response.setHeader("Content-Type", "text/javascript"); response.end(bundle.outputFiles[0].text); }
  else if (url.pathname === "/style.css") { response.setHeader("Content-Type", "text/css"); response.end(css); }
  else {
    const data = fixtures[url.searchParams.get("case")] || fixtures.downloading;
    response.setHeader("Content-Type", "text/html; charset=utf-8");
    response.end(`<meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/style.css"><style>body{padding:12px}#root{max-width:960px;margin:auto}.core-control-panel{margin-top:0}</style><div id="root"></div><script>window.fixture=${JSON.stringify(data)};window.polls=[];</script><script src="/fixture.js"></script>`);
  }
});
await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
const base = `http://127.0.0.1:${server.address().port}`;
const results = [], errors = [];
let browser;
try {
  browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE });
  for (const width of [390, 1440]) {
    for (const [name, data] of Object.entries(fixtures)) {
      const page = await browser.newPage({ viewport: { width, height: 1000 }, timezoneId: "Asia/Taipei" });
      page.on("pageerror", (error) => errors.push(error.message));
      await page.route("**/*", (route) => new URL(route.request().url()).origin === base ? route.continue() : route.abort());
      await page.clock.install({ time: new Date(now) });
      await page.goto(`${base}/?case=${name}`);
      await page.locator(".upgrade-steps").waitFor();
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `${name} ${width}: overflow`);
      assert.equal(await page.locator(".upgrade-steps li").count(), 7);
      if (name === "downloading") assert.match(await page.locator(".upgrade-download").innerText(), /42% · 4.2\/10.0 MB/);
      if (name === "stalled") assert.match(await page.locator(".upgrade-stall").innerText(), /仍在上报/);
      if (name === "stalled-offline") assert.match(await page.locator(".upgrade-stall").innerText(), /Helper 离线/);
      const running = phaseNames.includes(data.status.update.phase);
      assert.equal(await page.locator(".upgrade-log").getAttribute("open") !== null, running);
      if (!running) await page.locator(".upgrade-log summary").click();
      await page.screenshot({ path: path.join(output, `${name}-${width}.png`), fullPage: true, animations: "disabled" });
      await page.clock.runFor(running ? 2100 : 5100);
      const polls = await page.evaluate(() => window.polls);
      assert.ok(polls.length >= 2 && Math.abs(polls[1] - polls[0] - (running ? 2000 : 5000)) < 250, `${name}: polling interval ${polls}`);
      if (name === "installing") {
        await page.evaluate(() => { window.fixture.status.update.log.push(...Array.from({ length: 20 }, (_, i) => ({ at: new Date(Date.now()).toISOString(), phase: "installing", text: `安装步骤 ${i + 1}` }))); });
        await page.clock.runFor(2100);
        assert.equal(await page.locator(".upgrade-log-lines").evaluate((box) => box.scrollTop + box.clientHeight >= box.scrollHeight - 2), true, "auto-scroll");
        await page.locator(".upgrade-log summary").click();
        await page.clock.runFor(2100);
        assert.equal(await page.locator(".upgrade-log").getAttribute("open"), null, "collapse persists across polls");
      }
      results.push({ name, width, polling: running ? 2000 : 5000, overflow: false });
      await page.close();
    }
  }
  assert.deepEqual(errors, []);
} finally {
  await browser?.close();
  await new Promise((resolve) => server.close(resolve));
  writeFileSync(path.join(output, "results.json"), JSON.stringify({ results, errors }, null, 2));
}
console.log(`Upgrade progress: ${results.length} desktop/mobile fixtures passed`);
