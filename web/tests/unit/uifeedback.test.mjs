import test from "node:test";
import assert from "node:assert/strict";
import { safeURL, boundedLog, changeTags, pendingSubmission, normalizeEntry } from "../../src/uifeedback/model.ts";
import { annotationForUpload, restoreReview } from "../../src/uifeedback/pharos-model.ts";

test("annotation URLs preserve routing while excluding credentials and login tokens", () => {
  assert.equal(safeURL("http://user:secret@localhost:18765/library?q=filter&token=private&access_token=other#secret"), "http://localhost:18765/library?q=filter");
  assert.equal(safeURL("javascript:alert(1)"), "");
});
test("diagnostics redact known credentials and remain bounded in UTF-8 bytes", () => {
  const text = boundedLog("Bearer secret-value token=private api_key=private /Users/alice/project");
  assert.ok(!text.includes("secret-value")); assert.ok(!text.includes("private")); assert.ok(!text.includes("alice"));
  const long = boundedLog("日".repeat(65536)); assert.ok(new TextEncoder().encode(long).length <= 65536);
});
test("tag actions preserve other tags and allow clearing a tag", () => {
  assert.deepEqual(changeTags(["ui", "fixed"], "ui"), ["ui", "fixed"]);
  assert.deepEqual(changeTags(["ui", "fixed"], "fixed", true), ["ui"]);
  assert.deepEqual(changeTags(["ui"], "ui", true), []);
});
test("unchanged retries keep identity while edited submissions acquire another ID", () => {
  const value = { version: 1, application: { name: "Pharos" }, kind: "prompt", comment: "Make this easier", annotations: [] };
  const first = pendingSubmission(value, null, () => "first");
  assert.equal(pendingSubmission(value, first, () => "unused"), first);
  assert.equal(pendingSubmission({ ...value, email: "someone@example.test" }, first, () => "second").id, "second");

  const firstAnnotation = { id: "first", comment: "Exclude this" }, secondAnnotation = { id: "second", comment: "Keep this", screenshot: { mime: "image/jpeg", data: "image" } };
  const reviewed = { annotations: [secondAnnotation], sourceIDs: ["first", "second"], diagnostics: { browser: "edited error", service: "reviewed logs" }, includeDiagnostics: true };
  const report = { ...value, annotations: reviewed.annotations, diagnostics: reviewed.diagnostics };
  const pending = pendingSubmission(report, null, () => "reviewed");
  const restored = restoreReview(structuredClone(reviewed), [firstAnnotation, secondAnnotation]);
  assert.deepEqual(restored, reviewed);
  assert.equal(pendingSubmission({ ...value, annotations: restored.annotations, diagnostics: restored.includeDiagnostics ? restored.diagnostics : undefined }, pending, () => "changed").id, "reviewed");

  const excludedAll = restoreReview({ ...reviewed, annotations: [], includeDiagnostics: false }, [firstAnnotation, secondAnnotation]);
  assert.deepEqual(excludedAll.annotations, []);
  assert.equal(excludedAll.includeDiagnostics, false);
  assert.deepEqual(excludedAll.diagnostics, reviewed.diagnostics);
  const added = { id: "third", comment: "New annotation" };
  assert.deepEqual(restoreReview(reviewed, [secondAnnotation, added]).annotations, [secondAnnotation, added]);
});

test("general report details normalize missing and null annotations", () => {
  const report = { comment: "General feedback", tags: [] };
  assert.deepEqual(normalizeEntry(report).annotations, []);
  assert.deepEqual(normalizeEntry({ ...report, annotations: null }).annotations, []);
  const annotations = [{ id: "first", comment: "Change this" }];
  assert.deepEqual(normalizeEntry({ ...report, annotations }).annotations, annotations);
});

test("annotation attachments and captured URLs follow each annotation's opt-in", () => {
  const base = { id: "a", note: "Change this", label: "Heading", selector: "h1", text: "private archive text", url: "http://localhost/library?q=first&token=secret", created_at: "2026-10-26T01:00:00Z", bounds: { x: 0, y: 0, width: 100, height: 20 }, viewport: { width: 390, height: 844, device_pixel_ratio: 2 }, styles: { color: "red" }, source: "archive/ui.py" };
  const omitted = annotationForUpload(base);
  assert.equal(omitted.url, undefined); assert.equal(omitted.text, undefined); assert.equal(omitted.styles, undefined); assert.equal(omitted.source, undefined);
  const screenshot = { mime: "image/jpeg", data: "raster" };
  const first = annotationForUpload({ ...base, include_url: true, include_text: true }, screenshot);
  const second = annotationForUpload({ ...base, id: "b", include_url: true, url: "http://localhost/analyze?q=second", created_at: "2026-10-26T02:00:00Z" });
  assert.equal(first.url, "http://localhost/library?q=first"); assert.equal(second.url, "http://localhost/analyze?q=second");
  assert.equal(first.captured_at, base.created_at); assert.equal(first.screenshot, screenshot); assert.equal(second.screenshot, undefined);
});
