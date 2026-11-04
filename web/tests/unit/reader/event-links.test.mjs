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
