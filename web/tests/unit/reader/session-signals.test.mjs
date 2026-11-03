// The reader's Session signals strip (reader-model.js sessionSignalStrip):
// which chips show, what a chip's click writes to the URL, and that its
// signals are the Session Shape dashboard's.
import { test } from "node:test";
import assert from "node:assert/strict";
import { plain } from "../helpers/classic-script.mjs";
import { reader } from "../helpers/transcript.mjs";
import { sessionSignalLabels, sessionSignals } from "../../../src/session-shape-model";

const { sessionSignalStrip, sessionSignalActive, sessionSignalAttention } = reader;
const payload = { signals: [{ signal: "corrections", count: 1, message_ids: ["m9"] }, { signal: "failed_calls", count: 3, message_ids: ["m1", "m4", "m7"] }, { signal: "rejections", count: 0, message_ids: [] }],
  coverage: { has_tool_data: true, has_timestamps: true, has_authorship: true, has_context_window: false } };

test("the strip shows the signals that fired in the dashboard's order and names those without evidence", () => {
  assert.deepEqual(plain(reader.SESSION_SIGNALS.map(([id]) => id)), [...sessionSignals]);
  assert.deepEqual(Object.fromEntries(reader.SESSION_SIGNALS.map(([id, label]) => [id, label])), sessionSignalLabels);
  const { chips, note } = sessionSignalStrip(payload);
  assert.deepEqual(plain(chips.map(chip => [chip.signal, chip.label, chip.count, chip.messageIds])), [["failed_calls", "Failed calls", 3, ["m1", "m4", "m7"]], ["corrections", "Corrections", 1, ["m9"]]]);
  assert.match(note, /context pressure/);
  assert.doesNotMatch(note, /tool call|corrections/, "covered signals are not called missing");
  assert.deepEqual(plain(sessionSignalStrip({ signals: [], coverage: { has_tool_data: false } })), { chips: [], note: "Not enough evidence for tool call signals (no tool ledger)." });
  assert.deepEqual(plain(sessionSignalStrip(null)), { chips: [], note: "" });
});

test("a chip reveals its next message, highlights the rest and replaces other attention", () => {
  const [failed] = sessionSignalStrip(payload).chips;
  const start = "?conversation=c1&highlight=message%3Aother&reveal=section%3Aarchive";
  assert.equal(sessionSignalActive(start, failed), -1);
  let params = sessionSignalAttention(start, failed);
  assert.equal(params.get("conversation"), "c1");
  assert.equal(params.get("reveal"), "message:m1");
  assert.deepEqual(params.getAll("highlight"), ["message:m4", "message:m7"]);
  assert.equal(sessionSignalActive(`?${params}`, failed), 0);
  params = sessionSignalAttention(`?${params}`, failed);
  assert.deepEqual([params.get("reveal"), params.getAll("highlight")], ["message:m4", ["message:m1", "message:m7"]]);
  params = sessionSignalAttention(`?${sessionSignalAttention(`?${params}`, failed)}`, failed);
  assert.equal(params.get("reveal"), "message:m1", "stepping wraps to the first");
  // Highlights are capped so a link stays short; stepping reaches every one.
  const many = { signal: "failed_calls", messageIds: Array.from({ length: 100 }, (_, index) => `m${index}`) };
  let search = "";
  for (let step = 0; step < 61; step++) search = `?${sessionSignalAttention(search, many)}`;
  assert.equal(new URLSearchParams(search).get("reveal"), "message:m60");
  assert.equal(new URLSearchParams(search).getAll("highlight").length, 40);
});
