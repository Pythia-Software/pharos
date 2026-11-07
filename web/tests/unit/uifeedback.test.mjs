import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import vm from "node:vm";
import { assets, loadClassicScript } from "./helpers/classic-script.mjs";
import { safeURL, boundedLog, changeTags, pendingSubmission, normalizeEntry } from "../../src/uifeedback/model.ts";
import { annotationForUpload, createAskFeedbackSender, feedbackReceipt, restoreReview } from "../../src/uifeedback/pharos-model.ts";

test('report copy waits for native success, clears stale notices and restores copy state', async () => {
  const source=readFileSync(join(assets,'../../../web/src/uifeedback/inbox.tsx'),'utf8');
  const copySource=source.slice(source.indexOf('  async function copyReport()'),source.indexOf('\n  async function mutate('));
  for(const outcome of ['native','native-failure','browser-denial']){
    const transfer=Promise.withResolvers(),writes=[];let browserCalls=0;
    const window=loadClassicScript('host-services.js',{
      ...(outcome==='browser-denial'?{}:{webkit:{messageHandlers:{pharosClipboard:{postMessage:text=>{writes.push(text);return transfer.promise;}}}}}),
      navigator:{clipboard:{writeText:()=>{browserCalls++;return outcome==='browser-denial'?transfer.promise:Promise.reject(Error('Browser denied'));}}},
    }).window;
    const detail={id:'report',comment:'A report\nwith source text',annotations:[]};
    let copying=false,notice='Previous success',error='Previous failure';
    const copy=vm.runInNewContext(copySource+';copyReport',{
      window,detail,get copying(){return copying;},setCopying:value=>{copying=value;},setNotice:value=>{notice=value;},setError:value=>{error=value;},
    });
    const pending=copy();
    assert.equal(copying,true);assert.equal(notice,'');assert.equal(error,'');
    await copy();assert.equal(writes.length+browserCalls,1);
    if(outcome==='native')transfer.resolve(true);else transfer.reject(Error('Copy denied'));
    await pending;
    assert.equal(copying,false);
    if(outcome==='native'){assert.ok(notice);assert.equal(error,'');}
    else {assert.equal(notice,'');assert.match(error,/could not copy/i);}
    assert.deepEqual(writes,outcome==='browser-denial'?[]:[JSON.stringify(detail,null,2)]);
    assert.equal(browserCalls,outcome==='browser-denial'?1:0);
  }
});

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


test("Ask feedback sends survive editor remounts and reconcile confirmed outcomes before retrying", async () => {
  const receipts = new Map(), pending = new Map(), requests = [];
  const entered = Promise.withResolvers(), delivered = Promise.withResolvers();
  let sequence = 0;
  const sender = createAskFeedbackSender({
    prepare: async (id, payload) => {
      if (receipts.has(id)) return {status:"sent",receipt:receipts.get(id)};
      const submission = pendingSubmission(payload, pending.get(id) ?? null, () => `report-${++sequence}`);
      pending.set(id, submission); return {status:"pending",submission};
    },
    submit: async submission => {requests.push(submission); entered.resolve(); await delivered.promise;},
    complete: async (id, submission) => {receipts.set(id,feedbackReceipt(submission)); pending.delete(id);},
  });
  const edited = {version:1,application:{name:"Pharos"},kind:"feedback",comment:"The edited report actually approved by the user.",email:"person@example.test",annotations:[],diagnostics:{service:"Only attached to the submission"}};
  const first = sender.send("proposal", edited);
  await entered.promise;
  const remounted = sender.pending("proposal");
  assert.equal(remounted, first, "a replacement editor observes the in-flight send");
  assert.equal(sender.send("proposal", {...edited,comment:"A stale replacement draft"}), first, "another approval cannot start a second in-flight report");
  delivered.resolve();
  const outcome = await remounted;
  assert.equal(requests.length,1);
  assert.equal(outcome.receipt.comment,edited.comment);
  assert.equal(outcome.receipt.kind,edited.kind);
  assert.equal(outcome.receipt.diagnostics,undefined, "receipts keep submitted text without retaining diagnostic attachments");
  assert.equal(sender.pending("proposal"),undefined);
  const restored = await sender.send("proposal",{...edited,comment:"The original generated proposal"});
  assert.deepEqual(restored,outcome,"approval reconciles the saved receipt instead of replacing it with the current draft");
  assert.equal(requests.length,1);
});

test("Ask feedback retains retry identity after a received report's receipt cannot be saved", async () => {
  let pending = null, attempts = 0;
  const requests = [];
  const sender = createAskFeedbackSender({
    prepare: async (_id,payload) => ({status:"pending",submission:pending=pendingSubmission(payload,pending,()=>`report-${requests.length+1}`)}),
    submit: async submission => {requests.push(submission.id);},
    complete: async () => {if (++attempts===1) throw new Error("Receipt storage unavailable");},
  });
  const payload={version:1,application:{name:"Pharos"},kind:"feature",comment:"Add export",annotations:[]};
  await assert.rejects(sender.send("proposal",payload),/Receipt storage unavailable/);
  assert.equal(sender.pending("proposal"),undefined,"a failed save must allow an explicit retry");
  await sender.send("proposal",payload);
  assert.equal(requests.length,2);
  assert.equal(requests[0],requests[1],"retry confirms the same report even after the first network request succeeded");
});

test("Ask feedback honors a persisted discard before executing any submission", async () => {
  const sender=createAskFeedbackSender({
    prepare:async()=>({status:"discarded"}),
    submit:async()=>assert.fail("a discarded proposal must not submit"),
    complete:async()=>assert.fail("a discarded proposal must not acquire a receipt"),
  });
  assert.deepEqual(await sender.send("proposal",{}),{status:"discarded"});
});
