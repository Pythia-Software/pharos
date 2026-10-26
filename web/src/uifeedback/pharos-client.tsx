import React, { useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { toJpeg } from "html-to-image";
import { boundedLog, pendingSubmission, safeURL, type Annotation, type Diagnostics, type Screenshot, type Submission } from "./model";
import "./pharos-client.css";
import { annotationForUpload, restoreReview, type FeedbackReview, type PharosAnnotation } from "./pharos-model";
declare global {
  interface Window {
    pharosFeedback?: { annotations: () => PharosAnnotation[]; clear: (ids: string[]) => void; toast: (message: string) => void; annotate: () => void };
    uiFeedback?: { safeURL: typeof safeURL; capture: (element: HTMLElement, id: string) => Promise<Screenshot>; removeScreenshot: (id: string) => void };
  }
}
const draftKey = "uifeedback.v1.draft", contactKey = "uifeedback.v1.contact";
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
async function capture(element: HTMLElement, id: string): Promise<Screenshot> {
  const width = Math.max(1, element.scrollWidth, element.getBoundingClientRect().width), height = Math.max(1, element.scrollHeight, element.getBoundingClientRect().height);
  const ratio = Math.min(1, 2048 / Math.max(width, height));
  const image = await toJpeg(element, { quality: .8, pixelRatio: ratio, backgroundColor: getComputedStyle(document.body).backgroundColor, filter: node => !(node instanceof Element) || !node.hasAttribute("data-feedback-ui"), skipFonts: false });
  const data = image.split(",")[1]; if (!data || data.length > 2 * 1024 * 1024) throw new Error("This element is too large to attach. Select a smaller element.");
  const screenshot: Screenshot = { mime: "image/jpeg", data }; await stored("screenshots", id, "put", screenshot); return screenshot;
}
window.uiFeedback = { safeURL, capture, removeScreenshot: id => { void stored("screenshots", id, "delete").catch(() => {}); } };

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
interface Draft { comment: string; kind: Submission["kind"]; email: string; remember: boolean }
function readDraft(): Draft {
  const empty: Draft = { comment: "", kind: "feedback", email: "", remember: false };
  try { const draft = JSON.parse(localStorage.getItem(draftKey) || "null"), email = localStorage.getItem(contactKey) || ""; return { ...empty, ...draft, email: draft?.email ?? email, remember: Boolean(email) }; } catch { return empty; }
}
async function annotationsForSubmission(): Promise<Annotation[]> {
  return await Promise.all((window.pharosFeedback?.annotations() ?? []).map(async a => {
    const screenshot = a.has_screenshot ? await stored<Screenshot>("screenshots", a.id, "get") : undefined;
    if (a.has_screenshot && !screenshot) throw new Error("An attached screenshot is missing. Remove that annotation and capture it again.");
    return annotationForUpload(a, screenshot);
  }));
}
function Composer({ close }: { close: () => void }) {
  const [draft, setDraft] = useState(readDraft), [annotations, setAnnotations] = useState<Annotation[]>([]), [ready, setReady] = useState(false), [error, setError] = useState("");
  const [diagnostics, setDiagnostics] = useState<Diagnostics | null>(null), [logs, setLogs] = useState(false), [collecting, setCollecting] = useState(false), [busy, setBusy] = useState(false), [receipt, setReceipt] = useState("");
  const [sourceIDs, setSourceIDs] = useState<string[]>([]);
  const dialog = useRef<HTMLDialogElement>(null);
  const busyRef = useRef(busy); busyRef.current = busy || collecting;
  const closeRef = useRef<() => void>(close); closeRef.current = () => { void keepDraft(); };
  useEffect(() => { const el = dialog.current!, previous = document.activeElement; el.showModal(); const cancel = (e: Event) => { e.preventDefault(); if (!busyRef.current) closeRef.current(); }; el.addEventListener("cancel", cancel);
    return () => { el.removeEventListener("cancel", cancel); el.close(); if (previous instanceof HTMLElement) restoreFocus(previous); };
  }, []);
  useEffect(() => { void Promise.all([annotationsForSubmission(), stored<FeedbackReview>("outbox", "review", "get")]).then(([items, saved]) => {
    const review = restoreReview(saved, items); setAnnotations(review.annotations); setSourceIDs(review.sourceIDs); setDiagnostics(review.diagnostics); setLogs(review.includeDiagnostics); setReady(true);
  }, e => setError(String(e))); }, []);
  useEffect(() => { if (receipt) return; try { localStorage.setItem(draftKey, JSON.stringify(draft)); if (draft.remember && draft.email) localStorage.setItem(contactKey, draft.email); else localStorage.removeItem(contactKey); } catch { setError("Could not save this draft. Check local storage before sending."); } }, [draft, receipt]);
  useEffect(() => { if (ready && !receipt) void saveReview().catch(e => setError(String(e))); }, [annotations, sourceIDs, diagnostics, logs, ready, receipt]);
  function saveReview() { return stored<FeedbackReview>("outbox", "review", "put", { annotations, sourceIDs, diagnostics, includeDiagnostics: logs }); }
  async function keepDraft(afterClose?: () => void) {
    if (busyRef.current) return;
    busyRef.current = true;
    setBusy(true);
    try { if (ready && !receipt) await saveReview(); close(); afterClose?.(); }
    catch (e) { busyRef.current = false; setError(String(e)); setBusy(false); }
  }
  function update(patch: Partial<Draft>) { setDraft(value => ({ ...value, ...patch })); }
  async function collect() {
    if (logs) { setLogs(false); return; }
    setCollecting(true); setError("");
    try {
      const res = await fetch("/api/feedback/diagnostics"); if (!res.ok) throw new Error("Could not collect service diagnostics. Try again, or send without logs.");
      const data = await res.json(); setDiagnostics({ browser: boundedLog(`Browser: ${navigator.userAgent}\n${browserErrors}`), requests: boundedLog(failedRequests), service: boundedLog(String(data.service || "")) }); setLogs(true);
    } catch (e) { setError(String(e)); } finally { setCollecting(false); }
  }
  async function send(event: React.FormEvent) {
    event.preventDefault(); if (!ready || busy) return; setBusy(true); setError("");
    try {
      const previous = await stored<Submission>("outbox", "pending", "get") ?? null;
      const submission = pendingSubmission({ version: 1, application: { name: "Pharos" }, kind: draft.kind, comment: draft.comment.trim(), email: draft.email.trim() || undefined, annotations, diagnostics: logs && diagnostics ? diagnostics : undefined }, previous, () => crypto.randomUUID());
      const body = JSON.stringify(submission); if (new TextEncoder().encode(body).length > 8 * 1024 * 1024) throw new Error("This report exceeds 8 MiB. Remove screenshots or split it into smaller reports.");
      await stored("outbox", "pending", "put", submission);
      const res = await fetch("/api/feedback/submit", { method: "POST", headers: { "Content-Type": "application/json" }, body, signal: AbortSignal.timeout(55000) });
      if (!res.ok) { const value = await res.json().catch(() => ({})); throw new Error(value.error || "Could not send feedback. Your draft is saved; try again."); }
      const value = await res.json(); if (value.id !== submission.id || !value.received) throw new Error("Receipt could not be confirmed. Retry to confirm delivery.");
      setReceipt(value.id); window.pharosFeedback?.clear(annotations.map(a => a.id));
      try { await stored("outbox", "pending", "delete"); await stored("outbox", "review", "delete"); for (const a of annotations) await stored("screenshots", a.id, "delete"); localStorage.removeItem(draftKey); }
      catch { setError("Feedback was received, but this device could not clear its saved draft."); }
    } catch (e) { setError(String(e)); } finally { setBusy(false); }
  }
  return <dialog className="uifeedback-dialog" ref={dialog} aria-labelledby="uifeedback-title" data-feedback-ui><form onSubmit={event => void send(event)}><header><h2 id="uifeedback-title">{receipt ? "Feedback sent" : "Send feedback"}</h2><button type="button" disabled={busy || collecting} onClick={() => void keepDraft()} aria-label="Close feedback">Close</button></header>
    {receipt ? <><p role="status">Your feedback was received. Thank you.</p><p>Report: <code>{receipt}</code></p><p>{draft.email ? "We can contact you at the address you provided." : "No contact email was included."}</p></> : <>
      <label>Type<select value={draft.kind} disabled={busy} onChange={e => update({ kind: e.target.value as Draft["kind"] })}><option value="feedback">Feedback</option><option value="bug">Bug report</option><option value="feature">Feature request</option><option value="prompt">Prompt to build</option></select></label>
      <label>What would you like changed?<textarea value={draft.comment} maxLength={16000} disabled={busy} onChange={e => update({ comment: e.target.value })} placeholder={annotations.length ? "Optional context for these annotations" : "Describe the issue, request, or prompt"} required={!annotations.length} /></label>
      <button type="button" disabled={busy || collecting || !ready} onClick={() => void keepDraft(() => window.pharosFeedback?.annotate())}>Annotate an element</button>
      <section aria-label="Included annotations">{annotations.map((a, i) => <article key={a.id}><h3>{i + 1}. {a.label}</h3><p>{a.comment}</p><small><code>{a.selector}</code> · {new Date(a.captured_at).toLocaleString()}</small>{a.url && <p className="uifeedback-url">{a.url}</p>}{a.text && <blockquote>{a.text}</blockquote>}{a.screenshot && <img alt={`Screenshot of ${a.label}`} src={`data:${a.screenshot.mime};base64,${a.screenshot.data}`} />}<button type="button" disabled={busy} onClick={() => setAnnotations(items => items.filter(item => item.id !== a.id))}>Exclude from this report</button></article>)}{!annotations.length && <p>No annotated elements included. You can send general feedback here.</p>}</section>
      <label>Contact email (optional)<input type="email" maxLength={254} value={draft.email} disabled={busy} onChange={e => update({ email: e.target.value })} autoComplete="email" /></label>
      <label className="uifeedback-check"><input type="checkbox" disabled={busy} checked={draft.remember} onChange={e => update({ remember: e.target.checked })} />Remember my email on this device for future reports</label>
      <button type="button" disabled={busy} onClick={() => { update({ email: "", remember: false }); localStorage.removeItem(contactKey); }}>Forget my email</button>
      <label className="uifeedback-check"><input type="checkbox" disabled={busy || collecting || !ready} checked={logs} onChange={() => void collect()} />Include recent browser errors, failed requests, and service logs</label>
      {collecting && <p role="status">Collecting recent logs…</p>}
      {logs && diagnostics && <details open><summary>Review or edit attached logs</summary>{(["browser", "requests", "service"] as const).map(kind => <label key={kind}>{kind}<textarea disabled={busy} value={diagnostics[kind] || ""} onChange={e => setDiagnostics(d => ({ ...d, [kind]: boundedLog(e.target.value) }))} /></label>)}</details>}
      <p className="uifeedback-privacy">Sending shares your comments, annotations, layout details, and selected attachments with Pythia Software. Reports are private and retained for 90 days. An email address is used only to follow up on your feedback. Prompts enter a review queue.</p>
      <footer><button type="button" disabled={busy || collecting} onClick={() => void keepDraft()}>Keep draft</button><button className="primary" disabled={!ready || busy || collecting} type="submit">{busy ? "Sending…" : "Send feedback"}</button></footer>
    </>}{error && <p role="alert" className="uifeedback-error">{error}</p>}
  </form></dialog>;
}
function restoreFocus(opener: HTMLElement) {
  const target = opener.getClientRects().length ? opener : document.getElementById("headerMore"); target?.focus();
}
if (!(window as unknown as { pharosShare?: unknown }).pharosShare) {
  const toolbar = document.querySelector(".feedback-toolbar-controls");
  if (toolbar) { const button = document.createElement("button"); button.type = "button"; button.id = "feedbackSend"; button.textContent = "Send feedback"; toolbar.append(button);
    const open = (opener: HTMLButtonElement) => { const host = document.createElement("div"); document.body.append(host); const root = createRoot(host); root.render(<Composer close={() => { root.unmount(); host.remove(); restoreFocus(opener); }} />); };
    button.onclick = () => open(button);
    const header = document.getElementById("feedbackSubmitToggle"); if (header instanceof HTMLButtonElement) header.onclick = () => open(header);
  }
}
