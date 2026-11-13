// A loopback-only, standalone experiment; no Pharos service or library needed.
import { mkdir, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { context } from "../web/node_modules/esbuild/lib/main.js";

if (Number(process.versions.node.split(".")[0]) < 24) throw new Error("Use Node 24 (nvm use), then rerun node tools/model-color-lab.mjs.");
const args = process.argv.slice(2);
if (args.length && (args.length !== 2 || args[0] !== "--port" || !/^\d+$/.test(args[1]) || Number(args[1]) > 65535)) throw new Error("Usage: node tools/model-color-lab.mjs [--port 18790] (0 selects a free port)");
const root = fileURLToPath(new URL("../", import.meta.url));
const directory = fileURLToPath(new URL("../.context/model-color-lab/", import.meta.url));
await mkdir(directory, { recursive: true });
await writeFile(`${directory}/index.html`, '<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Pharos · Model color lab</title><link rel="icon" href="data:,"><link rel="stylesheet" href="/lab.css"></head><body><div id="root"></div><script type="module" src="/lab.js"></script></body></html>');
const build = await context({ absWorkingDir: root, entryPoints: { lab: "web/experiments/model-color-lab/lab.tsx" }, outdir: directory, bundle: true, format: "esm", target: "es2022", jsx: "automatic", sourcemap: true, logLevel: "info" });
await build.watch();
const server = await build.serve({ host: "127.0.0.1", port: Number(args[1] ?? 18790), servedir: directory });
console.log(`Model color lab: http://127.0.0.1:${server.port}\nLocal experiment; changes never write to your Pharos library. Source changes rebuild automatically; refresh to see them.`);
for (const signal of ["SIGTERM", "SIGINT"]) process.once(signal, async () => { await build.dispose(); process.exit(0); });
