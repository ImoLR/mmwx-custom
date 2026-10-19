// Pixel-compares two screenshot folders: node tests/visual/png-diff.mjs <before-dir> <after-dir> [diff-dir]
// Prints one line per PNG and exits 1 when any file differs or is missing; diff images mark changed pixels red.
import { createRequire } from "node:module";
import { readdirSync, readFileSync, writeFileSync, mkdirSync, existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const { PNG } = createRequire(path.join(root, "frontend/package.json"))("pngjs");
const [before, after, diffDir] = process.argv.slice(2);
if (!before || !after) { console.error("usage: png-diff.mjs <before-dir> <after-dir> [diff-dir]"); process.exit(2); }
if (diffDir) mkdirSync(diffDir, { recursive: true });
const names = [...new Set([...readdirSync(before), ...readdirSync(after)].filter((name) => name.endsWith(".png")))].sort();
const results = names.map((name) => {
  if (!existsSync(path.join(before, name)) || !existsSync(path.join(after, name))) return { name, status: "missing" };
  const a = PNG.sync.read(readFileSync(path.join(before, name)));
  const b = PNG.sync.read(readFileSync(path.join(after, name)));
  if (a.width !== b.width || a.height !== b.height) return { name, status: "size", before: `${a.width}x${a.height}`, after: `${b.width}x${b.height}` };
  const diff = new PNG({ width: a.width, height: a.height });
  let pixels = 0;
  for (let i = 0; i < a.data.length; i += 4) {
    const changed = a.data[i] !== b.data[i] || a.data[i + 1] !== b.data[i + 1] || a.data[i + 2] !== b.data[i + 2] || a.data[i + 3] !== b.data[i + 3];
    if (changed) pixels++;
    const gray = Math.round((a.data[i] + a.data[i + 1] + a.data[i + 2]) / 3 * 0.3 + 178);
    diff.data.set(changed ? [255, 0, 0, 255] : [gray, gray, gray, 255], i);
  }
  if (pixels && diffDir) writeFileSync(path.join(diffDir, name), PNG.sync.write(diff));
  return { name, status: pixels ? "changed" : "same", pixels, total: a.width * a.height };
});
for (const result of results) console.log(JSON.stringify(result));
const bad = results.filter((result) => result.status !== "same");
console.log(`${results.length} compared, ${results.length - bad.length} identical, ${bad.length} different`);
process.exit(bad.length ? 1 : 0);
