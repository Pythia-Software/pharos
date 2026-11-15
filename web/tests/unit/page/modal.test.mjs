import assert from "node:assert/strict";
import { test } from "node:test";
import { loadClassicScript, plain } from "../helpers/classic-script.mjs";

const { pharosModal } = loadClassicScript("modal.js").window;
const { model } = pharosModal;

test("a backdrop click is one outside the dialog's box, edges included as inside", () => {
  const rect = { left: 10, right: 110, top: 20, bottom: 220 };
  assert.equal(model.outside(rect, 5, 100), true);
  assert.equal(model.outside(rect, 60, 230), true);
  assert.equal(model.outside(rect, 10, 20), false);
  assert.equal(model.outside(rect, 60, 120), false);
});

test("Tab wraps through a modal's stops in both directions and enters from outside", () => {
  const stops = ["a", "b", "c"];
  assert.equal(model.tabTarget(stops, 0, false), 1);
  assert.equal(model.tabTarget(stops, 2, false), 0, "Tab from the last stop wraps to the first");
  assert.equal(model.tabTarget(stops, 0, true), 2, "Shift+Tab from the first stop wraps to the last");
  assert.equal(model.tabTarget(stops, -1, false), 0, "focus outside the stops enters at the first");
  assert.equal(model.tabTarget(stops, -1, true), 2, "or, backwards, at the last");
  assert.equal(model.tabTarget([], -1, false), -1, "a modal without stops keeps focus on itself");
});

test("stacked modals paint one scrim: every dialog but the topmost is covered", () => {
  assert.deepEqual(plain(model.covered([])), []);
  assert.deepEqual(plain(model.covered(["only"])), [false]);
  assert.deepEqual(plain(model.covered(["page", "nested", "top"])), [true, true, false]);
});

test("without a page the stack has nothing on top and loading twice keeps one stack", () => {
  assert.equal(pharosModal.top(), null);
  const again = loadClassicScript("modal.js", { pharosModal }).window.pharosModal;
  assert.equal(again, pharosModal);
});
