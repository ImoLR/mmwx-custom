import assert from "node:assert/strict";
import test from "node:test";
import { formatHTTPError, MAX_API_ERROR_LENGTH } from "../../frontend/src/http-error.ts";

test("Cloudflare HTML 502 becomes a concise upstream error", () => {
  const html = "<!DOCTYPE html><html><title>502: Bad gateway</title>" + "x".repeat(20_000) + "</html>";
  assert.equal(formatHTTPError(502, "text/html; charset=UTF-8", html), "请求失败：上游服务暂时不可用（HTTP 502）");
});

test("long text error is truncated", () => {
  const message = formatHTTPError(500, "text/plain", "x".repeat(20_000));
  assert.ok(message.length <= MAX_API_ERROR_LENGTH + 8);
  assert.match(message, /已截断/);
  assert.doesNotMatch(message, /x{500}/);
});

test("delete preview 502 HTML uses the same concise error", () => {
  assert.equal(formatHTTPError(502, "text/html", "<html>preview failed</html>"), "请求失败：上游服务暂时不可用（HTTP 502）");
});
