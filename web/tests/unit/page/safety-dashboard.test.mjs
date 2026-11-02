import test from "node:test";
import assert from "node:assert/strict";
import { safetyDashboardReviews, safetyDashboardPlaces } from "../../../src/safety-dashboard-model";

const review = (id, patch = {}) => ({ id, state: "open", target: "repo:a", target_label: "Alpha", calls: 1, material_change: false, last_seen_at: "2026-11-01T12:00:00Z", ...patch });
const cases = [
  review("old-evidence", { last_seen_at: "2026-10-31T12:00:00Z", material_change: true }),
  review("frequent", { target: "machine:b", target_label: "Mac", calls: 8 }),
  review("recent", { last_seen_at: "2026-11-01T10:00:00-03:00", calls: 3 }),
  review("handed", { state: "handed_off", calls: 99 }),
];

test("risk dashboard combines lifecycle and place filters without changing the underlying reviews", () => {
  const before = structuredClone(cases);
  assert.deepEqual(safetyDashboardReviews(cases, "open", "repo:a", "recent").map(item => item.id), ["recent", "old-evidence"]);
  assert.deepEqual(safetyDashboardReviews(cases, "handed_off", "repo:a", "calls").map(item => item.id), ["handed"]);
  assert.deepEqual(safetyDashboardReviews(cases, "dismissed", "", "recent"), []);
  assert.deepEqual(cases, before);
});

test("risk ranking uses actual recent activity, calls, and new evidence with deterministic ties", () => {
  assert.deepEqual(safetyDashboardReviews(cases, "open", "", "recent").map(item => item.id), ["recent", "frequent", "old-evidence"]);
  assert.deepEqual(safetyDashboardReviews(cases, "open", "", "calls").map(item => item.id), ["frequent", "recent", "old-evidence"]);
  assert.deepEqual(safetyDashboardReviews(cases, "open", "", "evidence").map(item => item.id), ["old-evidence", "recent", "frequent"]);
  for (const rank of ["recent", "calls", "evidence"]) assert.deepEqual(safetyDashboardReviews([review("b"), review("a")], "open", "", rank).map(item => item.id), ["a", "b"]);
});

test("place filter counts reviews across lifecycle lists like Efficiency", () => {
  assert.deepEqual(safetyDashboardPlaces(cases), [{ key: "repo:a", label: "Alpha", count: 3 }, { key: "machine:b", label: "Mac", count: 1 }]);
  assert.deepEqual(safetyDashboardPlaces([]), []);
});
