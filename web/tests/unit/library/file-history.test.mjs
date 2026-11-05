import test from "node:test";
import assert from "node:assert/strict";
import { commitState, fileHistoryURL, gitText, linesText, linkText, readFileHistoryRoute } from "../../../src/file-history-model";

test("a file history link reads back, with or without its project", () => {
  for (const [path, project] of [["src/main.go", "pharos"], ["~/notes/plan & ideas.md", ""]]) {
    const url = new URL(fileHistoryURL(path, project), "http://localhost");
    assert.equal(url.pathname, "/files");
    assert.deepEqual(readFileHistoryRoute(url.searchParams), { file: path, file_project: project });
  }
});

test("missing Git facts read as not applicable or not checked, never as none", () => {
  assert.match(gitText({ status: "not_applicable" }, 0), /folder/);
  assert.equal(gitText({ status: "not_checked", reason: "Local Git has not checked this repository on this Mac yet." }, 0), "Local Git has not checked this repository on this Mac yet.");
  assert.match(gitText({ status: "checked" }, 0), /no commit/);
  assert.match(gitText({ status: "checked" }, 2), /^2 commits/);
  // Git is read from the first session edit: an empty list says since when.
  assert.match(gitText({ status: "checked", commits_since: "2026-09-01T10:00:00Z" }, 0), /no commit of this file since 2026-09-01\. Earlier commits are not read/);
  // A tool that recorded no counts is not a change of zero lines.
  assert.equal(linesText(null, null), "lines not recorded");
  assert.equal(linesText(1200, 0), "+1,200 −0");
});

test("a commit's state and its session links", () => {
  assert.equal(commitState({ hash: "a", reverted_by: "0123456789", on_mainline: true, sessions: [] }), "reverted by 0123456");
  assert.equal(commitState({ hash: "a", on_mainline: false, sessions: [] }), "not on mainline");
  assert.equal(commitState({ hash: "a", on_mainline: null, sessions: [] }), "");
  assert.equal(linkText({ workspace_id: "w", method: "inferred", confidence: 0.5, counted: false }), "candidate, inferred from its edits");
  assert.equal(linkText({ workspace_id: "w", method: "transcript", confidence: 1, counted: true }), "printed by the session");
});
