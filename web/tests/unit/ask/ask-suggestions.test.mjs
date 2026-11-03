// The Ask Pharos suggestion catalog (web/src/ask-suggestions.ts): every page
// has an area or a reason, and questions stay generic. Go's signals and
// ranking are tested in internal/archive/ask_suggestions_test.go.
import test from "node:test";
import assert from "node:assert/strict";
import map from "../../../../internal/archive/assets/site-map.json";
import generated from "../../../../internal/archive/assets/ask-suggestions.json";
import { askAreas, askFamilies, buildAskSuggestions } from "../../../src/ask-suggestions";

const pages = Object.keys(map.nodes);

test("the generated catalog is the one the site map's pages build", () => {
  assert.deepEqual(buildAskSuggestions(pages), generated);
});

test("a page without an area or a reason fails generation", () => {
  assert.throws(() => buildAskSuggestions([...pages, "analyze/new-page"]), /page analyze\/new-page has no Ask suggestion area/);
});

test("questions name only their optional scope and period", () => {
  for (const family of askFamilies) for (const text of family.variants) {
    assert.doesNotMatch(text, /[0-9`"“”]/, text);
    assert.deepEqual([...text.matchAll(/\{([^}]*)\}/g)].map(match => match[1]).filter(name => name !== "scope" && name !== "period"), [], text);
  }
  const original = askFamilies[0].variants;
  askFamilies[0].variants = ["Why does go test fail 41 times in pharos?"];
  try { assert.throws(() => buildAskSuggestions(pages), /names specifics/); } finally { askFamilies[0].variants = original; }
});

test("every area offers a question and every family's signals belong to its area", () => {
  for (const area of askAreas) assert.ok(askFamilies.some(family => family.area === area.id), area.id);
  for (const family of askFamilies) {
    const area = askAreas.find(item => item.id === family.area);
    for (const signal of family.signals ?? []) assert.ok(area.signals.includes(signal), `${family.id} ${signal}`);
  }
});
