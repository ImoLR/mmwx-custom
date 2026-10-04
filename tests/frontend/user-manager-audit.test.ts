import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";

const enabled = process.env.MMWXC_RUN_USER_AUDIT === "1";

// Compile the real, unexported card in isolation. API calls and the page's
// effects are deliberately excluded; no product export is added for this audit.
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

test("audit UA-D03 official inactive user must offer enable rather than display enabled", {
  skip: enabled ? false : "audit: UA-D03",
}, () => {
  const html = renderUserCard({ username: "alice", role: "user", is_active: false });
  assert.doesNotMatch(html, />已启用</, "officially disabled account is incorrectly labelled enabled");
  assert.match(html, />启用<\/button>/, "the account needs an enable action");
});

test("audit control: delete_partial hides access, admin hides destructive actions", {
  skip: enabled ? false : "audit: S7 controls",
}, () => {
  const partial = renderUserCard({ username: "alice", role: "user", is_active: true }, {
    effective_state: "delete_partial", operation: "delete", pending_count: 1,
  });
  assert.match(partial, /删除未完成/);
  assert.doesNotMatch(partial, />(启用|禁用)<\/button>/);
  const admin = renderUserCard({ username: "admin", role: "admin", is_active: true });
  assert.doesNotMatch(admin, />(启用|禁用|重置密码|重置流量)<\/button>/);
  assert.doesNotMatch(admin, /<b>删除<\/b>/);
});

test("audit UA-D06 saving an existing permanent package must not invent an expiry", {
  skip: enabled ? false : "audit: UA-D06",
}, async () => {
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
  const element = PackageDialog({
    token: "audit-session", user: { username: "alice", package_id: 1, package_end_date: null },
    packages: [{ id: 1, name: "permanent" }], onClose: () => undefined, onSaved: async () => undefined,
  });
  element.props.footer.props.children[1].props.onClick();
  await new Promise((resolve) => setImmediate(resolve));
  assert.ok(saved, "save must call the package API");
  assert.ok(!saved.expire_date, `permanent assignment received finite expiry ${saved.expire_date}`);
});

test("audit UA-D07 package counts and filters include additional assignments", {
  skip: enabled ? false : "audit: UA-D07",
}, () => {
  const require = createRequire(new URL("../../frontend/package.json", import.meta.url));
  const ts = require("typescript");
  const source = readFileSync(new URL("../../frontend/src/user-manager.tsx", import.meta.url), "utf8");
  const file = ts.createSourceFile("user-manager.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const page = file.statements.find((node: any) => ts.isFunctionDeclaration(node) && node.name?.text === "UserManagementPage");
  const declarations = page.body.statements.filter((node: any) => ts.isVariableStatement(node))
    .flatMap((node: any) => node.declarationList.declarations);
  const users = [{ username: "alice", role: "user", package_id: 1, assignment_package_ids: [1, 2] }];
  const packages = [{ id: 1, name: "primary" }, { id: 2, name: "additional" }];
  const compute = (name: string) => {
    const declaration = declarations.find((node: any) => node.name.text === name);
    const callback = declaration.initializer.arguments[0];
    const compiled = ts.transpileModule(`const compute = ${callback.getText(file)};`, {
      compilerOptions: { target: ts.ScriptTarget.ES2020 },
    }).outputText;
    return new Function("users", "packages", "query", "packageFilter", `${compiled}\nreturn compute();`)(users, packages, "", "2");
  };
  assert.deepEqual({ count: compute("counts").get("2"), visible: compute("visible").map((user: { username: string }) => user.username) }, {
    count: 1, visible: ["alice"],
  }, "additional package must count and display its assigned user");
});
