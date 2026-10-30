// Writes the site map (internal/archive/assets/site-map.json) and its
// cross-language cases (testdata/site-map-cases.json) from the TypeScript
// that defines the pages: web/src/site-map.ts and site-map-cases.ts. With
// --check, fails when either file is stale. See docs/site-map.md.
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { build } from "esbuild";
import ts from "typescript";
import { publishedQueryTypes } from "./query-types.mjs";
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
const map = {...buildSiteMap(inputs),query_types:publishedQueryTypes()};
// Literal IDs are discovered from the same page sources, not a second hand
// maintained list. Runtime entity IDs remain documented target families.
const sectionOwners = {
 "card-library.tsx":["cards"], "tl1.tsx":["tl1","tl1/*"],
 "findings.tsx":["findings","findings/*"], "delegation.tsx":["work"],
 "concurrency.tsx":["analyze/parallelism"],
};
const sections={};
for(const [file,pages] of Object.entries(sectionOwners)){
 const source=readFileSync(path("src/"+file),"utf8");
 const tree=ts.createSourceFile(file,source,ts.ScriptTarget.Latest,true,ts.ScriptKind.TSX);
 const visit=node=>{
  if(ts.isJsxAttribute(node)&&node.name.getText(tree)==="id"&&node.initializer&&ts.isStringLiteral(node.initializer)){
   const id=node.initializer.text;
   // Editing dialogs and generated IDs are outside content navigation.
   if(!/dialog|template/i.test(id)){
    let scoped=pages;
    for(let ancestor=node.parent;ancestor;ancestor=ancestor.parent){
     if(ts.isConditionalExpression(ancestor)&&node.pos>=ancestor.whenTrue.pos&&node.end<=ancestor.whenTrue.end){const match=ancestor.condition.getText(tree).match(/page === "(\/tl1[^"\s]*)"/);if(match){scoped=[match[1].slice(1)];break}}
     if(ts.isFunctionDeclaration(ancestor)&&ancestor.name?.text==="Followups"){scoped=["tl1/followups"];break}
    }
    for(const name of Object.keys(map.nodes))if(scoped.some(pattern=>pattern===name||pattern.endsWith("/*")&&name.startsWith(pattern.slice(0,-1))))(sections[name]??=[]).push("section:"+id);
   }
  }
  ts.forEachChild(node,visit);
 };visit(tree);
}
const shellViews=[...ui.matchAll(/<section\b[^>]*\bid="([^"]+)"[^>]*class="view\b[^"]*"[^>]*>/g)];
for(const [index,match] of shellViews.entries()){
 const root=match[1],end=shellViews[index+1]?.index??ui.indexOf("</section>",match.index)+10;
 const ids=[...ui.slice(match.index,end).matchAll(/\bid="([^"{}]+)"/g)].map(item=>"section:"+item[1]);
 for(const [name,node] of Object.entries(map.nodes)){
  const owner=name==="work"?"detail":node.route==="/"?"home":node.route.split("/")[1];
  if(owner===root)(sections[name]??=[]).push(...ids);
 }
}
for(const name of Object.keys(sections))sections[name]=[...new Set(sections[name])].sort();
map.view_contract.sections=sections;
const files = { map: formatJSON(map, 3) + "\n", cases: formatJSON(buildSiteMapCases(map), 2) + "\n" };
if (process.argv.includes("--check")) {
  const stale = Object.keys(targets).filter(key => { try { return readFileSync(targets[key], "utf8") !== files[key]; } catch { return true; } });
  if (stale.length) throw new Error(`The site map is stale (${stale.map(key => targets[key]).join(", ")}); run npm --prefix web run generate-site-map`);
} else {
  for (const key of Object.keys(targets)) writeFileSync(targets[key], files[key]);
  console.log(`site-map.json ${files.map.length} bytes, site-map-cases.json ${files.cases.length} bytes`);
}
