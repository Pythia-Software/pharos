// Writes the site map (internal/archive/assets/site-map.json) and its
// cross-language cases (testdata/site-map-cases.json) from the TypeScript
// that defines the pages: web/src/site-map.ts and site-map-cases.ts. With
// --check, fails when either file is stale. See docs/site-map.md.
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";
process.env.TZ = "UTC";
const web = fileURLToPath(new URL("./", import.meta.url));
const path = relative => fileURLToPath(new URL(relative, import.meta.url));
const targets = { map: path("../internal/archive/assets/site-map.json"), cases: path("../testdata/site-map-cases.json") };
// The bundle runs from a file, so a failure names its source line.
const scratch = mkdtempSync(join(tmpdir(), "pharos-site-map-")), entry = join(scratch, "site-map.mjs");
await build({ absWorkingDir: web, entryPoints: ["src/site-map-cases.ts"], outfile: entry, bundle: true, sourcemap: "inline", platform: "node", format: "esm", loader: { ".css": "empty" }, logLevel: "warning" });
globalThis.location = { origin: "http://localhost", search: "", pathname: "/" };
let loaded;
try { loaded = await import(entry); } finally { rmSync(scratch, { recursive: true, force: true }); }
const { buildSiteMap, buildSiteMapCases, formatJSON, settingsPages, viewTitles } = loaded;
const ui = readFileSync(path("../internal/archive/assets/ui.py"), "utf8");
const inputs = { settings: settingsPages(ui, readFileSync(path("../internal/archive/assets/managed-library.html"), "utf8")), titles: viewTitles(ui) };
const map = buildSiteMap(inputs);
const files = { map: formatJSON(map, 3) + "\n", cases: formatJSON(buildSiteMapCases(map), 2) + "\n" };
if (process.argv.includes("--check")) {
  const stale = Object.keys(targets).filter(key => { try { return readFileSync(targets[key], "utf8") !== files[key]; } catch { return true; } });
  if (stale.length) throw new Error(`The site map is stale (${stale.map(key => targets[key]).join(", ")}); run npm --prefix web run generate-site-map`);
} else {
  for (const key of Object.keys(targets)) writeFileSync(targets[key], files[key]);
  console.log(`site-map.json ${files.map.length} bytes, site-map-cases.json ${files.cases.length} bytes`);
}
