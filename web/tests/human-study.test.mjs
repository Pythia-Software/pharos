import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { build } from "esbuild";
import { EMPTY_QUERY, applyQuery, decodeQuery, loadSchema } from "@pythia-software/query-table-core";

const bundle = await build({ entryPoints: ["src/human-study.ts"], bundle: true, write: false, platform: "node", format: "esm" });
const { buildHumanStudy, contributionSignals, createHumanStudyLoader, clearHumanStudyCache } = await import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString("base64")}`);
test.beforeEach(() => clearHumanStudyCache());
const schema = loadSchema(JSON.parse(readFileSync("../schemas/writing_messages.schema.json", "utf8")));
const now = Date.parse("2026-10-15T12:00:00Z"), day = 86400000;
const row = (id, text, extra = {}) => ({ id, text, sent_at: new Date(now - 1000).toISOString(), typed_words: text.split(/\s+/).length, pasted_words: 0, attachment_words: 0, conversation_id: id, work_id: id, repository_name: "repo", segments: [{ text, category: "typed" }], ...extra });

test("volume baseline averages thirty comparable trailing totals including zero windows", () => {
  const rows = [row("recent", "Text", { typed_words: 100, sent_at: new Date(now - day - 1000).toISOString() }), row("older", "Text", { typed_words: 200, sent_at: new Date(now - 8 * day - 1000).toISOString() })];
  const study = buildHumanStudy("volume", rows, [], 7, now, "UTC");
  assert.equal(study.value, 100);
  assert.equal(study.points.length, 31);
  assert.deepEqual(study.pointTimes, Array.from({ length: 31 }, (_, index) => now - (30 - index) * day), "hover dates use the same fixed endpoint as each calculation");
  assert.equal(study.baseline, 50);
  assert.equal(study.points[0], 0);
  assert.equal(study.points.at(-1), study.value);
});

test("volume without any historical writing is empty while a quiet current window retains its zero", () => {
  const empty = buildHumanStudy("volume", [], [], 7, now, "UTC");
  assert.equal(empty.value, undefined);
  const historical = buildHumanStudy("volume", [row("earlier", "Written", {sent_at: new Date(now - 9 * day).toISOString()})], [], 7, now, "UTC");
  assert.equal(historical.value, 0);
  assert.ok(historical.points.some(value => value > 0));
});

test("rolling averages divide totals at each endpoint and leave empty denominators as gaps", () => {
  const rows = [row("a", "A", { typed_words: 10, conversation_id: "one" }), row("b", "B", { typed_words: 90, conversation_id: "one" }), row("c", "C", { typed_words: 200, conversation_id: "two" })];
  const messages = buildHumanStudy("message-average", rows, [], 7, now, "UTC"), conversations = buildHumanStudy("conversation-average", rows, [], 7, now, "UTC");
  assert.equal(messages.value, 100);
  assert.equal(conversations.value, 150);
  assert.equal(messages.points[0], null);
  assert.equal(messages.baseline, undefined, "no invented zero average without messages");
});

test("fixed conversation bands preserve counts and keep an extreme outlier in the open-ended band", () => {
  const totals = [49, 50, 199, 200, 500, 999, 1000, 1999, 2000, 100000];
  const rows = totals.map((typed_words, index) => row(String(index), "Writing", { typed_words }));
  rows[0].typed_words = 20;
  rows.push(row("continued", "More writing", { conversation_id: "0", typed_words: 29 }));
  rows.push(row("machine", "Generated", { typed_words: 0 }));
  const profile = buildHumanStudy("conversation-lengths", rows, [], 7, now, "UTC");
  assert.equal(profile.kind, "profile");
  assert.deepEqual(profile.rows.map(row => row.count), [1, 2, 1, 2, 2, 2]);
  assert.equal(profile.rows.reduce((sum, row) => sum + row.count, 0), 10);
  assert.equal(profile.rows.at(-1).label, "2,000+ words");
  assert.equal(profile.rows.at(-1).value, .2);
});

test("concrete feedback trend uses trailing message ratios and keeps percentage units", () => {
  const rows = [row("feedback", "Rename `Count` to `Messages`."), row("other", "Here is some ordinary writing."), row("older", "Replace 2 fields because they repeat.", { sent_at: new Date(now - 8 * day).toISOString() }), row("clipped", "Replace 3 fields.", { text_truncated: true })];
  const study = buildHumanStudy("specific-feedback", rows, [], 7, now, "UTC");
  assert.equal(study.kind, "trend");
  assert.equal(study.format, "percent");
  assert.equal(study.value, .5);
  assert.equal(study.points.length, 31);
  assert.deepEqual(study.pointTimes, Array.from({ length: 31 }, (_, index) => now - (30 - index) * day), "hover dates use the same fixed endpoint as each calculation");
  assert.equal(study.points.at(-1), .5);
  assert.ok(study.points.includes(1));
  assert.equal(study.points[0], null);
  assert.ok(study.baseline >= 0 && study.baseline <= 1);
  assert.match(study.sampleLabel, /1 of 2/);
});

test("workspace average joins separate work records at repository plus directory identity", () => {
  const rows = [row("a", "A", { typed_words: 40 }), row("b", "B", { typed_words: 60 }), row("c", "C", { typed_words: 900 })];
  const workspaces = [{ id: "a", location: "/repo/worktree/", repository_name: "repo" }, { id: "b", location: "/repo/worktree", repository_name: "repo" }];
  const study = buildHumanStudy("workspace-average", rows, workspaces, 7, now, "UTC");
  assert.equal(study.value, 100);
  assert.match(study.sampleLabel, /1 workspace directories · 2 conversations/);
  assert.match(study.detail, /1 written messages lack/);
  const separated = buildHumanStudy("workspace-average", rows.slice(0, 2), [{ ...workspaces[0] }, { ...workspaces[1], repository_name: "other repo" }], 7, now, "UTC");
  assert.equal(separated.value, 50, "same path across different repositories stays separate");
});

test("contribution cues use authored spans and exact continuation phrases, not message length", () => {
  assert.equal(contributionSignals(row("a", "Please continue.")).continuation, true);
  assert.equal(contributionSignals(row("b", "Yes, but replace 3 columns.")).continuation, false);
  assert.equal(contributionSignals(row("c", "Rename `Count` to `Messages`.")).feedback, true, "a short, specific correction qualifies");
  const injected = row("d", "Please continue. MUST change 3 fields because it is wrong.", { segments: [{ text: "Please continue.", category: "typed" }, { text: "MUST change 3 fields because it is wrong.", category: "harness" }] });
  assert.equal(contributionSignals(injected).feedback, false);
  assert.equal(contributionSignals(injected).continuation, true);
  const clipped = row("e", "Replace 3 fields.", { text_truncated: true });
  assert.equal(contributionSignals(clipped).feedback, false);
  const study = buildHumanStudy("contribution-patterns", [row("nudge", "Please continue."), row("specific", "Rename `Count` to `Messages`.")], [], 7, now, "UTC");
  assert.deepEqual(study.evidence.map(evidence => evidence.label), ["Brief continuation", "Concrete feedback cue"]);
});

test("supplied-format percentages include context-only messages, exclude automation, and allow overlap", () => {
  const image = row("image", "", { typed_words: 0, attachment_words: 1, segments: [{ text: "@⟦screenshot.png⟧(.context/attachments/x/screenshot.png)", category: "attachment" }] });
  const pasted = row("paste", "Use this", { pasted_words: 20, segments: [{ text: "Use this", category: "typed" }, { text: "some pasted example code", category: "pasted" }, { text: "[fixture](fixtures/example.csv)", category: "typed" }] });
  const automated = row("auto", "", { typed_words: 0, segments: [{ text: "(image attachment) http://example.test/f.txt", category: "harness" }] });
  const study = buildHumanStudy("examples", [image, pasted, row("plain", "Just some writing"), automated], [], 7, now, "UTC");
  assert.equal(study.value, 2 / 3);
  assert.deepEqual(study.rows.map(row => row.count), [1, 1, 1]);
  assert.equal(contributionSignals(image).file, false, "an image marker does not also count as a file reference");
  assert.equal(contributionSignals(automated).image, false);
  assert.equal(study.evidence.length, 2);
  assert.ok(study.evidence.every(evidence => evidence.url.includes("q_writing_messages=")));
  assert.match(study.sampleLabel, /3 assessed contributions/);
});

test("hour-of-day writing combines repeated DST hours and preserves the word total", () => {
  const rows = [row("first", "A", { typed_words: 12, sent_at: "2026-11-01T07:30:00Z" }), row("second", "B", { typed_words: 8, sent_at: "2026-11-01T08:30:00Z" })];
  const study = buildHumanStudy("hours", rows, [], 7, Date.parse("2026-11-02T12:00:00Z"), "America/Denver");
  assert.equal(study.rows.length, 24);
  assert.equal(study.rows[1].value, 20);
  assert.equal(study.rows.reduce((sum, row) => sum + row.value, 0), 20);
  assert.equal(study.rows[2].value, 0);
});

test("example percentages use each shared workspace directory's contribution denominator", () => {
  const rows = [row("a", "Here is context", { pasted_words: 20 }), row("b", "My own direction"), row("c", "Another direction")];
  const study = buildHumanStudy("examples", rows, [{ id: "a", location: "/repo/tree", repository_name: "repo" }, { id: "b", location: "/repo/tree", repository_name: "repo" }, { id: "c", location: "/repo/other", repository_name: "repo" }], 7, now, "UTC");
  assert.deepEqual(study.workspaceRows.map(row => [row.location, row.count, row.total, row.value]), [["/repo/tree", 1, 2, .5], ["/repo/other", 0, 1, 0]]);
  assert.ok(study.workspaceRows[0].url.includes("q_writing_messages="));
});

for (const ids of ["work", "workspace", "mixed"]) test(`directory drill-down matches its contribution totals with ${ids} IDs`, () => {
  const rows = [
    row("work", "Writing", { work_id: "work-tree" }),
    row("workspace", "Context", { work_id: null, workspace_id: "workspace-tree", pasted_words: 20 }),
    row("both", "More context", { work_id: "work-tree", workspace_id: "other-tree", pasted_words: 5 }),
    row("fallback", "More writing", { work_id: undefined, workspace_id: "workspace-tree" }),
  ].filter(message => ids === "mixed" || (ids === "work" ? message.work_id != null : message.work_id == null));
  const excluded = [
    row("conflict", "Other directory", { work_id: "other-tree", workspace_id: "workspace-tree" }),
    row("old", "Old context", { work_id: null, workspace_id: "workspace-tree", sent_at: new Date(now - 8 * day).toISOString() }),
    row("automated", "", { typed_words: 0, work_id: "work-tree", workspace_id: "workspace-tree" }),
  ];
  const workspaces = [
    { id: "work-tree", location: "/repo/tree", repository_name: "repo" },
    { id: "workspace-tree", location: "/repo/tree", repository_name: "repo" },
    { id: "other-tree", location: "/repo/other", repository_name: "repo" },
  ];
  const study = buildHumanStudy("examples", [...rows, ...excluded], workspaces, 7, now, "UTC");
  const directory = study.workspaceRows.find(group => group.location === "/repo/tree");
  const query = decodeQuery(new URL(directory.url, "http://study.test").searchParams.get("q_writing_messages"));
  const actual = applyQuery([...rows, ...excluded], query, schema, { now }).rows;
  assert.deepEqual(actual.map(message => message.id).sort(), rows.map(message => message.id).sort());
  assert.equal(actual.length, directory.total);
  assert.equal(buildHumanStudy("examples", actual, workspaces, 7, now, "UTC").value, directory.value);
});

for (const firstDays of [7, 30]) test(`mixed-window directory loads stay complete when the ${firstDays}-day evidence finishes first`, async t => {
  const prior = global.fetch, calls = [];
  let release;
  const blocked = new Promise(resolve => { release = resolve; });
  t.after(() => { release(); global.fetch = prior; });
  const rows = [
    row("recent", "Context", { typed_words: 100, pasted_words: 20 }),
    row("older", "Earlier writing", { typed_words: 900, sent_at: new Date(now - 45 * day).toISOString() }),
  ];
  const workspaces = rows.map(message => ({ id: message.work_id, location: `/repo/${message.work_id}`, repository_name: "repo" }));
  global.fetch = async (url, init) => {
    const request = JSON.parse(init.body);
    calls.push({ url, request });
    if (url.includes("writing_messages")) {
      const from = Date.parse(request.where[0].value), days = (now - from) / day - 30;
      if (days !== firstDays) await blocked;
      const selected = rows.filter(message => Date.parse(message.sent_at) >= from);
      return Response.json({ total: selected.length, rows: selected });
    }
    assert.ok(url.includes("library"));
    const ids = request.where[0].any.map(term => term.value);
    const selected = workspaces.filter(workspace => ids.includes(workspace.id));
    return Response.json({ total: selected.length, rows: selected });
  };
  const load = createHumanStudyLoader(now, schema);
  const first = load(firstDays === 7 ? "examples" : "workspace-average", EMPTY_QUERY, firstDays);
  const second = load(firstDays === 7 ? "workspace-average" : "examples", EMPTY_QUERY, firstDays === 7 ? 30 : 7);
  const sameWindow = load("workspace-average", EMPTY_QUERY, firstDays);
  const initial = await first;
  await sameWindow;
  release();
  const following = await second;
  const wide = firstDays === 30 ? initial : following;
  const expected = buildHumanStudy("workspace-average", rows, workspaces, 30, now, "UTC");
  assert.equal(wide.points[0], 900, "older directories remain mapped in the historical windows");
  assert.deepEqual(wide.points, expected.points);
  assert.equal(wide.baseline, expected.baseline);
  assert.equal(calls.filter(call => call.url.includes("library")).length, 2);
  const cached = await load("workspace-average", EMPTY_QUERY, 30);
  assert.deepEqual(cached.points, expected.points);
  assert.equal(calls.length, 4, "cards using the same window still share evidence and directory lookups");
});

test("the loader shares bounded paged evidence and honors additional source filters", async t => {
  const prior = global.fetch, calls = [];
  const rows = Array.from({ length: 501 }, (_, index) => row(String(index), "Some words", { typed_words: index === 500 ? 100 : 2, repository_name: index === 500 ? "target" : "other" }));
  global.fetch = async (_url, init) => {
    const request = JSON.parse(init.body); calls.push(request);
    return new Response(JSON.stringify({ total: rows.length, rows: rows.slice(request.offset, request.offset + request.limit) }), { status: 200 });
  };
  t.after(() => { global.fetch = prior; });
  const load = createHumanStudyLoader(now, schema);
  const query = { ...EMPTY_QUERY, where: [{ field: "repository_name", op: "=", value: "target" }] };
  const [volume, mean] = await Promise.all([load("volume", query, 7), load("message-average", query, 7)]);
  assert.equal(volume.value, 100);
  assert.equal(mean.value, 100);
  assert.equal(calls.length, 2);
  assert.equal(calls[0].where[0].value, new Date(now - 37 * day).toISOString());
  assert.equal(calls[0].where[1].value, new Date(now).toISOString());
  const cached = await createHumanStudyLoader(now + 1000, schema)("volume", query, 7);
  assert.equal(cached.pointTimes.at(-1), now, "cached points keep their original dates rather than a new render time");
  assert.equal(calls.length, 2, "completed evidence is reused briefly without sharing pending abort signals");
});

test("oversized or incomplete evidence produces an error instead of a partial score", async t => {
  const prior = global.fetch;
  t.after(() => { global.fetch = prior; });
  global.fetch = async () => new Response(JSON.stringify({ total: 20001, rows: [] }), { status: 200 });
  await assert.rejects(createHumanStudyLoader(now, schema)("volume", EMPTY_QUERY, 7), /exceeds 20,000/);
  global.fetch = async () => new Response(JSON.stringify({ total: 2, rows: [row("a", "Only one")] }), { status: 200 });
  await assert.rejects(createHumanStudyLoader(now, schema)("volume", EMPTY_QUERY, 7), /incomplete/);
});
