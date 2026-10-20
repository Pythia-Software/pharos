// Writes the shipped card recipes (queryRecipes in src/curiosity-data.ts) to
// the `implemented_recipes` key of docs/homepage-query-cards.json, which
// TestHomeQueryRecipesV2 executes against the Go service. With --check it
// only reports whether the file is current.
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { build } from 'esbuild';
const web = fileURLToPath(new URL('./', import.meta.url));
const snapshot = fileURLToPath(new URL('../docs/homepage-query-cards.json', import.meta.url));
const check = process.argv.includes('--check');
const bundle = await build({ absWorkingDir: web, entryPoints: ['src/curiosity-data.ts'], bundle: true, write: false, platform: 'node', format: 'esm', loader: { '.css': 'empty' }, logLevel: 'warning' });
const { queryRecipes } = await import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString('base64')}`);
const current = readFileSync(snapshot, 'utf8'), tracker = JSON.parse(current);
tracker.implemented_recipes = JSON.parse(JSON.stringify(queryRecipes));
const next = `${JSON.stringify(tracker, null, 2)}\n`;
if (check) {
  if (next !== current) {
    console.error('docs/homepage-query-cards.json is out of date with web/src recipes; run `npm --prefix web run generate-recipes`.');
    process.exit(1);
  }
  console.log('recipe snapshot matches web/src');
} else if (next !== current) {
  writeFileSync(snapshot, next);
  console.log(`Wrote ${queryRecipes.length} recipes to docs/homepage-query-cards.json`);
}
