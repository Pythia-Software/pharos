import test from "node:test";
import assert from "node:assert/strict";

// delegation.tsx builds its date formatters when it loads, so the zone is set
// first. Each test file runs in its own process.
process.env.TZ = "America/Denver";
const { cellTitle, clampRange, formatted, hierarchyFrames, localTime } = await import("../../../src/delegation");

const start = Date.parse("2026-09-24T12:00:00Z"), winter = Date.parse("2026-01-24T12:00:00Z");
const metric = (value, quality = "observed") => ({ value, quality, known: value === null ? 0 : 1, total: 1 });
const spec = (id, unit) => ({ id, label: id, unit, definition: `Definition for ${id}` });
const session = (id, outline, child_count = 0) => ({ id, outline, depth: outline.split(".").length - 1, child_count });
const frames = (rows, continues = false) => Object.fromEntries([...hierarchyFrames(rows, continues)].map(([id, frame]) => [id, { start: frame.start, active: frame.active.map(row => row.id), ends: frame.ends }]));

test("first evidence shows local time and its zone in cells, tooltips and timeline bounds", () => {
  const cell = formatted(start, "date");
  assert.match(cell, /\b24\b/);
  assert.match(cell, /\b0?6:00\b/, "noon UTC is 6:00 in Denver");
  assert.match(cell, /MDT|GMT-6/);
  assert.doesNotMatch(cell, /12:00/);
  const title = cellTitle(metric(start), spec("start", "date"));
  const [exact] = title.split("\n");
  assert.equal(exact, localTime(start), "tooltips give the full local time");
  assert.match(exact, /2026/);
  assert.match(exact, /\b0?6:00:00\b/);
  assert.match(exact, /MDT|GMT-6/);
  assert.ok(!title.includes(String(start)) && !title.includes(start.toLocaleString("en-US")), "the raw epoch never shows");
  assert.ok(!title.includes(" date"), "the date unit is not printed after the value");
  assert.match(title, /Definition for start/);
  assert.doesNotMatch(localTime(start), /T12:00|Z\b/);
  assert.match(formatted(winter, "date"), /\b0?5:00\b/, "standard time applies in January");
  assert.match(localTime(winter), /MST|GMT-7/);
});

test("metric cells use compact counts, durations and exact tooltips", () => {
  assert.equal(formatted(1000), "1K");
  assert.equal(formatted(1900), "1.9K");
  assert.equal(formatted(600), "600");
  assert.equal(formatted(300000, "ms"), "5m 0s");
  assert.equal(formatted(450, "ms"), "450ms");
  assert.equal(formatted(null), formatted(undefined, "usd"), "missing values share one placeholder");
  assert.match(cellTitle(metric(1000), spec("tokens", "count")).split("\n")[0], /^1[,.\s ]?000 count$/, "tooltips keep the exact value and unit");
  const missing = cellTitle(metric(null, "unknown"), spec("wait", "ms"));
  assert.match(missing, /^Definition for wait\nunknown · 0\/1 /, "a missing value starts with its definition");
});

test("timeline ranges keep their duration and stay inside the workspace", () => {
  const full = [start, start + 300000];
  assert.deepEqual(clampRange([start + 60000, start + 120000], full), [start + 60000, start + 120000]);
  assert.deepEqual(clampRange([start - 30000, start + 30000], full), [start, start + 60000], "panning past the start stops at the first evidence");
  assert.deepEqual(clampRange([start + 280000, start + 340000], full), [start + 240000, start + 300000], "panning past the end stops at the last evidence");
  assert.deepEqual(clampRange([start - 100000, start + 400000], full), full, "a range wider than the workspace shows all of it");
  assert.deepEqual(clampRange([start + 1000, start + 1000], full), [start + 1000, start + 1001], "ranges are at least one millisecond wide");
  assert.deepEqual(clampRange([start + 2000, start + 1000], full), [start + 2000, start + 2001]);
});

test("hierarchy frames open at parents and close after their last visible descendant", () => {
  const rows = [session("root", "1", 1), session("child", "1.1", 1), session("nested", "1.1.1")];
  assert.deepEqual(frames(rows), {
    root: { start: 0, active: ["root"], ends: [] },
    child: { start: 1, active: ["root", "child"], ends: [] },
    nested: { start: undefined, active: ["root", "child"], ends: [1, 0] },
  });
  assert.deepEqual(frames(rows, true).nested.ends, [], "a page that continues leaves its branches open");
  assert.deepEqual(frames([rows[0]]).root, { start: 0, active: ["root"], ends: [0] }, "a collapsed parent closes its own frame");
  const siblings = frames([session("root", "1", 3), session("a", "1.1", 1), session("b", "1.10"), session("c", "1.2")]);
  assert.deepEqual(siblings.a.ends, [1], "1.10 is not inside 1.1");
  assert.deepEqual(siblings.b, { start: undefined, active: ["root"], ends: [] });
  assert.deepEqual(siblings.c.ends, [0]);
  assert.deepEqual(frames([session("x", "1"), session("y", "2")]), { x: { start: undefined, active: [], ends: [] }, y: { start: undefined, active: [], ends: [] } });
});


test("session pagination reuses live cursors and replays ordinals only for a new execution",async()=>{
  const {CursorPagination}=await import("../../../src/cursor-pagination");
  const pages=new CursorPagination(),calls=[],signal=new AbortController().signal;
  let execution=0;
  const load=async cursor=>{calls.push(cursor);if(!cursor)execution++;const page=cursor?Number(cursor.split(":")[1]):0;return {next_cursor:page<3?`${execution}:${page+1}`:"",value:page};};
  const first=await pages.load("scope-a",0,load,signal);assert.equal(first.body.value,0);assert.deepEqual(calls,[""]);
  for(const page of [1,2,1,0,1]){const before=calls.length;const result=await pages.load("scope-a",page,load,signal);assert.equal(result.body.value,page);assert.equal(calls.length-before,1);assert.equal(result.previous.length,page);}
  pages.reset();calls.length=0;
  await pages.load("scope-a",2,load,signal);assert.deepEqual(calls,["","3:1","3:2"]);
  calls.length=0;await pages.load("scope-b",1,load,signal);assert.deepEqual(calls,["","4:1"]);
  const short=await pages.load("scope-short",4,async()=>({next_cursor:""}),signal);assert.equal(short.page,0);
});

test("aborted session requests cannot extend the replacement execution's cursor chain",async()=>{
  const {CursorPagination}=await import("../../../src/cursor-pagination");
  const pages=new CursorPagination(),controller=new AbortController();let finish;
  const pending=pages.load("scope",0,()=>new Promise(resolve=>finish=resolve),controller.signal);
  controller.abort();pages.reset();finish({next_cursor:"expired"});await assert.rejects(pending,{name:"AbortError"});
  const calls=[];await pages.load("scope",1,async cursor=>{calls.push(cursor);return {next_cursor:cursor?"":"fresh"};},new AbortController().signal);
  assert.deepEqual(calls,["","fresh"]);
});
