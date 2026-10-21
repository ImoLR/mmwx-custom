import assert from "node:assert/strict";
import test from "node:test";
import { flvxExtraProtocols, flvxHelperIssue, flvxHelperUpgradable, flvxProtocolName, flvxStateText, type FLVXHelper } from "../../frontend/src/flvx-share-logic.ts";

const ready: FLVXHelper = { installed: true, version: "v0.7.5", architecture: "amd64", flvx_ready: true, upgrading: false };
const outdated: FLVXHelper = { ...ready, version: "v0.7.4", flvx_ready: false };

test("unreported desired nodes are pending, not removed", () => {
  assert.equal(flvxStateText("pending", ready), "待应用");
  assert.equal(flvxStateText("pending", outdated), "等待 Helper");
  assert.equal(flvxStateText("removed", ready), "已移除");
  assert.equal(flvxStateText("active", outdated), "已启用");
});

test("core state of an old Helper reads as an upgrade request; unknown states pass through", () => {
  assert.equal(flvxStateText("helper_outdated"), "Helper 需升级");
  assert.equal(flvxStateText("stopped"), "已停止");
  assert.equal(flvxStateText("future_state"), "future_state");
});

test("Helper issue and upgrade offer follow the reported capability", () => {
  assert.equal(flvxHelperIssue(ready), "");
  assert.equal(flvxHelperIssue(undefined), "");
  assert.match(flvxHelperIssue(outdated), /Helper 需升级：当前 v0\.7\.4/);
  assert.match(flvxHelperIssue({ ...outdated, installed: false, version: "" }), /未安装 Helper/);
  assert.equal(flvxHelperUpgradable(outdated), true);
  assert.equal(flvxHelperUpgradable(ready), false);
  assert.equal(flvxHelperUpgradable({ ...outdated, version: "v0.1.9" }), false);
  assert.equal(flvxHelperUpgradable({ ...outdated, installed: false }), false);
});

test("node protocols are named, including the landing-protocols-v1 ones", () => {
  assert.equal(flvxProtocolName("ss2022"), "SS2022");
  assert.equal(flvxProtocolName("vless_reality_vision"), "VLESS Reality Vision");
  assert.equal(flvxProtocolName("anytls"), "AnyTLS");
  assert.equal(flvxProtocolName("snell"), "Snell");
  assert.equal(flvxProtocolName("mieru"), "Mieru");
  assert.equal(flvxProtocolName("future"), "future");
});

test("扩展协议 line: advertised, TCP-only, or what to upgrade", () => {
  const capable = { ...ready, flvx_protocols: true };
  const all = { versions: [1], protocols: { anytls: { udp: true }, snell: { udp: true }, mieru: { udp: true } } };
  assert.deepEqual(flvxExtraProtocols(all, capable, "running"), { ready: true, text: "AnyTLS · Snell · Mieru 可由 FLVX 创建" });
  assert.deepEqual(flvxExtraProtocols({ versions: [1], protocols: { snell: { udp: false }, mieru: { udp: true } } }, capable, "running"), { ready: true, text: "Snell · Mieru 可由 FLVX 创建 · Snell 仅 TCP" });
  assert.equal(flvxExtraProtocols(undefined, ready, "running").text, "升级 Helper 后可用（AnyTLS / Snell / Mieru）");
  assert.equal(flvxExtraProtocols(undefined, capable, "stopped").text, "等待 FLVX Core 运行后核对");
  assert.equal(flvxExtraProtocols(undefined, capable, "running").text, "升级 FLVX Core 后可用（AnyTLS / Snell / Mieru）");
});

