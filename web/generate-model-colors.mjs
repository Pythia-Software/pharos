// Class-level design rules are authored; per-model colors are generated.
import { readFileSync, writeFileSync, renameSync, rmSync, mkdirSync } from 'node:fs';
import { dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { build } from 'esbuild';

const root = new URL('../', import.meta.url);
const catalogPath = fileURLToPath(new URL('pricing/model_colors.json', root));
const readJSON = path => JSON.parse(readFileSync(new URL(path, root), 'utf8'));
const escape = text => String(text).replace(/[&<>"']/g, char => ({'&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;'}[char]));

export function colorPreview(catalog, dates, warnings) {
  const providers = Object.entries(catalog.providers).map(([provider, brand]) => `<section><h2>${escape(brand.label)}</h2>${catalog.class_order[provider].map(family => {
    const names = Object.keys(catalog.models).filter(name => catalog.models[name].provider === provider && catalog.models[name].class === family)
      .sort((a, b) => (dates.get(a) ?? '9999').localeCompare(dates.get(b) ?? '9999') || a.localeCompare(b));
    return `<h3>${escape(family)}</h3><div class="models">${names.map(name => {
      const color = catalog.models[name].color;
      return `<article><div class="light"><i style="background:${color}"></i></div><div class="dark"><i style="background:${color}"></i></div><strong>${escape(name)}</strong><small>${escape(dates.get(name) ?? 'Date unavailable')} · ${color}</small></article>`;
    }).join('')}</div>`;
  }).join('')}</section>`).join('');
  return `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Generated model colors</title><style>body{font:15px system-ui;background:#f5f6f8;color:#17212f;margin:32px}h2{border-top:1px solid #ccd3dc;padding-top:20px}h3{margin-bottom:12px}.models{display:flex;flex-wrap:wrap;gap:16px}article{width:210px;overflow-wrap:anywhere}strong,small{display:block;margin:6px 0}small{color:#536172}.light,.dark{padding:12px;background:#fff}.dark{background:#171b23}i{display:block;height:32px;border-radius:4px}aside{color:#754b10}</style><h1>Generated model colors</h1><p>All ${Object.keys(catalog.models).length} catalog entries. Older → newer within each class; undated entries follow. Each swatch is shown on light and dark backgrounds.</p>${warnings.map(warning => `<aside>${escape(warning)}</aside>`).join('')}${providers}\n`;
}

export async function updateModelColors({check = false, preview = false} = {}) {
  const bundle = await build({ absWorkingDir: fileURLToPath(new URL('./', import.meta.url)), entryPoints: ['src/model-color-generation.ts'], bundle: true, write: false, platform: 'node', format: 'esm', logLevel: 'warning' });
  const { generateModelColors, modelCatalogDates } = await import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString('base64')}`);
  const prices = readJSON('pricing/cost_changes.json');
  const {catalog, warnings} = generateModelColors(readJSON('pricing/model_colors.json'), prices.changes, readJSON('pricing/model_color_rules.json'));
  const current = readFileSync(catalogPath, 'utf8'), next = `${JSON.stringify(catalog, null, 2)}\n`;
  if (check && current !== next) throw new Error('Model colors are stale; run `npm --prefix web run generate-model-colors`.');
  if (!check && current !== next) {
    const temporary = `${catalogPath}.${process.pid}.tmp`;
    try { writeFileSync(temporary, next); renameSync(temporary, catalogPath); }
    finally { rmSync(temporary, {force: true}); }
  }
  for (const warning of warnings) console.warn(`Model colors: ${warning}`);
  if (preview) {
    const target = fileURLToPath(new URL('.context/model-colors.html', root));
    mkdirSync(dirname(target), {recursive: true});
    writeFileSync(target, colorPreview(catalog, modelCatalogDates(prices.changes), warnings));
    console.log(`Palette preview: ${target}`);
  }
  return Object.keys(catalog.models).length;
}
if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  try {
    if (process.argv.slice(2).some(flag => !['--check', '--preview'].includes(flag))) throw new Error('Usage: node generate-model-colors.mjs [--check | --preview]');
    if (process.argv.includes('--check') && process.argv.includes('--preview')) throw new Error('--check does not write a preview');
    const count = await updateModelColors({check: process.argv.includes('--check'), preview: process.argv.includes('--preview')});
    console.log(`Model colors: ${count} catalog entries ${process.argv.includes('--check') ? 'are current' : 'generated'}`);
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
