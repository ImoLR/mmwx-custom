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
