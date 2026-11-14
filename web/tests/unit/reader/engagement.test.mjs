// Engagement labels in the conversation reader (reader-model.js:
// messageEngagementView, engagementSegments, engagementMask, engagementAtMark,
// engagementLayers, engagementDrawn, engagementTipView, writingMarkIndexes, engagementSnap,
// engagementTimeline, engagementHoverTints): spans of what a
// person typed, underlined by dimension. The texts here are made up.
import { test } from "node:test";
import assert from "node:assert/strict";
import displayRules from "../../../../testdata/engagement-display.json";
import { reader } from "../helpers/transcript.mjs";
import { plain } from "../helpers/classic-script.mjs";

const typed = "Don't open it yet. ", pasted = "ERROR importer.go:42", rest = " First show me the diff 🙂 please.";
const text = typed + pasted + rest;
const bytes = value => new TextEncoder().encode(value).length;
const range = part => { const start = text.indexOf(part); assert.notEqual(start, -1, part); return { start, end: start + part.length }; };
function message(engagement) {
  return { id: "m1", role: "user", kind: "message", text, engagement,
    authorship: { spans: [{ category: "typed", start: 0, end: bytes(typed) }, { category: "pasted", start: bytes(typed), end: bytes(typed + pasted) },
      { category: "typed", start: bytes(typed + pasted), end: bytes(text) }] } };
}
const spans = [
  { label: "bounds.prohibition", dimension: "bounds", score: 0.91, ...range("Don't open it yet") },
  { label: "bounds.timing_hold", dimension: "bounds", score: 0.72, ...range("open it yet") },
  { label: "intent.task", dimension: "intent", score: 0.8, ...range("open it") },
  // The model's span never covers pasted text, but a stale one could.
  { label: "acceptance.verification", dimension: "acceptance", score: 0.7, start: range("yet").start, end: range("show me the diff 🙂").end },
  { label: "mood.playful", dimension: "mood", score: 0.75, ...range("🙂 please") },
];

test("engagement spans are read only for the stored text of a person's message, with a slot per dimension", () => {
  const view = plain(reader.messageEngagementView(message(spans)));
  assert.deepEqual(view.map(span => [span.label, span.dimensionName, span.name, span.className]), [
    ["bounds.prohibition", "Bounds", "Prohibition", "eng-slot-2"], ["bounds.timing_hold", "Bounds", "Timing hold", "eng-slot-2"],
    ["intent.task", "Intent", "Task", "eng-slot-0"], ["acceptance.verification", "Verification", "Verification", "eng-slot-3"],
    ["mood.playful", "Mood", "Playful", view[4].className]]);
  // A dimension the reader does not know still gets one of the slots, the same one each time.
  assert.match(view[4].className, /^eng-slot-[0-8]$/);
  assert.equal(reader.engagementSlot("mood"), view[4].slot);
  // A slot is only a colour. A tenth dimension shares a known one's colour, never its Key chip or its hide: each is keyed by dimension id.
  const mate = ["intent", "grounding", "bounds", "acceptance", "rights", "evaluation", "inquiry", "authorship", "register"][view[4].slot];
  const crowded = message([{ label: "mood.playful", dimension: "mood", score: 0.9, ...range("open it") }, { label: mate + ".kind", dimension: mate, score: 0.8, ...range("open it yet") }]);
  const pair = reader.messageEngagementView(crowded), stretch = reader.engagementAtMark(pair, reader.engagementSegments(pair), "engagement eng-at-0");
  assert.equal(pair[0].className, pair[1].className, "the palette has nine colours");
  assert.deepEqual(plain(pair.map(span => span.key)), ["engagement:mood", "engagement:" + mate]);
  assert.deepEqual(plain(reader.engagementLayers(stretch).map(layer => layer.dimension)).sort(), ["mood", mate].sort(), "two dimensions of one colour are two underlines");
  assert.deepEqual(plain(reader.engagementLayers(stretch, new Set(["engagement:" + mate])).map(layer => layer.dimension)), [mate], "hiding the new dimension leaves its colour-mate");
  assert.deepEqual(plain(reader.engagementTipView(stretch, new Set(["engagement:mood"])).map(row => row.label)), ["mood.playful"]);
  const chips = plain(reader.readerHighlightOptions({ engagement: { messages_read: 1 } }, [crowded])).find(option => option.id === "engagement").legend;
  assert.deepEqual(chips.map(chip => [chip.name, chip.key]).sort(), [["Mood", "mood"], [reader.engagementDimensionName(mate), mate]].sort(), "each has a Key chip of its own");
  assert.deepEqual(plain(reader.engagementTimeline([crowded])).map(event => [event.dimension, event.name.charAt(0)]).sort(), [["mood", "M"], [mate, reader.engagementDimensionName(mate).charAt(0)]].sort(), "and a timeline letter from its own name");
  // Before two visible dimensions share a colour, the new one takes a palette slot none of them uses.
  assert.deepEqual(plain([...reader.engagementSlots(["mood", "intent", "bounds", "aside"])]), [["aside", 1], ["bounds", 2], ["intent", 0], ["mood", 3]]);
  const roomy = [message([{ label: "mood.playful", dimension: "mood", score: 0.9, ...range("open it") }]), message([{ label: "intent.task", dimension: "intent", score: 0.9, ...range("open it") }])];
  reader.engagementAssignSlots(roomy);
  assert.deepEqual(plain(roomy.map(item => reader.messageEngagementView(item)[0].className)), ["eng-slot-1", "eng-slot-0"]);
  assert.equal(new Set(reader.engagementSlots(["a", "b", "c", "d", "e", "f", "g", "h", "i", "j"]).values()).size, 9, "ten unknown dimensions use all nine colours before any two share");
  // A hide saved by colour slot, from when the nine known dimensions were the only ones, reads as its dimension; other keys pass through.
  assert.deepEqual(plain(reader.readerHighlightHidden(["writing", "engagement:eng-slot-2", "engagement:bounds", "engagement:mood", "events:event-friction", "engagement:eng-slot-42"])),
    ["writing", "engagement:bounds", "engagement:mood", "events:event-friction", "engagement:eng-slot-42"]);
  assert.equal(reader.messageEngagementView({ ...message(spans), role: "assistant" }), null);
  assert.equal(reader.messageEngagementView(message(spans), "the prompt without its wrapper"), null, "offsets apply to the stored text only");
  assert.equal(reader.messageEngagementView(message([])), null);
  const invalid = [{ label: "intent.task", start: 4, end: 4 }, { label: "intent.task", start: -1, end: 3 }, { label: "intent.task", start: 0, end: text.length + 1 }, { label: "", start: 0, end: 3 }, { label: "intent.task", start: 0.5, end: 3 }];
  assert.equal(reader.messageEngagementView(message(invalid)), null);
});

test("overlapping spans split into segments, and only human writing is marked", () => {
  const item = message(spans), view = reader.messageEngagementView(item), segments = plain(reader.engagementSegments(view));
  const at = part => segments.find(segment => text.slice(segment.start, segment.end) === part);
  assert.deepEqual(segments.map(segment => text.slice(segment.start, segment.end)).slice(0, 4), ["Don't ", "open it", " ", "yet"]);
  assert.deepEqual(at("Don't ").spans, [0]);
  assert.deepEqual(at("open it").spans, [0, 1, 2]);
  assert.deepEqual(at("yet").spans, [0, 1, 3]);
  for (let index = 1; index < segments.length; index++) assert.ok(segments[index].start >= segments[index - 1].end, "segments do not overlap");

  const writing = reader.humanWritingMask(item), mask = reader.engagementMask(text, view, writing);
  assert.equal(mask.length, text.length);
  const pasteStart = text.indexOf(pasted);
  for (let index = 0; index < text.length; index++) {
    const className = reader.markClass(mask[index]), inside = segments.findIndex(segment => index >= segment.start && index < segment.end);
    if (writing[index] !== "1") assert.equal(mask[index], writing[index], `text the person did not type stays unmarked at ${index}`);
    else if (inside < 0) assert.equal(className, "human-written");
    else assert.equal(className, `human-written engagement eng-at-${inside}`);
  }
  assert.equal(mask.slice(pasteStart, pasteStart + pasted.length), "0".repeat(pasted.length));
  // Markdown keeps each segment a mark of its own, in order.
  const marks = reader.markdownText(text, mask).filter(part => typeof part !== "string");
  assert.deepEqual(plain(marks.slice(0, 4).map(mark => [mark.className, mark.children[0]])), [
    ["human-written engagement eng-at-0", "Don't "], ["human-written engagement eng-at-1", "open it"], ["human-written engagement eng-at-2", " "], ["human-written engagement eng-at-3", "yet"]]);
  // No writing, no marks; and an event's target over an engagement mark stays human writing.
  assert.equal(reader.engagementMask(text, view, ""), "");
  assert.equal(reader.engagementMask(text, null, writing), writing);
  const target = reader.eventTargetMask(text, [{ range: [0, 5], links: ["e1"] }], mask);
  assert.equal(reader.markClass(target[0]), "event-target event-target-at-0 human-written engagement eng-at-0");
});

test("text an event links back to keeps its engagement marks: both sets of boundaries cut it, and an annotation stays whole", () => {
  const item = message(spans), view = reader.messageEngagementView(item), segments = reader.engagementSegments(view);
  const writing = reader.humanWritingMask(item), mask = reader.engagementMask(text, view, writing);
  const segmentOf = part => segments.findIndex(segment => text.slice(segment.start, segment.end) === part);
  const pieces = targets => plain(reader.markdownText(text, reader.eventTargetMask(text, targets.map((part, index) => ({ range: [range(part).start, range(part).end], links: ["e" + index] })), mask))
    .filter(part => typeof part !== "string").map(mark => [mark.className, mark.children[0]]));
  const both = (target, part) => `event-target event-target-at-${target} human-written engagement eng-at-${segmentOf(part)}`, only = part => `human-written engagement eng-at-${segmentOf(part)}`;
  // A target inside one annotation's stretch: the stretch is cut around it, every piece naming the same segment.
  assert.deepEqual(pieces(["open"]).slice(0, 4), [[only("Don't "), "Don't "], [both(0, "open it"), "open"], [only("open it"), " it"], [only(" "), " "]]);
  // Annotations inside a target: each segment is a mark of both; typed text with no annotation is the target's alone, as is pasted text.
  const whole = pieces([text]);
  assert.deepEqual(whole.slice(0, 4), [[both(0, "Don't "), "Don't "], [both(0, "open it"), "open it"], [both(0, " "), " "], [both(0, "yet"), "yet"]]);
  assert.ok(whole.some(([className, part]) => className === "event-target event-target-at-0" && part.includes(pasted)), "pasted text under the target carries no engagement");
  assert.ok(whole.every(([className]) => className.startsWith("event-target event-target-at-0")));
  // Staggered: the target starts inside an annotation and ends past it.
  const staggered = pieces(["it yet. ERROR"]);
  assert.deepEqual(staggered.slice(0, 5), [[only("Don't "), "Don't "], [only("open it"), "open "], [both(0, "open it"), "it"], [both(0, " "), " "], [both(0, "yet"), "yet"]]);
  assert.ok(staggered.some(([className, part]) => className === "event-target event-target-at-0" && part === "ERROR"), "pasted text past the annotation is the target's alone");
  // Two targets over one annotation name it apart.
  assert.deepEqual(pieces(["Don't", "yet"]).filter(([className]) => className.startsWith("event-target")), [[both(0, "Don't "), "Don't"], [both(1, "yet"), "yet"]]);
  // A target that splits an annotation leaves it one annotation: hovering either piece tints every segment of it, and its marks read as its whole text.
  const prohibition = view.find(span => span.label === "bounds.prohibition"), marks = pieces(["open"]);
  const lit = className => plain(reader.engagementHoverTints(view, segments, className, new Set([prohibition.key]))).map(tint => tint.segment);
  assert.deepEqual(lit(both(0, "open it")), lit(only("Don't ")));
  assert.deepEqual(lit(both(0, "open it")), [segmentOf("Don't "), segmentOf("open it"), segmentOf(" "), segmentOf("yet")]);
  const names = new Set(reader.engagementSpanMarks(segments, prohibition.start, prohibition.end));
  const covered = marks.filter(([className]) => className.split(" ").some(name => names.has(name)));
  assert.equal(reader.engagementCopyText(covered.map(([, part]) => ({ text: part, gap: "" }))), "Don't open it yet");
  assert.deepEqual(plain(reader.engagementAtMark(view, segments, both(0, "open it"))).map(span => span.label).sort(), plain(reader.engagementAtMark(view, segments, only("open it"))).map(span => span.label).sort());
  // Past the targets and segments a flag can name, the target alone is marked, as before.
  const far = reader.eventTargetMask(text, Array.from({ length: 33 }, (_, index) => ({ range: index === 32 ? [0, 5] : [text.length, text.length], links: ["e" + index] })), mask);
  assert.equal(reader.markClass(far[0]), "event-target event-target-at-32 human-written");
});

test("a popover row copies its own annotation: typed text only, joined across what lies between its marks", () => {
  const item = message(spans), view = reader.messageEngagementView(item), segments = reader.engagementSegments(view);
  const under = reader.engagementAtMark(view, segments, `eng-at-${segments.findIndex(segment => text.slice(segment.start, segment.end) === "open it")}`);
  // Each row keeps the range of the annotation it stands for, which may reach further than the stretch under the pointer.
  const rows = plain(reader.engagementTipView(under));
  assert.deepEqual(rows.map(row => [row.label, text.slice(row.start, row.end)]).sort(), [["bounds.prohibition", "Don't open it yet"], ["bounds.timing_hold", "open it yet"], ["intent.task", "open it"]]);
  assert.deepEqual(plain(reader.engagementSpanMarks(segments, rows.find(row => row.label === "intent.task").start, rows.find(row => row.label === "intent.task").end)).length, 1);
  // Touching marks join as they are; a pasted block or a chip between two becomes one space; blanks are not doubled.
  assert.equal(reader.engagementCopyText([{ text: "Don't ", gap: "" }, { text: "open it", gap: "" }]), "Don't open it");
  assert.equal(reader.engagementCopyText([{ text: "show me", gap: "" }, { text: "the diff", gap: "ERROR importer.go:42" }]), "show me the diff");
  assert.equal(reader.engagementCopyText([{ text: "yet. ", gap: "" }, { text: " First", gap: "ERROR" }, { text: "", gap: "x" }]), "yet. First");
  assert.equal(reader.engagementCopyText([]), "");
});

test("the display threshold and the labels never shown are the ones the Timeline's payload is cut to", () => {
  // engagement.go repeats both to leave spans the page cannot show out of a lane; TestEngagementDisplayRules compares its copy with the same file.
  assert.equal(reader.ENGAGEMENT_MIN_SCORE, displayRules.min_score);
  assert.deepEqual([...reader.ENGAGEMENT_UNDISPLAYED].sort(), displayRules.undisplayed);
});

test("a mark stacks one underline per dimension and names its labels, likeliest first", () => {
  const view = reader.messageEngagementView(message(spans)), segments = reader.engagementSegments(view);
  const index = segments.findIndex(segment => text.slice(segment.start, segment.end) === "open it");
  const under = reader.engagementAtMark(view, segments, `human-written engagement eng-at-${index}`);
  assert.deepEqual(plain(reader.engagementLayers(under).map(layer => [layer.className, layer.dimensionName, layer.score])), [["eng-slot-0", "Intent", 0.8], ["eng-slot-2", "Bounds", 0.91]]);
  assert.deepEqual(plain(reader.engagementTipView(under)).map(({ start, end, ...row }) => row), [
    { label: "bounds.prohibition", title: "Bounds · Prohibition", className: "eng-slot-2", score: 0.91 },
    { label: "intent.task", title: "Intent · Task", className: "eng-slot-0", score: 0.8 },
    { label: "bounds.timing_hold", title: "Bounds · Timing hold", className: "eng-slot-2", score: 0.72 }]);
  // At most two underlines are drawn: the best-scoring dimensions that are on.
  const three = [{ dimension: "intent", slot: 0, score: 0.6 }, { dimension: "bounds", slot: 2, score: 0.9 }, { dimension: "evaluation", slot: 5, score: 0.6 }];
  assert.deepEqual([...reader.engagementDrawn(reader.engagementLayers(under))].sort(), ["bounds", "intent"]);
  assert.deepEqual([...reader.engagementDrawn(three)].sort(), ["bounds", "intent"], "a tie goes to the lower slot");
  assert.deepEqual([...reader.engagementDrawn(three, 1)], ["bounds"]);
  assert.deepEqual([...reader.engagementDrawn([])], []);
  // The Key hides by dimension: a hidden one leaves the layers (so the next dimension can show) and the popover.
  assert.deepEqual(plain(reader.engagementLayers(under, new Set(["engagement:intent"])).map(layer => layer.className)), ["eng-slot-0"]);
  assert.deepEqual(plain(reader.engagementLayers(under, new Set())), []);
  assert.deepEqual(plain(reader.engagementTipView(under, new Set(["engagement:intent"])).map(row => row.label)), ["intent.task"]);
  assert.deepEqual(plain(reader.engagementAtMark(view, segments, "human-written")), []);
  assert.deepEqual(plain(reader.engagementAtMark(view, segments, "human-written engagement eng-at-999")), []);
  // The same label twice under one mark is listed once, with its best score.
  const twice = [{ ...under[0], score: 0.5 }, under[0]];
  assert.deepEqual(plain(reader.engagementTipView(twice).map(row => [row.label, row.score])), [["bounds.prohibition", 0.91]]);
});

test("the Engagement highlight is offered once the pass has read the conversation, with a legend of its dimensions", () => {
  const item = message(spans), reply = { role: "assistant", kind: "message", text: "Here is the diff." };
  assert.equal(plain(reader.readerHighlightOptions({}, [item, reply])).some(option => option.id === "engagement"), false, "a library without the pass offers nothing");
  const options = plain(reader.readerHighlightOptions({ engagement: { messages_read: 1 } }, [item, reply]));
  assert.deepEqual(options.map(option => [option.id, option.field, option.available]), [["writing", "reader_writing", true], ["engagement", "reader_engagement", true]]);
  assert.deepEqual(options[1].legend.slice(0, 3), [{ name: "Intent", className: "eng-slot-0", key: "intent" }, { name: "Bounds", className: "eng-slot-2", key: "bounds" }, { name: "Verification", className: "eng-slot-3", key: "acceptance" }]);
  assert.equal(options[1].legend.length, 4);
  const shown = reader.readerHighlightsShown(reader.readerHighlightOptions({ engagement: { messages_read: 1 } }, [item, reply]), field => field === "reader_engagement" ? false : null);
  assert.deepEqual([...shown], ["writing"]);
  // Read, nothing found: listed as unavailable; awaiting another read: says so.
  const none = plain(reader.readerHighlightOptions({ engagement: { messages_read: 1, pending: true } }, [message(undefined), reply]));
  assert.deepEqual(none.map(option => [option.id, option.available]), [["writing", true], ["engagement", false]]);
  assert.notEqual(none[1].note, "");
  assert.equal(plain(reader.readerKeyPresetValues("content")).reader_engagement, false);
});

test("marks that engagement splits out of one stretch of writing keep that stretch's number", () => {
  const mark = (className, joined = false) => ({ className, joined });
  // Without engagement: every mark its own number.
  assert.deepEqual(plain(reader.writingMarkIndexes([mark("human-written"), mark("human-written"), mark("event-target event-target-at-0 human-written", true)])), [0, 1, 2]);
  // One stretch split in three, another stretch, then a target beside a split.
  assert.deepEqual(plain(reader.writingMarkIndexes([
    mark("human-written"), mark("human-written engagement eng-at-0", true), mark("human-written", true),
    mark("human-written engagement eng-at-1"), mark("human-written engagement eng-at-2", true),
    mark("event-target event-target-at-0 human-written", true), mark("human-written engagement eng-at-3", true)])), [0, 0, 0, 1, 1, 2, 3]);
  // Engagement dividing an event's target leaves the target one stretch; two targets side by side stay two.
  assert.deepEqual(plain(reader.writingMarkIndexes([
    mark("human-written engagement eng-at-0"), mark("event-target event-target-at-0 human-written engagement eng-at-0", true), mark("event-target event-target-at-0 human-written engagement eng-at-1", true),
    mark("event-target event-target-at-0 human-written", true), mark("event-target event-target-at-1 human-written engagement eng-at-2", true), mark("human-written", true)])), [0, 1, 1, 1, 2, 3]);
  // A cited and annotated message: every typed character keeps its stretch's number with engagement or without, so a saved hide stays on its text.
  const cited = message(spans), view = reader.messageEngagementView(cited), writing = reader.humanWritingMask(cited);
  const targets = ["it yet. ERROR", "show me the diff"].map((part, index) => ({ range: [range(part).start, range(part).end], links: ["e" + index] }));
  const numbers = mask => {
    const parts = reader.markdownText(text, reader.eventTargetMask(text, targets, mask)), marks = [];
    parts.forEach((part, at) => { if (typeof part !== "string" && part.className.split(" ").includes("human-written")) marks.push({ className: part.className, text: part.children[0], joined: marks.length > 0 && parts[at - 1] === marks.at(-1).part, part }); });
    const indexes = plain(reader.writingMarkIndexes(marks));
    return marks.flatMap((item, at) => Array.from(item.text, () => indexes[at]));
  };
  const withEngagement = numbers(reader.engagementMask(text, view, writing)), without = numbers(writing);
  assert.ok(new Set(without).size > 2, "the message has several stretches of writing");
  assert.deepEqual(withEngagement, without);
});

test("the page shows a label only from the display threshold up", () => {
  assert.equal(reader.ENGAGEMENT_MIN_SCORE, 0.7);
  const weak = [{ label: "intent.task", dimension: "intent", score: 0.69, ...range("open it") }, { label: "bounds.prohibition", dimension: "bounds", score: 0.7, ...range("Don't open it yet") },
    { label: "inquiry.question", dimension: "inquiry", score: 0.5, ...range("show me the diff") }];
  const item = message(weak), reply = { role: "assistant", kind: "message", text: "Here is the diff." };
  assert.deepEqual(plain(reader.messageEngagementView(item)).map(span => span.label), ["bounds.prohibition"]);
  // The Key and the timeline list only what the page shows.
  const option = plain(reader.readerHighlightOptions({ engagement: { messages_read: 1 } }, [item, reply])).find(option => option.id === "engagement");
  assert.deepEqual(option.legend, [{ name: "Bounds", className: "eng-slot-2", key: "bounds" }]);
  assert.deepEqual(plain(reader.engagementTimeline([item, reply])).map(event => event.title), ["Bounds · Prohibition"]);
  // Nothing at the threshold: the conversation has nothing to show.
  assert.equal(reader.messageEngagementView(message(weak.filter(span => span.score < 0.7))), null);
  // A caller can ask for another threshold; the stored spans are all still there.
  assert.equal(reader.messageEngagementView(item, text, reader.humanWritingMask(item), 0.5).length, 3);
});

test("a span's edges move to word boundaries, and part of one word is not a span", () => {
  const line = "hmmm, don't reformat anything else";
  const snap = (part, from = line.indexOf(part)) => { const result = plain(reader.engagementSnap(line, from, from + part.length)); return result && line.slice(result[0], result[1]); };
  assert.equal(snap("m", 3), null, "one letter of hmmm");
  assert.equal(snap("mmm"), null, "most of a word is still a fragment");
  assert.equal(snap("hmmm"), "hmmm", "the whole word stays");
  assert.equal(snap("hmmm, "), "hmmm,", "blanks at the edges are dropped");
  assert.equal(snap("on't reformat anyth"), "don't reformat anything", "an edge inside a word moves to the nearer end, here outward");
  assert.equal(snap("t reformat a"), "reformat", "and here inward");
  assert.equal(snap("format anything"), "reformat anything");
  assert.equal(snap(" ", line.indexOf(" ")), null);
  // Words stop where the typed text stops.
  const typed = at => at >= line.indexOf("reformat") + 2;
  assert.deepEqual(plain(reader.engagementWordAt(line, line.indexOf("format"), typed)), [line.indexOf("format"), line.indexOf("format") + 6]);
  assert.equal(reader.engagementWordAt(line, line.indexOf("reformat"), typed), null);
  // In a message: the fragment is dropped, the ragged span is drawn on whole words.
  const start = text.indexOf("show me"), item = message([
    { label: "inquiry.request", dimension: "inquiry", score: 0.9, start: start + 2, end: start + 6 },
    { label: "register.hedge", dimension: "register", score: 0.9, start: text.indexOf("please") + 2, end: text.indexOf("please") + 3 }]);
  assert.deepEqual(plain(reader.messageEngagementView(item)).map(span => [span.label, text.slice(span.start, span.end)]), [["inquiry.request", "show me"]]);
});

test("the timeline gets one marker for each annotation, placed where it starts in the message", () => {
  const item = message(spans), reply = { id: "m2", role: "assistant", kind: "message", text: "Here is the diff." };
  const events = plain(reader.engagementTimeline([item, reply, { ...message([{ label: "intent.task", dimension: "intent", score: 0.4, ...range("open it") }]), id: "m3" }]));
  assert.deepEqual(events.map(event => [event.message_id, event.title, event.className, event.text]), [
    ["m1", "Bounds · Prohibition", "eng-slot-2", "Don't open it yet"], ["m1", "Intent · Task", "eng-slot-0", "open it"], ["m1", "Bounds · Timing hold", "eng-slot-2", "open it yet"],
    ["m1", "Verification", "eng-slot-3", events[3].text], ["m1", "Mood · Playful", events[4].className, "🙂 please"]]);
  assert.equal(new Set(events.map(event => event.id)).size, events.length);
  assert.deepEqual(events.map(event => event.position), events.map(event => event.start / text.length));
  assert.equal(events[0].position, 0); assert.ok(events[4].position > 0.8 && events[4].position < 1);
  assert.deepEqual([events[0].label, events[0].labelName, events[0].name, events[0].score], ["bounds.prohibition", "Prohibition", "Bounds", 0.91]);
  assert.deepEqual(plain(reader.engagementTimeline([])), []);
  // The marks a marker's annotation covers in its card, for the flash after a click.
  const view = reader.messageEngagementView(item), segments = reader.engagementSegments(view), hold = events[2];
  assert.deepEqual(plain(reader.engagementSpanMarks(segments, hold.start, hold.end)).map(name => { const segment = segments[Number(name.slice(7))]; return text.slice(segment.start, segment.end); }), ["open it", " ", "yet"]);
});

test("dimensions have one-word display names with their own first letters, and Register · Filler is never shown", () => {
  const names = ["intent", "grounding", "bounds", "acceptance", "rights", "evaluation", "inquiry", "authorship", "register"].map(reader.engagementDimensionName);
  assert.deepEqual(names, ["Intent", "Grounding", "Bounds", "Verification", "Delegation", "Evaluation", "Questions", "Authorship", "Tone"]);
  assert.equal(new Set(names.map(name => name[0])).size, 9);
  assert.equal(reader.engagementDimensionName("mood_swing"), "Mood swing", "a dimension the reader has not met keeps its own name");
  // Stored ids stay the model's; only the names change.
  const item = message([{ label: "register.hedge", dimension: "register", score: 0.9, ...range("please") }, { label: "rights.go_ahead", dimension: "rights", score: 0.9, ...range("open it") },
    { label: "register.filler", dimension: "register", score: 0.99, ...range("Don't") }]);
  const view = plain(reader.messageEngagementView(item));
  assert.deepEqual(view.map(span => [span.label, span.dimension, span.dimensionName, span.className]), [["register.hedge", "register", "Tone", "eng-slot-8"], ["rights.go_ahead", "rights", "Delegation", "eng-slot-4"]]);
  assert.deepEqual(plain(reader.engagementTipView(reader.messageEngagementView(item))).map(row => row.title).sort(), ["Delegation · Go ahead", "Tone · Hedge"]);
  // The filler label reaches no surface: Key, timeline, or a message that has nothing else.
  const option = plain(reader.readerHighlightOptions({ engagement: { messages_read: 1 } }, [item])).find(option => option.id === "engagement");
  assert.deepEqual(option.legend, [{ name: "Delegation", className: "eng-slot-4", key: "rights" }, { name: "Tone", className: "eng-slot-8", key: "register" }]);
  assert.deepEqual(plain(reader.engagementTimeline([item])).map(event => [event.label, event.name]), [["rights.go_ahead", "Delegation"], ["register.hedge", "Tone"]]);
  assert.equal(reader.messageEngagementView(message([{ label: "register.filler", dimension: "register", score: 0.99, ...range("Don't") }])), null);
});

test("a turn's hover card says what its header says", () => {
  const at = "2026-09-20T12:00:00Z", turn = { human: { role: "user", kind: "message", text: "Check the importer again", created_at: at },
    events: [{ kind: "message" }, { kind: "tool_call" }, { kind: "tool_call" }, { kind: "tool_result" }, { kind: "delegation" }, { kind: "message" }] };
  assert.deepEqual(plain(reader.turnCardView(turn, 1)), { title: "Turn 2 · You", at, prompt: "Check the importer again", stats: ["2 replies", "4 activities", "2 tool calls", "1 sub-agent"] });
  // Token usage records are left out of the count, as the turn's header leaves them out (turnActivities is what both count).
  const metered = { ...turn, events: [...turn.events, { kind: "metadata", role: "system", text: JSON.stringify({ type: "token_count", info: {} }) }, { kind: "metadata", role: "system", text: JSON.stringify({ type: "task_started" }) }] };
  assert.equal(plain(reader.turnCardView(metered, 1)).stats[1], "5 activities");
  assert.equal(reader.turnActivities(metered).length, 5);
  const setup = plain(reader.turnCardView({ human: null, events: [{ kind: "message", created_at: at }] }, 0));
  assert.deepEqual([setup.title, setup.at, setup.prompt, setup.stats[0]], ["Turn 1", at, "Agent setup", "1 reply"]);
});

test("hovering a stretch lights every annotation over it, end to end", () => {
  // Annotations A and B overlap: R1 has A only, R2 has both, R3 has B only.
  const line = "alpha beta gamma", part = value => { const start = line.indexOf(value); return { start, end: start + value.length }; };
  const view = [{ label: "intent.task", dimension: "intent", className: "eng-slot-0", slot: 0, ...part("alpha beta") }, { label: "bounds.scope", dimension: "bounds", className: "eng-slot-2", slot: 2, ...part("beta gamma") }];
  const segments = reader.engagementSegments(view);
  assert.deepEqual(plain(segments.map(segment => line.slice(segment.start, segment.end))), ["alpha ", "beta", " gamma"]);
  const [R1, R2, R3] = [0, 1, 2], hover = (segment, shown = null) => plain(reader.engagementHoverTints(view, segments, `human-written engagement eng-at-${segment}`, shown));
  assert.deepEqual(hover(R1), [{ segment: R1, slots: [0] }, { segment: R2, slots: [0] }], "R1: all of A");
  assert.deepEqual(hover(R2), [{ segment: R1, slots: [0] }, { segment: R2, slots: [0, 2] }, { segment: R3, slots: [2] }], "R2: all of A and all of B, the overlap as both");
  assert.deepEqual(hover(R3), [{ segment: R2, slots: [2] }, { segment: R3, slots: [2] }], "R3: all of B");
  // Only annotations the popover lists: a dimension switched off in the Key lights nothing.
  assert.deepEqual(hover(R2, new Set(["engagement:bounds"])), [{ segment: R2, slots: [2] }, { segment: R3, slots: [2] }]);
  assert.deepEqual(hover(R1, new Set(["engagement:bounds"])), []);
  // Two annotations of one dimension stay two: hovering one does not light the other's own stretch.
  const same = [view[0], { ...view[1], className: "eng-slot-0", slot: 0 }], parts = reader.engagementSegments(same);
  assert.deepEqual(plain(reader.engagementHoverTints(same, parts, "engagement eng-at-0")), [{ segment: 0, slots: [0] }, { segment: 1, slots: [0] }]);
  assert.deepEqual(plain(reader.engagementHoverTints(view, segments, "human-written")), []);
  assert.deepEqual(plain(reader.engagementHoverTints(view, segments, "engagement eng-at-99")), []);
});
