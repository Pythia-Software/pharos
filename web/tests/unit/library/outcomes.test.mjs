import test from "node:test";
import assert from "node:assert/strict";
import library from "../../../../schemas/library.schema.json";
import { gitFactDisplay, outcomeDisplay, outcomeLabels } from "../../../src/outcomes";

const field = name => library.fields.find(field => field.name === name);

test("outcome labels keep one order and wording in the page and the Library's filter", () => {
  assert.deepEqual(outcomeLabels.map(label => label.id), ["landed", "committed", "changed_kept", "changed_recent", "reworked", "no_changes", "unknown"]);
  assert.deepEqual(field("outcome_label").filter.values.options, outcomeLabels.map(({ id, label }) => ({ value: id, label })));
  // A missing or unrecognized label is Unknown, never a blank.
  for (const value of [undefined, null, "", "merged"]) assert.equal(outcomeDisplay(value).label, "Unknown");
  assert.equal(outcomeDisplay("no_changes").label, "No changes");
});

test("a Git count outside a repository reads as not applicable, never zero", () => {
  assert.deepEqual(gitFactDisplay(null, "folder"), { text: "Not a repository", muted: true, title: "This project is a folder; Git facts do not apply." });
  assert.equal(gitFactDisplay(null, "none").text, "No git data");
  // Recorded Git data shows wherever it came from; a repository's zero is zero.
  assert.deepEqual(gitFactDisplay(2, "folder"), { text: "2", muted: false });
  assert.deepEqual(gitFactDisplay(0, "repository"), { text: "0", muted: false });
  for (const name of ["pr_count", "branch_count", "linked_commits", "landed_commits"]) assert.equal(field(name).render, "git_count");
});

test("a repository local Git could not check reads Not checked with its reason, never zero", () => {
  const reason = "No checkout of this repository was recorded on this Mac.";
  assert.deepEqual(gitFactDisplay(null, "repository", "not_checked", reason), { text: "Not checked", muted: true, title: reason });
  assert.equal(gitFactDisplay("", "repository", "not_checked").title, "Local Git has not been checked.");
  // Not applicable wins over not checked, and a checked count shows.
  assert.equal(gitFactDisplay(null, "folder", "not_checked").text, "Not a repository");
  assert.deepEqual(gitFactDisplay(3, "repository", "checked"), { text: "3", muted: false });
  assert.equal(gitFactDisplay(null, "repository", "not_applicable").text, "Not applicable");
  assert.deepEqual(field("git_check").filter.values.options.map(option => option.value), ["checked", "not_checked", "not_applicable"]);
});
