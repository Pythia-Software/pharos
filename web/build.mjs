import { build, context } from "esbuild";
import { readFileSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
const check = process.argv.includes("--check"), watch = process.argv.includes("--watch"), temporary = check ? mkdtempSync(join(tmpdir(), "pharos-bundles-")) : null;
const bundles = [["src/index.tsx", "../internal/archive/assets/query-tables.js"], ["src/data-guide-entry.tsx", "../internal/archive/assets/data-guide.js"], ["src/uifeedback/inbox.tsx", "../internal/uifeedback/assets/inbox.js"]];
bundles.push(["src/fingerprint-renderer.ts", "../internal/archive/assets/fingerprint-renderer.js"]);
try {
  for (const [entry, target] of bundles) {
    const output = temporary ? join(temporary, target.split("/").at(-1)) : target;
    const fingerprint = entry === "src/fingerprint-renderer.ts";
    // Goja supports ES2020; native object spread avoids costly ES2015 helpers.
    const options = { entryPoints: [entry], bundle: true, preserveSymlinks: true, minify: true, format: "iife", target: fingerprint ? "es2020" : "safari17", ...(fingerprint ? {globalName: "PharosFingerprint"} : {}), outfile: output };
    if (watch) { const watcher = await context(options); await watcher.watch(); } else await build(options);
    if (check) for (const ext of fingerprint ? ["js"] : ["js", "css"]) { const file = target.replace(/js$/, ext), actual = output.replace(/js$/, ext); if (!readFileSync(file).equals(readFileSync(actual))) throw new Error(`${file} differs from its sources; run npm --prefix web run build`); }
  }
} finally { if (temporary) rmSync(temporary, { recursive: true, force: true }); }
