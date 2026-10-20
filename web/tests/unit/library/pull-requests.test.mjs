import test from "node:test";
import assert from "node:assert/strict";
import { githubPullURL, mainIntegration, pullRequestLabel, pullRequestURL, pullRequests } from "../../../src/pull-requests";

// The Library row shape the navigator test served.
const row = {
  id: "work-1", canonical_remote: "git@github.com:acme/pharos.git",
  main_merge_commit: "abcdef1234567890", main_merge_title: "Merge feature into main", main_merge_method: "merge", main_merge_url: "https://github.com/acme/pharos/commit/abcdef1234567890",
  pr_details: JSON.stringify([{ number: 42, title: "Improve archive search", url: "https://github.com/acme/pharos/pull/42", host: "github.com" }, { number: 73, title: null, url: null, host: "github.com" }]),
};

test("past work links each recorded PR, deriving GitHub URLs from the canonical remote", () => {
  const prs = pullRequests(row);
  assert.deepEqual(prs.map(pr => pr.number), [42, 73]);
  assert.deepEqual(prs.map(pr => pullRequestURL(pr, row.canonical_remote)), ["https://github.com/acme/pharos/pull/42", "https://github.com/acme/pharos/pull/73"]);
  assert.match(pullRequestLabel(prs[0]), /Improve archive search/);
  assert.match(pullRequestLabel(prs[0]), /#42\b/);
  assert.match(pullRequestLabel({ number: 73, title: "  " }), /#73\b/, "untitled PRs are named by number");
  assert.equal(pullRequestLabel({ number: 9, title: " Fix " }), pullRequestLabel({ number: 9, title: "Fix" }));
});

test("PR details are read from JSON or arrays and malformed entries are dropped", () => {
  const details = [{ number: 1 }, { number: 0 }, { number: -2 }, { number: 1.5 }, { number: "3" }, null, { title: "no number" }, { number: 4 }];
  assert.deepEqual(pullRequests({ pr_details: details }).map(pr => pr.number), [1, 4]);
  assert.deepEqual(pullRequests({ pr_details: JSON.stringify(details) }).map(pr => pr.number), [1, 4]);
  for (const pr_details of [undefined, null, "", "not json", "{\"number\":1}", 7]) assert.deepEqual(pullRequests({ pr_details }), [], String(pr_details));
});

test("GitHub pull URLs come from SSH and HTTPS remotes only", () => {
  for (const remote of ["git@github.com:acme/pharos.git", "git@github.com:acme/pharos", "https://github.com/acme/pharos.git", "https://github.com/acme/pharos", "ssh://git@github.com/acme/pharos.git"])
    assert.equal(githubPullURL(remote, 7), "https://github.com/acme/pharos/pull/7", remote);
  for (const remote of ["git@gitlab.com:acme/pharos.git", "https://example.com/acme/pharos", "https://github.com/acme", "", undefined, null])
    assert.equal(githubPullURL(remote, 7), "", String(remote));
});

test("PR links use recorded http(s) URLs and never invent links for other hosts", () => {
  const remote = row.canonical_remote;
  assert.equal(pullRequestURL({ number: 5, url: "https://git.example.com/acme/pharos/pull/5", host: "git.example.com" }, remote), "https://git.example.com/acme/pharos/pull/5");
  assert.equal(pullRequestURL({ number: 5, url: null, host: "git.example.com" }, remote), "", "only GitHub URLs are derived");
  assert.equal(pullRequestURL({ number: 5, url: null, host: "github.com" }, "git@gitlab.com:acme/pharos.git"), "");
  for (const url of ["javascript:alert(1)", "file:///etc/passwd", "//github.com/acme/pharos/pull/5", "github.com/acme/pharos/pull/5"])
    assert.equal(pullRequestURL({ number: 5, url, host: "github.com" }, remote), "", url);
});

test("origin/main merges say how they landed and link only to GitHub", () => {
  const merged = mainIntegration(row);
  assert.equal(merged.url, "https://github.com/acme/pharos/commit/abcdef1234567890");
  assert.equal(merged.commit, "abcdef1234567890");
  assert.match(merged.label, /: Merge feature into main$/);
  const squashed = mainIntegration({ ...row, main_merge_method: "squash" });
  assert.notEqual(squashed.label, merged.label, "a merge commit and a squash or rebase are labelled differently");
  assert.equal(mainIntegration({ ...row, main_merge_method: "rebase" }).label, squashed.label);
  assert.match(mainIntegration({ ...row, main_merge_title: "" }).label, /: abcdef123456$/, "untitled merges show the short commit");
  for (const main_merge_url of ["https://gitlab.com/acme/pharos/commit/abc", "http://github.com/acme/pharos/commit/abc", "javascript:alert(1)", "", undefined])
    assert.equal(mainIntegration({ ...row, main_merge_url }).url, "", String(main_merge_url));
  assert.equal(mainIntegration({ ...row, main_merge_commit: "" }), undefined);
  assert.equal(mainIntegration({ id: "unmerged" }), undefined);
});
