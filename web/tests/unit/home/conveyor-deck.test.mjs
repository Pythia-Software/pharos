import test from "node:test";
import assert from "node:assert/strict";
import { suggestionPool } from "../../../src/curiosity-data";
import { SPACING, beltKind, chooseNext, spreadDeck, leanBeside } from "../../../src/conveyor-deck";

// Same-kind pairs closer than `reach` places in a linear order.
const repeats = (order, key, reach) => order.reduce((total, item, index) => total + order.slice(index + 1, index + 1 + reach).filter(other => key(other) === key(item)).length, 0);

test("spread decks never put two cards of a kind side by side and keep kinds SPACING apart where the mix allows", () => {
  for (let trial = 0; trial < 40; trial++) {
    const deck = suggestionPool(), ordered = spreadDeck(deck);
    assert.equal(ordered.length, deck.length);
    assert.deepEqual(new Set(ordered), new Set(deck));
    assert.equal(repeats(ordered, beltKind, 1), 0, ordered.map(beltKind).join(" "));
    // About 30% of the catalog draws a ranking, too many to keep SPACING
    // apart: they sit every third or fourth card instead of clumping.
    assert.ok(repeats(ordered, beltKind, 2) <= 2, ordered.map(beltKind).join(" "));
    assert.ok(repeats(ordered, beltKind, SPACING) < repeats(deck, beltKind, SPACING) * .6);
  }
});

test("a mix that allows it is spread with no kind repeated within SPACING places", () => {
  const kinds = [["pie", "Composition"], ["box", "Distribution"], ["scatter", "Relationship"], ["bar-horizontal", "Ranking"]];
  const deck = Array.from({ length: 24 }, (_, index) => ({ id: `card-${index}`, category: ["cost", "usage", "tools"][index % 3], measures: [], exploration: { valueField: "x" }, cover: { mode: "result", metricId: "x", kind: kinds[Math.floor(index / 6)][0] } }));
  assert.deepEqual([...new Set(deck.map(beltKind))], kinds.map(([, kind]) => kind));
  assert.equal(repeats(spreadDeck(deck), beltKind, SPACING), 0);
});

test("each belt end takes the waiting card least like its own neighbors", () => {
  const card = (id, kind, category = "cost") => ({ id, category, measures: [], exploration: { valueField: "x" }, cover: { mode: "result", metricId: "x", kind } });
  const neighbors = [card("a", "pie"), card("b", "box"), card("c", "scatter")];
  assert.equal(chooseNext([card("d", "pie"), card("e", "box"), card("f", "bar-horizontal")], neighbors).id, "f");
  // Within a kind, a different subject from the neighbors wins.
  assert.equal(chooseNext([card("g", "bar-horizontal", "cost"), card("h", "bar-horizontal", "tools")], neighbors).id, "h");
});

test("leans alternate often, never lean one way three times running, and differ visibly from the next card", () => {
  let seed = 7;
  const random = () => (seed = (seed * 16807) % 2147483647) / 2147483647;
  const leans = [];
  for (let index = 0; index < 400; index++) leans.push(leanBeside(leans.slice(-2).reverse(), random));
  for (let index = 0; index < leans.length; index++) {
    assert.ok(Math.abs(leans[index]) >= .6 && Math.abs(leans[index]) <= 2.1);
    if (index >= 1) assert.ok(Math.abs(Math.abs(leans[index]) - Math.abs(leans[index - 1])) >= .44, `${leans[index - 1]} → ${leans[index]}`);
    if (index >= 2) assert.ok(!(Math.sign(leans[index]) === Math.sign(leans[index - 1]) && Math.sign(leans[index]) === Math.sign(leans[index - 2])));
  }
  const flips = leans.slice(1).filter((lean, index) => Math.sign(lean) !== Math.sign(leans[index])).length;
  assert.ok(flips > leans.length * .6 && flips < leans.length * .95);
});
