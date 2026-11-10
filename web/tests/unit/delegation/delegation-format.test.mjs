import test from "node:test";
import assert from "node:assert/strict";

// delegation.tsx builds its date formatters when it loads, so the zone is set
// first. Each test file runs in its own process.
process.env.TZ = "America/Denver";
const { addSessionColumns, sessionTimelineState, cellTitle, metricText, tableMetricText, tokenAttributionText, clampRange, formatted, localTime, timelineTickLabels, timelinePointerTime, timelineDragRange } = await import("../../../src/delegation");
const { readDelegationRoute, delegationRouteValues } = await import("../../../src/delegation-route");
const { timelineSessionBranches } = await import("../../../src/timeline-label-model");

const start = Date.parse("2026-09-24T12:00:00Z"), winter = Date.parse("2026-01-24T12:00:00Z");
const metric = (value, quality = "observed") => ({ value, quality, known: value === null ? 0 : 1, total: 1 });
const spec = (id, unit) => ({ id, label: id, unit, definition: `Definition for ${id}` });
const session = (id, depth, child_count = 0) => ({ id, depth, child_count });


test("new session columns join related metrics while preserving custom choices", () => {
  const saved = ['cost', 'calls', 'tokens', 'files'];
  const upgraded = addSessionColumns(saved);
  assert.deepEqual(upgraded, ['cost', 'messages', 'calls', 'events', 'tokens', 'files', 'filesRead']);
  assert.deepEqual(saved, ['cost', 'calls', 'tokens', 'files']);
  assert.deepEqual(addSessionColumns(upgraded), upgraded);
  assert.deepEqual(addSessionColumns(['messages', 'events', 'cost', 'filesRead']), ['messages', 'events', 'cost', 'filesRead']);
});

test("session timeline uses its own bounds and expands exactly one event level", () => {
  const node = {...session('child', 1), start: 100, end: 200, subtree_start: 50, subtree_end: 300};
  const disclosures = {'delegation-labels:child:session': false, 'delegation-labels:child:events': true, 'delegation-labels:child:events:assumption': true, 'delegation-labels:other:events': true};
  assert.deepEqual(sessionTimelineState(node, disclosures), {range: [100, 200], disclosures: {'delegation-labels:other:events': true, 'delegation-labels:child:session': true}});
  assert.equal(disclosures['delegation-labels:child:events'], true);
  assert.deepEqual(sessionTimelineState({...node, end: 100}, {}).range, [100, 101]);
  assert.equal(sessionTimelineState({...node, start: null}, {}), null);
  assert.equal(sessionTimelineState({...node, end: NaN}, {}), null);
});

test("hierarchy scope survives ranking and copied URLs", () => {
  const initial = readDelegationRoute(new URLSearchParams({delegation_scope: 'self'}));
  assert.equal(initial.mode, 'hierarchy');
  assert.equal(initial.scope, 'self');
  for (const mode of ['hierarchy', 'rank']) {
    const values = delegationRouteValues({...initial, mode});
    assert.equal(values.delegation_scope, 'self');
    assert.equal(readDelegationRoute(new URLSearchParams(values)).scope, 'self');
  }
});

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
  for (const [value, unit] of [[600, "count"], [450, "ms"], [12.34, "usd"]]) {
    assert.match(cellTitle(metric(value), spec("value", unit)), /^Definition for value\nobserved · 1\/1 /, "unabbreviated values need only definition and coverage");
  }
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

test("timeline drags clamp at canvas edges without moving the opposite resize endpoint",()=>{
  const full=[1000,11000],range=[3000,7000],canvas={left:80,width:1000};
  assert.equal(timelinePointerTime(-20,canvas,full),0,"raw horizontal positions survive outside the canvas");
  assert.equal(timelinePointerTime(1280,canvas,full),13000);
  assert.equal(timelinePointerTime(580,canvas,full),6000);
  for(const grab of [6990,7050])assert.deepEqual(timelineDragRange('end',range,full,grab,13000),[3000,11000]);
  for(const grab of [2950,3010])assert.deepEqual(timelineDragRange('start',range,full,grab,0),[1000,7000]);
  assert.deepEqual(timelineDragRange('end',range,full,7000,0),[3000,3001]);
  assert.deepEqual(timelineDragRange('start',range,full,3000,13000),[6999,7000]);
  assert.deepEqual(timelineDragRange('pan',range,full,5000,13000),[7000,11000]);
  assert.deepEqual(timelineDragRange('pan',range,full,5000,0),[1000,5000]);
  assert.deepEqual(timelineDragRange('select',range,full,7000,13000),[7000,11000]);
  assert.deepEqual(timelineDragRange('select',range,full,7000,0),[1000,7000]);
  assert.deepEqual(timelineDragRange('select',range,full,11000,11000),[10999,11000]);
});

test("timeline date headings follow local day changes and distinguish repeated DST times",()=>{
  const ticks=(from,to)=>timelineTickLabels([Date.parse(from),Date.parse(to)],4);
  const midnight=ticks('2026-09-25T05:50:00Z','2026-09-25T06:20:00Z');
  assert.deepEqual(midnight.map(tick=>!!tick.date),[true,true,false,false]);
  assert.notEqual(midnight[0].date,midnight[1].date);
  const utcMidnight=ticks('2026-09-24T23:50:00Z','2026-09-25T00:20:00Z');
  assert.deepEqual(utcMidnight.map(tick=>!!tick.date),[true,false,false,false]);
  const newYear=ticks('2027-01-01T06:50:00Z','2027-01-01T07:20:00Z');
  assert.deepEqual(newYear.map(tick=>!!tick.date),[true,true,false,false]);
  const repeated=timelineTickLabels([Date.parse('2026-11-01T07:30:00Z'),Date.parse('2026-11-01T08:30:00Z')],3);
  assert.deepEqual(repeated.map(tick=>!!tick.date),[true,false,false]);
  assert.notEqual(repeated[0].time,repeated[2].time,'zone names distinguish the repeated local clock time');
  assert.ok(midnight.every(tick=>tick.time&&Number.isFinite(tick.at)));
});

test("session tree branches connect visible parents and continue only toward remaining siblings", () => {
  const child = (id, parent_id, depth, child_count = 0) => ({ id, parent_id, depth, child_count });
  const rows = [child("root", undefined, 0, 2), child("a", "root", 1, 1), child("aa", "a", 2), child("b", "root", 1, 1), child("ba", "b", 2), child("other", undefined, 0)];
  const branches = timelineSessionBranches(rows);
  assert.equal(branches.get("root").children, true);
  assert.equal(branches.get("root").parentDepth, undefined);
  assert.deepEqual(branches.get("a"), {parentDepth:0,rails:[0],children:true});
  assert.deepEqual(branches.get("aa"), {parentDepth:1,rails:[0],children:false});
  assert.deepEqual(branches.get("b"), {parentDepth:0,rails:[],children:true});
  assert.deepEqual(branches.get("ba"), {parentDepth:1,rails:[],children:false});
  assert.deepEqual(branches.get("other"), {parentDepth:undefined,rails:[],children:false});
  assert.equal(timelineSessionBranches(rows.filter(row=>row.id!=="aa")).get("a").children,false,"folded children do not leave dangling branches");
  assert.equal(timelineSessionBranches(rows.slice(1,3)).get("a").parentDepth,undefined,"off-page parents do not invent a connection");
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


test("saved Parallel activity routes use Timeline while retaining navigation and zoom", () => {
  const state = { delegation_start: "100", delegation_end: "200", delegation_session: "child", delegation_collapsed: "parent", delegation_mode: "rank", delegation_sort: "cost", delegation_scope: "self", delegation_direction: "asc" };
  const legacy = readDelegationRoute(new URLSearchParams({ ...state, explorer: "parallel" }));
  const current = readDelegationRoute(new URLSearchParams({ ...state, explorer: "timeline" }));
  assert.deepEqual(legacy, current);
  assert.equal(legacy.tab, "timeline");
  assert.deepEqual(legacy.range, [100, 200]);
  assert.equal(legacy.selected, "child");
  assert.deepEqual(legacy.collapsed, ["parent"]);
  assert.equal(readDelegationRoute(new URLSearchParams()).tab, "table");
  assert.equal(readDelegationRoute(new URLSearchParams({ explorer: "unknown" })).tab, "table");
});


test("token observations distinguish minima, unallocated branches and missing prices",()=>{
  assert.equal(metricText(metric(116837,"reported-minimum")),"≥116.8K");
  assert.equal(tableMetricText(metric(116837,"reported-minimum")),"≥116.8K*");
  assert.equal(tableMetricText(metric(1000,"reported")),"1K");
  assert.equal(tableMetricText(metric(1000,"partial")),"1K+");
  assert.equal(tableMetricText(metric(null,"reported-minimum")),formatted(null));
  assert.equal(metricText(metric(1000,"partial")),"1K+");
  assert.equal(metricText(metric(1000,"unverified")),"1K?","a value reached through an unverified link is not shown as observed");
  assert.equal(metricText(metric(null,"unknown")),formatted(null));
  const child={self:{tokens:metric(116837,"reported-minimum")},token_attribution:{direct_tokens:0,accounted_tokens:0,unallocated_tokens:0,observed_minimum_tokens:116837}};
  assert.match(tokenAttributionText(child),/Reported minimum.*last request/);
  assert.match(tokenAttributionText(child),/breakdown not recorded.*API cost unavailable/);
  assert.match(cellTitle(child.self.tokens,spec('tokens','tokens')),/Reported minimum/);
  assert.match(tokenAttributionText({...child,token_attribution:{...child.token_attribution,unallocated_tokens:200000}}),/unallocated within this branch.*Self \+ children/);
  assert.equal(tokenAttributionText({...child,token_attribution:{...child.token_attribution,direct_tokens:200000}}),"","request evidence supersedes a smaller observation");
});
