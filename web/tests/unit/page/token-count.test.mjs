import test from 'node:test';
import assert from 'node:assert/strict';
import { formatTokens, formatTokenCount, tokenMetricFormat, isTokenExpression } from '../../../src/token-count';
import { formatPreviewValue } from '../../../src/query-previews';
import { formatted } from '../../../src/delegation';
import { analysisCatalogs } from '../../../src/analysis-catalogs';
import { scatterMetrics } from '../../../src/scatter-metrics';
import { loadClassicScript } from '../helpers/classic-script.mjs';
import fixture from '../../../../testdata/token-count-cases.json';

const classic = loadClassicScript('token-count.js').window.pharosTokens;
const stats = loadClassicScript('quick-stats.js');

test('token displays share three significant figures and K/M/B/T units', () => {
  const formats = [formatTokens, classic.formatTokens, value => formatPreviewValue(value, 'tokens'),
    value => formatted(value, 'tokens'), value => stats.formatQuickStatsValue(value, 'tokens')];
  // Go's formatTokens reads the same cases (TestFormatTokensMatchesWebCases).
  for (const [value, expected] of fixture.cases) for (const format of formats) assert.equal(format(value), expected, String(value));
});

test('unavailable token counts stay blank', () => {
  for (const value of [NaN, Infinity, -Infinity]) { assert.equal(formatTokens(value), ''); assert.equal(classic.formatTokens(value), ''); }
  assert.equal(formatTokenCount(1), '1 token');
  assert.equal(formatTokenCount(1234567), '1.23M tokens');
});

test('every token measure uses the shared token format', () => {
  const measures = [...Object.values(analysisCatalogs).flatMap(catalog => Object.entries(catalog.measures)),
    ...scatterMetrics.map(item => [item.id, item.measure])];
  const tokenMeasures = measures.filter(([, measure]) => measure.unit === 'tokens');
  assert.ok(tokenMeasures.length > 10);
  for (const [id, measure] of tokenMeasures) assert.deepEqual(measure.format, tokenMetricFormat, id);
  for (const [id, measure] of scatterMetrics.map(item => [item.id, item.measure]))
    if (/_tokens$/.test(measure.field ?? '') && !['count', 'count_distinct'].includes(measure.op)) assert.equal(measure.unit, 'tokens', id);
});

test('only a single token field reads as a token expression', () => {
  assert.equal(isTokenExpression('AVG([result_tokens])'), true);
  assert.equal(isTokenExpression('SUM([total_tokens])'), true);
  assert.equal(isTokenExpression('[cost_usd]/NULLIF([total_tokens],0)'), false);
  assert.equal(isTokenExpression('AVG([result_bytes])'), false);
  assert.equal(isTokenExpression(undefined), false);
});
