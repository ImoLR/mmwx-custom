import { createRequire } from "node:module";
import { existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "../..");
const frontendDir = path.join(repoRoot, "frontend");
const requireFromFrontend = createRequire(path.join(frontendDir, "package.json"));
const { chromium } = requireFromFrontend("playwright");
const viteModule = await import(pathToFileURL(requireFromFrontend.resolve("vite")).href);

const chromiumCandidates = [
  process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE,
  "/root/.cache/ms-playwright/chromium-1124/chrome-linux/chrome",
  "/root/projects/vps-stock-watch/vps-stock-watch/.playwright-browsers/chromium-1223/chrome-linux64/chrome",
].filter(Boolean);
const executablePath = chromiumCandidates.find((candidate) => existsSync(candidate));
const widths = [360, 375, 390, 412];

const tcp = (overrides = {}) => ({
  tcp_total: 24, established: 8, syn_sent: 1, syn_recv: 0,
  fin_wait_1: 1, fin_wait_2: 0, time_wait: 12, close_wait: 0,
  last_ack: 1, closing: 0, close: 1, unknown: 0, ...overrides,
});
const proxy = (tag, user, port, outbound) => ({
  identity: { inbound_tag: tag, user }, inbound_tag: tag, user, inbound_port: port,
  current_total: outbound + 3, inbound_active: 3, inbound_tcp: tcp({ tcp_total: 3, established: 3, time_wait: 0 }),
  inbound_online_ips: [{ ip: "198.51.100.23", connections: 3 }], outbound_active: outbound,
  outbound_pending: 1, outbound_tcp: tcp({ tcp_total: outbound + 1, established: outbound, time_wait: 1 }),
  outbound_new_rate: 2, outbound_new_total: 300, outbound_rejected_total: 5,
  rejected_active_limit: 2, rejected_new_rate_limit: 1, rejected_user_total_limit: 1,
  rejected_port_total_limit: 2, rejected_user_new_rate_limit: 1, rejected_port_new_rate_limit: 1,
  rejected_online_ip_limit: 0, rejected_global_total_limit: 0, max_inbound_online_ips: null,
  max_total_connections: null, max_outbound_tcp_active: null, max_outbound_tcp_new_per_second: null,
  close_wait_timeout_seconds: null, source: "xray_core_runtime", management_group: "ken",
});
const proxyA = proxy("shadowsocks2022-10015", "proto-a", 10015, 8);
const proxyB = proxy("vless-10016", "proto-b", 10016, 6);
const aggregate = {
  group: "ken", current_total: 20, inbound_active: 6, inbound_tcp: tcp({ tcp_total: 6, established: 6, time_wait: 0 }),
  inbound_online_ips: [{ ip: "198.51.100.23", connections: 6 }], outbound_active: 14, outbound_pending: 2,
  outbound_tcp: tcp(), outbound_new_rate: 4, outbound_new_total: 600, outbound_rejected_total: 10,
  rejected_user_total_limit: 2, rejected_user_new_rate_limit: 2, rejected_port_total_limit: 4,
  rejected_port_new_rate_limit: 2, rejected_online_ip_limit: 0, rejected_global_total_limit: 0,
  max_outbound_tcp_active: 100, max_outbound_tcp_new_per_second: 20,
};
const fixture = {
  success: true, available: true, stale_timeout_seconds: 15,
  settings: {
    default_close_wait_timeout_seconds: null, online_ip_grace_period_seconds: 30,
    global_total_limit_enabled: true, max_global_total_connections: 500,
    users: [], management_users: [{ username: "ken", max_outbound_tcp_active: 100, max_outbound_tcp_new_per_second: 20 }],
    ports: [
      { inbound_tag: proxyA.inbound_tag, max_outbound_tcp_active: 30, max_outbound_tcp_new_per_second: 10 },
      { inbound_tag: proxyB.inbound_tag, max_outbound_tcp_active: 50, max_outbound_tcp_new_per_second: null },
    ],
  },
  management: {
    users: [{
      username: "ken", source: "binding", aggregate,
      limits: { username: "ken", max_outbound_tcp_active: 100, max_outbound_tcp_new_per_second: 20 },
      ports: [
        { inbound_tag: proxyA.inbound_tag, port: 10015, protocol: "shadowsocks", source: "binding", protocol_identities: ["proto-a"], aggregate: proxyA, limits: { inbound_tag: proxyA.inbound_tag, max_outbound_tcp_active: 30, max_outbound_tcp_new_per_second: 10 } },
        { inbound_tag: proxyB.inbound_tag, port: 10016, protocol: "vless", source: "binding", protocol_identities: ["proto-b"], aggregate: proxyB, limits: { inbound_tag: proxyB.inbound_tag, max_outbound_tcp_active: 50, max_outbound_tcp_new_per_second: null } },
      ],
    }],
    unassigned_ports: [{ inbound_tag: "orphan-inbound-with-a-long-tag", port: 12345, protocol: "shadowsocks", protocol_identities: ["orphan-identity"], reason: "no_binding_owner_or_manual_assignment" }],
    assignable_users: ["ken", "imolr", "odingAI"], warnings: [],
  },
  record: {
    server_id: "12", helper_version: "v0.6.0", updated_at: "2026-09-15T10:00:00Z",
    snapshot: {
      system: tcp({ tcp_total: 200, established: 82, time_wait: 107 }),
      inbounds: [], proxy_users: [proxyA, proxyB], management_groups: [aggregate],
      global: { current_total: 40, max_total: 500, rejected_global_total_limit: 0 },
      core: { available: true, interface_version: 3, started_at: "2026-09-15T10:00:00Z" }, sampled_at: "2026-09-15T10:00:00Z",
    },
  },
};

const fixtureModule = `
  import React from "react";
  import { createRoot } from "react-dom/client";
  import { ConnectionsManager } from "/src/connections-manager.tsx";
  import "/src/styles.css";
  const server = { id: 12, name: "Boil Hinet mobile layout validation" };
  createRoot(document.getElementById("root")).render(React.createElement("section", { className: "service-dialog fixture-dialog" }, React.createElement(ConnectionsManager, { server, token: "fixture" })));
`;

const server = await viteModule.default.createServer({
  root: frontendDir,
  logLevel: "error",
  server: { host: "127.0.0.1", port: 0 },
  plugins: [{
    name: "connections-layout-fixture",
    transformIndexHtml: () => [{ tag: "script", attrs: { type: "module", src: "/virtual-connections-layout" }, injectTo: "body" }],
    resolveId: (id) => id === "/virtual-connections-layout" ? "\0virtual-connections-layout" : undefined,
    load: (id) => id === "\0virtual-connections-layout" ? fixtureModule : undefined,
  }],
});

await server.listen();
const browser = await chromium.launch({ headless: true, executablePath });
try {
  const baseURL = server.resolvedUrls.local[0];
  for (const width of widths) {
    const context = await browser.newContext({ viewport: { width, height: 844 }, isMobile: true, hasTouch: true });
    const page = await context.newPage();
    await page.route("**/api/custom/servers/12/connections", async (route) => {
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(fixture) });
    });
    await page.goto(baseURL, { waitUntil: "networkidle" });
    await page.waitForSelector(".connection-user-fold");
    await assertNoOverflow(page, width, "folded");
    await page.locator(".connection-user-fold > summary").click();
    await assertNoOverflow(page, width, "user-open");
    await page.locator(".connection-port-fold > summary").first().click();
    await assertNoOverflow(page, width, "port-open");
    const machineOpen = await page.locator(".connection-manager > details.connection-fold").evaluate((element) => element.hasAttribute("open"));
    const secondPortOpen = await page.locator(".connection-port-fold").nth(1).evaluate((element) => element.hasAttribute("open"));
    if (machineOpen || secondPortOpen) throw new Error(`${width}px default folding regressed`);
    await context.close();
  }
  process.stdout.write(`connections layout passed at ${widths.join(", ")}px\n`);
} finally {
  await browser.close();
  await server.close();
}

async function assertNoOverflow(page, width, state) {
  const dimensions = await page.evaluate(() => ({
    document: [document.documentElement.scrollWidth, document.documentElement.clientWidth],
    body: [document.body.scrollWidth, document.body.clientWidth],
    manager: (() => { const element = document.querySelector(".connection-manager"); return [element.scrollWidth, element.clientWidth]; })(),
  }));
  if (dimensions.document[0] > dimensions.document[1] || dimensions.body[0] > dimensions.body[1] || dimensions.manager[0] > dimensions.manager[1]) {
    throw new Error(`${width}px ${state} horizontal overflow: ${JSON.stringify(dimensions)}`);
  }
}
