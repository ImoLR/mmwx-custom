import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";
import { credentialWriteState, expiredUnboundPackage, rebindPackageInput, parseRenewDays, renewedDate, eligiblePackages, parseTrafficOverride, remainingDays, matchesExpiry } from "../../frontend/src/user-manager-logic.ts";

// Compile the real, unexported card in isolation. API calls and the page's
// effects are deliberately excluded; no product export is needed for these tests.
function renderUserCard(user: Record<string, unknown>, lifecycle?: Record<string, unknown>, props: Record<string, unknown> = {}) {
  const require = createRequire(new URL("../../frontend/package.json", import.meta.url));
  const ts = require("typescript");
  const React = require("react");
  const { renderToStaticMarkup } = require("react-dom/server");
  const source = readFileSync(new URL("../../frontend/src/user-manager.tsx", import.meta.url), "utf8");
  const file = ts.createSourceFile("user-manager.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const names = new Set(["UserCard", "UserFact", "UserAccessStatus", "formatLimit", "formatBytes"]);
  const functions = file.statements.filter((node: any) => ts.isFunctionDeclaration(node) && names.has(node.name?.text));
  const iconImport = file.statements.find((node: any) => ts.isImportDeclaration(node) && node.moduleSpecifier.text === "lucide-react");
  const iconNames = iconImport.importClause.namedBindings.elements.map((element: any) => element.name.text);
  const compiled = ts.transpileModule(functions.map((node: any) => node.getText(file)).join("\n"), {
    compilerOptions: { target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React },
  }).outputText;
  const UserCard = new Function("React", "remainingDays", "expiredUnboundPackage", ...iconNames, `${compiled}\nreturn UserCard;`)(React, remainingDays, expiredUnboundPackage, ...iconNames.map(() => () => null));
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

test("UA-A08 disabled cards retain intent after official renew and explain every node's protection", () => {
  const access = [
    { server_id: 1, server_name: "封禁服务器", inbound_tag: "a", node_id: 10, node_name: "已确认节点", status: "blocked" },
    { server_id: 2, server_name: "内置服务器", inbound_tag: "b", node_id: 20, node_name: "尽力节点", status: "best_effort" },
    { server_id: 3, server_name: "共享服务器", inbound_tag: "c", node_id: 30, node_name: "共享节点", status: "conflict", reason: "该节点与其他用户共用身份，不能封禁" },
    { server_id: 1, server_name: "封禁服务器", inbound_tag: "d", node_id: 40, node_name: "新增节点", status: "pending", reason: "封禁已安排，等待服务器确认" },
  ];
  for (const is_active of [false, true]) {
    const html = renderUserCard({ username: "alice", role: "user", is_active }, { desired_state: "disabled", effective_state: "disabled", pending_count: 0, access });
    assert.match(html, /已禁用/);
    assert.match(html, /已封禁/);
    assert.match(html, /尽力禁用/);
    assert.match(html, /冲突/);
    assert.match(html, /待处理/);
    for (const item of access) assert.ok(html.includes(item.server_name) && html.includes(item.node_name));
    assert.match(html, /此服务器不支持封禁，只能尽力禁用：官方续期\/改套餐\/加节点后该用户可能恢复连接/);
    assert.match(html, /该节点与其他用户共用身份，不能封禁/);
    assert.match(html, /封禁已安排，等待服务器确认/);
    assert.doesNotMatch(html, /blocked_identities|Core/);
  }
});

test("a partially disabled user can enable again or retry unresolved nodes", () => {
  const html = renderUserCard({ username: "alice", role: "user", is_active: false }, {
    desired_state: "disabled", effective_state: "partially_disabled", operation: "disable", pending_count: 1,
    access: [{ server_id: 1, server_name: "共享服务器", node_name: "共享节点", status: "conflict", reason: "该节点与其他用户共用身份，不能封禁" }],
  });
  assert.match(html, /启用 \(1\)<\/button>/);
  assert.match(html, /重试禁用<\/button>/);
  assert.match(html, /禁用未完成/);
});

test("disable confirmation reads the current affected nodes and never submits a missing preview", async () => {
  const require = createRequire(new URL("../../frontend/package.json", import.meta.url));
  const ts = require("typescript");
  const React = require("react");
  const { renderToStaticMarkup } = require("react-dom/server");
  const source = readFileSync(new URL("../../frontend/src/user-manager.tsx", import.meta.url), "utf8");
  const file = ts.createSourceFile("user-manager.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const names = new Set(["DisableUserDialog", "UserAccessStatus", "messageOf"]);
  const functions = file.statements.filter((node: any) => ts.isFunctionDeclaration(node) && names.has(node.name?.text));
  const compiled = ts.transpileModule(functions.map((node: any) => node.getText(file)).join("\n"), {
    compilerOptions: { target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React },
  }).outputText;
  for (const failed of [false, true]) {
    const states: unknown[] = [];
    let stateIndex = 0;
    let effect: () => void;
    let resolve: (value: unknown) => void;
    let reject: (reason: Error) => void;
    let confirms = 0;
    const preview = new Promise((accept, refuse) => { resolve = accept; reject = refuse; });
    const DisableUserDialog = new Function("React", "useState", "useEffect", "DialogShell", "fetchManagedUserAccessPreview", `${compiled}\nreturn DisableUserDialog;`)(
      React, (initial: unknown) => {
        const index = stateIndex++;
        if (!(index in states)) states[index] = initial;
        return [states[index], (value: unknown) => { states[index] = value; }];
      }, (callback: () => void) => { effect = callback; },
      ({ children, footer }: { children: unknown; footer: unknown }) => React.createElement("div", null, children, footer),
      (token: string, username: string) => { assert.equal(token, "session"); assert.equal(username, "alice"); return preview; },
    );
    const render = () => { stateIndex = 0; return DisableUserDialog({ token: "session", user: { username: "alice" }, onClose: () => undefined, onConfirm: () => { confirms++; } }); };
    let element = render();
    effect!();
    let button = element.props.footer.props.children[1];
    assert.equal(button.props.disabled, true);
    button.props.onClick();
    assert.equal(confirms, 0);
    if (failed) reject!(new Error("服务器暂时不可用"));
    else resolve!({ access: [
      { server_id: 1, server_name: "服务器 A", node_name: "正常节点", status: "blocked" },
      { server_id: 2, server_name: "服务器 B", node_name: "尽力节点", status: "best_effort" },
      { server_id: 3, server_name: "服务器 C", node_name: "共享节点", status: "conflict", reason: "与管理员默认凭据共用，不能封禁" },
    ] });
    await new Promise((resolve) => setImmediate(resolve));
    element = render();
    button = element.props.footer.props.children[1];
    const html = renderToStaticMarkup(element);
    if (failed) {
      assert.match(html, /服务器暂时不可用/);
      assert.equal(button.props.disabled, true);
      button.props.onClick();
      assert.equal(confirms, 0);
    } else {
      assert.match(html, /将封禁/);
      assert.match(html, /尽力节点/);
      assert.match(html, /服务器 B/);
      assert.match(html, /官方续期\/改套餐\/加节点后该用户可能恢复连接/);
      assert.match(html, /与管理员默认凭据共用，不能封禁/);
      assert.doesNotMatch(html, /已封禁/);
      assert.equal(button.props.disabled, false);
      button.props.onClick();
      assert.equal(confirms, 1);
    }
  }
});

test("credential repush confirmation distinguishes persistent blocks and best-effort nodes", () => {
  const require = createRequire(new URL("../../frontend/package.json", import.meta.url));
  const ts = require("typescript");
  const source = readFileSync(new URL("../../frontend/src/user-manager.tsx", import.meta.url), "utf8");
  const file = ts.createSourceFile("user-manager.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const fn = file.statements.find((node: any) => ts.isFunctionDeclaration(node) && node.name?.text === "confirmCredentialWrite");
  const compiled = ts.transpileModule(fn.getText(file), { compilerOptions: { target: ts.ScriptTarget.ES2020 } }).outputText;
  let message = "";
  const confirm = new Function("window", "credentialWriteState", `${compiled}\nreturn confirmCredentialWrite;`)({ confirm: (text: string) => { message = text; return true; }, alert: () => undefined }, credentialWriteState);
  const blocked = { status: "blocked", server_name: "服务器 A", node_name: "封禁节点" };
  assert.equal(confirm({ effective_state: "disabled", access: [blocked] }), true);
  assert.match(message, /已封禁节点保持禁用/);
  assert.doesNotMatch(message, /可能恢复连接/);
  confirm({ effective_state: "disabled", access: [blocked, { status: "best_effort", server_name: "服务器 B", node_name: "尽力节点" }] });
  assert.match(message, /可能恢复连接/);
  assert.match(message, /服务器 B \/ 尽力节点/);
  confirm({ effective_state: "disabled" });
  assert.match(message, /尚未确认的节点可能恢复连接/);
  assert.equal(confirm({ effective_state: "delete_partial" }), null);
});

test("history query notices preserve disabled lifecycle on page load and polling", async () => {
  const require = createRequire(new URL("../../frontend/package.json", import.meta.url));
  const ts = require("typescript");
  const source = readFileSync(new URL("../../frontend/src/user-manager.tsx", import.meta.url), "utf8");
  const file = ts.createSourceFile("user-manager.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const page = file.statements.find((node: any) => ts.isFunctionDeclaration(node) && node.name?.text === "UserManagementPage");
  const load = page.body.statements.find((node: any) => ts.isVariableStatement(node) && node.declarationList.declarations[0].name.text === "load");
  const polling = page.body.statements.find((node: any) => ts.isExpressionStatement(node) && node.getText(file).includes("fetchUserConnections(token)"));
  const confirm = file.statements.find((node: any) => ts.isFunctionDeclaration(node) && node.name?.text === "confirmCredentialWrite");
  const compiled = ts.transpileModule([load, polling, confirm].map((node: any) => node.getText(file)).join("\n"), {
    compilerOptions: { target: ts.ScriptTarget.ES2020 },
  }).outputText;
  for (const historyError of [undefined, "读取上次套餐失败"]) {
    const users = { alice: { desired_state: "disabled", effective_state: "disabled", pending_count: 0 } };
    const state: Record<string, any> = {};
    let effect: () => void;
    let confirmations = 0;
    const noop = () => undefined;
    const deps = {
      token: "session", credentialWriteState,
      useCallback: (callback: any) => callback,
      useEffect: (callback: () => void) => { effect = callback; },
      fetchManagedUserLifecycles: async () => ({ users, expired_packages: {}, expired_packages_error: historyError }),
      fetchUserManagementData: async () => ({ users: [{ username: "alice" }], packages: [], nodes: [], failures: [] }),
      fetchManagedUsers: noop, fetchPackages: noop, fetchManagedUserNodes: noop,
      fetchUserConnections: async () => ({}), fetchHelperUserConnections: async () => ({}),
      messageOf: (error: Error) => error.message,
      document: { visibilityState: "visible", addEventListener: noop, removeEventListener: noop },
      window: { setTimeout: noop, clearTimeout: noop, confirm: () => { confirmations++; return false; }, alert: noop },
      ...Object.fromEntries(["Loading", "Users", "Lifecycles", "ExpiredPackages", "Packages", "Nodes", "Notice", "Connections", "HelperConnections", "RealtimeError"].map((name) => [`set${name}`, (value: unknown) => { state[name] = value; }])),
    };
    const handlers = new Function(...Object.keys(deps), `${compiled}\nreturn { load, confirmCredentialWrite };`)(...Object.values(deps));
    await handlers.load();
    assert.deepEqual(state.Users, [{ username: "alice" }]);
    assert.deepEqual(state.Lifecycles, users);
    assert.deepEqual(state.ExpiredPackages, {});
    assert.equal(state.Loading, false);
    assert.equal(state.Notice?.text, historyError);
    assert.equal(handlers.confirmCredentialWrite(state.Lifecycles.alice), null);
    effect!();
    await new Promise((resolve) => setImmediate(resolve));
    assert.deepEqual(state.Lifecycles, users);
    assert.deepEqual(state.ExpiredPackages, {});
    assert.equal(state.RealtimeError, historyError ?? "");
    assert.equal(handlers.confirmCredentialWrite(state.Lifecycles.alice), null);
    assert.equal(confirmations, 2, "history failure must not bypass disabled-user confirmation");
  }
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
    states[5] = lifecycle;
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

test("deletion dialog refreshes stale lifecycle and waits for both preview reads", async () => {
  const require = createRequire(new URL("../../frontend/package.json", import.meta.url));
  const ts = require("typescript");
  const React = require("react");
  const { renderToStaticMarkup } = require("react-dom/server");
  const source = readFileSync(new URL("../../frontend/src/user-manager.tsx", import.meta.url), "utf8");
  const file = ts.createSourceFile("user-manager.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const names = new Set(["DeleteUserDialog", "deletionDecision", "messageOf"]);
  const functions = file.statements.filter((node: any) => ts.isFunctionDeclaration(node) && names.has(node.name?.text));
  const compiled = ts.transpileModule(functions.map((node: any) => node.getText(file)).join("\n"), {
    compilerOptions: { target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React },
  }).outputText;
  const preview = {
    package_bindings: 0, subscriptions: 0, sessions_and_tokens: 0, telegram_bindings: 0,
    subaccounts: 0, inbound_bindings: 1, private_nodes: 0, routed_relations: 0,
    user_limits: 0, custom_assignments: 0, traffic_records: 0, other_private: 0,
    inbound_plan: [{ item_kind: "inbound", server_id: 5, inbound_tag: "conflicted", action: "CONFLICT" }],
  };
  for (const lifecycleFails of [false, true]) {
    const states: unknown[] = [];
    let stateIndex = 0;
    let effect: () => void;
    let resolveLifecycle: (value: unknown) => void;
    let rejectLifecycle: (error: Error) => void;
    const lifecycleResponse = new Promise((resolve, reject) => { resolveLifecycle = resolve; rejectLifecycle = reject; });
    let deleteCalls = 0;
    let lifecycleCalls = 0;
    const DeleteUserDialog = new Function(
      "React", "useState", "useEffect", "DialogShell", "fetchManagedUserDeletionPreview", "fetchManagedUserLifecycles", "deleteManagedUser",
      `${compiled}\nreturn DeleteUserDialog;`,
    )(
      React, (initial: unknown) => {
        const index = stateIndex++;
        if (!(index in states)) states[index] = initial;
        return [states[index], (value: unknown) => { states[index] = value; }];
      }, (callback: () => void) => { effect = callback; },
      ({ children, footer }: { children: unknown; footer: unknown }) => React.createElement("div", null, children, footer),
      async () => ({ preview }),
      () => { lifecycleCalls++; return lifecycleResponse; },
      async () => { deleteCalls++; return { result: { user_deleted: false, pending_count: 1, items: preview.inbound_plan } }; },
    );
    const render = () => {
      stateIndex = 0;
      return DeleteUserDialog({ token: "test-session", user: { username: "alice" }, lifecycle: { effective_state: "enabled" }, onClose: () => undefined, onResult: async () => undefined });
    };
    let element = render();
    effect!();
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(lifecycleCalls, 1, "opening the dialog must refresh the cached lifecycle");
    element = render();
    let button = element.props.footer.props.children[1];
    assert.equal(button.props.disabled, true, "preview alone cannot enable deletion before lifecycle is read");
    button.props.onClick();
    assert.equal(deleteCalls, 0);
    if (lifecycleFails) rejectLifecycle!(new Error("读取用户生命周期失败"));
    else resolveLifecycle!({ users: { alice: { effective_state: "delete_partial" } } });
    await new Promise((resolve) => setImmediate(resolve));
    element = render();
    button = element.props.footer.props.children[1];
    const html = renderToStaticMarkup(element);
    if (lifecycleFails) {
      assert.match(html, /读取用户生命周期失败/);
      assert.equal(button.props.disabled, true);
      button.props.onClick();
      assert.equal(deleteCalls, 0, "failed lifecycle reads must leave deletion unavailable");
    } else {
      assert.doesNotMatch(html, /存在冲突，删除不会执行/);
      assert.match(html, /重试待清理项/);
      assert.equal(button.props.disabled, false, "fresh partial state must override stale enabled prop");
      button.props.onClick();
      await new Promise((resolve) => setImmediate(resolve));
      assert.equal(deleteCalls, 1, "existing partial operation can retry despite conflicts");
    }
  }
});

const expiredPackage = { role: "user", last_package_id: 3, last_package_name: "Boil HKT 用户2", last_package_end_date: "2026-08-25T00:00:00", rebindable: true, reason: "" };

test("expired-unbound card shows history and rebind choices, never-bound keeps its text", () => {
  const user = { username: "Riczzoe", role: "user", package_id: null, package_end_date: null };
  for (const view of ["full", "renewal"]) {
    const html = renderUserCard(user, undefined, { view, expiredPackage });
    assert.match(html, />已过期</);
    assert.match(html, /上次套餐 Boil HKT 用户2，到期 2026-08-25（官方已解绑）/);
    assert.doesNotMatch(html, /未绑定套餐/);
    assert.match(html, /<button type="button">重新绑定上次套餐并续期<\/button>/);
    if (view === "renewal") {
      for (const days of [30, 90, 365]) assert.ok(html.includes(`<button type="button">+${days} 天</button>`));
      assert.match(html, /<button type="button">自定义续期<\/button>/);
    }
  }
  const never = renderUserCard(user, undefined, { view: "renewal" });
  assert.match(never, /未绑定套餐/);
  assert.match(never, /可绑定套餐后生成订阅/);
  assert.doesNotMatch(never, /重新绑定上次套餐并续期|官方已解绑/);
});

test("rebind card excludes admins and current bindings and explains unavailable packages", () => {
  for (const extra of [{ role: "admin" }, { package_id: 4 }, { assignment_package_ids: [4] }]) {
    const html = renderUserCard({ username: "alice", role: "user", ...extra }, undefined, { view: "renewal", expiredPackage });
    assert.doesNotMatch(html, /重新绑定上次套餐并续期|官方已解绑/);
  }
  for (const reason of ["上次套餐已删除或不存在，请在「套餐」中选择其他套餐", "上次套餐已绑定其他用户，请在「套餐」中选择其他套餐"]) {
    const html = renderUserCard({ username: "alice", role: "user" }, undefined, { view: "renewal", expiredPackage: { ...expiredPackage, rebindable: false, reason } });
    assert.match(html, /<button type="button" disabled="">重新绑定上次套餐并续期<\/button>/);
    assert.ok(html.includes(reason));
    assert.match(html, /请打开下方「套餐」管理/);
  }
});


test("card rebind buttons submit the existing assign-package API only after confirmation", async () => {
  const require = createRequire(new URL("../../frontend/package.json", import.meta.url));
  const ts = require("typescript");
  const React = require("react");
  function compile(path: string, names: string[]) {
    const source = readFileSync(new URL(path, import.meta.url), "utf8");
    const file = ts.createSourceFile(path, source, ts.ScriptTarget.Latest, true, path.endsWith("tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
    return ts.transpileModule(file.statements.filter((node: any) => ts.isFunctionDeclaration(node) && names.includes(node.name?.text)).map((node: any) => node.getText(file).replace(/^export /, "")).join("\n"), { compilerOptions: { target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React } }).outputText;
  }
  const ui = compile("../../frontend/src/user-manager.tsx", ["UserCard", "RenewDialog", "confirmCredentialWrite", "messageOf"]);
  const api = compile("../../frontend/src/api.ts", ["managedUserFeature", "assignManagedUserPackage"]);
  const find = (element: any, predicate: (element: any) => boolean): any => {
    if (!element || typeof element !== "object") return undefined;
    if (predicate(element)) return element;
    for (const child of [element.props?.children, element.props?.footer].flat(3)) {
      const result = find(child, predicate);
      if (result) return result;
    }
  };
  for (const choice of [30, 90, 365, 1, 3650, 0, 3651, "cancel", "unavailable", "admin"] as const) {
    const states: unknown[] = [];
    let index = 0;
    const requests: { url: string; token: string; options: any }[] = [];
    const confirmations: string[] = [];
    let saved = 0;
    const icons = Object.fromEntries(["Link2", "PackageCheck", "Edit3", "SlidersHorizontal", "UserRoundCog", "KeyRound", "RefreshCw", "Send", "RotateCcw", "Power", "PowerOff", "Trash2"].map((name) => [name, () => null]));
    const deps = {
      React, remainingDays, expiredUnboundPackage, parseRenewDays, renewedDate, credentialWriteState,
      rebindPackageInput: (user: any, last: any, days: number) => rebindPackageInput(user, last, days, false, new Date("2026-10-07T16:30:00Z")),
      formatBytes: () => "0", UserAccessStatus: () => null, DialogShell: () => null,
      useState: (initial: unknown) => { const slot = index++; if (!(slot in states)) states[slot] = initial; return [states[slot], (value: unknown) => { states[slot] = value; }]; },
      window: { confirm: (message: string) => { confirmations.push(message); return choice !== "cancel"; }, alert: () => assert.fail("unexpected alert") },
      extendManagedUserPackage: () => assert.fail("expired-unbound must use assign-package"),
      MMWX_CUSTOM_API_BASE_URL: "", joinUrl: (base: string, path: string) => base + path,
      requestCustomOperator: async (url: string, token: string, options: any) => { requests.push({ url, token, options }); return { success: true }; },
      ...icons,
    };
    const { UserCard, RenewDialog } = new Function(...Object.keys(deps), `${api}\n${ui}\nreturn { UserCard, RenewDialog };`)(...Object.values(deps));
    const user = { username: "Riczzoe", role: "user", is_active: false, package_id: null, package_end_date: null, is_reset: true, reset_day: 12, traffic_limit_override_gb: 25 };
    const lifecycle = { desired_state: "disabled", effective_state: "disabled", pending_count: 0, access: [{ status: "blocked" }] };
    let dialog: any;
    const card = UserCard({ user, lifecycle, expiredPackage, busy: "", view: "renewal", onDialog: (value: any) => { dialog = value; }, onExtend: () => assert.fail("legacy renew triggered") });
    const quick = [30, 90, 365].includes(Number(choice));
    const button = find(card, (element) => element.type === "button" && (quick ? element.props.children?.join?.("") === `+${choice} 天` : element.props.children === "重新绑定上次套餐并续期"));
    assert.ok(button);
    assert.equal(button.props.disabled, false);
    button.props.onClick();
    assert.equal(dialog.kind, "rebind");
    if (quick) assert.equal(dialog.days, choice);
    const last = choice === "unavailable" ? { ...expiredPackage, rebindable: false, reason: "上次套餐已绑定其他用户" } : expiredPackage;
    const render = () => {
      index = 0;
      return RenewDialog({ token: "session", user: choice === "admin" ? { ...user, role: "admin" } : user, lifecycle, rebind: true, expiredPackage: last, initialDays: dialog.days, onClose: () => undefined, onSaved: async () => { saved++; } });
    };
    let element = render();
    if (typeof choice === "number" && !quick) {
      find(element, (item) => item.type === "input").props.onChange({ target: { value: String(choice) } });
      element = render();
    }
    const submit = element.props.footer.props.children[1];
    if (choice === "unavailable" || choice === "admin") assert.equal(submit.props.disabled, true);
    submit.props.onClick();
    await new Promise((resolve) => setImmediate(resolve));
    if (typeof choice === "string" || choice === 0 || choice === 3651) {
      assert.equal(requests.length, 0);
      assert.equal(saved, 0);
      continue;
    }
    assert.equal(requests.length, 1);
    assert.equal(saved, 1);
    assert.equal(confirmations.length, 2);
    const expected = rebindPackageInput(user, expiredPackage, choice, true, new Date("2026-10-07T16:30:00Z"));
    assert.ok(confirmations[0].includes("Boil HKT 用户2") && confirmations[0].includes(expected.expire_date));
    assert.match(confirmations[0], /台湾今天 2026-10-08/);
    assert.match(confirmations[0], /官方将重建该套餐所在服务器的 Core/);
    assert.match(confirmations[0], /清零且无法找回/);
    assert.match(confirmations[1], /已封禁节点保持禁用/);
    assert.equal(requests[0].url, "/api/custom/users/Riczzoe/features/assign-package");
    assert.equal(requests[0].token, "session");
    assert.equal(requests[0].options.method, "POST");
    assert.deepEqual(JSON.parse(requests[0].options.body), expected);
  }
});
