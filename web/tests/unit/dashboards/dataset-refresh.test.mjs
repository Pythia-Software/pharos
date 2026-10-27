// When engine results refetch: Refresh, or a refresh of their own dataset,
// which a saved Carbon or Parallelism setting triggers.
import test from "node:test";
import assert from "node:assert/strict";
import { datasetsForSavedPreferences, onDatasetRefresh } from "../../../src/dataset-refresh";

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
