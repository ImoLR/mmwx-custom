import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";

// Compile the real, unexported card in isolation. API calls and the page's
// effects are deliberately excluded; no product export is needed for these tests.
function renderUserCard(user: Record<string, unknown>, lifecycle?: Record<string, unknown>) {
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
  const UserCard = new Function("React", ...iconNames, `${compiled}\nreturn UserCard;`)(React, ...iconNames.map(() => () => null));
  const noop = () => undefined;
  return renderToStaticMarkup(React.createElement(UserCard, {
    user, lifecycle, busy: "", view: "full", onDialog: noop, onStatus: noop,
    onExtend: noop, onResetTraffic: noop, onDelete: noop,
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
    "React", "useState", "today", "nextMonth", "DialogShell", "assignManagedUserPackage", "unassignManagedUserPackage", "messageOf",
    `${compiled}\nreturn PackageDialog;`,
  )(
    React, (initial: unknown) => [initial, () => undefined], () => "2026-10-04", () => "2026-11-04", () => null,
    assign, async () => assert.fail("existing permanent package must remain assigned"), () => "failure",
  );
  for (const expiry of [null, "", "2027-05-15"]) {
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
    return new Function("users", "packages", "query", "packageFilter", `${compiled}\nreturn compute();`)(users, packages, "", filter);
  };
  assert.deepEqual({ count: compute("counts").get("2"), visible: compute("visible").map((user: { username: string }) => user.username) }, {
    count: 3, visible: ["alice", "bob", "fallback"],
  }, "additional assignments are counted once per user, including without a legacy binding");
  assert.equal(compute("counts").get("1"), 2, "legacy users remain counted and administrators excluded");
  assert.equal(compute("counts").get("all"), 5);
  assert.equal(compute("counts").get("none"), 1);
  assert.deepEqual(compute("visible", "none").map((user: { username: string }) => user.username), ["none"]);
});
