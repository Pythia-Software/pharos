import test from 'node:test';
import assert from 'node:assert/strict';
import { formatUSD } from '../../../src/currency';
import { formatPreviewValue } from '../../../src/query-previews';
import { boxAxisCost, boxCost } from '../../../src/compact-box-model';
import { cellTitle, formatted } from '../../../src/delegation';
import { loadClassicScript } from '../helpers/classic-script.mjs';

const classic = loadClassicScript('currency.js').window.pharosCurrency;
const stats = loadClassicScript('quick-stats.js');

test('dollar displays share cents below $100 and whole dollars at and above it', () => {
  const formats = [formatUSD, classic.formatUSD, boxCost, boxAxisCost,
    value => formatPreviewValue(value, 'USD'), value => formatted(value, 'usd'),
    value => stats.formatQuickStatsValue(value, 'usd')];
  for (const [value, expected] of [
    [0, '$0.00'], [.000001, '$0.00'], [.0049, '$0.00'], [.005, '$0.01'],
    [.123456, '$0.12'], [1.235, '$1.24'], [12.3456, '$12.35'],
    [99.994, '$99.99'], [99.995, '$100'], [100, '$100'], [100.49, '$100'],
    [100.5, '$101'], [1234.56, '$1,235'], [1234567.89, '$1,234,568'],
    [-.0001, '$0.00'], [-12.3456, '-$12.35'], [-99.995, '-$100'], [-1234.56, '-$1,235'],
  ]) {
    for (const format of formats) assert.equal(format(value), expected, String(value));
    const title = cellTitle({ value, quality: 'observed', known: 1, total: 1 }, { unit: 'usd', definition: 'API equivalent' });
    assert.equal(title.split('\n')[0], expected, 'hover follows the visible amount');
  }
});

test('unavailable dollar amounts do not display NaN or infinity', () => {
  for (const value of [NaN, Infinity, -Infinity]) {
    assert.equal(formatUSD(value), '');
    assert.equal(classic.formatUSD(value), '');
  }
});
