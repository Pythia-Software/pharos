import test from "node:test";
import assert from "node:assert/strict";
import { suggestionPool } from "../../../src/curiosity-data";
import { SPACING, beltSlots, growBelt, beltKind, previewKind, chooseNext, spreadDeck, leanBeside } from "../../../src/conveyor-deck";

test("singleton and mixed arrivals keep every card readable until navigation is needed", () => {
  for (const arrivals of [[1, 1, 1, 1], [1, 2, 1], [2, 1, 1], [1, 1, 2], [2, 2]]) {
    const anchor = { id: "anchor" };
    let items = [anchor], serial = 0;
    for (const size of arrivals) {
      const right = { id: `card-${++serial}` }, left = size === 2 ? { id: `card-${++serial}` } : undefined;
      const before = [...items], center = items.indexOf(anchor);
      const growth = growBelt(items, center, right, left);
      assert.deepEqual(items, before, "placing arrivals leaves the published deck unchanged until reveal");
      items = growth.items;
      const { first, last, positions } = beltSlots(items.length, items.indexOf(anchor), items.length, 6);
      assert.ok(positions.every(position => position >= first && position <= last), `all ${items.length} cards are readable after ${arrivals.join(",")} arrivals`);
      assert.deepEqual(new Set(items), new Set([...before, right, ...left ? [left] : []]));
      for (const item of before) assert.equal(items.indexOf(item) - items.indexOf(anchor), before.indexOf(item) - center, "existing cards stay fixed around the anchor");
    }
  }
});

test("singletons fill the shorter side and break ties toward the extra reading slot", () => {
  const anchor = { id: "anchor" }, incoming = { id: "incoming" }, neighbor = { id: "neighbor" };
  assert.equal(growBelt([anchor, neighbor], 0, incoming).left, incoming);
  assert.equal(growBelt([neighbor, anchor], 1, incoming).right, incoming);
  assert.equal(growBelt([anchor], 0, incoming).right, incoming, "an even deck has an extra slot on the right");
});

test("a growing ready deck keeps a bounded reading area at either end and in the middle", () => {
  for (const size of [7, 160, 1000]) for (const center of [0, Math.floor(size / 2), size - 1]) {
    const { first, last, positions } = beltSlots(size, center, 5, 6);
    assert.equal(positions.length, 17, "loading more previews never mounts the whole catalog");
    assert.ok(positions.includes(0), "the center card stays readable");
    assert.ok(positions[0] < first && positions.at(-1) > last, "both sides retain their preview cards");
    assert.equal(new Set(positions).size, positions.length, "every mounted slot has a distinct position");
  }
});

test("identical values and collapsed chart kinds still offer every ready card, including the last singleton", () => {
  const preview = { state: "ready", total: 42, summary: "42", coverMetricId: "count", metrics: [{
    clause: { id: "count", op: "count", groupBy: ["tool_name"] },
    result: { id: "count", buckets: [{ keys: ["Read"], value: 42, count: 42 }] },
  }] };
  const cards = suggestionPool().slice(0, 7), kindOf = card => previewKind(card, preview);
  assert.ok(cards.every(card => kindOf(card) === "Number"));
  const pending = cards.slice(1), published = [cards[0]];
  while (pending.length) {
    const next = chooseNext(pending, published.slice(-SPACING).reverse(), kindOf);
    assert.ok(next, "a repeated kind must not prevent an otherwise ready card from joining");
    published.push(next);
    pending.splice(pending.indexOf(next), 1);
  }
  assert.deepEqual(new Set(published), new Set(cards));
});

test("small ready decks show each real card once, including empty and unpaired arrivals", () => {
  assert.deepEqual(beltSlots(0, 0, 0, 6).positions, []);
  for (const size of [1, 2, 3, 4, 5]) for (let center = 0; center < size; center++) {
    const { positions } = beltSlots(size, center, size, 6);
    assert.equal(positions.length, size);
    assert.deepEqual(positions.map(position => center + position), Array.from({ length: size }, (_, index) => index));
  }
});

test("retargeted slides cover their current and destination reading areas in both directions", () => {
  for (const [at, offset] of [[0, 5], [4, 10], [4, -5], [-4, 5], [-4, -10], [5, 5]]) {
    const { first, last, positions } = beltSlots(160, 80, 5, 6, { at, offset });
    assert.equal(positions.length, 17 + Math.abs(offset - at));
    for (const center of [at, offset]) for (let position = center + first; position <= center + last; position++) {
      assert.ok(positions.includes(position), `slide ${at} → ${offset} keeps position ${position} mounted`);
    }
  }
});

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
