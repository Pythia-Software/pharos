import test from "node:test";
import assert from "node:assert/strict";
import { commitState, entryText, fileBreadcrumbs, fileHistoryURL, filePageURL, fileTreeKey, gitText, keepFilesView, linesText, linkText, notInTreeText, parentDir, placeText, readFilePageRoute, reworkText, sessionCommits, treeAncestors, treeText } from "../../../src/file-history-model";
import { workFilesSummary } from "../../../src/work-files";
import { loadClassicScript } from "../helpers/classic-script.mjs";

test("a file history link reads back, with or without its project", () => {
  for (const [path, project] of [["src/main.go", "pharos"], ["~/notes/plan & ideas.md", ""]]) {
    const url = new URL(fileHistoryURL(path, project), "http://localhost");
    assert.equal(url.pathname, "/files");
    assert.deepEqual(readFilePageRoute(url.searchParams), { file: path, project, dir: "", commit: "" });
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
  assert.equal(linkText({ workspace_id: "w", method: "window", confidence: 0.5, counted: false }), "candidate, its git command ran then");
});

test("a Files page address reads back, and moving keeps the open directories", () => {
  for (const target of [{ project: "pharos", file: "src/main.go" }, { project: "repository:repo-1", dir: "internal/archive" }, { project: "~/notes", commit: "0123abc" }, { project: "pharos" }]) {
    const url = new URL(filePageURL(target), "http://localhost");
    assert.equal(url.pathname, "/files");
    assert.deepEqual(readFilePageRoute(url.searchParams), { project: "", file: "", dir: "", commit: "", ...target });
  }
  // Parameters are sorted, as the service's filePageURL writes them.
  assert.equal(filePageURL({ project: "pharos", file: "a.go" }), "/files?file=a.go&file_project=pharos");
  const disclosures = JSON.stringify({ "files:pharos:internal": true });
  assert.equal(new URL(keepFilesView("/files?file=a.go&file_project=pharos", `?disclosures=${encodeURIComponent(disclosures)}&file=b.go`), "http://localhost").searchParams.get("disclosures"), disclosures);
  assert.equal(keepFilesView("/work/w-1?conversation=c", `?disclosures=${encodeURIComponent(disclosures)}`), "/work/w-1?conversation=c");
});

test("a path's directories open the tree and lead back to the root", () => {
  assert.deepEqual(treeAncestors("internal/archive/api.go"), ["internal", "internal/archive"]);
  assert.deepEqual(treeAncestors("README.md"), []);
  assert.equal(parentDir("internal/archive"), "internal");
  assert.deepEqual(fileBreadcrumbs("internal/archive/api.go"), [{ name: "internal", dir: "internal" }, { name: "archive", dir: "internal/archive" }, { name: "api.go", dir: null }]);
  // A folder project's file outside the folder keeps its own root.
  assert.deepEqual(fileBreadcrumbs("/opt/tool/run.sh"), [{ name: "/opt", dir: "/opt" }, { name: "tool", dir: "/opt/tool" }, { name: "run.sh", dir: null }]);
  assert.deepEqual(treeAncestors("/opt/tool/run.sh"), ["/opt", "/opt/tool"]);
  assert.notEqual(fileTreeKey("pharos", "internal"), fileTreeKey("repository:repo-1", "internal"));
});

test("tree entries, edits and rework say what touched them", () => {
  assert.equal(entryText({ sessions: 2, commits: 1 }, true), "2 sessions · 1 commit");
  assert.equal(entryText({ sessions: 1, commits: 0 }, true), "1 session");
  // Without Git a folder has no commits to count.
  assert.equal(entryText({ sessions: 3, commits: 0 }, false), "3 sessions");
  assert.match(treeText({ status: "checked", tree_ref: "origin/main", tree_commit: "0123456789" }), /^The tree at origin\/main \(0123456\)/);
  assert.match(treeText({ status: "not_applicable" }), /folder/);
  assert.match(treeText({ status: "not_checked", reason: "The checkout is gone." }), /^The checkout is gone\. These are the files its sessions edited/);
  assert.match(notInTreeText({ status: "checked", tree_ref: "origin/main" }), /^Not in the tree at origin\/main/);
  assert.match(placeText({ at: "2026-11-01T10:00:00Z", lines_added: 3, lines_removed: 1, url: "/work/w" }, 1), /^Edit 2 · .+ · \+3 −1$/);
  assert.equal(placeText({ url: "/work/w" }, 0), "Edit 1");
  assert.equal(reworkText({ workspace_id: "w", removed_lines: 8, url: "" }, "by"), "removed 8 lines it added");
  assert.equal(reworkText({ workspace_id: "w", removed_lines: 1, url: "" }, "of"), "had 1 line removed by this session");
  const link = { workspace_id: "w-1", method: "transcript", confidence: 1, counted: true };
  const commits = [{ hash: "a", sessions: [link] }, { hash: "b", sessions: [] }];
  assert.deepEqual(sessionCommits({ commits }, "w-1").map(item => item.commit.hash), ["a"]);
  assert.deepEqual(sessionCommits({ commits }, "w-2"), []);
});

test("a session's files and commits summarize what they hold", () => {
  const file = { places: [], reworked_by: [], reworked: [] };
  assert.equal(workFilesSummary({ files: [file, file], commits: [] }), "2 files edited");
  assert.equal(workFilesSummary({ files: [file], files_truncated: true, commits: [{ counted: true }, { counted: false }] }), "1+ file edited · 1 linked commit and 1 candidate");
});

test("the reader's file chips open their files' pages", () => {
  const { fileLinkIndex } = loadClassicScript("reader-model.js");
  const links = fileLinkIndex([
    { message_id: "m-1", call_id: "c-1", path: "/Users/me/app/src/main.go", file: "src/main.go", url: "/files?file=src%2Fmain.go&file_project=app" },
    { message_id: "m-1", call_id: "c-1", path: "src/lib.go", file: "src/lib.go", url: "/files?file=src%2Flib.go&file_project=app" },
    { message_id: "m-2", call_id: "c-2", path: "/Users/me/app/src/main.go", file: "src/main.go", url: "/files?file=src%2Fmain.go&file_project=fork" },
  ]);
  assert.equal(links("m-1", "/Users/me/app/src/main.go", "c-1"), "/files?file=src%2Fmain.go&file_project=app");
  // A patch names its other files relative to the checkout.
  assert.equal(links("m-1", "/Users/me/app/src/lib.go", "c-1"), "/files?file=src%2Flib.go&file_project=app");
  assert.equal(links("m-2", "/Users/me/app/src/main.go"), "/files?file=src%2Fmain.go&file_project=fork");
  // A file the service did not link opens the page by its path.
  assert.equal(links("m-3", "/tmp/scratch.txt"), "/files?file=%2Ftmp%2Fscratch.txt");
  assert.equal(fileLinkIndex(undefined)("m-1", ""), "");
});
