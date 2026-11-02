import assert from 'node:assert/strict';
import test from 'node:test';
import { highlightParts, matchFindState, summaryEntries } from '../../../src/find-match-model';

test('a keyword snippet splits at its highlight spans', () => {
  assert.deepEqual(highlightParts('…Running the dashboard in a browser', [[1, 22], [28, 35]]), [
    { text: '…', match: false }, { text: 'Running the dashboard', match: true }, { text: ' in a ', match: false }, { text: 'browser', match: true },
  ]);
  // UTF-16 offsets: an emoji before the match counts as two units.
  assert.deepEqual(highlightParts('🙂 cache', [[3, 8]]), [{ text: '🙂 ', match: false }, { text: 'cache', match: true }]);
});

test('missing, overlapping or out-of-range spans still show the whole snippet', () => {
  assert.deepEqual(highlightParts('plain', undefined), [{ text: 'plain', match: false }]);
  assert.deepEqual(highlightParts('', []), [{ text: '', match: false }]);
  const parts = highlightParts('abcdef', [[1, 4], [2, 5], [9, 12], 'x']);
  assert.equal(parts.map(part => part.text).join(''), 'abcdef');
  assert.deepEqual(parts.filter(part => part.match).map(part => part.text), ['bcd', 'e']);
});

test('a card summary interleaves its messages with the counts between them', () => {
  const matches = { complete: true, between: [4, 0, 12], items: [
    { kind: 'match', message_id: 'a', preview: 'first' }, { kind: 'match', message_id: 'b' }, { kind: 'match', message_id: 'c' }, { kind: 'last', message_id: 'z' },
  ] };
  assert.deepEqual(summaryEntries(matches, 9).map(entry => entry.label), [
    'First ask · Match · 3 of 9 matching messages', '4 messages between', 'Match', 'Directly after', 'Match', '12 messages between', 'Latest response',
  ]);
  const one = { complete: true, between: [1, 1], items: [{ kind: 'first', message_id: 'a' }, { kind: 'match', message_id: 'm' }, { kind: 'last', message_id: 'z' }] };
  assert.deepEqual(summaryEntries(one, 1).map(entry => entry.label), ['First ask', '1 message between', 'Match', '1 message between', 'Latest response']);
});

test('opening a match hands its marked terms to page Find', () => {
  assert.deepEqual(matchFindState('Running the dashboard, then running the Dashboard again', [[0, 21], [28, 49]]), { text: 'Running the dashboard', caseSensitive: false, regex: false });
  assert.deepEqual(matchFindState('cache and buffer', [[0, 5], [10, 16]]), { text: 'cache|buffer', caseSensitive: false, regex: true });
  assert.equal(matchFindState('no marks', []), null);
});
