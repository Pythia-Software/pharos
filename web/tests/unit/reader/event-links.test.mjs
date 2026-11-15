// Two-sided agent events in the conversation reader (reader-model.js
// eventLinks, eventTargetSpans, eventTargetMask, eventLinkEnd).
import { test } from "node:test";
import assert from "node:assert/strict";
import { reader } from "../helpers/transcript.mjs";
import { plain } from "../helpers/classic-script.mjs";

const prompt = "Make the drawer only removal flow work on phones.";
const reply = 'I read "drawer only" as the bottom sheet. Done.';
const range = (text, part) => [text.indexOf(part), text.indexOf(part) + part.length];
const messages = [
  { id: "p", role: "user", kind: "message", text: prompt },
  { id: "r", role: "assistant", kind: "message", text: reply, agent_events: [
    { id: "e1", kind: "assumption", label: "Assumption", group: "Choices you may not see", probability: 0.9, sentence: range(reply, 'I read "drawer only" as the bottom sheet.'), subject: [], response: [],
      target: { method: "quote", message_id: "p", start: 9, end: 20, text: "drawer only", strong: true } },
    { id: "e2", kind: "discovery", label: "Discovery", group: "Reactions and learning", probability: 0.8, sentence: range(reply, "Done."), subject: [], response: [],
      target: { method: "quote", message_id: "elsewhere", start: 0, end: 4, text: "gone", strong: true } },
    { id: "e3", kind: "obstacle", label: "Obstacle", group: "Friction", probability: 0.8, sentence: range(reply, "Done."), subject: [], response: [], target: null },
  ] },
];

test("pairs are the events whose target is in the conversation, numbered in reading order", () => {
  const links = reader.eventLinks(messages);
  assert.deepEqual(plain(links.map(link => [link.id, link.n, link.message, link.target.message_id, link.weak])), [["e1", 1, "r", "p", false]]);
  assert.deepEqual(plain(reader.eventLinkEnd(links, "e1", "observation")), plain({ side: "target", message: "p", link: links[0] }));
  assert.equal(reader.eventLinkEnd(links, "e1", "target").message, "r");
  assert.equal(reader.eventLinkEnd(links, "missing", "target"), null);
});

test("target spans use offsets on the stored text and find the words in other shown text", () => {
  const links = reader.eventLinks(messages);
  assert.deepEqual(plain(reader.eventTargetSpans(prompt, links)), [{ range: [9, 20], links: ["e1"] }]);
  const shown = "Note: " + prompt;
  assert.deepEqual(plain(reader.eventTargetSpans(shown, links)), [{ range: [15, 26], links: ["e1"] }]);
  assert.deepEqual(plain(reader.eventTargetSpans("Nothing here.", links)), []);
});

test("a target mark keeps the person's writing marked under it", () => {
  const links = reader.eventLinks(messages), spans = reader.eventTargetSpans(prompt, links);
  const mask = reader.eventTargetMask(prompt, spans, "1".repeat(prompt.length));
  const classes = [...new Set([...mask].map(reader.markClass))];
  assert.deepEqual(classes, ["human-written", "event-target event-target-at-0 human-written"]);
  assert.equal(reader.markClass(reader.eventTargetMask("abc", [{ range: [0, 3], links: ["x"] }], "")[0]), "event-target event-target-at-0");
});

// One sentence, two events (and one shared target): the conversation shows
// one mark and one pair, with both labels and the text once.
const doubled = 'I read "drawer only" as the bottom sheet, so I skipped the side panel. Done.';
const sentence = 'I read "drawer only" as the bottom sheet, so I skipped the side panel.';
const target = { method: "quote", message_id: "p", start: 9, end: 20, text: "drawer only", strong: true };
const twice = [
  { id: "p", role: "user", kind: "message", text: prompt },
  { id: "r", role: "assistant", kind: "message", text: doubled, agent_events: [
    { id: "a1", kind: "assumption", label: "Assumption", group: "Choices you may not see", probability: 0.9, sentence: range(doubled, sentence), subject: [], response: [], target },
    // Near-identical: the same sentence without its last clause, inside the first.
    { id: "a2", kind: "workaround", label: "Workaround", group: "Choices you may not see", probability: 0.7, sentence: range(doubled, 'I read "drawer only" as the bottom sheet'), subject: [], response: [range(doubled, "so I skipped the side panel.")], target: { ...target, end: 19, text: "drawer onl" } },
    { id: "a3", kind: "obstacle", label: "Obstacle", group: "Friction", probability: 0.8, sentence: range(doubled, "Done."), subject: [], response: [], target: null },
  ] },
];

test("near-identical sentences merge: one contains the other, or they share 80% of what they cover", () => {
  assert.equal(reader.nearSameSpan([0, 10], [2, 8]), true, "containment");
  assert.equal(reader.nearSameSpan([0, 10], [1, 10]), true, "90% shared");
  assert.equal(reader.nearSameSpan([0, 10], [3, 13]), false, "54% shared");
  assert.equal(reader.nearSameSpan([0, 10], [10, 20]), false, "touching");
  const view = reader.messageEventView(twice[1]);
  assert.deepEqual(plain(view.map(event => [event.id, event.sentence])), [["a1", range(doubled, sentence)], ["a2", range(doubled, sentence)], ["a3", range(doubled, "Done.")]]);
  assert.deepEqual(plain(view[1].ownSentence), range(doubled, 'I read "drawer only" as the bottom sheet'), "each event keeps its own range");
  assert.deepEqual(plain(reader.eventSpans(view).sentences), [range(doubled, sentence), range(doubled, "Done.")], "one mark for the pair");
});

test("a merged mark's popover lists every label with the text once", () => {
  const view = reader.messageEventView(twice[1]), tip = reader.eventTipView(view.slice(0, 2));
  assert.deepEqual(plain(tip.labels.map(label => label.label)), ["Assumption", "Workaround"]);
  assert.deepEqual(plain(tip.fields), [["Said", [sentence]], ["What it did next", ["so I skipped the side panel."]], ["Points back at", ["“drawer only”", "“drawer onl”"]]]);
  // The same kind twice is one label, the likelier.
  assert.deepEqual(plain(reader.eventTipView([view[0], { ...view[0], id: "x", probability: 0.95 }]).labels.map(label => label.probability)), [0.95]);
});

test("events on one sentence pointing at the same text are one pair, found by either id", () => {
  const links = reader.eventLinks(twice);
  assert.deepEqual(plain(links.map(link => [link.id, link.ids, link.labels, link.n])), [["a1", ["a1", "a2"], ["Assumption", "Workaround"], 1]]);
  assert.equal(reader.eventLinkFor(links, "a2").id, "a1", "an old link to the second event selects the merged pair");
  assert.equal(reader.eventLinkEnd(links, "a2", "observation").message, "p");
  assert.deepEqual(plain(reader.eventTargetSpans(prompt, links)), [{ range: [9, 20], links: ["a1"] }], "the target is marked once");
  // A different target on the same sentence stays its own pair.
  const other = structuredClone(twice); other[1].agent_events[1].target = { method: "quote", message_id: "p", start: 30, end: 35, text: "works", strong: true };
  assert.deepEqual(plain(reader.eventLinks(other).map(link => link.ids)), [["a1"], ["a2"]]);
  other[1].agent_events[0].group = 'Friction';
  other[1].agent_events[1].group = 'Reactions and learning';
  const separate = reader.eventLinks(other), shown = new Set(['events:event-learning']);
  assert.deepEqual(plain(separate.filter(link => reader.eventLinkVisible(link, shown)).map(link => link.id)), ['a2'], 'a sentence only offers links belonging to visible groups');
  other[1].agent_events[1].target = target;
  const merged = reader.eventLinkFor(reader.eventLinks(other), 'a2');
  assert.deepEqual(plain(reader.eventLinkHighlightKeys(merged)), ['events:event-friction', 'events:event-learning']);
  assert.equal(reader.eventLinkVisible(merged, shown), true, 'a visible alias keeps its merged pair available');
  assert.equal(reader.eventLinkVisible(merged, new Set()), false);
});


test("hidden annotation identities distinguish conversations, messages, kinds and spans", () => {
  const key = reader.readerAnnotationKey('c,\"1', 'm:1', 'event', 'event-at-0');
  assert.deepEqual(JSON.parse(key.slice('annotation:'.length)), ['c,\"1', 'm:1', 'event', 'event-at-0']);
  assert.ok(key.startsWith(reader.readerAnnotationPrefix('c,\"1')));
  for (const other of [reader.readerAnnotationKey('c,\"2', 'm:1', 'event', 'event-at-0'), reader.readerAnnotationKey('c,\"1', 'm:2', 'event', 'event-at-0'), reader.readerAnnotationKey('c,\"1', 'm:1', 'target', 'event-at-0'), reader.readerAnnotationKey('c,\"1', 'm:1', 'event', 'event-at-1')]) assert.notEqual(other, key);
  assert.equal(key.startsWith(reader.readerAnnotationPrefix('c,\"')), false, 'a conversation prefix does not match a different ID');
  assert.notEqual(reader.readerAnnotationSpan('event','event-context event-at-0'), reader.readerAnnotationSpan('event','agent-event event-at-0'), 'context and sentence with the same span number remain distinct');
});
