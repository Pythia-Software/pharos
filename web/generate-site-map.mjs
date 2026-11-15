// Writes the site map (internal/archive/assets/site-map.json), its
// cross-language cases (testdata/site-map-cases.json) and the Ask Pharos
// suggestion catalog (internal/archive/assets/ask-suggestions.json) from the
// TypeScript that defines the pages: web/src/site-map.ts, site-map-cases.ts
// and ask-suggestions.ts. They are build outputs, never committed:
// npm run build writes them when an input changed since the last run (the
// files the bundle read, which esbuild lists, and the files read here), and
// the unit tests do the same before they run. `node generate-site-map.mjs`
// rewrites them regardless. See docs/site-map.md and docs/ask-suggestions.md.
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { availableParallelism, tmpdir } from "node:os";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { Worker } from "node:worker_threads";
import { build } from "esbuild";
import ts from "typescript";
import { publishedQueryTypes } from "./query-types.mjs";
const web = fileURLToPath(new URL("./", import.meta.url)), repo = resolve(web, "..");
const path = relative => fileURLToPath(new URL(relative, import.meta.url));
const targets = { map: path("../internal/archive/assets/site-map.json"), cases: path("../testdata/site-map-cases.json"), suggestions: path("../internal/archive/assets/ask-suggestions.json") };
// What the last run read and wrote, by content hash. node_modules/.cache, so
// npm ci starts over.
const stampFile = path("node_modules/.cache/pharos-site-map.json");
// Read here rather than through the bundle, and the dependency manifests that
// decide how the bundle resolves its imports.
const sectionOwners = {
 "card-library.tsx":["cards"],
 "thinking-dashboard.tsx":["analyze/thinking-overview","analyze/thinking-detail"],
 "findings.tsx":["findings","findings/*"], "delegation.tsx":["work"], "timeline-legend.tsx":["work"],
 "concurrency.tsx":["analyze/parallelism"],
};
const directInputs = ["generate-site-map.mjs", "site-map-worker.mjs", "query-types.mjs", "package.json", "package-lock.json", "tsconfig.json",
  "../internal/archive/assets/ui.py", "../internal/archive/assets/managed-library.html",
  "node_modules/@pythia-software/query-table-core/package.json", "node_modules/@pythia-software/query-table-core/dist/index.d.ts",
  ...Object.keys(sectionOwners).map(file => "src/" + file)].map(file => path(file));

const digest = file => { try { return createHash("sha256").update(readFileSync(file)).digest("hex"); } catch { return "missing"; } };
const hashes = files => Object.fromEntries([...new Set(files)].map(file => [relative(repo, file), digest(file)]).sort(([a], [b]) => a.localeCompare(b)));
function fresh() {
  let stamp;
  try { stamp = JSON.parse(readFileSync(stampFile, "utf8")); } catch { return false; }
  if (stamp.node !== process.version) return false;
  // An import added or removed is an edit to a file read last time, so the
  // recorded list is enough to notice it.
  for (const [file, hash] of Object.entries({ ...stamp.inputs, ...stamp.outputs })) if (digest(join(repo, file)) !== hash) return false;
  return true;
}
function writeAtomically(file, text) {
  const temporary = `${file}.${process.pid}.tmp`;
  writeFileSync(temporary, text);
  renameSync(temporary, file);
}

// The three files' text and the files that went into them, rendered in UTC.
async function render() {
  const zone = process.env.TZ;
  process.env.TZ = "UTC";
  try { return await renderInUTC(); } finally { if (zone === undefined) delete process.env.TZ; else process.env.TZ = zone; }
}
async function renderInUTC() {
  // The bundle runs from a file, so a failure names its source line.
  const scratch = mkdtempSync(join(tmpdir(), "pharos-site-map-")), entry = join(scratch, "site-map.mjs");
  const bundled = await build({ absWorkingDir: web, entryPoints: ["src/site-map-cases.ts"], outfile: entry, bundle: true, sourcemap: "inline", platform: "node", format: "esm", loader: { ".css": "empty" }, logLevel: "warning", metafile: true });
  const inputs = [...Object.keys(bundled.metafile.inputs).map(file => resolve(web, file)), ...directInputs];
  globalThis.location = { origin: "http://localhost", search: "", pathname: "/" };
  let loaded;
  const computed = {}, picked = {};
  try {
    loaded = await import(pathToFileURL(entry).href);
    // Each dashboard view is checked over every combination of its options
    // (viewFacts), independently of the others: workers take them heaviest
    // first. PHAROS_SITE_MAP_WORKERS=1 works them out in this thread. The
    // result does not depend on the order or the number of workers.
    const views = loaded.siteMapViews(), count = Math.max(1, Math.min(views.length, Number(process.env.PHAROS_SITE_MAP_WORKERS) || Math.min(8, availableParallelism())));
    const keep = ({ topic, view, facts, picks }) => { (computed[topic] ??= {})[view] = facts; picked[`${topic}/${view}`] = picks; };
    if (count === 1) for (const { topic, view, label } of views) { const { facts, states } = loaded.viewFacts(topic, view, label); keep({ topic, view, facts, picks: loaded.viewCasePicks(topic, view, states) }); }
    else await new Promise((resolve, reject) => {
      const queue = [...views], workers = [];
      let pending = views.length;
      const stop = error => { for (const worker of workers) worker.terminate(); reject(error); };
      const next = worker => { const task = queue.shift(); if (task) worker.postMessage(task); };
      for (let index = 0; index < count; index++) {
        const worker = new Worker(new URL("./site-map-worker.mjs", import.meta.url), { workerData: { entry: pathToFileURL(entry).href } });
        workers.push(worker);
        worker.on("error", stop);
        worker.on("message", result => {
          if (result.error) return stop(new Error(`site map: ${result.topic} ${result.view}: ${result.error}`));
          keep(result);
          if (--pending === 0) { for (const item of workers) item.terminate(); resolve(); } else next(worker);
        });
        next(worker);
      }
    });
  } finally { rmSync(scratch, { recursive: true, force: true }); }
  const { buildAskSuggestions, buildSiteMap, buildSiteMapCases, formatJSON, settingsPages, viewTitles } = loaded;
  const ui = readFileSync(path("../internal/archive/assets/ui.py"), "utf8");
  const pages = { settings: settingsPages(ui, readFileSync(path("../internal/archive/assets/managed-library.html"), "utf8")), titles: viewTitles(ui) };
  const map = {...buildSiteMap(pages, computed),query_types:publishedQueryTypes()};
  // Literal IDs are discovered from the same page sources, not a second hand
  // maintained list. Runtime entity IDs remain documented target families.
  const sections={};
  for(const [file,owners] of Object.entries(sectionOwners)){
   const source=readFileSync(path("src/"+file),"utf8");
   const tree=ts.createSourceFile(file,source,ts.ScriptTarget.Latest,true,ts.ScriptKind.TSX);
   const visit=node=>{
    if(ts.isJsxAttribute(node)&&["id","viewKey"].includes(node.name.getText(tree))&&node.initializer&&ts.isStringLiteral(node.initializer)){
     const id=node.initializer.text;
     // Editing dialogs and generated IDs are outside content navigation.
     if(!/dialog|template/i.test(id)){
      for(const name of Object.keys(map.nodes))if(owners.some(pattern=>pattern===name||pattern.endsWith("/*")&&name.startsWith(pattern.slice(0,-1))))(sections[name]??=[]).push("section:"+id);
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
  const files = { map: formatJSON(map, 3) + "\n", cases: formatJSON(buildSiteMapCases(map, picked), 2) + "\n", suggestions: formatJSON(buildAskSuggestions(Object.keys(map.nodes)), 3) + "\n" };
  return { files, inputs };
}

// Brings the three files up to date: nothing to do when no input changed
// since they were written, unless force. check fails on a stale file instead
// of writing it. Returns whether it rendered.
export async function generateSiteMap({ force = false, check = false, quiet = false } = {}) {
  if (!force && fresh()) return false;
  const started = performance.now(), { files, inputs } = await render();
  if (check) {
    const stale = Object.keys(targets).filter(key => { try { return readFileSync(targets[key], "utf8") !== files[key]; } catch { return true; } });
    if (stale.length) throw new Error(`The site map is stale (${stale.map(key => targets[key]).join(", ")}); run npm --prefix web run build`);
  } else for (const key of Object.keys(targets)) if (!existsSync(targets[key]) || readFileSync(targets[key], "utf8") !== files[key]) writeAtomically(targets[key], files[key]);
  mkdirSync(dirname(stampFile), { recursive: true });
  writeAtomically(stampFile, JSON.stringify({ node: process.version, inputs: hashes(inputs), outputs: hashes(Object.values(targets)) }, null, 1) + "\n");
  if (!quiet) console.log(`site map: site-map.json ${files.map.length} bytes, site-map-cases.json ${files.cases.length} bytes, ask-suggestions.json ${files.suggestions.length} bytes (${((performance.now() - started) / 1000).toFixed(1)} s)`);
  return true;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await generateSiteMap({ force: true });
