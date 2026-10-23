// Keep Ask Pharos destinations on the same URL builders as the card library.
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { build } from 'esbuild';
process.env.TZ = 'UTC';
const web = fileURLToPath(new URL('./', import.meta.url));
const target = fileURLToPath(new URL('../internal/archive/assets/ask-destinations.json', import.meta.url));
const bundle = await build({ absWorkingDir: web, entryPoints: ['src/curiosity-data.ts'], bundle: true, write: false, platform: 'node', format: 'esm', loader: { '.css': 'empty' }, logLevel: 'warning' });
globalThis.location = { origin: 'http://localhost' };
const { queryRecipes, instantiateRecipe } = await import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString('base64')}`);
const destinations = queryRecipes.map(recipe => {
  const card = instantiateRecipe(recipe, 7, new Date('2026-01-01T12:00:00Z'));
  return { id: recipe.id, title: card.title, description: card.description, kind: 'Analyze', href: card.url };
});
const next = JSON.stringify(destinations, null, 2) + '\n';
if (process.argv.includes('--check')) {
  if (readFileSync(target, 'utf8') !== next) throw new Error('Ask Pharos destinations are stale; run npm --prefix web run generate-ask-destinations');
} else writeFileSync(target, next);
