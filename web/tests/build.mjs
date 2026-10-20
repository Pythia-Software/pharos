// Bundles every unit test in one esbuild call. Tests import web/src TypeScript
// and JSON directly; packages stay external, so tests and the code they test
// share one Query Table core from web/node_modules. Output goes under
// web/.test-build (gitignored, and outside node_modules, which `npm ci` wipes).
//
//   node web/tests/build.mjs     builds into web/.test-build/unit
import { build } from "esbuild";
import { readdirSync, rmSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

export const web = fileURLToPath(new URL("..", import.meta.url));
export const unitSource = join(web, "tests", "unit");
export const buildRoot = join(web, ".test-build");

const testFile = /\.test\.m?[jt]sx?$/;

export function unitEntries() {
  return readdirSync(unitSource, { recursive: true }).filter(file => testFile.test(file)).sort().map(file => join(unitSource, file));
}

// CSS side-effect imports (including package CSS) have no meaning in node.
const emptyCSS = {
  name: "empty-css",
  setup(build) {
    build.onResolve({ filter: /\.css$/ }, args => ({ path: args.path, namespace: "empty-css" }));
    build.onLoad({ filter: /.*/, namespace: "empty-css" }, () => ({ contents: "", loader: "js" }));
  },
};

// Builds into `outdir` (which must sit under web/ so packages resolve from
// web/node_modules) and returns the built test files in source order.
export async function buildUnitTests(outdir = join(buildRoot, "unit")) {
  const entries = unitEntries();
  rmSync(outdir, { recursive: true, force: true });
  await build({
    absWorkingDir: web,
    entryPoints: entries,
    outdir,
    outbase: unitSource,
    outExtension: { ".js": ".mjs" },
    bundle: true,
    splitting: true,
    format: "esm",
    platform: "node",
    target: "node24",
    packages: "external",
    plugins: [emptyCSS],
    sourcemap: "inline",
    logLevel: "warning",
  });
  return entries.map(entry => join(outdir, relative(unitSource, entry)).replace(/\.m?[jt]sx?$/, ".mjs"));
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const built = await buildUnitTests();
  console.log(`Built ${built.length} unit test files into ${relative(process.cwd(), join(buildRoot, "unit"))}`);
}
