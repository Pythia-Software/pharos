// DOM-free interaction rules shared by the composer and its unit tests.
function askComposerActions({draft = '', turns = [], pending = false, enabled = false, model = ''}) {
  const populated = turns.length > 0 || draft.length > 0;
  return {send: enabled && !!model && !pending && !!draft.trim(), history: !populated, clear: populated, modelLocked: turns.length > 0};
}
function askShouldNavigate({cards = [], draft = '', enabled, regular, edited}) {
  return enabled && !regular && !edited && draft.length === 0 && cards.length === 1;
}
function askChatURL(href, id) {
  const url = new URL(href);
  if (!['/', '/home'].includes(url.pathname)) return url.href;
  if (id) url.searchParams.set('ask_chat', id);
  else url.searchParams.delete('ask_chat');
  return url.href;
}
// Search results are matched against complete chats on the server. Their
// summaries cannot be searched again without dropping later-message matches.
function askHistoryEntries({sessions = [], current = null, query = ''}) {
  const entries = query || !current ? sessions : [current, ...sessions.filter(session => session.id !== current.id)];
  return [...entries].sort((a, b) => b.updatedAt - a.updatedAt);
}
// Keep snapshots/replacements ahead of later removals, even on slow requests.
// A failed write must not prevent the next deletion or clear from running.
let askVisitWrites = Promise.resolve();
function askWriteVisit(write) {
  const result = askVisitWrites.then(write);
  askVisitWrites = result.catch(() => {});
  return result;
}
// Keep a little of the preceding reply for context, then the latest exchange.
// The viewport budget wins when a single exchange is itself very long.
function askThreadHeight({total, latest, previous = 0, gap = 0, available}) {
  const recent = latest + Math.min(previous, 64) + (previous > 0 ? gap : 0);
  return Math.max(0, Math.min(total, recent, available));
}
function askClarificationActions({latest = false, status, pending = false, enabled = false, opening = false, draft = ''}) {
  const active = latest && status === 'clarification' && enabled && !pending && !opening;
  return {active, sendOther: active && !!draft.trim()};
}
