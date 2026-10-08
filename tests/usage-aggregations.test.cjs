const { test } = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const { buildSync } = require('../web/node_modules/esbuild');
const { outputFiles } = buildSync({ stdin: { contents: 'export { applyUsageAggregations } from "./usage-aggregations"; export { loadSchema } from "@pythia-software/query-table-core";', resolveDir: path.join(__dirname, '../web/src') }, bundle: true, platform: 'node', format: 'cjs', write: false });
const loaded = { exports: {} };
new Function('module', 'exports', outputFiles[0].text)(loaded, loaded.exports);
const { applyUsageAggregations, loadSchema } = loaded.exports;
const schema = loadSchema(require('../schemas/usage.schema.json'));
const daily = [
  { id: 'session|2026-09-20|model', agent_session_id: 'session', day: '2026-09-20', model: 'model', total_tokens: 1020, first_usage_at: '2026-09-20T23:00:00Z' },
  { id: 'session|2026-09-21|model', agent_session_id: 'session', day: '2026-09-21', model: 'model', total_tokens: 2130, first_usage_at: '2026-09-21T01:00:00Z' },
];
const hourly = [
  { ...daily[0], hour: '2026-09-20T23:00:00Z', total_tokens: 1020 },
  { ...daily[1], hour: '2026-09-21T01:00:00Z', total_tokens: 2030 },
  { ...daily[1], hour: '2026-09-21T03:00:00Z', first_usage_at: '2026-09-21T03:00:00Z', total_tokens: 100 },
];
const dailyMetrics = [
  { id: 'days', op: 'count', groupBy: ['model'] },
  { id: 'avg', op: 'avg', field: 'total_tokens', groupBy: [] },
  { id: 'sum', op: 'sum', field: 'total_tokens', groupBy: [] },
];
const hourlyMetrics = [
  { id: 'hours', op: 'count', groupBy: ['hour'] },
  { id: 'last-hour', op: 'max', field: 'hour', groupBy: [] },
];
const cases = [
  { name: 'unfiltered', where: [], days: 2, hours: 3, average: 1575, total: 3150 },
  { name: 'daily threshold', where: [{ field: 'total_tokens', op: '>', value: '1500' }], days: 1, hours: 2, average: 2130, total: 2130 },
  { name: 'timestamp', where: [{ field: 'first_usage_at', op: '<', value: '2026-09-21T02:00:00Z' }], days: 2, hours: 2, average: 1575, total: 3150 },
  { name: 'hour filter', where: [{ field: 'hour', op: '=', value: '2026-09-21T03:00:00Z' }], days: 1, hours: 1, average: 2130, total: 2130 },
  { name: 'mixed hour OR', where: [{ any: [{ field: 'hour', op: '=', value: '2026-09-21T03:00:00Z' }, { field: 'total_tokens', op: '>', value: '2000' }] }], days: 1, hours: 2, average: 2130, total: 2130 },
];
for (const sample of cases) {
  test(`shared metrics keep daily counts and averages: ${sample.name}`, () => {
    const request = { where: sample.where, aggregations: [dailyMetrics[0], hourlyMetrics[0], ...dailyMetrics.slice(1), hourlyMetrics[1]] };
    const before = JSON.stringify(request);
    const result = applyUsageAggregations(daily, hourly, request, schema);
    assert.deepEqual(result.metrics.map(metric => metric.id), request.aggregations.map(metric => metric.id));
    assert.deepEqual(result.metrics.slice(0, 4).map(metric => metric.buckets.reduce((sum, bucket) => sum + bucket.value, 0)), [sample.days, sample.hours, sample.average, sample.total]);
    for (const i of [0, 2, 3]) assert.equal(result.metrics[i].buckets[0].count, sample.days);
    assert.equal(JSON.stringify(request), before);
    if (!sample.where.some(term => term.field === 'hour' || term.any)) {
      const alone = applyUsageAggregations(daily, [], { where: sample.where, aggregations: dailyMetrics }, schema);
      assert.deepEqual([result.metrics[0], ...result.metrics.slice(2, 4)], alone.metrics);
    }
  });
}
