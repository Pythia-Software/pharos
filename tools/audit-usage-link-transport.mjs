#!/usr/bin/env node
// Reconcile flat dashboard drilldowns with the records transport the UI uses.
// Grouped links retain the browser/V3 evidence in the selected main runs.
import { readFileSync, appendFileSync, mkdirSync, existsSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { resolve, join } from 'node:path';
import { createHash } from 'node:crypto';
const require = createRequire(new URL('../web/package.json', import.meta.url));
const core = require('@pythia-software/query-table-core');
const args = process.argv.slice(2);
const option = (name, fallback) => { const index = args.indexOf(`--${name}`); return index < 0 ? fallback : args[index + 1]; };
const runs = option('runs', 'audit-01,audit-corrections,audit-controls,audit-time-coverage').split(',');
const folder = resolve(option('output', '.context/usage-audit/link-transport'));
mkdirSync(folder, { recursive: true });
const stable = value => JSON.stringify(value, (_key, v) => v && typeof v === 'object' && !Array.isArray(v) ? Object.fromEntries(Object.entries(v).sort(([a], [b]) => a.localeCompare(b))) : v);
const selected = new Map();
let origin;
for (const run of runs) {
  const base = resolve('.context/usage-audit', run);
  origin ??= JSON.parse(readFileSync(join(base, 'manifest.json'), 'utf8')).origin;
  for (const line of readFileSync(join(base, 'results.jsonl'), 'utf8').split('\n').filter(Boolean)) {
    const row = JSON.parse(line); selected.set(row.id, row);
  }
}
const queries = new Map();
if (args.includes('--link-sources')) for (const line of readFileSync(resolve(option('link-sources')), 'utf8').split('\n').filter(Boolean)) {
  const source = JSON.parse(line), original = selected.get(source.id);
  if (!original || original.url !== source.url) throw new Error(`${source.id}: supplemental link source does not match the saved view URL`);
  original.linkTargets = source.linkTargets;
}
for (const row of selected.values()) for (const link of row.linkTargets ?? []) {
  if (!/records/i.test(link.label)) continue;
  const url = new URL(link.href, origin), dataset = url.searchParams.get('analyze_data');
  const query = core.decodeQueryStrict(url.searchParams.get(`q_${dataset}`) ?? '');
  if (!query || query.grouping) throw new Error(`${row.id}: flat drilldown has no ordinary query`);
  const params = new URLSearchParams();
  if (dataset === 'usage' && url.searchParams.has('analyze_carbon')) params.set('carbon_settings', url.searchParams.get('analyze_carbon'));
  const signature = stable([dataset, query, params.toString()]);
  const entry = queries.get(signature) ?? { signature, dataset, query, params: params.toString(), references: [] };
  entry.references.push({ id: row.id, label: link.label }); queries.set(signature, entry);
}
const config = option('config', '/Volumes/euclid/pharos-dev-riyadh-v18/library.toml');
const token = JSON.parse(readFileSync(config, 'utf8').match(/^api_token\s*=\s*(.+)$/m)[1]);
const api = async (path, body) => {
  const response = await fetch(origin + path, { method: body ? 'POST' : 'GET',
    headers: { Authorization: `Bearer ${token}`, Cookie: `pharos_dev_token_${new URL(origin).port}=${encodeURIComponent(token)}`, ...(body ? { 'Content-Type': 'application/json' } : {}) },
    body: body ? JSON.stringify(body) : undefined, signal: AbortSignal.timeout(30000) });
  const payload = await response.json();
  if (!response.ok) throw new Error(`HTTP ${response.status}: ${payload.error ?? 'Query failed'}`);
  return payload;
};
const file = join(folder, 'results.jsonl');
const previous = existsSync(file) ? readFileSync(file, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse) : [];
const completed = new Set(previous.map(row => row.signature));
const handshakes = new Map();
writeFileSync(join(folder, 'manifest.json'), JSON.stringify({ runs, origin, queries: [...queries.values()], sourceHash: createHash('sha256').update(readFileSync('internal/archive/assets/query-tables.js')).digest('hex') }, null, 2));
let index = 0;
for (const entry of queries.values()) {
  index++;
  if (completed.has(entry.signature)) continue;
  const { dataset, query } = entry;
  if (!handshakes.has(dataset)) handshakes.set(dataset, await api(`/api/query/${dataset}/capabilities`));
  const schema = core.loadSchema(JSON.parse(readFileSync(`schemas/${dataset}.schema.json`, 'utf8')));
  const request = core.toServerQueryV2(query, schema, handshakes.get(dataset).computedExecution);
  // The host applies its short exact-search budget whenever these fields
  // occur in a predicate, independent of the search panel being open.
  const budgetFields = dataset === 'tool_calls' ? ['command', 'file_path'] : dataset === 'writing_messages' ? ['text'] : dataset === 'mcp_calls' ? ['arguments_json', 'error_text'] : [];
  const budgeted = request.where.some(term => ('any' in term ? term.any : [term]).some(clause => budgetFields.includes(clause.field)));
  const params = new URLSearchParams(entry.params);
  if (budgeted) params.set('interactive_search', '1');
  const path = `/api/query/${dataset}/rows-v2${params.size ? `?${params}` : ''}`;
  const started = performance.now();
  const result = { ...entry, path, ok: false, ms: 0 };
  try {
    const response = await api(path, request);
    if (!Array.isArray(response.rows) || !Number.isFinite(response.total)) throw new Error('No completed rows/count response');
    result.ok = true; result.total = response.total;
  } catch (error) { result.error = String(error.message).replaceAll(token, '[credential removed]'); }
  result.ms = performance.now() - started;
  appendFileSync(file, JSON.stringify(result) + '\n');
  console.log(`${index}/${queries.size} ${dataset} ${result.ok ? 'OK' : 'ERROR'} ${(result.ms / 1000).toFixed(2)}s`);
}
// Resuming can add another referrer for an already measured query. Refresh
// those references while preserving the original execution and timing.
const reconciled = readFileSync(file, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse)
  .filter(row => queries.has(row.signature)).map(row => ({ ...row, references: queries.get(row.signature).references }));
writeFileSync(file, reconciled.map(row => JSON.stringify(row)).join('\n') + '\n');
