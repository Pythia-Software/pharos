#!/usr/bin/env node
import { existsSync, realpathSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawn } from 'node:child_process';
const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const children = [];
let stopping = false;
function stop(code = 0) {
  if (stopping) return;
  stopping = true;
  for (const child of children) child.kill('SIGTERM');
  process.exitCode = code;
}
function start(args, cwd) {
  const child = spawn('npm', args, { cwd, stdio: 'inherit' });
  children.push(child);
  child.on('error', error => { console.error(error); stop(1); });
  child.on('exit', code => { if (!stopping) stop(code ?? 1); });
}
if (existsSync(`${root}/.context/query-table`)) {
  const source = realpathSync(`${root}/.context/query-table`);
  // Keep dist available while sibling packages and Pharos watch its imports.
  // The link script performs the initial clean build.
  for (const name of ['core', 'react', 'ui', 'codegen'])
    start(['run', 'build', '--workspace', `@pythia-software/query-table-${name}`, '--', '--watch', '--no-clean'], source);
}
start(['run', 'watch'], `${root}/web`);
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => stop());
