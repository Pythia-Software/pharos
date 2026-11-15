import { test } from 'node:test';
import assert from 'node:assert/strict';
import { effectiveProjectColor, fitProjectColor, installProjectColors, loadProjectColors, noProjectColor, projectColorAliases, projectColors, projectMetricTheme, projectSeriesColor, savedProjectColor, saveProjectColor } from '../../../src/project-colors';
import { sortProjectColors } from '../../../src/project-color-order';
import { chartMetricTheme, chartSeriesStyle, keyColorTheme, metricSeriesColor } from '../../../src/chart-colors';
import { ribbonCover } from '../../../src/parallelism-ribbon-card';
import { normalizeConcurrencySettings } from '../../../src/concurrency-settings';

const project = (id = 'app', name = 'app', custom_color) => ({ id: `repository:${id}`, key: id === 'app' ? 'a'.repeat(64) : 'b'.repeat(64), name, kind: 'repository', default_color: '#2a83a4', source: 'public/favicon.svg', ...(custom_color ? { custom_color } : {}) });

test('project scopes share one saved identity, with an explicit user choice taking precedence', t => {
  installProjectColors([project('app', 'app', '#bb6622')]); t.after(() => installProjectColors([]));
  const expected = savedProjectColor('repository:app');
  for (const field of ['project_name', 'root_project_name', 'repository_name', 'root_repository_name']) assert.equal(chartSeriesStyle(field, 'app').color, expected);
  for (const field of ['project_id', 'root_project_id']) assert.equal(chartSeriesStyle(field, 'repository:app').color, expected);
  assert.equal(chartSeriesStyle('repository_id', 'app').color, expected);
  assert.equal(effectiveProjectColor(project('app', 'app', '#bb6622')), '#bb6622');
  assert.equal(effectiveProjectColor(projectColors()[0]), '#bb6622');
  assert.equal(effectiveProjectColor(project('app', 'app', 'invalid')), '#2a83a4');
  assert.equal(metricSeriesColor(['repository_name'], ['app']), expected);
  assert.equal(keyColorTheme(['repository_name'], [{ keys: ['app'] }]).dimensions.repository_name.overrides[0].color, expected);
  assert.equal(chartMetricTheme().dimensions.project_name.overrides.find(item => item.value === 'app').color, expected);
  assert.equal(metricSeriesColor(['repository_name', 'model_family'], ['app', 'opus-5-5']), chartSeriesStyle('model_family', 'opus-5-5').color);
});

test('projects without a saved assignment use the chart and metric fallbacks', t => {
  installProjectColors([project()]); t.after(() => installProjectColors([]));
  assert.equal(savedProjectColor('small-project'), undefined);
  assert.match(projectSeriesColor('small-project', '#123456'), /, #123456\)$/);
  for (const field of ['project_name', 'project_id', 'repository_name']) {
    assert.equal(projectMetricTheme().dimensions[field].overrides.some(item => item.value === 'small-project'), false);
  }
});

test('stable IDs stay separate for a shared name and resolve after renaming', t => {
  const projects = [project(), project('other', 'app', '#ab34cd')];
  installProjectColors(projects); t.after(() => installProjectColors([]));
  assert.notEqual(savedProjectColor('repository:app'), savedProjectColor('repository:other'));
  assert.equal(savedProjectColor(''), noProjectColor);
  const before = savedProjectColor('repository:app');
  installProjectColors([{ ...project(), name: 'renamed' }]);
  assert.equal(savedProjectColor('renamed'), before);
  assert.equal(savedProjectColor('app'), before, 'raw repository ID still resolves');
});

test('a shared name takes the color of the project with the most conversations, ties to the lower ID', t => {
  const main = { ...project('main', 'app'), key: 'c'.repeat(64), conversations: 13176 };
  const clone = { ...project('other', 'app'), conversations: 4 };
  assert.equal(projectColorAliases([clone, main]).get('app').id, main.id);
  assert.equal(projectColorAliases([main, { ...clone, conversations: 9000 }]).get('app').id, main.id, 'any lead decides');
  assert.equal(projectColorAliases([{ ...main, conversations: 4 }, clone]).get('app').id, main.id, 'tie goes to the lower ID');
  assert.equal(projectColorAliases([clone, { ...main, conversations: 4 }]).get('app').id, main.id, 'regardless of order');
  assert.equal(projectColorAliases([{ ...main, conversations: undefined }, clone]).get('app').id, clone.id, 'unknown counts as none');
  installProjectColors([clone, main]); t.after(() => installProjectColors([]));
  assert.equal(savedProjectColor('app'), savedProjectColor('repository:main'));
  assert.equal(chartSeriesStyle('project_name', 'app').color, savedProjectColor('repository:main'));
  assert.notEqual(savedProjectColor('repository:other'), savedProjectColor('repository:main'));
});

test('saved marks keep the same CSS identity when a color is edited or reset', t => {
  const properties = new Map();
  const original = globalThis.document;
  globalThis.document = { documentElement: { style: { setProperty: (key, value) => properties.set(key, value), removeProperty: key => properties.delete(key) } } };
  installProjectColors([project()]); t.after(() => { installProjectColors([]); if (original === undefined) delete globalThis.document; else globalThis.document = original; });
  const before = savedProjectColor('app');
  installProjectColors([project('app', 'app', '#bb6622')]);
  assert.equal(savedProjectColor('app'), before);
  assert.equal(properties.get(`--project-color-${'a'.repeat(64)}`), '#bb6622');
  installProjectColors([project()]);
  assert.equal(properties.get(`--project-color-${'a'.repeat(64)}`), '#2a83a4');
});

test('provisional colors remain consistent across scopes and meet both theme contrast targets', () => {
  for (const name of ['new project', 'constructor', '__proto__', 'repo/日本語']) {
    assert.equal(chartSeriesStyle('project_name', name).color, chartSeriesStyle('repository_name', name).color);
    assert.match(projectSeriesColor(name, '#123456'), /^var\(--project-value-/);
  }
  const luminance = color => color.slice(1).match(/../g).map(c => parseInt(c, 16) / 255).map(c => c <= .04045 ? c / 12.92 : ((c + .055) / 1.055) ** 2.4).reduce((sum, c, i) => sum + c * [.2126, .7152, .0722][i], 0);
  for (const original of ['#000000', '#ffffff', '#ff0000', '#00ff00', '#0000ff', '#ab12cd']) {
    const color = fitProjectColor(original);
    for (const bg of ['#eee7d8', '#fffaf0', '#183529', '#10241b']) assert.ok((Math.max(luminance(color), luminance(bg)) + .05) / (Math.min(luminance(color), luminance(bg)) + .05) >= 3, `${color} / ${bg}`);
  }
});

test('parallelism card colors agree with project charts despite filtering and extra projects', t => {
  installProjectColors([project()]); t.after(() => installProjectColors([]));
  const data = { generated_at: '2026-11-10T12:00:00Z', timezone: 'UTC', period_start: '2026-11-10', period_end: '2026-11-10', days: ['2026-11-10'], threads: [
    { id: 'a', workspace_id: 'a', project: 'app', agent_depth: 0, turns: [[Date.parse('2026-11-10T10:00:00Z'), Date.parse('2026-11-10T11:00:00Z')]], model_spans: [] },
  ] };
  const settings = normalizeConcurrencySettings({ range: 'all', mode: 'user' });
  const before = ribbonCover(data, settings).series[0].color;
  assert.equal(before, chartSeriesStyle('project_name', 'app').color);
  const expanded = { ...data, threads: [{ ...data.threads[0], id: 'b', workspace_id: 'b', project: 'aaa' }, ...data.threads] };
  for (const repository of ['', 'app']) assert.equal(ribbonCover(expanded, { ...settings, repository }).series.find(item => item.value === 'app').color, before);
});

test('failed saves retain the displayed palette and successful saves update it', async t => {
  installProjectColors([project()]); t.after(() => installProjectColors([]));
  t.mock.method(globalThis, 'fetch', async () => ({ ok: false }));
  await assert.rejects(saveProjectColor('repository:app', '#bb6622'), /could not be saved/);
  assert.equal(effectiveProjectColor(projectColors()[0]), '#2a83a4');
  t.mock.method(globalThis, 'fetch', async (_url, options) => {
    assert.deepEqual(JSON.parse(options.body), { id: 'repository:app', color: '#bb6622' });
    return { ok: true, json: async () => [project('app', 'app', '#bb6622')] };
  });
  const result = await saveProjectColor('repository:app', '#bb6622');
  assert.equal(effectiveProjectColor(result[0]), '#bb6622');
  assert.equal(projectMetricTheme().dimensions.repository_name.overrides.find(item => item.value === 'app').color, savedProjectColor('app'));
});

test('a palette read started before an edit cannot restore the old color', async t => {
  installProjectColors([project()]); t.after(() => installProjectColors([]));
  const oldRead = Promise.withResolvers();
  let reads = 0;
  t.mock.method(globalThis, 'fetch', async (_url, options) => {
    if (!options) { reads++; return oldRead.promise; }
    return { ok: true, json: async () => [project('app', 'app', '#bb6622')] };
  });
  const pending = loadProjectColors();
  assert.equal(loadProjectColors(), pending);
  await saveProjectColor('repository:app', '#bb6622');
  oldRead.resolve({ ok: true, json: async () => [project()] });
  await pending;
  assert.equal(reads, 1);
  assert.equal(effectiveProjectColor(projectColors()[0]), '#bb6622');
});

test('project color settings list the most-used projects first, or the most recent, with name ties', () => {
  const rows = [
    { ...project('app', 'beta'), conversations: 5, latest_conversation_at: '2026-03-01T00:00:00Z' },
    { ...project('two', 'alpha'), id: 'repository:two', conversations: 40, latest_conversation_at: '2026-01-01T00:00:00Z' },
    { ...project('three', 'gamma'), id: 'repository:three', conversations: 5 },
  ];
  const names = sort => sortProjectColors(rows, sort).map(row => row.name);
  assert.deepEqual(names('conversations'), ['alpha', 'beta', 'gamma']);
  assert.deepEqual(names('latest'), ['beta', 'alpha', 'gamma']);
  assert.deepEqual(names('name'), ['alpha', 'beta', 'gamma']);
  assert.deepEqual(rows.map(row => row.name), ['beta', 'alpha', 'gamma'], 'sorting leaves the registry order alone');
});
