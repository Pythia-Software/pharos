import test from 'node:test';
import assert from 'node:assert/strict';

const freshness = await import('../../../src/agent-events-freshness');
const { withAgentEventsFreshness } = await import('../../../src/query-previews');

test('agent-event counts the server withheld or is rebuilding never read as confirmed', () => {
  assert.equal(freshness.agentEventsFreshnessNote({ state: 'current', pending_conversations: 0, withheld_conversations: 0 }), '');
  assert.equal(freshness.agentEventsFreshnessNote(null), '');
  const pending = { state: 'pending', pending_conversations: 2, withheld_conversations: 1 };
  assert.match(freshness.agentEventsFreshnessNote(pending), /Events for 1 conversation are left out until they are rebuilt/);
  assert.match(freshness.agentEventsFreshnessNote(pending), /2 conversations are waiting for events to be rebuilt\. These counts may change\./);
  assert.match(freshness.agentEventsFreshnessNote({ state: 'unknown' }), /^Conversations changed since the last events run\./);
  assert.equal(freshness.agentEventsFreshnessMark(pending), '≈');
  assert.equal(freshness.agentEventsFreshnessMark({ state: 'current' }), '');
});

test('coverage is taken only from agent-event query responses', () => {
  freshness.reportQueryCoverage('/api/query/usage/query-v3', { coverage: { state: 'pending', withheld_conversations: 9 } });
  assert.notEqual(freshness.currentAgentEventsCoverage()?.withheld_conversations, 9, 'other datasets do not set agent-event freshness');
  freshness.reportQueryCoverage('/api/query/agent_event_messages/query-v3?x=1', { coverage: { state: 'pending', withheld_conversations: 3 } });
  assert.equal(freshness.currentAgentEventsCoverage()?.withheld_conversations, 3);
  freshness.reportQueryCoverage('/api/query/agent_events/query-v3', { coverage: { state: 'current', pending_conversations: 0, withheld_conversations: 0 } });
  assert.equal(freshness.currentAgentEventsCoverage()?.state, 'current');
  // Rates leave out the same withheld conversations, so they report the same coverage.
  freshness.reportQueryCoverage('/api/query/agent_event_rates/query-v3', { coverage: { state: 'pending', withheld_conversations: 4 } });
  assert.equal(freshness.currentAgentEventsCoverage()?.withheld_conversations, 4);
});

test('agent-event cards mark their summary and explain pending coverage', () => {
  const preview = { state: 'ready', total: 4, summary: '4 events', note: 'Model output' };
  const marked = withAgentEventsFreshness(preview, 'agent_events', { coverage: { state: 'pending', withheld_conversations: 1, pending_conversations: 0 } });
  assert.equal(marked.summary, '≈4 events');
  assert.equal(withAgentEventsFreshness(preview, 'agent_event_rates', { coverage: { state: 'pending', withheld_conversations: 1 } }).summary, '≈4 events', 'Agent Events rate cards too');
  assert.match(marked.note, /^Events for 1 conversation are left out .* · Model output$/);
  assert.equal(withAgentEventsFreshness(preview, 'agent_events', { coverage: { state: 'current' } }), preview);
  assert.equal(withAgentEventsFreshness(preview, 'usage', { coverage: { state: 'pending' } }), preview, 'only agent-event datasets carry this coverage');
});

test('a saved event the dataset withholds reads pending, never not found', () => {
  const { agentEventItemPendingMessage } = freshness;
  const coverage = { state: 'pending', pending_conversations: 1, withheld_conversations: 1 };
  assert.match(agentEventItemPendingMessage('agent_events', { row: null, pending: true, coverage }), /^This event will be back once events are rebuilt/);
  assert.match(agentEventItemPendingMessage('agent_event_messages', { row: null, pending: true, coverage }), /^This message's events/);
  // Truly gone, present, or another dataset: the ordinary unavailable state.
  assert.equal(agentEventItemPendingMessage('agent_events', { row: null, pending: false, coverage }), null);
  assert.equal(agentEventItemPendingMessage('agent_events', { row: { id: 'e1' }, pending: true }), null);
  assert.equal(agentEventItemPendingMessage('tool_calls', { row: null, pending: true }), null);
  assert.equal(agentEventItemPendingMessage('agent_events', null), null);
});

test('nothing visible because every event conversation is withheld reads as a pending rebuild, not never read', () => {
  assert.equal(freshness.agentEventsNeverRead(0, null), true);
  assert.equal(freshness.agentEventsNeverRead(0, { state: 'current' }), true);
  // A catalog the events pass never ran on can still have changes waiting.
  assert.equal(freshness.agentEventsNeverRead(0, { state: 'pending', pending_conversations: 9, withheld_conversations: 0 }), true);
  const withheld = { state: 'pending', pending_conversations: 0, withheld_conversations: 3 };
  assert.equal(freshness.agentEventsNeverRead(0, withheld), false);
  assert.ok(freshness.agentEventsFreshnessNote(withheld), 'the pending view has a note to show');
  assert.equal(freshness.agentEventsNeverRead(5, { state: 'current' }), false);
});
