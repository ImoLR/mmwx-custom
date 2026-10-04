import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";
import { eligiblePackages, parseTrafficOverride, remainingDays, matchesExpiry } from "../../frontend/src/user-manager-logic.ts";

// Compile the real, unexported card in isolation. API calls and the page's
// effects are deliberately excluded; no product export is needed for these tests.
function renderUserCard(user: Record<string, unknown>, lifecycle?: Record<string, unknown>, props: Record<string, unknown> = {}) {
  const require = createRequire(new URL("../../frontend/package.json", import.meta.url));
  const ts = require("typescript");
  const React = require("react");
  const { renderToStaticMarkup } = require("react-dom/server");
  const source = readFileSync(new URL("../../frontend/src/user-manager.tsx", import.meta.url), "utf8");
  const file = ts.createSourceFile("user-manager.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const names = new Set(["UserCard", "UserFact", "formatLimit", "formatBytes"]);
  const functions = file.statements.filter((node: any) => ts.isFunctionDeclaration(node) && names.has(node.name?.text));
  const iconImport = file.statements.find((node: any) => ts.isImportDeclaration(node) && node.moduleSpecifier.text === "lucide-react");
  const iconNames = iconImport.importClause.namedBindings.elements.map((element: any) => element.name.text);
  const compiled = ts.transpileModule(functions.map((node: any) => node.getText(file)).join("\n"), {
    compilerOptions: { target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React },
  }).outputText;
  const UserCard = new Function("React", "remainingDays", ...iconNames, `${compiled}\nreturn UserCard;`)(React, remainingDays, ...iconNames.map(() => () => null));
  const noop = () => undefined;
  return renderToStaticMarkup(React.createElement(UserCard, {
    user, lifecycle, busy: "", view: "full", onDialog: noop, onStatus: noop,
    onExtend: noop, onResetTraffic: noop, onDelete: noop,
    ...props,
  }));
}

test("UA-D03 official inactive state remains visible alongside Custom lifecycle state", () => {
  for (const lifecycle of [undefined, { effective_state: "enabled", pending_count: 0 }, { effective_state: "disabled", pending_count: 0 }, { effective_state: "delete_partial", pending_count: 1 }]) {
    const html = renderUserCard({ username: "alice", role: "user", is_active: false }, lifecycle);
    assert.doesNotMatch(html, /已启用/, "officially inactive account must not be labelled enabled");
    assert.match(html, /官方已停用/, "official inactive state must remain visible");
    assert.match(html, /class="off"/, "official inactive state must use the inactive badge");
  }
  assert.match(renderUserCard({ username: "alice", role: "user", is_active: true }), />已启用</);
});

test("delete_partial hides access, admin hides destructive actions", () => {
  const partial = renderUserCard({ username: "alice", role: "user", is_active: true }, {
    effective_state: "delete_partial", operation: "delete", pending_count: 1,
  });
  assert.match(partial, /删除未完成/);
  assert.doesNotMatch(partial, />(启用|禁用)<\/button>/);
  const admin = renderUserCard({ username: "admin", role: "admin", is_active: true });
  assert.doesNotMatch(admin, />(启用|禁用|重置密码|重置流量)<\/button>/);
  assert.doesNotMatch(admin, /<b>删除<\/b>/);
});

test("assignment-only users can open subscription and custom renewal without a legacy short code", () => {
  const html = renderUserCard({ username: "alice", role: "user", is_active: true, assignment_package_ids: [2] }, undefined, { view: "renewal" });
  assert.match(html, /<button type="button">订阅<\/button>/);
  assert.match(html, /<button type="button">自定义续期<\/button>/);
  assert.match(html, /已绑定 1 个套餐/);
  const partial = renderUserCard({ username: "alice", role: "user", is_active: true, assignment_package_ids: [2] }, { effective_state: "delete_partial" }, { view: "renewal" });
  assert.match(partial, /<button type="button" disabled="">自定义续期<\/button>/);
});

test("admin credential actions only target the current signed-in administrator", () => {
  const admin = { username: "admin", role: "admin", is_active: true };
  assert.match(renderUserCard(admin, undefined, { currentUsername: "admin" }), /更换订阅凭据/);
  assert.match(renderUserCard(admin, undefined, { currentUsername: "admin" }), /修复自己节点凭据/);
  assert.doesNotMatch(renderUserCard(admin, undefined, { currentUsername: "someone-else" }), /更换订阅凭据|修复自己节点凭据/);
});

test("UA-D06 saving an existing package preserves permanent and finite expiry", async () => {
  const require = createRequire(new URL("../../frontend/package.json", import.meta.url));
  const ts = require("typescript");
  const React = require("react");
  const source = readFileSync(new URL("../../frontend/src/user-manager.tsx", import.meta.url), "utf8");
  const file = ts.createSourceFile("user-manager.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const dialog = file.statements.find((node: any) => ts.isFunctionDeclaration(node) && node.name?.text === "PackageDialog");
  const compiled = ts.transpileModule(dialog.getText(file), {
    compilerOptions: { target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React },
  }).outputText;
  let saved: Record<string, unknown> | undefined;
  const assign = async (_token: string, body: Record<string, unknown>) => { saved = body; return {}; };
  const PackageDialog = new Function(
    "React", "useState", "useEffect", "today", "nextMonth", "DialogShell", "assignManagedUserPackage", "unassignManagedUserPackage", "messageOf", "eligiblePackages", "parseTrafficOverride", "confirmCredentialWrite", "Plus",
    `${compiled}\nreturn PackageDialog;`,
  )(
    React, (initial: unknown) => [stateIndex++ === 1 ? [1] : initial, () => undefined], () => undefined, () => "2026-10-04", () => "2026-11-04", () => null,
    assign, async () => assert.fail("existing permanent package must remain assigned"), () => "failure", eligiblePackages, parseTrafficOverride, () => false, () => null,
  );
  let stateIndex = 0;
  for (const expiry of [null, "", "2027-05-15"]) {
    stateIndex = 0;
    saved = undefined;
    const element = PackageDialog({
      token: "test-session", user: { username: "alice", package_id: 1, package_end_date: expiry },
      packages: [{ id: 1, name: "existing" }], onClose: () => undefined, onSaved: async () => undefined,
    });
    element.props.footer.props.children[1].props.onClick();
    await new Promise((resolve) => setImmediate(resolve));
    assert.ok(saved, "save must call the package API");
    assert.equal(saved.expire_date, expiry || "", "saving unchanged must preserve the expiry");
  }
});

test("UA-D07 package counts and filters use every assignment with legacy fallback", () => {
  const require = createRequire(new URL("../../frontend/package.json", import.meta.url));
  const ts = require("typescript");
  const source = readFileSync(new URL("../../frontend/src/user-manager.tsx", import.meta.url), "utf8");
  const file = ts.createSourceFile("user-manager.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const page = file.statements.find((node: any) => ts.isFunctionDeclaration(node) && node.name?.text === "UserManagementPage");
  const declarations = page.body.statements.filter((node: any) => ts.isVariableStatement(node))
    .flatMap((node: any) => node.declarationList.declarations);
  const users = [
    { username: "alice", role: "user", package_id: 1, assignment_package_ids: [1, 2, 2] },
    { username: "bob", role: "user", assignment_package_ids: [2] },
    { username: "legacy", role: "user", package_id: 1 },
    { username: "fallback", role: "user", package_id: 2, assignment_package_ids: [] },
    { username: "none", role: "user", assignment_package_ids: [] },
    { username: "admin", role: "admin", package_id: 1 },
  ];
  const packages = [{ id: 1, name: "primary" }, { id: 2, name: "additional" }];
  const helper = file.statements.find((node: any) => ts.isFunctionDeclaration(node) && node.name?.text === "managedUserPackageIds");
  const compute = (name: string, filter = "2") => {
    const declaration = declarations.find((node: any) => node.name.text === name);
    const callback = declaration.initializer.arguments[0];
    const compiled = ts.transpileModule(`${helper.getText(file)}\nconst compute = ${callback.getText(file)};`, {
      compilerOptions: { target: ts.ScriptTarget.ES2020 },
    }).outputText;
    return new Function("users", "packages", "query", "packageFilter", "view", "expiryFilter", "matchesExpiry", `${compiled}\nreturn compute();`)(users, packages, "", filter, "full", "all", matchesExpiry);
  };
  assert.deepEqual({ count: compute("counts").get("2"), visible: compute("visible").map((user: { username: string }) => user.username) }, {
    count: 3, visible: ["alice", "bob", "fallback"],
  }, "additional assignments are counted once per user, including without a legacy binding");
  assert.equal(compute("counts").get("1"), 2, "legacy users remain counted and administrators excluded");
  assert.equal(compute("counts").get("all"), 5);
  assert.equal(compute("counts").get("none"), 1);
  assert.deepEqual(compute("visible", "none").map((user: { username: string }) => user.username), ["none"]);
});

test("UA-D01 and UA-D05 deletion preview explains package actions and node ownership", () => {
  const require = createRequire(new URL("../../frontend/package.json", import.meta.url));
  const ts = require("typescript");
  const React = require("react");
  const { renderToStaticMarkup } = require("react-dom/server");
  const source = readFileSync(new URL("../../frontend/src/user-manager.tsx", import.meta.url), "utf8");
  const file = ts.createSourceFile("user-manager.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const names = new Set(["DeleteUserDialog", "deletionDecision"]);
  const functions = file.statements.filter((node: any) => ts.isFunctionDeclaration(node) && names.has(node.name?.text));
  const compiled = ts.transpileModule(functions.map((node: any) => node.getText(file)).join("\n"), {
    compilerOptions: { target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React },
  }).outputText;
  const preview = {
    package_bindings: 5, subscriptions: 0, sessions_and_tokens: 0, telegram_bindings: 0,
    subaccounts: 0, inbound_bindings: 0, private_nodes: 2, routed_relations: 0,
    user_limits: 0, custom_assignments: 0, traffic_records: 0, other_private: 0,
    inbound_plan: [
      { item_kind: "node", server_id: 0, server_name: "Alice 外部节点", inbound_tag: "node:15", action: "DELETE_NODE", node_ids: [15], decision_note: "该用户拥有的外部节点，随用户删除" },
      { item_kind: "inbound", server_id: 5, server_name: "server-5", inbound_tag: "shared", action: "REMOVE_USER_ONLY", remaining_users: 1, decision_note: "还有 1 个业务用户使用，保留 Inbound；保留该用户名下的服务器节点：共享节点（ID 16）" },
      { item_kind: "package", package_id: 1, package_name: "只有自己的套餐", action: "DELETE_PACKAGE", own_nodes: [{ id: 10, name: "Alice 节点" }], other_user_nodes: [], decision_note: "没有其他用户的节点，随用户删除" },
      { item_kind: "package", package_id: 2, package_name: "混合节点套餐", action: "KEEP_PACKAGE", deleted_node_ids: [10, 11], own_nodes: [{ id: 10, name: "Alice 节点" }, { id: 11, name: "Alice 管理员共同节点" }], other_user_nodes: [{ id: 20, name: "Bob 节点" }] },
      { item_kind: "package", package_id: 3, package_name: "Bob 的套餐", action: "KEEP_PACKAGE", deleted_node_ids: [10], own_nodes: [{ id: 10, name: "Alice 节点" }], other_user_nodes: [{ id: 20, name: "Bob 节点" }] },
      { item_kind: "package", package_id: 4, package_name: "意外共享套餐", action: "CONFLICT", decision_note: "套餐还绑定了其他用户，不能删除" },
      { item_kind: "package", package_id: 5, package_name: "未知节点套餐", action: "CONFLICT", unknown_nodes: [{ id: 30, name: "待核对节点" }], decision_note: "无法确认节点归属，不能删除套餐" },
      { item_kind: "package", package_id: 6, package_name: "移除后为空的另一套餐", action: "DELETE_EMPTY_PACKAGE", deleted_node_ids: [10], decision_note: "删除（移除节点后为空）" },
    ],
  };
  const states: unknown[] = [preview, null, false, false, ""];
  let stateIndex = 0;
  const DeleteUserDialog = new Function("React", "useState", "useEffect", "DialogShell", `${compiled}\nreturn DeleteUserDialog;`)(
    React, () => [states[stateIndex++], () => undefined], () => undefined,
    ({ children, footer }: { children: unknown; footer: unknown }) => React.createElement("div", null, children, footer),
  );
  const render = (lifecycle?: Record<string, unknown>) => {
    stateIndex = 0;
    return renderToStaticMarkup(React.createElement(DeleteUserDialog, {
      token: "test-session", user: { username: "alice" }, lifecycle, onClose: () => undefined, onResult: async () => undefined,
    }));
  };
  const html = render();
  assert.match(html, /<b>删除<\/b>/);
  assert.match(html, /Alice 外部节点/);
  assert.match(html, /该用户节点（ID 15）/);
  assert.match(html, /该用户拥有的外部节点，随用户删除/);
  assert.match(html, /保留该用户名下的服务器节点：共享节点（ID 16）/);
  assert.match(html, /保留（移除 2 个该用户节点）/);
  assert.match(html, /保留（移除 1 个该用户节点）/);
  assert.match(html, /该用户节点：Alice 节点（ID 10）/);
  assert.match(html, /其他用户节点：Bob 节点（ID 20）/);
  assert.match(html, /其他用户节点：无/);
  assert.match(html, /冲突：套餐还绑定了其他用户，不能删除/);
  assert.match(html, /冲突：无法确认节点归属，不能删除套餐/);
  assert.match(html, /归属待确认节点：待核对节点（ID 30）/);
  assert.match(html, /<b>删除（移除节点后为空）<\/b>/);
  assert.doesNotMatch(html, /没有其他绑定用户|仍有业务绑定的套餐/);
  assert.match(html, /role="alert">存在冲突，删除不会执行，请先处理以下项目/);
  assert.match(html, /<button class="danger" type="button" disabled="">确认删除<\/button>/);
  for (const effective_state of ["deleting", "delete_partial"]) {
    const retry = render({ effective_state });
    assert.doesNotMatch(retry, /存在冲突，删除不会执行/);
    assert.match(retry, /<button class="danger" type="button">重试待清理项<\/button>/);
  }
  states[1] = { user_deleted: false, pending_count: 2, items: preview.inbound_plan };
  assert.match(render(), /<button class="danger" type="button">重试待清理项<\/button>/);
  assert.doesNotMatch(render(), /存在冲突，删除不会执行/);
  states[1] = null;
  states[0] = { ...preview, inbound_plan: preview.inbound_plan.filter((item) => item.action !== "CONFLICT") };
  assert.doesNotMatch(render(), /存在冲突，删除不会执行/);
  assert.match(render(), /<button class="danger" type="button">确认删除<\/button>/);
});
