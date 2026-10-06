import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import test from "node:test";
import ts from "typescript";

test("every subscription client resolves to its official icon asset", () => {
  const sourceURL = new URL("../src/user-manager.tsx", import.meta.url);
  const source = ts.createSourceFile("user-manager.tsx", readFileSync(sourceURL, "utf8"), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const imports = new Map<string, string>();
  const actual: Record<string, string> = {};
  for (const statement of source.statements) {
    if (ts.isImportDeclaration(statement) && statement.importClause?.name && ts.isStringLiteral(statement.moduleSpecifier)) {
      imports.set(statement.importClause.name.text, statement.moduleSpecifier.text);
    }
    if (!ts.isVariableStatement(statement)) continue;
    for (const declaration of statement.declarationList.declarations) {
      if (declaration.name.getText(source) !== "clients") continue;
      const initializer = declaration.initializer;
      assert.ok(initializer && ts.isAsExpression(initializer) && ts.isArrayLiteralExpression(initializer.expression));
      for (const entry of initializer.expression.elements) {
        assert.ok(ts.isArrayLiteralExpression(entry));
        assert.equal(entry.elements.length, 3, "each client needs a type, name and icon");
        const [type, name, icon] = entry.elements;
        assert.ok(ts.isStringLiteral(type) && ts.isStringLiteral(name) && ts.isIdentifier(icon));
        assert.ok(name.text);
        assert.ok(!Object.hasOwn(actual, type.text), `duplicate client: ${type.text}`);
        const asset = imports.get(icon.text);
        assert.ok(asset?.startsWith("./assets/client-icons/"), `missing icon import: ${type.text}`);
        assert.ok(existsSync(new URL(asset, sourceURL)), `missing asset: ${asset}`);
        actual[type.text] = asset.split("/").at(-1)!;
      }
    }
  }
  assert.deepEqual(actual, {
    auto: "auto.svg",
    clash: "clash_color.png",
    stash: "stash_color.png",
    shadowrocket: "shadowrocket_color.png",
    "clash-to-shadowrocket": "shadowrocket_color.png",
    surfboard: "surfboard_color.png",
    surge: "surge_color.png",
    surgemac: "surgeformac_icon_color.png",
    "clash-to-surge": "surge_color.png",
    loon: "loon_color.png",
    "clash-to-loon": "loon_color.png",
    "clash-to-loon-kelee": "loon_color.png",
    qx: "quanx_color.png",
    egern: "egern_color.png",
    "sing-box": "sing-box_color.png",
    v2ray: "v2ray_color.png",
    uri: "uri.svg",
  });
});
