// Runs the JS test tiers from any directory.
//
//   node web/tests/run.mjs [unit|browser|all] [--full] [--test-* flags] [file filters]
//
// unit (the default) bundles web/tests/unit once and runs it in node; it never
// starts a browser. browser runs the Playwright files in tests/. --full sets
// PHAROS_TEST_FULL=1, which widens the combinatorial unit tests. Filters keep
// the test files whose path contains any of them. Flags go to `node --test`
// before the file list, so `--test-name-pattern=x` works. The 10 slowest
// tests are listed at the end, and the exit status is non-zero on failure.
import { spawn } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { buildRoot, buildUnitTests, unitEntries, unitSource, web } from "./build.mjs";

const repo = join(web, "..");
const browserSource = join(repo, "tests");
const timingsReporter = fileURLToPath(new URL("./timings-reporter.mjs", import.meta.url));
const valueFlags = new Set(["--test-name-pattern", "--test-skip-pattern", "--test-reporter", "--test-reporter-destination", "--test-concurrency", "--test-timeout", "--test-shard"]);

const args = process.argv.slice(2);
const mode = ["unit", "browser", "all"].includes(args[0]) ? args.shift() : "unit";
const flags = [], filters = [];
let full = process.env.PHAROS_TEST_FULL === "1";
for (let i = 0; i < args.length; i++) {
  const arg = args[i];
  if (arg === "--full") full = true;
  else if (arg.startsWith("-")) flags.push(valueFlags.has(arg) && i + 1 < args.length ? `${arg}=${args[++i]}` : arg);
  else filters.push(arg);
}

const tiers = {
  // Each run builds into its own directory so concurrent runs cannot clobber
  // each other's bundles.
  // The full matrix runs a few thousand preview comparisons in one test.
  unit: { cwd: web, timeout: full ? 600000 : 60000, nodeFlags: ["--enable-source-maps"], testFlags: [], files: () => unitEntries().map(file => relative(unitSource, file)) },
  browser: { cwd: repo, timeout: 120000, nodeFlags: [], testFlags: ["--test-concurrency=3"], files: () => readdirSync(browserSource).filter(file => file.endsWith("-ui.test.cjs")).sort() },
};
const selected = (mode === "all" ? ["unit", "browser"] : [mode]).map(name => {
  const files = tiers[name].files();
  return { name, ...tiers[name], files: filters.length ? files.filter(file => filters.some(filter => file.includes(filter))) : files };
});
for (const filter of filters) {
  if (!selected.some(tier => tier.files.some(file => file.includes(filter)))) {
    console.error(`No ${mode === "all" ? "" : `${mode} `}test file matches "${filter}".`);
    process.exit(1);
  }
}

const has = name => flags.some(flag => flag === name || flag.startsWith(`${name}=`));
const values = name => flags.filter(flag => flag.startsWith(`${name}=`)).map(flag => flag.slice(name.length + 1));
const env = full ? { ...process.env, PHAROS_TEST_FULL: "1" } : process.env;
mkdirSync(buildRoot, { recursive: true });
const scratch = mkdtempSync(join(buildRoot, "run-"));

function run(tier, files, timingsFile) {
  // Reporters pair with destinations by position; the timings reporter is last.
  const reporters = values("--test-reporter"), destinations = values("--test-reporter-destination");
  if (!reporters.length) reporters.push("spec");
  while (destinations.length < reporters.length) destinations.push("stdout");
  const argv = [
    ...tier.nodeFlags, "--test",
    ...(has("--test-timeout") ? [] : [`--test-timeout=${tier.timeout}`]),
    ...tier.testFlags.filter(flag => !has(flag.split("=")[0])),
    ...flags.filter(flag => !flag.startsWith("--test-reporter")),
    ...reporters.map(reporter => `--test-reporter=${reporter}`), `--test-reporter=${timingsReporter}`,
    ...destinations.map(destination => `--test-reporter-destination=${destination}`), `--test-reporter-destination=${timingsFile}`,
    ...files,
  ];
  return new Promise(resolve => spawn(process.execPath, argv, { cwd: tier.cwd, env, stdio: "inherit" }).on("exit", (code, signal) => resolve(code ?? (signal ? 1 : 0))));
}

let failed = false, interrupted = false;
const timings = [];
// Ctrl-C reaches the test processes too; stop after them and clean up.
process.on("SIGINT", () => { interrupted = true; });
try {
  for (const tier of selected) {
    if (!tier.files.length || interrupted) continue;
    let files = tier.files.map(file => join(browserSource, file));
    if (tier.name === "unit") {
      const wanted = new Set(tier.files.map(file => join(unitSource, file)));
      // esbuild prints its own errors.
      const built = await buildUnitTests(join(scratch, "unit")).catch(() => undefined);
      if (!built) { failed = true; break; }
      files = unitEntries().map((entry, i) => [entry, built[i]]).filter(([entry]) => wanted.has(entry)).map(([, output]) => output);
    }
    if (selected.length > 1) console.log(`\n# ${tier.name} tests (${files.length} files)`);
    const timingsFile = join(scratch, `${tier.name}-timings.jsonl`);
    failed = (await run(tier, files, timingsFile)) !== 0 || failed;
    try { timings.push(...readFileSync(timingsFile, "utf8").split("\n").filter(Boolean).map(line => JSON.parse(line))); } catch {}
  }
  const slowest = timings.sort((a, b) => b.duration - a.duration).slice(0, 10);
  if (slowest.length) {
    console.log("\nSlowest tests:");
    for (const test of slowest) console.log(`${(test.duration / 1000).toFixed(2).padStart(8)} s  ${test.file ? `${relative(repo, test.file.startsWith("file:") ? fileURLToPath(test.file) : test.file)} › ` : ""}${test.name}`);
  }
} finally { rmSync(scratch, { recursive: true, force: true }); }
process.exitCode = interrupted ? 130 : failed ? 1 : 0;
