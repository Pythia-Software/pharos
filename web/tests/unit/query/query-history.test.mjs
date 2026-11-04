import test from "node:test";
import assert from "node:assert/strict";
import { createQueryHistory, queryHistoryKey } from "../../../src/query-history";

// A browser history stack: entries, a cursor, and Back/Forward.
function fakeHistory() {
  const entries = [{ state: null, url: "/t?q=start" }];
  let index = 0;
  return {
    entries, get index() { return index; },
    get state() { return entries[index].state; },
    get url() { return entries[index].url; },
    replaceState(state, _title, url) { entries[index] = { state, url: url ?? entries[index].url }; },
    pushState(state, _title, url) { entries.splice(index + 1); entries.push({ state, url }); index++; },
    go(delta) { index += delta; },
  };
}
const query = (where = [], patch = {}) => ({ where, orderBy: [], select: [{ field: "subject", width: 300 }], limit: 50, offset: 0, ...patch });
const url = q => `/t?q=${encodeURIComponent(JSON.stringify(q.where))}`;
const filtered = query([{ field: "rule", op: "=", value: "permissions" }]);
const narrowed = query([...filtered.where, { field: "subject", op: "contains", value: "secrets" }]);

test("each query edit is a history entry that Back returns to", () => {
  let clock = 0;
  const history = fakeHistory(), steps = createQueryHistory("safety", history, () => clock);
  assert.equal(steps.observe(filtered, url), false, "the first query belongs to the entry it opened");
  clock += 5000;
  assert.equal(steps.observe(narrowed, url), true);
  assert.deepEqual(history.entries.map(entry => entry.url), [url(filtered), url(narrowed)]);
  history.go(-1); steps.navigated(history.state);
  // The table restores the entry's query: recorded, not pushed, so Forward survives.
  assert.equal(steps.observe(filtered, url), false);
  assert.equal(history.entries.length, 2);
  history.go(1); steps.navigated(history.state);
  assert.equal(steps.observe(narrowed, url), false);
  clock += 5000;
  assert.equal(steps.observe(query(), url), true, "an edit after returning is a new step");
  assert.equal(history.entries.length, 3);
});

test("rapid edits are one step, and widths are not a step", () => {
  let clock = 0;
  const history = fakeHistory(), steps = createQueryHistory("safety", history, () => clock);
  steps.observe(query(), url);
  clock += 5000; steps.observe(filtered, url);
  clock += 200; assert.equal(steps.observe(narrowed, url), false);
  assert.deepEqual(history.entries.map(entry => entry.url), [url(query()), url(narrowed)]);
  assert.equal(queryHistoryKey({ ...narrowed, select: [{ field: "subject", width: 90 }], fitToScreen: true }), queryHistoryKey(narrowed));
  assert.notEqual(queryHistoryKey({ ...narrowed, offset: 50 }), queryHistoryKey(narrowed));
});

test("a link to another query is restored into its own entry, never pushed over it", () => {
  let clock = 0;
  const history = fakeHistory(), steps = createQueryHistory("safety", history, () => clock);
  steps.observe(query(), url);
  clock += 5000;
  history.pushState(null, "", url(filtered)); steps.navigated(history.state);
  assert.equal(steps.observe(filtered, url), false);
  assert.deepEqual(history.entries.map(entry => entry.url), ["/t?q=start", url(filtered)]);
  clock += 5000;
  assert.equal(steps.observe(narrowed, url), true);
  assert.equal(history.entries.length, 3);
});
