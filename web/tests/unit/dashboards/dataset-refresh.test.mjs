// When engine results refetch: Refresh, or a refresh of their own dataset,
// which a saved Carbon or Parallelism setting triggers.
import test from "node:test";
import assert from "node:assert/strict";
import { datasetsForSavedPreferences, onDatasetRefresh, onSafetyChange, findingsDiscoveryChanged } from "../../../src/dataset-refresh";

test("a saved Parallelism gap or counting refreshes the parallelism dataset, and its results refetch", () => {
  assert.deepEqual(datasetsForSavedPreferences({ set: { "pharos-concurrency-v1": { gap: 15, counting: "workspace" } } }), ["parallelism"]);
  assert.deepEqual(datasetsForSavedPreferences({ set: { "pharos-carbon-v1": {}, "pharos-theme": "dark" }, delete: ["pharos-concurrency-v1"] }), ["usage", "parallelism"]);
  assert.deepEqual(datasetsForSavedPreferences({ set: { "pharos-theme": "dark" } }), []);
  const target = new EventTarget(), calls = [];
  const stop = onDatasetRefresh("parallelism", () => calls.push("parallelism"), target);
  onDatasetRefresh("usage", () => calls.push("usage"), target);
  target.dispatchEvent(new CustomEvent("pharos:data-refresh", { detail: "parallelism" }));
  assert.deepEqual(calls, ["parallelism"], "only the refreshed dataset's results refetch");
  target.dispatchEvent(new Event("pharos:usage-refresh"));
  assert.deepEqual(calls, ["parallelism", "parallelism", "usage"], "Refresh reruns every dashboard query");
  stop();
  target.dispatchEvent(new CustomEvent("pharos:data-refresh", { detail: "parallelism" }));
  assert.equal(calls.filter(call => call === "parallelism").length, 2);
});

test("discovery completion and review changes invalidate risk views and their query results", () => {
  const before = { built_at: "2026-11-01T00:00:00Z", running: false };
  assert.equal(findingsDiscoveryChanged(undefined, before), false);
  assert.equal(findingsDiscoveryChanged(before, { ...before }), false);
  assert.equal(findingsDiscoveryChanged(before, { ...before, running: true }), false);
  assert.equal(findingsDiscoveryChanged({ ...before, running: true }, before), true);
  assert.equal(findingsDiscoveryChanged(before, { ...before, built_at: "2026-11-02T00:00:00Z" }), true, "a pass can finish between polls");
  const target = new EventTarget(), calls = [];
  const stopCards = onSafetyChange(() => calls.push("cards"), target);
  const stopTable = onSafetyChange(() => calls.push("rows and metrics"), target);
  target.dispatchEvent(new Event("pharos:safety-changed"));
  assert.deepEqual(calls, ["cards", "rows and metrics"]);
  stopCards(); stopTable();
  target.dispatchEvent(new Event("pharos:safety-changed"));
  assert.equal(calls.length, 2, "inactive/unmounted views release their subscriptions");
});
