import { execFileSync } from "node:child_process";
import { createRequire } from "node:module";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, "../..");
const frontendDir = path.join(repoRoot, "frontend");
const requireFromFrontend = createRequire(path.join(frontendDir, "package.json"));
const { PNG } = requireFromFrontend("pngjs");
const { chromium } = requireFromFrontend("playwright");
const visualRoot = path.join(repoRoot, "tests", "visual");
const baselineRoot = path.join(visualRoot, "baseline");
const currentRoot = path.join(visualRoot, "current");
const diffRoot = path.join(visualRoot, "diff");
const reportRoot = path.join(visualRoot, "report");

const args = new Set(process.argv.slice(2));
const updateBaseline = args.has("--update-baseline");
const skipBuild = args.has("--skip-build") || process.env.VISUAL_SKIP_BUILD === "1";
const baseUrl = process.env.VISUAL_BASE_URL || "https://mmwxc.imgamer.top";
const username = process.env.MMWXC_USERNAME || "";
const password = process.env.MMWXC_PASSWORD || "";
const widths = [360, 375, 390, 412];
const chromiumExecutable =
  process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE ||
  (existsSync("/root/.cache/ms-playwright/chromium-1124/chrome-linux/chrome")
    ? "/root/.cache/ms-playwright/chromium-1124/chrome-linux/chrome"
    : undefined);

const pages = [
  { key: "dashboard", name: "Dashboard", capture: captureDashboard },
  { key: "service", name: "Service Grid", capture: (page) => captureService(page, "grid") },
  { key: "service", name: "Service List", capture: (page) => captureService(page, "list") },
];

function ensureDir(dir) {
  mkdirSync(dir, { recursive: true });
}

function runBuild() {
  if (skipBuild) return;
  execFileSync("npm", ["run", "build"], {
    cwd: frontendDir,
    stdio: "inherit",
  });
}

async function loginIfNeeded(page) {
  await page.goto(`${baseUrl}/?visual=${Date.now()}`, { waitUntil: "domcontentloaded", timeout: 45000 });
  await page.waitForLoadState("networkidle", { timeout: 45000 }).catch(() => {});
  if (!(await page.locator('input[type="password"]').count())) return;
  if (!username || !password) {
    throw new Error("MMWXC_USERNAME and MMWXC_PASSWORD are required when the visual target is not already authenticated.");
  }
  await page.locator("input").nth(0).fill(username);
  await page.locator('input[type="password"]').first().fill(password);
  await page.locator("button").last().click();
  await page.waitForLoadState("networkidle", { timeout: 45000 }).catch(() => {});
  await page.waitForSelector(".system-status-card", { timeout: 45000 });
}

async function openMenu(page) {
  const menuButton = page.locator('button[aria-label="打开菜单"]').first();
  await menuButton.click();
  await page.waitForSelector(".side-menu", { timeout: 15000 });
}

async function captureDashboard(page, width) {
  await page.goto(`${baseUrl}/?visual=${Date.now()}-${width}`, { waitUntil: "domcontentloaded", timeout: 45000 });
  await page.waitForLoadState("networkidle", { timeout: 45000 }).catch(() => {});
  await page.waitForSelector(".system-status-card", { timeout: 45000 });
  await stabilizeVisualState(page);
  await assertNoOverflow(page, "dashboard", width);
}

async function captureService(page, mode, width) {
  await captureDashboard(page, width);
  await openMenu(page);
  await page.getByRole("button", { name: "服务管理" }).click();
  await page.waitForSelector(".service-page", { timeout: 15000 });
  await page.getByLabel(mode === "grid" ? "网格视图" : "列表视图").click();
  await page.waitForSelector(`.service-server-list.${mode}`, { timeout: 15000 });
  await stabilizeVisualState(page);
  await assertNoOverflow(page, `service-${mode}`, width);
}

async function assertNoOverflow(page, label, width) {
  const overflow = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
    bodyScrollWidth: document.body.scrollWidth,
    bodyClientWidth: document.body.clientWidth,
  }));
  if (overflow.scrollWidth !== overflow.clientWidth || overflow.bodyScrollWidth !== overflow.bodyClientWidth) {
    throw new Error(`${label} ${width}px overflow: ${JSON.stringify(overflow)}`);
  }
}

async function stabilizeVisualState(page) {
  await page.evaluate(() => {
    const setText = (selector, values) => {
      document.querySelectorAll(selector).forEach((element, index) => {
        element.textContent = values[index % values.length];
      });
    };
    const setSplitValue = (element, value) => {
      const [main, ...unit] = value.split(" ");
      const strong = element.querySelector("strong");
      const em = element.querySelector("em");
      if (strong) strong.textContent = main;
      if (em) em.textContent = unit.join(" ");
    };

    if (!document.getElementById("visual-stability-style")) {
      const style = document.createElement("style");
      style.id = "visual-stability-style";
      style.textContent = `
        .dashboard-content > :not(.metric-grid) { display: none !important; }
        .gauge-ring:not(.neutral) .gauge-value { stroke: #1677ff !important; stroke-opacity: .9 !important; }
      `;
      document.head.appendChild(style);
    }

    setText(".status-card-source strong, .status-card-source select", ["主控本机"]);
    setText(".gauge-ring text", ["12.34%", "56.78%", "0%", "45.67%"]);
    document.querySelectorAll(".gauge-ring .gauge-value").forEach((element, index) => {
      element.setAttribute("stroke-dasharray", ["32 232", "118 146", "0 264", "96 168"][index % 4]);
    });
    setText(".system-metric-label", [
      "CPU: 2 Core",
      "内存: 1.00 GB / 2.00 GB",
      "交换空间: 0 B / 0 B",
      "存储: 10.0 GB / 20.0 GB",
    ]);
    setText(".xray-card .version-badge", ["v26.6.27"]);
    setText(".xray-card .xray-version", ["v26.6.27"]);
    setText(".xray-card .xray-state span:last-child", ["运行中"]);
    setText(".service-server-identity h2", ["Boil HKT"]);
    setText(".service-location", ["地区数据暂无"]);

    document.querySelectorAll(".service-server-card").forEach((element, index) => {
      element.style.display = index === 0 ? "" : "none";
    });
    document.querySelectorAll(".service-summary-value").forEach((element, index) => {
      setSplitValue(element, ["2", "0", "2.53 KB/s", "1.21 KB/s"][index % 4]);
    });
    document.querySelectorAll(".service-v3-row-stats").forEach((row) => {
      row.querySelectorAll(".service-v3-value").forEach((element, index) => {
        setSplitValue(element, ["2.33 KB/s", "1.00 KB/s", "1.42 GB", "无限"][index % 4]);
      });
    });
  });
}

function screenshotName(pageName, width) {
  return `${pageName}-${width}.png`;
}

function comparePng(baselinePath, currentPath, diffPath) {
  if (!existsSync(baselinePath)) {
    return { status: "missing-baseline", diffPixels: 0, totalPixels: 0, diffPercent: 0 };
  }
  const baseline = PNG.sync.read(readFileSync(baselinePath));
  const current = PNG.sync.read(readFileSync(currentPath));
  const width = Math.max(baseline.width, current.width);
  const height = Math.max(baseline.height, current.height);
  const diff = new PNG({ width, height });
  let diffPixels = 0;
  const totalPixels = width * height;

  for (let y = 0; y < height; y += 1) {
    for (let x = 0; x < width; x += 1) {
      const bi = (baseline.width * y + x) << 2;
      const ci = (current.width * y + x) << 2;
      const di = (width * y + x) << 2;
      const inBaseline = x < baseline.width && y < baseline.height;
      const inCurrent = x < current.width && y < current.height;
      const br = inBaseline ? baseline.data[bi] : 255;
      const bg = inBaseline ? baseline.data[bi + 1] : 255;
      const bb = inBaseline ? baseline.data[bi + 2] : 255;
      const cr = inCurrent ? current.data[ci] : 255;
      const cg = inCurrent ? current.data[ci + 1] : 255;
      const cb = inCurrent ? current.data[ci + 2] : 255;
      const delta = Math.abs(br - cr) + Math.abs(bg - cg) + Math.abs(bb - cb);
      if (delta > 24 || !inBaseline || !inCurrent) {
        diffPixels += 1;
        diff.data[di] = 255;
        diff.data[di + 1] = 0;
        diff.data[di + 2] = 80;
        diff.data[di + 3] = 255;
      } else {
        diff.data[di] = cr;
        diff.data[di + 1] = cg;
        diff.data[di + 2] = cb;
        diff.data[di + 3] = 80;
      }
    }
  }
  writeFileSync(diffPath, PNG.sync.write(diff));
  return {
    status: diffPixels === 0 ? "unchanged" : "changed",
    diffPixels,
    totalPixels,
    diffPercent: totalPixels === 0 ? 0 : Number(((diffPixels / totalPixels) * 100).toFixed(4)),
  };
}

function writeReport(rows) {
  ensureDir(reportRoot);
  const htmlRows = rows.map((row) => `
    <tr>
      <td>${row.page}</td>
      <td>${row.width}</td>
      <td>${row.status}</td>
      <td>${row.diffPercent}%</td>
      <td>${row.baselineRel ? `<img src="../${row.baselineRel}" alt="baseline">` : ""}</td>
      <td><img src="../${row.currentRel}" alt="current"></td>
      <td>${row.diffRel ? `<img src="../${row.diffRel}" alt="diff">` : ""}</td>
    </tr>`).join("");
  writeFileSync(path.join(reportRoot, "index.html"), `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>mmwx-custom Visual Report</title>
  <style>
    body { font-family: system-ui, sans-serif; margin: 24px; color: #1f2328; }
    table { border-collapse: collapse; width: 100%; }
    th, td { border: 1px solid #d8dee4; padding: 8px; vertical-align: top; }
    th { background: #f6f8fa; text-align: left; }
    img { width: 180px; max-width: 100%; border: 1px solid #d8dee4; }
  </style>
</head>
<body>
  <h1>mmwx-custom Visual Report</h1>
  <p>Base URL: ${baseUrl}</p>
  <p>Generated: ${new Date().toISOString()}</p>
  <table>
    <thead><tr><th>Page</th><th>Width</th><th>Status</th><th>Diff</th><th>Baseline</th><th>Current</th><th>Diff Image</th></tr></thead>
    <tbody>${htmlRows}</tbody>
  </table>
</body>
</html>
`);
}

async function main() {
  runBuild();
  ensureDir(currentRoot);
  ensureDir(diffRoot);
  ensureDir(baselineRoot);

  const browser = await chromium.launch({ headless: true, executablePath: chromiumExecutable });
  const rows = [];
  try {
    for (const width of widths) {
      const context = await browser.newContext({
        viewport: { width, height: 844 },
        deviceScaleFactor: 3,
        isMobile: true,
        hasTouch: true,
      });
      await context.addInitScript(() => {
        class VisualWebSocket {
          static CONNECTING = 0;
          static OPEN = 1;
          static CLOSING = 2;
          static CLOSED = 3;
          binaryType = "blob";
          bufferedAmount = 0;
          extensions = "";
          protocol = "";
          readyState = 3;
          url = "";
          onclose = null;
          onerror = null;
          onmessage = null;
          onopen = null;
          constructor(url) {
            this.url = String(url);
            setTimeout(() => {
              this.onclose?.(new CloseEvent("close"));
            }, 0);
          }
          addEventListener() {}
          removeEventListener() {}
          dispatchEvent() { return true; }
          close() {}
          send() {}
        }
        window.WebSocket = VisualWebSocket;
        window.EventSource = class {
          close() {}
          addEventListener() {}
          removeEventListener() {}
        };
      });
      const page = await context.newPage();
      await loginIfNeeded(page);

      for (const item of pages) {
        const name = item.name.toLowerCase().replaceAll(" ", "-");
        const fileName = screenshotName(name, width);
        const baselineDir = path.join(baselineRoot, item.key);
        const currentDir = path.join(currentRoot, item.key);
        const diffDir = path.join(diffRoot, item.key);
        ensureDir(baselineDir);
        ensureDir(currentDir);
        ensureDir(diffDir);

        await item.capture(page, width);
        const currentPath = path.join(currentDir, fileName);
        await page.evaluate(() => window.scrollTo(0, 0));
        await page.waitForTimeout(150);
        await page.screenshot({ path: currentPath, fullPage: false });

        const baselinePath = path.join(baselineDir, fileName);
        if (updateBaseline) {
          await page.screenshot({ path: baselinePath, fullPage: false });
        }

        const diffPath = path.join(diffDir, fileName);
        const result = updateBaseline
          ? { status: "baseline-updated", diffPixels: 0, totalPixels: 0, diffPercent: 0 }
          : comparePng(baselinePath, currentPath, diffPath);
        rows.push({
          page: name,
          width,
          ...result,
          baselineRel: existsSync(baselinePath) ? path.relative(visualRoot, baselinePath) : "",
          currentRel: path.relative(visualRoot, currentPath),
          diffRel: existsSync(diffPath) ? path.relative(visualRoot, diffPath) : "",
        });
      }
      await context.close();
    }
  } finally {
    await browser.close();
  }

  writeReport(rows);
  console.table(rows.map(({ page, width, status, diffPercent }) => ({ page, width, status, diffPercent })));
  const failed = rows.some((row) => row.status === "changed" || row.status === "missing-baseline");
  if (!updateBaseline && failed) process.exitCode = 1;
}

main().catch((error) => {
  console.error(error.message);
  process.exit(1);
});
