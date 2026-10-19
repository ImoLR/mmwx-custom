import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFileSync, existsSync } from "node:fs";
import path from "node:path";

// Shared by the node, user/package and server visual harnesses. Never proxies APIs.
export async function serveFixtureBuild(dist) {
  dist = path.resolve(dist);
  assert.ok(existsSync(path.join(dist, "index.html")), "Build the UI before running this harness");
  const server = createServer((request, response) => {
    const pathname = new URL(request.url, "http://127.0.0.1").pathname;
    const file = path.resolve(dist, pathname === "/" ? "index.html" : `.${pathname}`);
    if (!file.startsWith(`${dist}/`) || !existsSync(file)) { response.writeHead(404); response.end(); return; }
    response.writeHead(200, { "Content-Type": ({ ".html": "text/html", ".js": "application/javascript", ".css": "text/css", ".png": "image/png", ".svg": "image/svg+xml", ".wasm": "application/wasm" })[path.extname(file)] || "application/octet-stream" });
    response.end(readFileSync(file));
  });
  server.on("upgrade", (_request, socket) => socket.destroy());
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  return server;
}
