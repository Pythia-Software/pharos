import React, { useEffect, useId, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { toJpeg } from "html-to-image";
import { boundedLog, pendingSubmission, safeURL, type Annotation, type Diagnostics, type Screenshot, type Submission } from "./model";
import { Icon } from "../icons";
import { preferences } from "../preferences";
import { SearchHelp } from "../search-help";
import { DictationButton } from "../dictation-button";
import "./pharos-client.css";
import { annotationForUpload, createAskFeedbackSender, feedbackReceipt, restoreReview, type AskFeedbackOutcome, type FeedbackReceipt, type FeedbackReview, type PharosAnnotation, type PreparedAskFeedback } from "./pharos-model";
declare global {
  interface Window {
    pharosFeedback?: { annotations: () => PharosAnnotation[]; clear: (ids: string[]) => void; toast: (message: string) => void; annotate: () => void };
    pharosSendFeedback?: () => void;
    pharosMountAskFeedback?: (host: HTMLElement, draft: AskFeedbackDraft) => () => void;
    uiFeedback?: { safeURL: typeof safeURL; capture: (element: HTMLElement, id: string) => Promise<Screenshot>; exportScreenshot: (id: string) => Promise<string>; removeScreenshot: (id: string) => void };
  }
}
type AskFeedbackDraft = { id: string; kind: "feature" | "bug" | "feedback"; comment: string };
// The contact email is an app preference (Settings → App preferences), so it
// follows the library; this device's earlier remembered address moves there once.
const draftKey = "uifeedback.v1.draft", contactKey = "pharos-feedback-contact-email", legacyContactKey = "uifeedback.v1.contact";
try { const legacy = localStorage.getItem(legacyContactKey); if (legacy !== null) { if (!preferences().get(contactKey, "")) preferences().set(contactKey, legacy); localStorage.removeItem(legacyContactKey); } } catch { /* Storage is unavailable; nothing to move. */ }
function saveContact(email: string) { if (email) preferences().set(contactKey, email); else preferences().remove(contactKey); }
let database: Promise<IDBDatabase> | null = null;
function db(): Promise<IDBDatabase> {
  if (!database) database = new Promise((resolve, reject) => {
    const request = indexedDB.open("uifeedback.v1", 1);
    request.onupgradeneeded = () => { for (const name of ["screenshots", "outbox"]) request.result.createObjectStore(name); };
    request.onsuccess = () => resolve(request.result); request.onerror = () => { database = null; reject(new Error("Local feedback storage is unavailable.")); };
    request.onblocked = () => reject(new Error("Close another Pharos tab to enable feedback storage."));
  });
  return database;
}
async function stored<T>(store: "screenshots" | "outbox", key: string, operation: "get" | "put" | "delete", value?: T): Promise<T | undefined> {
  const database = await db(); return new Promise((resolve, reject) => {
    const tx = database.transaction(store, operation === "get" ? "readonly" : "readwrite"), object = tx.objectStore(store);
    const request = operation === "get" ? object.get(key) : operation === "put" ? object.put(value, key) : object.delete(key);
    tx.oncomplete = () => resolve(operation === "get" ? request.result as T | undefined : undefined);
    tx.onerror = tx.onabort = () => reject(new Error("Could not save feedback locally. Free storage space and try again."));
  });
}
// Check the tombstone and write in one transaction. A delayed autosave from
// another mount or tab must never recreate the data of a discarded proposal.
async function saveAskDraft(id: string, values: Record<string, unknown>, discard = false): Promise<boolean> {
  const database = await db(), prefix = `ask:${id}:`;
  return new Promise((resolve, reject) => {
    const tx = database.transaction("outbox", "readwrite"), object = tx.objectStore("outbox");
    let saved = false;
    const request = object.get(prefix + "discarded");
    request.onsuccess = () => {
      if (request.result && !discard) return;
      const sent = object.get(prefix + "sent");
      sent.onsuccess = () => {
        if (sent.result) return;
        if (discard) {
          for (const name of ["draft", "review", "pending"]) object.delete(prefix + name);
          object.put(true, prefix + "discarded");
        } else for (const [name, value] of Object.entries(values)) object.put(value, prefix + name);
        saved = true;
      };
    };
    tx.oncomplete = () => resolve(saved);
    tx.onerror = tx.onabort = () => reject(new Error("Could not save feedback locally. Free storage space and try again."));
  });
}
async function prepareAskFeedback(id: string, payload: Omit<Submission, "id">): Promise<PreparedAskFeedback> {
  const database = await db(), prefix = `ask:${id}:`;
  return new Promise((resolve, reject) => {
    const tx = database.transaction("outbox", "readwrite"), object = tx.objectStore("outbox");
    let result: PreparedAskFeedback;
    const sent = object.get(prefix + "sent");
    sent.onsuccess = () => {
      if (sent.result) { result = { status: "sent", receipt: sent.result }; return; }
      const discarded = object.get(prefix + "discarded");
      discarded.onsuccess = () => {
        if (discarded.result) { result = { status: "discarded" }; return; }
        const pending = object.get(prefix + "pending");
        pending.onsuccess = () => {
          const submission = pendingSubmission(payload, pending.result ?? null, () => crypto.randomUUID());
          object.put(submission, prefix + "pending");
          result = { status: "pending", submission };
        };
      };
    };
    tx.oncomplete = () => resolve(result);
    tx.onerror = tx.onabort = () => reject(new Error("Could not prepare feedback locally. Your draft is saved; try again."));
  });
}
async function completeAskFeedback(id: string, submission: Submission): Promise<void> {
  const database = await db(), prefix = `ask:${id}:`;
  return new Promise((resolve, reject) => {
    const tx = database.transaction("outbox", "readwrite"), object = tx.objectStore("outbox");
    object.put(feedbackReceipt(submission), prefix + "sent");
    for (const name of ["pending", "review", "draft"]) object.delete(prefix + name);
    tx.oncomplete = () => resolve();
    tx.onerror = tx.onabort = () => reject(new Error("Feedback was received, but its receipt could not be saved. Retry to confirm delivery."));
  });
}
function feedbackBody(submission: Submission): string {
  const body = JSON.stringify(submission);
  if (new TextEncoder().encode(body).length > 8 * 1024 * 1024) throw new Error("This report exceeds 8 MiB. Remove screenshots or split it into smaller reports.");
  return body;
}
async function submitFeedback(submission: Submission, body = feedbackBody(submission)): Promise<void> {
  const res = await fetch("/api/feedback/submit", { method: "POST", headers: { "Content-Type": "application/json" }, body, signal: AbortSignal.timeout(55000) });
  if (!res.ok) { const value = await res.json().catch(() => ({})); throw new Error(value.error || "Could not send feedback. Your draft is saved; try again."); }
  const value = await res.json();
  if (value.id !== submission.id || !value.received) throw new Error("Receipt could not be confirmed. Retry to confirm delivery.");
}
const askFeedbackSender = createAskFeedbackSender({ prepare: prepareAskFeedback, submit: submitFeedback, complete: completeAskFeedback });
async function capture(element: HTMLElement, id: string): Promise<Screenshot> {
  const width = Math.max(1, element.scrollWidth, element.getBoundingClientRect().width), height = Math.max(1, element.scrollHeight, element.getBoundingClientRect().height);
  const ratio = Math.min(1, 2048 / Math.max(width, height));
  const image = await toJpeg(element, { quality: .8, pixelRatio: ratio, backgroundColor: getComputedStyle(document.body).backgroundColor, filter: node => !(node instanceof Element) || !node.hasAttribute("data-feedback-ui"), skipFonts: false });
  const data = image.split(",")[1]; if (!data || data.length > 2 * 1024 * 1024) throw new Error("This element is too large to attach. Select a smaller element.");
  const screenshot: Screenshot = { mime: "image/jpeg", data }; await stored("screenshots", id, "put", screenshot); return screenshot;
}
async function exportScreenshot(id: string): Promise<string> {
  const screenshot = await stored<Screenshot>("screenshots", id, "get");
  if (!screenshot) throw new Error("An attached screenshot is missing. Remove that annotation and capture it again.");
  const res = await fetch("/api/feedback/screenshot", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(screenshot), signal: AbortSignal.timeout(15000) });
  const value = await res.json();
  if (!res.ok) throw new Error(value.error || "Could not save the screenshot to temporary storage. Try again.");
  if (typeof value.url !== "string" || !value.url.startsWith("file:///")) throw new Error("Could not confirm the saved screenshot file. Try again.");
  return value.url;
}
window.uiFeedback = { safeURL, capture, exportScreenshot, removeScreenshot: id => { void stored("screenshots", id, "delete").catch(() => {}); } };

// These buffers contain neither HTTP headers nor request/response bodies.
let browserErrors = "", failedRequests = "";
addEventListener("error", event => { browserErrors = boundedLog(browserErrors + `\n${new Date().toISOString()} ${event.message}\n${event.error?.stack ?? ""}`); });
addEventListener("unhandledrejection", event => { browserErrors = boundedLog(browserErrors + `\n${new Date().toISOString()} ${event.reason instanceof Error ? event.reason.stack ?? event.reason.message : String(event.reason)}`); });
const originalFetch = window.fetch.bind(window);
window.fetch = async (input, init) => {
  const requestURL = input instanceof Request ? input.url : String(input);
  let path = ""; try { const u = new URL(requestURL, location.href); if (u.origin === location.origin && u.pathname.startsWith("/api/") && !u.pathname.startsWith("/api/feedback/")) path = u.pathname; } catch { /* Native fetch reports malformed input. */ }
  const record = (status: string) => { if (path) failedRequests = boundedLog(failedRequests + `\n${new Date().toISOString()} ${init?.method ?? (input instanceof Request ? input.method : "GET")} ${path} ${status}`); };
  try { const response = await originalFetch(input, init); if (!response.ok) record(String(response.status)); return response; } catch (e) { record("network error"); throw e; }
};
interface Draft { comment: string; kind: Submission["kind"] }
// Manual reports have no type to choose; Ask proposals keep the kind they were proposed with.
function readDraft(): Draft {
  const empty: Draft = { comment: "", kind: "feedback" };
  try { const draft = JSON.parse(localStorage.getItem(draftKey) || "null"); return { ...empty, comment: draft?.comment ?? empty.comment }; } catch { return empty; }
}
async function annotationsForSubmission(): Promise<Annotation[]> {
  return await Promise.all((window.pharosFeedback?.annotations() ?? []).map(async a => {
    const screenshot = a.has_screenshot ? await stored<Screenshot>("screenshots", a.id, "get") : undefined;
    if (a.has_screenshot && !screenshot) throw new Error("An attached screenshot is missing. Remove that annotation and capture it again.");
    return annotationForUpload(a, screenshot);
  }));
}
function Composer({ close, proposal }: { close?: () => void; proposal?: AskFeedbackDraft }) {
  // Ask drafts have separate storage: opening one cannot overwrite or attach
  // an unrelated manual report, annotations, screenshots, or diagnostic logs.
  const key = (name: string) => proposal ? `ask:${proposal.id}:${name}` : name;
  const [draft, setDraft] = useState<Draft>(() => proposal ? { kind: proposal.kind, comment: proposal.comment } : readDraft()), [email, setEmail] = useState(() => String(preferences().get(contactKey, ""))), [annotations, setAnnotations] = useState<Annotation[]>([]), [ready, setReady] = useState(false), [error, setError] = useState("");
  const [diagnostics, setDiagnostics] = useState<Diagnostics | null>(null), [logs, setLogs] = useState(false), [collecting, setCollecting] = useState(false), [busy, setBusy] = useState(false), [receipt, setReceipt] = useState<FeedbackReceipt | null>(null);
  const [sourceIDs, setSourceIDs] = useState<string[]>([]), [reviewingLogs, setReviewingLogs] = useState(false), logsPanel = useId();
  const dialog = useRef<HTMLDialogElement>(null), fields = useRef<HTMLDivElement>(null);
  const comment = useRef<HTMLTextAreaElement>(null);
  const [collapsed, setCollapsed] = useState(false), [discarded, setDiscarded] = useState(false);
  const discardMessage = useRef<HTMLParagraphElement>(null), discardedHere = useRef(false);
  const sentHere = useRef(false), wasCollapsed = useRef(false), receiptMessage = useRef<HTMLParagraphElement>(null);
  const busyRef = useRef(busy); busyRef.current = busy;
  const closeRef = useRef<() => void>(() => close?.()); closeRef.current = () => { void keepDraft(); };
  useEffect(() => { if (proposal) return; const el = dialog.current!, previous = document.activeElement, header = document.getElementById("feedbackSubmitToggle"); el.showModal(); header?.setAttribute("aria-expanded", "true"); const cancel = (e: Event) => { e.preventDefault(); if (!busyRef.current) closeRef.current(); }; el.addEventListener("cancel", cancel);
    return () => { el.removeEventListener("cancel", cancel); el.close(); header?.setAttribute("aria-expanded", "false"); if (previous instanceof HTMLElement) restoreFocus(previous); };
  }, []);
  useEffect(() => {
    let mounted = true;
    const pending = proposal ? askFeedbackSender.pending(proposal.id) : undefined;
    if (pending) { busyRef.current = true; setBusy(true); }
    void Promise.all([proposal ? Promise.resolve([]) : annotationsForSubmission(), stored<FeedbackReview>("outbox", key("review"), "get"), proposal ? stored<Draft>("outbox", key("draft"), "get") : undefined, proposal ? stored<FeedbackReceipt>("outbox", key("sent"), "get") : undefined, proposal ? stored<boolean>("outbox", key("discarded"), "get") : undefined]).then(async ([items, saved, savedDraft, sent, wasDiscarded]) => {
      if (!mounted) return;
      const review = restoreReview(saved, items); setAnnotations(review.annotations); setSourceIDs(review.sourceIDs); setDiagnostics(review.diagnostics); setLogs(review.includeDiagnostics); setReady(true);
      if (savedDraft) setDraft(savedDraft);
      if (sent) applyOutcome({ status: "sent", receipt: sent });
      if (wasDiscarded && !sent) setDiscarded(true);
      if (pending) {
        try { const outcome = await pending; if (mounted) applyOutcome(outcome); }
        catch (e) { if (mounted) setError(String(e)); }
        finally { if (mounted) { busyRef.current = false; setBusy(false); } }
      }
    }, e => { if (mounted) { setError(String(e)); busyRef.current = false; setBusy(false); } });
    return () => { mounted = false; };
  }, []);
  useEffect(() => { if (receipt || proposal) return; try { localStorage.setItem(draftKey, JSON.stringify(draft)); } catch { setError("Could not save this draft. Check local storage before sending."); } }, [draft, receipt]);
  useEffect(() => { if (proposal && ready && !receipt && !discarded) void saveAskDraft(proposal.id, { draft }).catch(e => setError(String(e))); }, [draft, ready, receipt, discarded]);
  useEffect(() => { if (ready && !receipt && !discarded) void saveReview().catch(e => setError(String(e))); }, [annotations, sourceIDs, diagnostics, logs, ready, receipt, discarded]);
  useEffect(() => { if (discarded && discardedHere.current) discardMessage.current?.focus({ preventScroll: true }); }, [discarded]);
  useEffect(() => {
    if (!collapsed && (wasCollapsed.current || receipt && sentHere.current)) {
      const target = receipt ? receiptMessage.current : fields.current?.querySelector("textarea");
      target?.focus({ preventScroll: true });
    }
    wasCollapsed.current = collapsed;
  }, [receipt, collapsed]);
  function applyOutcome(outcome: AskFeedbackOutcome) {
    if (outcome.status === "discarded") { setDiscarded(true); return; }
    const sent = outcome.receipt;
    setReceipt(sent); setDiscarded(false); setEmail(sent.email || "");
    if (sent.kind !== undefined && sent.comment !== undefined) setDraft({ kind: sent.kind, comment: sent.comment });
  }
  async function saveReview() {
    const review: FeedbackReview = { annotations, sourceIDs, diagnostics, includeDiagnostics: logs };
    if (proposal) await saveAskDraft(proposal.id, { draft, review });
    else await stored<FeedbackReview>("outbox", key("review"), "put", review);
  }
  async function discardDraft() {
    if (!proposal || !ready || busyRef.current || receipt || discarded) return;
    busyRef.current = true; setBusy(true); setError("");
    try {
      if (!await saveAskDraft(proposal.id, {}, true)) {
        const sent = await stored<FeedbackReceipt>("outbox", key("sent"), "get");
        if (sent) applyOutcome({ status: "sent", receipt: sent });
        return;
      }
      discardedHere.current = true; setDiscarded(true);
      setDraft({ kind: proposal.kind, comment: "" }); setDiagnostics(null); setLogs(false);
    } catch (e) { setError(String(e)); }
    finally { busyRef.current = false; setBusy(false); }
  }
  // Clears the manual report everywhere it is kept, including its annotations
  // on the page and their screenshots, then closes without sending anything.
  async function discardReport() {
    if (proposal || busyRef.current || receipt) return;
    busyRef.current = true; setBusy(true); setError("");
    try {
      const ids = [...new Set([...sourceIDs, ...annotations.map(a => a.id)])];
      window.pharosFeedback?.clear(ids);
      for (const id of ids) await stored("screenshots", id, "delete");
      await stored("outbox", key("review"), "delete"); await stored("outbox", key("pending"), "delete");
      localStorage.removeItem(draftKey);
      close?.();
    } catch (e) { busyRef.current = false; setBusy(false); setError(String(e)); }
  }
  async function keepDraft(afterClose?: () => void) {
    if (busyRef.current) return;
    busyRef.current = true;
    setBusy(true);
    try {
      if (ready && !receipt) await saveReview();
      if (proposal) { setCollapsed(true); busyRef.current = false; setBusy(false); } else close?.();
      afterClose?.();
    }
    catch (e) { busyRef.current = false; setError(String(e)); setBusy(false); }
  }
  function update(patch: Partial<Draft>) { setDraft(value => ({ ...value, ...patch })); }
  async function includeLogs(on: boolean) {
    if (!on) { setLogs(false); return; }
    setCollecting(true); setError("");
    try {
      const res = await fetch("/api/feedback/diagnostics"); if (!res.ok) throw new Error("Could not collect service diagnostics. Try again, or send without logs.");
      const data = await res.json(); setDiagnostics({ browser: boundedLog(`Browser: ${navigator.userAgent}\n${browserErrors}`), requests: boundedLog(failedRequests), service: boundedLog(String(data.service || "")) }); setLogs(true);
    } catch (e) { setError(String(e)); } finally { setCollecting(false); }
  }
  async function send() {
    if (!ready || busyRef.current || receipt || discarded || !Array.from(fields.current?.querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>("input,textarea,select") ?? []).every(field => field.reportValidity())) return;
    busyRef.current = true; setBusy(true); setError("");
    try {
      await saveReview();
      const payload: Omit<Submission, "id"> = { version: 1, application: { name: "Pharos" }, kind: draft.kind, comment: draft.comment.trim(), email: email.trim() || undefined, annotations, diagnostics: logs && diagnostics ? diagnostics : undefined };
      if (proposal) {
        const outcome = await askFeedbackSender.send(proposal.id, payload);
        sentHere.current = outcome.status === "sent";
        applyOutcome(outcome);
        return;
      }
      const previous = await stored<Submission>("outbox", key("pending"), "get") ?? null;
      const submission = pendingSubmission(payload, previous, () => crypto.randomUUID());
      const body = feedbackBody(submission);
      await stored("outbox", key("pending"), "put", submission);
      await submitFeedback(submission, body);
      sentHere.current = true; setReceipt(feedbackReceipt(submission)); window.pharosFeedback?.clear(annotations.map(a => a.id));
      try {
        await stored("outbox", key("pending"), "delete"); await stored("outbox", key("review"), "delete");
        for (const a of annotations) await stored("screenshots", a.id, "delete"); localStorage.removeItem(draftKey);
      }
      catch { setError("Feedback was received, but this device could not clear its saved draft."); }
    } catch (e) { setError(String(e)); } finally { busyRef.current = false; setBusy(false); }
  }
  if (proposal && discarded && !receipt) return <section className="uifeedback-inline" aria-label="Feedback draft discarded" data-feedback-ui>
    <p role="status" tabIndex={-1} ref={discardMessage}>Draft discarded.</p>
  </section>;
  const title = receipt ? "Feedback sent" : proposal ? "Review feedback from Ask Pharos" : "Send feedback";
  if (proposal && collapsed) return <section className="uifeedback-inline" aria-label="Feedback draft" data-feedback-ui>
    <strong>{receipt ? "Feedback sent" : {feature:"Feature request",bug:"Bug report",feedback:"Feedback",prompt:"Prompt to build"}[draft.kind]}</strong>
    {(!receipt || receipt.comment !== undefined) && <p className="uifeedback-draft-text">{receipt ? receipt.comment : draft.comment}</p>}
    <p role="status">{receipt ? "Your feedback was received." : "Draft kept. Nothing has been sent."}</p>
    <button type="button" autoFocus onClick={() => setCollapsed(false)}>{receipt ? "View receipt" : "Review feedback"}</button>
    {!receipt && <button type="button" disabled={!ready || busy} onClick={() => void discardDraft()}>Discard draft</button>}
  </section>;
  const Heading = proposal ? "h3" : "h2";
  const content = <div className="uifeedback-form" ref={fields} onKeyDown={proposal ? event => {
    // This editor lives inside Ask's form. Enter in a field cannot submit that
    // form or approve feedback; only the dedicated button grants consent.
    event.stopPropagation();
    if (event.key === "Escape") { event.preventDefault(); void keepDraft(); }
    else if (event.key === "Enter" && !(event.target instanceof HTMLTextAreaElement) && !(event.target instanceof HTMLButtonElement)) event.preventDefault();
  } : undefined}><div className="uifeedback-heading"><Heading id={proposal ? undefined : "uifeedback-title"}>{title}</Heading>{!proposal && <button type="button" disabled={busy} onClick={() => void keepDraft()} aria-label="Close feedback"><Icon name="close" /></button>}</div>
    {receipt ? <><p role="status" tabIndex={-1} ref={receiptMessage}>Your feedback was received. Thank you.</p><p>Report: <code>{receipt.id}</code></p><p>{email ? "We can contact you at the address you provided." : "No contact email was included."}</p></> : <>
      <label>What would you like changed?<span className="dictation-field"><textarea ref={comment} value={draft.comment} maxLength={16000} disabled={busy} onChange={e => update({ comment: e.target.value })} placeholder={annotations.length ? "Optional context for these annotations" : "Describe the issue, request, or prompt"} required={!annotations.length} /><DictationButton field={comment} onError={setError} /></span></label>
      {annotations.length > 0 && <ul className="uifeedback-annotations" aria-label="Included annotations">{annotations.map(a => <li key={a.id}>{a.screenshot && <img alt={`Screenshot of ${a.label}`} src={`data:${a.screenshot.mime};base64,${a.screenshot.data}`} />}<div><strong>{a.label}</strong>{a.comment && <span> · {a.comment}</span>}{(a.url || a.text) && <small title={[a.url, a.text].filter(Boolean).join("\n")}>{a.url || a.text}</small>}</div><button type="button" disabled={busy} onClick={() => setAnnotations(items => items.filter(item => item.id !== a.id))} aria-label={`Exclude ${a.label} from this report`} title="Exclude from this report"><Icon name="close" /></button></li>)}</ul>}
      <label>Contact email (optional)<span className="uifeedback-email"><input type="email" maxLength={254} value={email} disabled={busy} onChange={e => { setEmail(e.target.value); saveContact(e.target.value.trim()); }} autoComplete="email" /><button type="button" disabled={busy} onClick={() => void keepDraft(() => window.pharosNavigate?.("/settings/preferences"))} aria-label="Contact email settings" title="Contact email settings"><Icon name="settings" /></button></span></label>
      <div className="uifeedback-attachments">
        {!proposal && <button type="button" className="button-with-icon" disabled={busy || collecting || !ready} onClick={() => void keepDraft(() => window.pharosFeedback?.annotate())}><Icon name="target" />Annotate an element</button>}
        <label className="uifeedback-toggle button-with-icon" title="Attach recent browser errors, failed requests, and service logs"><input type="checkbox" disabled={busy || collecting || !ready} checked={logs} onChange={e => void includeLogs(e.target.checked)} />Include recent logs</label>
      </div>
      {collecting && <p role="status">Collecting recent logs…</p>}
      {logs && diagnostics && <div id={logsPanel} className="uifeedback-logs" hidden={!reviewingLogs}>{(["browser", "requests", "service"] as const).map(kind => <label key={kind}>{kind}<textarea disabled={busy} value={diagnostics[kind] || ""} onChange={e => setDiagnostics(d => ({ ...d, [kind]: boundedLog(e.target.value) }))} /></label>)}</div>}
      <footer><span className="uifeedback-links"><SearchHelp label="Privacy" text="Privacy">Sending shares your comments, annotations, layout details, and selected attachments with Pythia Software. Reports are private and retained for 90 days. An email address is used only to follow up on your feedback. Prompts enter a review queue.</SearchHelp>{logs && diagnostics && <button type="button" className="uifeedback-link" aria-expanded={reviewingLogs} aria-controls={logsPanel} onClick={() => setReviewingLogs(open => !open)}>Review attached logs</button>}</span><span className="uifeedback-actions">{proposal ? <><button type="button" disabled={!ready || busy} onClick={() => void discardDraft()}>Discard draft</button><button type="button" disabled={busy} onClick={() => void keepDraft()}>Keep draft</button></> : (draft.comment.trim() || annotations.length > 0 || logs) && <button type="button" disabled={!ready || busy} onClick={() => void discardReport()}>Discard</button>}<button className="primary" disabled={!ready || busy || collecting} type={proposal ? "button" : "submit"} onClick={proposal ? () => void send() : undefined}>{busy ? "Sending…" : proposal ? "Approve and send feedback" : "Send feedback"}</button></span></footer>
    </>}{error && <p role="alert" className="uifeedback-error">{error}</p>}
  </div>;
  return proposal ? <section className="uifeedback-inline" aria-label={title} data-feedback-ui>{content}</section>
    : <dialog id="uifeedback-dialog" className="uifeedback-dialog" ref={dialog} aria-labelledby="uifeedback-title" data-feedback-ui><form onSubmit={event => { event.preventDefault(); void send(); }}>{content}</form></dialog>;
}
function restoreFocus(opener: HTMLElement) {
  const target = opener.getClientRects().length ? opener : document.getElementById("headerMore"); target?.focus();
}
// Mount without focusing: receiving or restoring a proposal leaves the user's
// conversation editor and scroll position alone. Its owner disposes the root.
window.pharosMountAskFeedback = (host, proposal) => {
  const root = createRoot(host); root.render(<Composer proposal={proposal} />);
  return () => root.unmount();
};
const toolbar = document.querySelector(".feedback-toolbar-controls");
if (toolbar) { const button = document.createElement("button"); button.type = "button"; button.id = "feedbackSend"; button.textContent = "Send feedback"; toolbar.append(button);
  let active = false;
  const open = (opener: HTMLElement) => { if (active) return; active = true; const host = document.createElement("div"); document.body.append(host); const root = createRoot(host); root.render(<Composer close={() => { root.unmount(); host.remove(); active = false; restoreFocus(opener); }} />); };
  button.onclick = () => open(button);
  const header = document.getElementById("feedbackSubmitToggle"); if (header instanceof HTMLButtonElement) { header.setAttribute("aria-haspopup", "dialog"); header.setAttribute("aria-controls", "uifeedback-dialog"); header.setAttribute("aria-expanded", "false"); header.onclick = () => open(header); }
  // The Mac app's context menu; focus returns to the header button, or More.
  window.pharosSendFeedback = () => open(header instanceof HTMLButtonElement ? header : button);
}
