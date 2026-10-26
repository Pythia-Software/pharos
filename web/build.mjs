import { build, context } from "esbuild";
import { readFileSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
const check = process.argv.includes("--check"), watch = process.argv.includes("--watch"), temporary = check ? mkdtempSync(join(tmpdir(), "pharos-bundles-")) : null;
const bundles = [["src/index.tsx", "../internal/archive/assets/query-tables.js"], ["src/uifeedback/inbox.tsx", "../internal/uifeedback/assets/inbox.js"]];
try {
  for (const [entry, target] of bundles) {
    const output = temporary ? join(temporary, target.split("/").at(-1)) : target;
    const options = { entryPoints: [entry], bundle: true, preserveSymlinks: true, minify: true, format: "iife", target: "safari17", outfile: output };
    if (watch) { const watcher = await context(options); await watcher.watch(); } else await build(options);
    if (check) for (const ext of ["js", "css"]) { const file = target.replace(/js$/, ext), actual = output.replace(/js$/, ext); if (!readFileSync(file).equals(readFileSync(actual))) throw new Error(`${file} differs from its sources; run npm --prefix web run build`); }
  }
} finally { if (temporary) rmSync(temporary, { recursive: true, force: true }); }
