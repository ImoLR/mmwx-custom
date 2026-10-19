import assert from "node:assert/strict";
import test from "node:test";
import { flvxHelperIssue, flvxHelperUpgradable, flvxStateText, type FLVXHelper } from "../../frontend/src/flvx-share-logic.ts";

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
