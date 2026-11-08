// Related work on a work page (reader-model.js peerChip, peerReasonText,
// handoffMarkers): the chip strip's text and certainty, and the hand-off
// markers on the messages a link is anchored on.
import { test } from "node:test";
import assert from "node:assert/strict";
import { plain } from "../helpers/classic-script.mjs";
import { reader } from "../helpers/transcript.mjs";

const { peerChip, peerReasonText, handoffMarkers, peerRoleLabel, conversationMessages } = reader;
const now = Date.parse("2026-11-07T12:00:00Z");

test("a chip names the peer's role, title, provider and age, and explains itself from the page's side", () => {
  const review = { workspace_id: "w/2", title: "Review sorting", provider: "codex", started_at: "2026-11-07T10:00:00Z", role: "reviewer", confidence: "high",
    evidence: [
      { kind: "read_written_by", confidence: "high", from_workspace_id: "w1", to_workspace_id: "w/2", details: { basis: "diff", lines: 12, timing: "return" } },
      { kind: "copied_into", confidence: "high", from_workspace_id: "w/2", to_workspace_id: "w1", details: { words: 40 } },
    ] };
  const chip = peerChip(review, { page: "w1" }, now);
  assert.equal(chip.label, "Reviewer · Review sorting · codex · 2h ago");
  assert.equal(chip.href, "/work/w%2F2");
  assert.equal(chip.certain, true);
  assert.deepEqual(plain(chip.reasons), [
    "It saw 12 lines this workspace wrote, in a diff; this workspace’s next prompt followed",
    "A prompt in this workspace quoted 40 words of a reply in it",
  ]);
  // From the review's page the same link reads the other way round; a peer
  // one hop on names the work it was reached through.
  const second = peerChip({ workspace_id: "w3", title: "", role: "reviewer", confidence: "high", via: "w1",
    evidence: [{ kind: "read_written_by", confidence: "high", from_workspace_id: "w1", to_workspace_id: "w3", details: { basis: "diff", lines: 4, timing: "start" } }] },
  { page: "w/2", titles: { w1: "Sort the rows" } }, now);
  assert.equal(second.label, "Reviewer · Untitled work");
  assert.deepEqual(plain(second.reasons), ["It saw 4 lines Sort the rows wrote, in a diff, starting as Sort the rows finished a turn", "Linked through Sort the rows"]);
});

test("uncertain links say so, and roles and reasons fall back to generic related work", () => {
  const maybe = peerChip({ workspace_id: "w4", title: "Parallel work", role: "reviewer", confidence: "low",
    evidence: [{ kind: "read_written_by", confidence: "low", from_workspace_id: "w1", to_workspace_id: "w4", details: { basis: "read", lines: 1, timing: "start" } }] }, { page: "w1" }, now);
  assert.equal(maybe.certain, false);
  assert.match(maybe.title, /^Possibly related work · uncertain\n/);
  assert.deepEqual(plain(maybe.reasons), ["It saw 1 line this workspace wrote, in files it read, starting as this workspace finished a turn (uncertain)"]);
  assert.equal(peerRoleLabel("related"), "Related work");
  assert.equal(peerRoleLabel("unknown"), "Related work");
  assert.equal(peerReasonText({ kind: "tl1_candidate", confidence: "high", details: { candidate: "c" } }), "Both are TL1 tasks on the same candidate");
  assert.equal(peerReasonText({ kind: "new_kind", confidence: "low" }), "Related (uncertain)");
});

test("a message's hand-offs become one marker per linked message, certain first, and survive normalization", () => {
  const message = { id: "m1", role: "user", kind: "message", text: "Fix this: P1 …",
    handoffs_in: [
      { kind: "copied_into", confidence: "low", role: "related", workspace_id: "w2", conversation_id: "c2", message_id: "r1", title: "Second look" },
      { kind: "copied_into", confidence: "high", role: "related", workspace_id: "w3", conversation_id: "c3", message_id: "r9", title: "Review sorting" },
      { kind: "copied_into", confidence: "high", role: "related", workspace_id: "w3", conversation_id: "c3", message_id: "r9", title: "Review sorting" },
    ],
    handoffs_out: [{ kind: "read_written_by", confidence: "high", role: "reviewer", workspace_id: "w3", conversation_id: "c3", message_id: "d1", title: "Review sorting" }] };
  const [normalized] = conversationMessages({ messages: [message] });
  const markers = handoffMarkers(normalized);
  assert.deepEqual(plain(markers.map(m => [m.label, m.certain, m.message])), [
    ["Quotes Review sorting", true, "r9"],
    ["Read in Review sorting", true, "d1"],
    ["Quotes Second look", false, "r1"],
  ]);
  assert.equal(markers[0].href, "/work/w3/conversation/c3?message=r9");
  assert.deepEqual(plain(handoffMarkers({ id: "m2" })), []);
});
