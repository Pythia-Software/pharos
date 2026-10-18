import React, { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { QueryCard } from "./query-card";
import { Icon } from "./icons";
import type { QueryPreview } from "./query-previews";
import type { CardVerdict, ReviewFeedback } from "./card-review-model";
import { coverageDrafts, coverageFeedback, coverageReviewKey, coverageScope, coverageStatus, coverageTheses, draftContext, type DraftCard } from "./coverage-review-model";
import "./card-review.css";

const windows = [7, 30, 90] as const;
const verdicts = [["keep", "Keep"], ["change", "Change"], ["drop", "Discard"]] as const;
function initialFeedback(): ReviewFeedback { try { return coverageFeedback.readReviewFeedback(localStorage.getItem(coverageReviewKey)); } catch { return coverageFeedback.readReviewFeedback(null); } }

// Feedback changes on every keystroke; covers change only with their data.
const DraftPreview = memo(function DraftPreview({ card, days, preview, error, onOpen, onReload }: { card: DraftCard; days: number; preview?: QueryPreview; error?: string; onOpen: (url: string) => void; onReload: (card: DraftCard) => void }) {
  const url = card.url(days);
  return <QueryCard id={card.id} title={card.title(days)} description={card.description(days)} actionLabel={card.action} url={url} category={card.category}
    windowLabel={card.windowLabel ?? (card.window === "current" ? "Now" : card.window === "snapshot" ? "Snapshot" : `Last ${days}d`)} discovery preview={preview} error={error}
    loadingSpec={{ kind: card.loading, count: card.loadingCount ?? 5, headline: card.loading === "line", height: card.height ?? 205 }} onOpen={() => onOpen(url)} onReload={() => onReload(card)} />;
});

// Draft cards for app areas whose calculations are not Query Table fields
// yet. Each card reads the same API as its destination page, so the review
// judges the real numbers before any typed card adapter is built.
export function DraftCardReview({ onOpen }: { onOpen: (url: string) => void }) {
  const [days, setDays] = useState(() => { const value = Number(new URLSearchParams(location.search).get("review_days")); return (windows as readonly number[]).includes(value) ? value : 30; });
  const [feedback, setFeedback] = useState<ReviewFeedback>(initialFeedback), [saveError, setSaveError] = useState("");
  const [previews, setPreviews] = useState<Record<string, QueryPreview>>({}), [errors, setErrors] = useState<Record<string, string>>({});
  const [generation, setGeneration] = useState(0), [copyStatus, setCopyStatus] = useState(""), [exportText, setExportText] = useState("");
  const exportDialog = useRef<HTMLDialogElement>(null);
  const latestFeedback = useRef(feedback), saveTimer = useRef<ReturnType<typeof setTimeout>>(), lastSaved = useRef<string>();
  latestFeedback.current = feedback;
  const saveFeedback = useCallback(() => {
    clearTimeout(saveTimer.current);
    try {
      const serialized = JSON.stringify(latestFeedback.current);
      if (serialized !== lastSaved.current) { localStorage.setItem(coverageReviewKey, serialized); lastSaved.current = serialized; }
      setSaveError("");
    } catch { setSaveError("Draft storage is unavailable. Copy your feedback before leaving this page."); }
  }, []);
  useEffect(() => {
    const refresh = () => setGeneration(value => value + 1);
    window.addEventListener("pharos:data-refresh", refresh);
    return () => window.removeEventListener("pharos:data-refresh", refresh);
  }, []);
  useEffect(() => { saveTimer.current = setTimeout(saveFeedback, 350); setCopyStatus(""); return () => clearTimeout(saveTimer.current); }, [feedback, saveFeedback]);
  useEffect(() => {
    const hide = () => { if (document.visibilityState === "hidden") saveFeedback(); };
    window.addEventListener("pagehide", saveFeedback); document.addEventListener("visibilitychange", hide);
    return () => { window.removeEventListener("pagehide", saveFeedback); document.removeEventListener("visibilitychange", hide); saveFeedback(); };
  }, [saveFeedback]);
  useEffect(() => {
    const url = new URL(location.href); url.searchParams.set("review_days", String(days));
    history.replaceState(null, "", `${url.pathname}${url.search}${url.hash}`); setCopyStatus("");
  }, [days]);
  const load = useCallback((cards: DraftCard[], signal: AbortSignal) => {
    const context = draftContext(days, signal);
    for (const card of cards) {
      card.load(context).then(preview => {
        if (signal.aborted) return;
        setPreviews(current => ({ ...current, [card.id]: preview }));
        setErrors(current => { const next = { ...current }; delete next[card.id]; return next; });
      }, error => { if (!signal.aborted) setErrors(current => ({ ...current, [card.id]: error instanceof Error ? error.message : String(error) })); });
    }
  }, [days]);
  // A retry belongs to the current window: changing it (or reloading the set)
  // cancels pending retries so they cannot overwrite the new previews.
  const retries = useRef(new Map<string, AbortController>());
  useEffect(() => {
    const controller = new AbortController(), pending = retries.current; setPreviews({}); setErrors({});
    load(coverageDrafts, controller.signal);
    return () => { controller.abort(); for (const retry of pending.values()) retry.abort(); pending.clear(); };
  }, [load, generation]);
  const reload = useCallback((card: DraftCard) => {
    retries.current.get(card.id)?.abort();
    const controller = new AbortController(); retries.current.set(card.id, controller);
    setPreviews(current => { const next = { ...current }; delete next[card.id]; return next; });
    setErrors(current => { const next = { ...current }; delete next[card.id]; return next; });
    load([card], controller.signal);
  }, [load]);
  function updateCard(id: string, patch: { verdict?: CardVerdict; note?: string }) {
    setFeedback(current => ({ ...current, cards: { ...current.cards, [id]: { ...(current.cards[id] ?? { note: "" }), ...patch } } }));
  }
  const titles = useMemo(() => Object.fromEntries(coverageDrafts.map(card => [card.id, card.title(days)])), [days]);
  const report = () => coverageFeedback.exportFeedback(feedback, days, titles).replace(/^Verdict: drop$/gm, "Verdict: discard");
  async function copy() {
    const text = report();
    try { await navigator.clipboard.writeText(text); setCopyStatus("Copied. Paste your feedback into the chat."); }
    catch { setExportText(text); exportDialog.current?.showModal(); setCopyStatus("Select and copy the report below."); }
  }
  const open = (url: string) => (event: React.MouseEvent) => { if (!event.metaKey && !event.ctrlKey) { event.preventDefault(); onOpen(url); } };
  const completed = coverageDrafts.filter(card => previews[card.id] || errors[card.id]).length;
  let index = 0;
  return <section className="human-review" data-review-category="coverage" aria-label="Draft card review" data-feedback-label="Draft card review" onBlurCapture={saveFeedback}>
    <div className="human-review-heading"><div><span className="human-review-eyebrow">CARD STUDIES / 12 · UNCOVERED AREAS · REVIEW 4</span><h1>The rest of Pharos, on your dashboard</h1><p>Draft cards for the parts of Pharos that have no Home card yet: machine time and parallelism, carbon, findings and pricing. Review 4 adds seven-day trends for tool calls, tool call time and agents at once, removes the overlap headline, and shortens the model-size pie.</p></div><a href="/home?cards=library" onClick={open("/home?cards=library")}>Card library <Icon name="arrow-right" /></a></div>
    <div className="human-review-scope"><strong>What makes these drafts different?</strong><p>{coverageScope}</p></div>
    <nav className="human-review-thesis-nav" aria-label="Review sections">{coverageTheses.map((thesis, n) => <a key={thesis.id} href={`#review-${thesis.id}`}><span>0{n + 1}</span>{thesis.title}</a>)}</nav>
    <div className="human-review-toolbar"><label>Preview window<select aria-label="Review preview window" value={days} onChange={event => setDays(Number(event.target.value))}>{windows.map(window => <option key={window} value={window}>{`Past ${window} days`}</option>)}</select></label><span role="status">{completed < coverageDrafts.length ? `Loading ${completed}/${coverageDrafts.length} previews…` : "Live library previews"}<small>{coverageFeedback.reviewedCardCount(feedback)} of {coverageDrafts.length} cards reviewed</small></span><button type="button" onClick={() => { setExportText(report()); exportDialog.current?.showModal(); }}>View report</button><button type="button" className="human-review-copy" onClick={() => void copy()}><Icon name="copy" />Copy feedback</button></div>
    <p className="human-review-status" role="status">{copyStatus || saveError || "Feedback saves in this browser as you type. Copy the report when you’re ready to discuss it."}</p>
    {coverageTheses.map((thesis, n) => <section className="human-review-thesis" id={`review-${thesis.id}`} key={thesis.id} aria-labelledby={`thesis-${thesis.id}`}>
      <div className="human-review-thesis-heading"><span>0{n + 1}</span><div><h2 id={`thesis-${thesis.id}`}>{thesis.title}</h2><p>{thesis.thesis}</p></div></div>
      <div className="human-review-grid">{thesis.cards.map(definition => {
        const card = coverageDrafts.find(card => card.id === definition.id)!, entry = feedback.cards[card.id], preview = previews[card.id];
        index += 1;
        return <article className="human-review-entry" key={card.id} id={`review-card-${card.id}`} data-card-definition={card.id}>
          <div className="human-review-card-label"><span>{String(index).padStart(2, "0")}</span><span>{coverageStatus[card.id] ?? "Draft"}</span>{card.window === "current" ? <span>Current state</span> : card.window === "snapshot" ? <span>Timing snapshot</span> : null}</div>
          <DraftPreview card={card} days={days} preview={preview} error={errors[card.id]} onOpen={onOpen} onReload={reload} />
          <p className="human-review-sample">{preview?.evidence ?? ""}</p>
          <p className="human-review-rationale">{definition.rationale}</p>
          <details><summary>How this is counted</summary><p>{definition.calculation}</p>{preview?.note ? <p>{preview.note}</p> : null}<code>{card.id}</code></details>
          {errors[card.id] ? <div className="human-review-error" role="alert">{errors[card.id]}<button type="button" onClick={() => reload(card)}>Retry preview</button></div> : null}
          <div className="human-review-verdict" role="group" aria-label={`Verdict for ${card.title(days)}`}>{verdicts.map(([value, label]) => <button type="button" key={value} aria-pressed={entry?.verdict === value} onClick={() => updateCard(card.id, { verdict: entry?.verdict === value ? undefined : value })}>{label}</button>)}</div>
          <label className="human-review-note">Reasoning<textarea aria-label={`Feedback on ${card.title(days)}`} placeholder="Why keep, change, or discard it? What would make it useful?" value={entry?.note ?? ""} onChange={event => updateCard(card.id, { note: event.target.value })} /></label>
        </article>;
      })}</div>
      <label className="human-review-section-note">On this group <span>{thesis.question}</span><textarea aria-label={`Feedback on ${thesis.title}`} placeholder="Your thoughts on this group…" value={feedback.theses[thesis.id] ?? ""} onChange={event => setFeedback(current => ({ ...current, theses: { ...current.theses, [thesis.id]: event.target.value } }))} /></label>
    </section>)}
    <section className="human-review-overall" aria-labelledby="human-review-overall-title"><span className="human-review-eyebrow">THE SET AS A WHOLE</span><h2 id="human-review-overall-title">Which areas earn a place on Home?</h2><p>Which of these would you collect? What is missing, which areas should stay on their own pages, and what should a typed card adapter support first?</p><label><span className="human-review-sr-only">Overall feedback</span><textarea aria-label="Overall feedback" placeholder="If I could keep only three of these…" value={feedback.overall} onChange={event => setFeedback(current => ({ ...current, overall: event.target.value }))} /></label><button type="button" className="human-review-copy" onClick={() => void copy()}><Icon name="copy" />Copy all feedback for chat</button></section>
    <dialog ref={exportDialog} className="human-review-export" aria-labelledby="human-review-export-title"><h2 id="human-review-export-title">Your feedback report</h2><p>Copy this text and paste it into the chat. Card IDs keep each comment attached to its draft.</p><textarea aria-label="Feedback report" readOnly value={exportText} onFocus={event => event.currentTarget.select()} /><button type="button" onClick={() => exportDialog.current?.close()}>Close</button></dialog>
  </section>;
}
