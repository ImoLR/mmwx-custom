import assert from "node:assert/strict";
import test from "node:test";
import { userConnectionCoverage } from "../../frontend/src/user-manager-logic.ts";

const servers = [{ id: 5, name: "马年" }, { id: 12, name: "158" }, { id: 14, name: "黑五" }, { id: 16, name: "PQS TW" }];
const formal = { success: true, connection_count_ready: true, excluded_server_names: servers.map((server) => server.name), ips: {}, geo_available: false };
const helper = { success: true, available_server_ids: servers.map((server) => String(server.id)), connections: {} };

test("fresh Helper coverage completes External connection counts even with zero users", () => {
  const result = userConnectionCoverage(formal, helper, servers);
  assert.equal(result.incomplete, false);
  assert.match(result.message, /连接数已由 Helper 补齐：马年、158、黑五、PQS TW/);
  assert.match(result.message, /IP／地理位置.*未覆盖：马年、158、黑五、PQS TW/);
  assert.doesNotMatch(result.message, /统计不完整|连接数待补齐/);
});

test("expired or missing Helper coverage remains incomplete and names only missing counts", () => {
  const result = userConnectionCoverage(formal, { ...helper, available_server_ids: ["5", "12", "16"] }, servers);
  assert.equal(result.incomplete, true);
  assert.match(result.message, /连接数待补齐：黑五（请检查 Helper/);
  assert.match(result.message, /连接数已由 Helper 补齐：马年、158、PQS TW/);
  assert.match(userConnectionCoverage(formal, null, servers).message, /Helper 连接数据读取失败/);
});

test("unknown names and ambiguous partial matches cannot claim complete coverage", () => {
  assert.match(userConnectionCoverage(formal, helper, null).message, /连接数覆盖待核对/);
  const result = userConnectionCoverage(formal, helper, [...servers, { id: 99, name: "PQS TW" }]);
  assert.equal(result.incomplete, true);
  assert.match(result.message, /连接数待补齐：PQS TW/);
});

test("readiness combines official not-ready IDs with actual Helper coverage", () => {
  const covered = { ...formal, connection_count_ready: false, not_ready_server_ids: [12] };
  assert.equal(userConnectionCoverage(covered, helper, servers).incomplete, false);
  const missing = userConnectionCoverage({ ...covered, not_ready_server_ids: [99] }, helper, servers);
  assert.equal(missing.incomplete, true);
  assert.match(missing.message, /官方连接统计尚未就绪：服务器 #99/);
  assert.equal(userConnectionCoverage({ ...covered, not_ready_server_ids: [] }, helper, servers).incomplete, true);
  assert.equal(userConnectionCoverage(null, helper, servers).incomplete, true);
  assert.equal(userConnectionCoverage({ success: true }, { success: true }, []).message, "");
});
