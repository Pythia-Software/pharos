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
  for (const name of ["pr_count", "branch_count"]) assert.equal(field(name).render, "git_count");
});
