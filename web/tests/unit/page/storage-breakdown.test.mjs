// The storage breakdown's view model in storage-breakdown.js
// (window.pharosStorageBreakdown.model). storage_breakdown.go measures and
// divides the space; TestStorageBreakdownDividesOriginalsAndCatalog covers that.
import { test } from "node:test";
import assert from "node:assert/strict";
import { loadClassicScript, plain } from "../helpers/classic-script.mjs";

const { model } = loadClassicScript("storage-breakdown.js").window.pharosStorageBreakdown;

const breakdown = {
  total_bytes: 1000, volume_name: "Pharos SSD", volume_free_bytes: 5000,
  parts: [
    { key: "originals_latest", label: "Originals: latest copies", bytes: 600 },
    { key: "originals_history", label: "Originals: earlier versions", bytes: 395 },
    { key: "originals_other", label: "Originals: manifests and indexes", bytes: 5 },
    { key: "preserved", label: "Preserved packages", bytes: 0 },
  ],
  machines: [{ id: "a", label: "Studio", originals_bytes: 300, catalog_bytes: 100 }, { id: "b", label: "Air", originals_bytes: 100, catalog_bytes: 100 }],
  sources: [{ id: "antigravity-cli", label: "antigravity-cli", originals_bytes: 10, catalog_bytes: 0 }, { id: "mine", label: "mine", originals_bytes: 1, catalog_bytes: 1 }],
};

test("parts leave out empty ones and give each a share of the total", () => {
  const { parts, total, free, volume } = model(breakdown);
  assert.deepEqual(plain(parts.map(part => [part.key, part.share])), [["originals_latest", "60%"], ["originals_history", "40%"], ["originals_other", "<1%"]]);
  assert.equal(total, 1000);
  assert.equal(free, 5000);
  assert.equal(volume, "Pharos SSD");
});

test("rows total their originals and catalog, scaled to the largest row", () => {
  const { rows } = model(breakdown, "machines");
  assert.deepEqual(plain(rows.map(row => [row.label, row.total, row.originalsWidth, row.catalogWidth])), [["Studio", 400, 75, 25], ["Air", 200, 25, 25]]);
});

test("known source names read as their tools; configured names stay", () => {
  assert.deepEqual(plain(model(breakdown, "sources").rows.map(row => row.label)), ["Antigravity CLI", "mine"]);
});

test("a breakdown with nothing measured has no parts or rows", () => {
  const empty = model({ status: "measured", total_bytes: 0, parts: [{ key: "other", label: "Other", bytes: 0 }] }, "repositories");
  assert.deepEqual(plain([empty.parts, empty.rows, empty.volume]), [[], [], "the library drive"]);
});
