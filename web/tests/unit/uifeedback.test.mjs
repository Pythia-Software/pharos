import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import vm from "node:vm";
import { transformSync } from "esbuild";
import { assets, loadClassicScript } from "./helpers/classic-script.mjs";
import { safeURL, boundedLog, changeTags, pendingSubmission, normalizeEntry } from "../../src/uifeedback/model.ts";
import { annotationForUpload, createAskFeedbackSender, feedbackReceipt, restoreReview } from "../../src/uifeedback/pharos-model.ts";

test('annotation screenshot export reads the stored image and requires a confirmed local file', async () => {
  const source = readFileSync(join(assets, '../../../web/src/uifeedback/pharos-client.tsx'), 'utf8');
  const code = transformSync(source.slice(source.indexOf('async function exportScreenshot('), source.indexOf('\nwindow.uiFeedback =')) + ';exportScreenshot', {loader:'ts'}).code;
  const screenshot = {mime:'image/jpeg',data:'captured raster'};
  for (const outcome of ['saved', 'missing', 'write-failed', 'invalid-reply', 'network-failed']) {
    const requests = [];
    const exportScreenshot = vm.runInNewContext(code, {
      stored: async (store, id, operation) => {assert.deepEqual([store,id,operation], ['screenshots','attached','get']); return outcome === 'missing' ? undefined : screenshot;},
      AbortSignal,
      fetch: async (path, request) => {
        requests.push({path,request});
        if (outcome === 'network-failed') throw Error('Network unavailable');
        return {ok:outcome !== 'write-failed',json:async () => outcome === 'write-failed' ? {error:'Temporary storage is full'} : {url:outcome === 'invalid-reply' ? 'https://example.test/image.jpg' : 'file:///tmp/pharos-annotation-example/screenshot.jpg'}};
      },
    });
    if (outcome === 'saved') assert.equal(await exportScreenshot('attached'), 'file:///tmp/pharos-annotation-example/screenshot.jpg');
    else await assert.rejects(exportScreenshot('attached'), {missing:/screenshot is missing/, 'write-failed':/storage is full/, 'invalid-reply':/confirm/, 'network-failed':/Network unavailable/}[outcome]);
    assert.equal(requests.length, outcome === 'missing' ? 0 : 1);
    if (requests.length) {
      assert.equal(requests[0].path, '/api/feedback/screenshot');
      assert.equal(requests[0].request.method, 'POST');
      assert.deepEqual(JSON.parse(requests[0].request.body), screenshot);
    }
  }
});

function annotationCopyHarness({exportScreenshot, writeText, clear = false, annotations} = {}) {
  const source = readFileSync(join(assets, 'ui.py'), 'utf8');
  const notices = [], deleted = [], writes = [];
  const context = vm.createContext({
    feedbackAnnotations: annotations ?? [], feedbackCopying: false,
    $: () => ({checked:clear}), updateFeedbackCopyButtons() {}, saveFeedback() {},
    feedbackToast: notice => notices.push(notice),
    window: {
      uiFeedback: {exportScreenshot, removeScreenshot:id => deleted.push(id)},
      pharosHost: {clipboard:{writeText:async text => {writes.push(text); await writeText?.(text);}}},
    },
  });
  vm.runInContext(source.slice(source.indexOf('function feedbackMarkdownInline('), source.indexOf('\nfunction feedbackToast(')), context);
  return {context, notices, deleted, writes, copy:() => context.copyFeedback()};
}
const copyAnnotation = (id, screenshot = true) => ({id,has_screenshot:screenshot,note:`Change ${id}`,view:{title:'Settings',id:'settings'},label:'Heading',tag:'h1',selector:'h1',bounds:{x:0,y:0,width:10,height:20},viewport:{width:390,height:844,device_pixel_ratio:2},source:'ui.py',styles:{}});

test('annotation copy associates each screenshot link with its annotation and clears only after clipboard success', async () => {
  const exported = [], transfer = Promise.withResolvers(), copying = Promise.withResolvers();
  const app = annotationCopyHarness({clear:true,annotations:[copyAnnotation('first'),copyAnnotation('text-only',false),copyAnnotation('last')],
    exportScreenshot:async id => {exported.push(id); return `file:///tmp/pharos-annotation-${id}/screenshot.jpg`;},
    writeText:() => {copying.resolve(); return transfer.promise;}});
  const pending = app.copy();
  await app.copy();
  await copying.promise;
  assert.deepEqual(exported, ['first','last']);
  const sections = app.writes[0].split('## Annotation ').slice(1);
  assert.match(sections[0], /Change first[\s\S]*\[Open screenshot\]\(<file:\/\/\/tmp\/pharos-annotation-first\/screenshot.jpg>\)/);
  assert.doesNotMatch(sections[1], /Screenshot:/);
  assert.match(sections[2], /Change last[\s\S]*pharos-annotation-last/);
  assert.equal(app.context.feedbackAnnotations.length, 3);
  assert.deepEqual(app.deleted, []); assert.deepEqual(app.notices, []);
  app.context.feedbackAnnotations.push(copyAnnotation('added-later',false));
  transfer.resolve(); await pending;
  assert.deepEqual(app.deleted, ['first','text-only','last']);
  assert.deepEqual(Array.from(app.context.feedbackAnnotations, a => a.id), ['added-later']);
  assert.equal(app.context.feedbackCopying, false);
  assert.match(app.notices.at(-1), /Copied 3 annotations and cleared/);
});

test('annotation copy preserves screenshots on export or clipboard failure and can retry', async () => {
  for (const outcome of ['export-failed','clipboard-failed','unavailable']) {
    let fail = true;
    const app = annotationCopyHarness({clear:true,annotations:[copyAnnotation('attached')],
      exportScreenshot:outcome === 'unavailable' ? undefined : async () => {if(fail && outcome === 'export-failed')throw Error('Temporary storage is full'); return 'file:///tmp/screenshot.jpg';},
      writeText:async () => {if(fail && outcome === 'clipboard-failed')throw Error('Clipboard denied');}});
    await app.copy();
    assert.equal(app.context.feedbackAnnotations.length, 1);
    assert.deepEqual(app.deleted, []);
    assert.match(app.notices.at(-1), /Could not copy feedback/);
    assert.equal(app.context.feedbackCopying, false);
    assert.equal(app.writes.length, outcome === 'clipboard-failed' ? 1 : 0);
    if (outcome !== 'unavailable') {
      fail = false; await app.copy();
      assert.equal(app.context.feedbackAnnotations.length, 0);
      assert.deepEqual(app.deleted, ['attached']);
    }
  }
  const app = annotationCopyHarness({annotations:[copyAnnotation('text-only',false)]});
  await app.copy();
  assert.equal(app.writes.length, 1);
  assert.equal(app.context.feedbackAnnotations.length, 1);
});

test('report copy runs once while pending, settles before reporting and allows a retry', async () => {
  const source=readFileSync(join(assets,'../../../web/src/uifeedback/inbox.tsx'),'utf8');
  const copySource=source.slice(source.indexOf('  async function copyReport()'),source.indexOf('\n  async function mutate('));
  for(const outcome of ['native','native-failure','browser-denial']){
    const transfers=[],writes=[];let browserCalls=0;
    const transfer=()=>{const pending=Promise.withResolvers();transfers.push(pending);return pending.promise;};
    const window=loadClassicScript('host-services.js',{
      ...(outcome==='browser-denial'?{}:{webkit:{messageHandlers:{pharosClipboard:{postMessage:text=>{writes.push(text);return transfer();}}}}}),
      navigator:{clipboard:{writeText:()=>{browserCalls++;return outcome==='browser-denial'?transfer():Promise.reject(Error('Browser denied'));}}},
    }).window;
    const detail={id:'report',comment:'A report\nwith source text',annotations:[]};
    // The guard is a ref, so presses before React re-renders see it too.
    const copyPending={current:false};let copying=false,notice='Previous success',error='Previous failure';
    const copy=vm.runInNewContext(copySource+';copyReport',{
      window,detail,copyPending,setCopying:value=>{copying=value;},setNotice:value=>{notice=value;},setError:value=>{error=value;},
    });
    // The first attempt settles with the outcome; a retry then succeeds.
    for(const succeed of outcome==='native'?[true]:[false,true]){
      const started=transfers.length;
      const pending=copy();
      assert.equal(copying,true);assert.equal(notice,'');assert.equal(error,'');
      await copy();await copy();
      assert.equal(transfers.length,started+1,'repeat presses while pending do not start another write');
      if(succeed)transfers.at(-1).resolve(true);else transfers.at(-1).reject(Error('Copy denied'));
      await pending;
      assert.equal(copying,false);assert.equal(copyPending.current,false);
      if(succeed){assert.ok(notice);assert.equal(error,'');}
      else {assert.equal(notice,'');assert.match(error,/could not copy/i);}
    }
    const attempts=outcome==='native'?1:2;
    assert.deepEqual(writes,outcome==='browser-denial'?[]:Array(attempts).fill(JSON.stringify(detail,null,2)));
    assert.equal(browserCalls,outcome==='browser-denial'?attempts:0);
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
