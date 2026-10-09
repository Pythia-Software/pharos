import { readdirSync, readFileSync, writeFileSync, mkdtempSync, rmSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { resolve, join } from 'node:path';
import { tmpdir } from 'node:os';
const root = fileURLToPath(new URL('../', import.meta.url));
const check = process.argv.includes('--check');
const temp = mkdtempSync(join(tmpdir(), 'pharos-schemas-'));
try {
  for (const name of readdirSync(resolve(root, 'schemas')).filter(name => name.endsWith('.schema.json')).sort()) {
    const source = resolve(root, 'schemas', name);
    const destination = resolve(root, 'internal/archive', `${name.replace('.schema.json', '')}_schema_generated.go`);
    const output = check ? resolve(temp, name + '.go') : destination;
    execFileSync(process.execPath, [resolve(root, 'web/node_modules/@pythia-software/query-table-codegen/dist/cli.js'), source, '--go', output, '--go-package', 'archive', '--dialect', 'sqlite']);
    execFileSync('gofmt', ['-w', output]);
    const asset = resolve(root, 'internal/archive/assets/query-schemas', name);
    if (check) {
      if (!readFileSync(output).equals(readFileSync(destination)) || !readFileSync(source).equals(readFileSync(asset))) throw Error(`Generated schema differs: ${name}`);
    } else writeFileSync(asset, readFileSync(source));
  }
} finally { rmSync(temp, { recursive: true, force: true }); }
