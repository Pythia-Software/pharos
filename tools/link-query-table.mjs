#!/usr/bin/env node
// Worktree-local links; npm/package-lock and the repository's go.mod stay portable.
import { existsSync, mkdirSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { execFileSync } from 'node:child_process';
const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const source = resolve(process.argv[2] ?? '/Users/gbw/conductor/workspaces/query-table-v1/miami');
const run = (args, cwd) => execFileSync('npm', args, { cwd, stdio: 'inherit' });
if (!existsSync(`${source}/backends/go/go.mod`)) throw Error(`Not a query-table checkout: ${source}`);
if (!existsSync(`${source}/node_modules`)) run(['ci', '--no-audit', '--no-fund'], source);
run(['run', 'build:packages'], source);
if (!existsSync(`${root}/web/node_modules`)) run(['ci', '--no-audit', '--no-fund'], `${root}/web`);
// Install external runtime dependencies of the new packages without changing the pinned release.
const dependencies = {};
for (const path of ['packages/core', 'packages/react', 'packages/ui']) {
  const pkg = JSON.parse(readFileSync(`${source}/${path}/package.json`, 'utf8'));
  for (const [name, version] of Object.entries(pkg.dependencies ?? {}))
    if (!name.startsWith('@pythia-software/')) dependencies[name] = version;
}
run(['install', '--no-save', '--package-lock=false', '--no-audit', '--no-fund', ...Object.entries(dependencies).map(([name, version]) => `${name}@${version}`)], `${root}/web`);
mkdirSync(`${root}/.context`, { recursive: true });
const link = (target, path) => { rmSync(path, { recursive: true, force: true }); symlinkSync(target, path, 'dir'); };
link(source, `${root}/.context/query-table`);
for (const [name, path] of Object.entries({ core: 'packages/core', react: 'packages/react', ui: 'packages/ui', codegen: 'tools/schema-codegen' }))
  link(`${source}/${path}`, `${root}/web/node_modules/@pythia-software/query-table-${name}`);
writeFileSync(`${root}/go.work`, 'go 1.26.0\n\nuse .\n\nreplace github.com/Pythia-Software/query-table/backends/go => ./.context/query-table/backends/go\n');
console.log(`Linked Pharos to ${source}. Use npm run watch:local in web to rebuild both worktrees.`);
