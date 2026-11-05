import assert from "node:assert/strict";
import test from "node:test";
import { activityPresentation, machineFilterKey } from "../../../src/machine-activity-data";

test("activities keep their labels and other tools get a stable name and colour", () => {
  assert.equal(activityPresentation("test").label, "Tests & checks");
  assert.equal(activityPresentation("tool:special_tool").label, "special_tool");
  assert.equal(activityPresentation("command:make").color, activityPresentation("command:make").color);
  assert.equal(activityPresentation("").label, "(Unidentified tool)");
});

test("filter keys ignore the order of OR and AND members", () => {
  const a = { field: "project_name", op: "=", value: "a" }, b = { field: "provider", op: "=", value: "claude" };
  assert.equal(machineFilterKey({ any: [a, { all: [a, b] }] }), machineFilterKey({ any: [{ all: [b, a] }, a] }));
  assert.notEqual(machineFilterKey(a), machineFilterKey({ ...a, negated: true }));
});
