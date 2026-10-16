import React, { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { FieldSchema } from "@pythia-software/query-table-core";
import { cardTemplates } from "./card-library-model";
import { instantiateRecipe, loadQueryPreviews, queryRecipes, type QueryPreview, type QuerySuggestion } from "./curiosity-data";
import { cardLoadingPresentation } from "./card-loading-model";
import type { PreviewJob } from "./query-previews";
import { QueryCard } from "./query-card";
import { usePreviewReload } from "./use-preview-reload";
import { Icon } from "./icons";
import type { QueryDataset } from "./query-links";
import { type CardReviewStudy, type CardVerdict, type ReviewFeedback } from "./card-review-model";
import "./human-word-review.css";

function initialFeedback(study: CardReviewStudy) { try { return study.feedback.readReviewFeedback(localStorage.getItem(study.storageKey)); } catch { return study.feedback.readReviewFeedback(null); } }
function sampleSize(preview?: QueryPreview): string {
  if (preview?.study) return preview.study.sampleLabel;
  const counts = [["human_messages", "messages"], ["human_conversations", "conversations"], ["human_work", "work items"]].flatMap(([id, label]) => {
    const metric = preview?.metrics?.find(metric => metric.clause.id === id && metric.clause.groupBy.length === 0);
    const value = metric?.result.buckets[0]?.value;
    return typeof value === "number" ? [`${value.toLocaleString()} ${label}`] : [];
  });
  return counts.length ? counts.join(" · ") : preview?.evidence ? `Top result: ${preview.evidence}` : "";
}

// Feedback changes frequently; chart props change only when the query or its
// data changes. Keep dense SVG rendering out of the typing path entirely.
const ReviewCardPreview = memo(function ReviewCardPreview({ item, days, preview, error, schema, onOpen, onKeep, onReload }: {
  item: QuerySuggestion; days: number; preview?: QueryPreview; error?: string; schema: FieldSchema;
  onOpen: (url: string) => void; onKeep: (item: QuerySuggestion) => void; onReload: (job: PreviewJob) => void;
}) {
  return <QueryCard id={item.id} title={item.title} description={item.description} actionLabel={item.action} url={item.url} category={item.category} windowLabel={`Last ${days}d`} discovery preview={preview} error={error}
    loadingSpec={cardLoadingPresentation(item.query, { recipe: item, cover: item.cover, days })}
    onOpen={() => onOpen(item.url)} onKeep={() => onKeep(item)}
    onReload={() => onReload({ id: item.id, dataset: item.dataset, query: item.query, schema, options: { recipe: item, cover: item.cover, window: item.window, url: item.url } })} />;
});

export function CategoryCardReview({ study, schemas, onOpen, onKeep, collected, message }: { study: CardReviewStudy; schemas: Record<QueryDataset, FieldSchema>; onOpen: (url: string) => void; onKeep: (item: QuerySuggestion) => void; collected: Set<string | undefined>; message: string }) {
  const [days, setDays] = useState(() => { const value = Number(new URLSearchParams(location.search).get("review_days")); return [1, 7, 30, 90].includes(value) ? value : 7; });
  const [secondaryDays, setSecondaryDays] = useState(() => { const value = Number(new URLSearchParams(location.search).get(study.secondaryWindow?.param ?? "")); return [1, 7, 30, 90].includes(value) ? value : study.secondaryWindow?.defaultDays ?? 7; });
  const [recipes, setRecipes] = useState(cardTemplates);
  const [feedback, setFeedback] = useState<ReviewFeedback>(() => initialFeedback(study)), [saveError, setSaveError] = useState("");
  const [previews, setPreviews] = useState<Record<string, QueryPreview>>({}), [errors, setErrors] = useState<Record<string, string>>({});
  const [generation, setGeneration] = useState(0), [copyStatus, setCopyStatus] = useState(""), [exportText, setExportText] = useState("");
  const exportDialog = useRef<HTMLDialogElement>(null);
  const latestFeedback = useRef(feedback), saveTimer = useRef<ReturnType<typeof setTimeout>>(), lastSaved = useRef<string>();
  latestFeedback.current = feedback;
  const saveFeedback = useCallback(() => {
    clearTimeout(saveTimer.current);
    try {
      const serialized = JSON.stringify(latestFeedback.current);
      if (serialized !== lastSaved.current) { localStorage.setItem(study.storageKey, serialized); lastSaved.current = serialized; }
      setSaveError("");
    } catch { setSaveError("Draft storage is unavailable. Copy your feedback before leaving this page."); }
  }, [study.storageKey]);
  const cards = useMemo(() => study.theses.flatMap(thesis => thesis.cards).map(card => instantiateRecipe(recipes.find(recipe => recipe.id === card.id) ?? queryRecipes.find(recipe => recipe.id === card.id)!, study.secondaryWindow?.cardIds.includes(card.id) ? secondaryDays : days)), [recipes, days, secondaryDays, study]);
  const reloadPreview = usePreviewReload(setPreviews, setErrors, [cards, schemas, generation]);
  useEffect(() => {
    const refresh = () => { setRecipes(cardTemplates()); setGeneration(value => value + 1); };
    window.addEventListener("pharos:card-templates", refresh); window.addEventListener("pharos:data-refresh", refresh);
    return () => { window.removeEventListener("pharos:card-templates", refresh); window.removeEventListener("pharos:data-refresh", refresh); };
  }, []);
  useEffect(() => {
    saveTimer.current = setTimeout(saveFeedback, 350);
    setCopyStatus("");
    return () => clearTimeout(saveTimer.current);
  }, [feedback, saveFeedback]);
  useEffect(() => {
    const hide = () => { if (document.visibilityState === "hidden") saveFeedback(); };
    window.addEventListener("pagehide", saveFeedback);
    document.addEventListener("visibilitychange", hide);
    return () => {
      window.removeEventListener("pagehide", saveFeedback);
      document.removeEventListener("visibilitychange", hide);
      saveFeedback();
    };
  }, [saveFeedback]);
  useEffect(() => {
    const url = new URL(location.href); url.searchParams.set("review_days", String(days)); if (study.secondaryWindow) url.searchParams.set(study.secondaryWindow.param, String(secondaryDays)); history.replaceState(null, "", `${url.pathname}${url.search}${url.hash}`); setCopyStatus("");
  }, [days, secondaryDays, study]);
  useEffect(() => {
    const controller = new AbortController(); setPreviews({}); setErrors({});
    void loadQueryPreviews(cards.map(item => ({ id: item.id, dataset: item.dataset, query: item.query, schema: schemas[item.dataset], options: { recipe: item, cover: item.cover, window: item.window, url: item.url } })), (id, preview, error) => {
      if (controller.signal.aborted) return;
      if (preview) setPreviews(current => ({ ...current, [id]: preview }));
      if (error) setErrors(current => ({ ...current, [id]: String(error) }));
    }, controller.signal);
    return () => controller.abort();
  }, [cards, schemas, generation]);
  function updateCard(id: string, patch: { verdict?: CardVerdict; note?: string }) {
    setFeedback(current => ({ ...current, cards: { ...current.cards, [id]: { ...(current.cards[id] ?? { note: "" }), ...patch } } }));
  }
  const report = () => study.feedback.exportFeedback(feedback, days, Object.fromEntries(cards.map(card => [card.id, card.title])), Object.fromEntries(cards.map(card => [card.id, card.window.days])));
  async function copy() {
    const text = report();
    try { await navigator.clipboard.writeText(text); setCopyStatus("Copied. Paste your feedback into the chat."); }
    catch { setExportText(text); exportDialog.current?.showModal(); setCopyStatus("Select and copy the report below."); }
  }
  const completed = cards.filter(card => previews[card.id] || errors[card.id]).length;
  return <section className="human-review" data-review-category={study.category} aria-label={study.label} data-feedback-label={study.label} onBlurCapture={saveFeedback}>
    <div className="human-review-heading"><div><span className="human-review-eyebrow">{study.eyebrow}</span><h1>{study.title}</h1><p>{study.intro}</p></div><a href="/home?cards=library" onClick={event => { if (!event.metaKey && !event.ctrlKey) { event.preventDefault(); onOpen("/home?cards=library"); } }}>Card library <Icon name="arrow-right" /></a></div>
    <div className="human-review-scope"><strong>{study.scopeTitle}</strong><p>{study.scope}</p></div>
    <nav className="human-review-thesis-nav" aria-label="Review sections">{study.theses.map((thesis, index) => <a key={thesis.id} href={`#review-${thesis.id}`}><span>0{index + 1}</span>{thesis.title}</a>)}</nav>
    <div className="human-review-toolbar"><label>Preview window<select aria-label="Review preview window" value={days} onChange={event => setDays(Number(event.target.value))}>{[1, 7, 30, 90].map(days => <option key={days} value={days}>{days === 1 ? "Past 24 hours" : `Past ${days} days`}</option>)}</select></label>{study.secondaryWindow ? <label>{study.secondaryWindow.label}<select aria-label={study.secondaryWindow.label} value={secondaryDays} onChange={event => setSecondaryDays(Number(event.target.value))}>{[1, 7, 30, 90].map(window => <option key={window} value={window}>{window === 1 ? "Past 24 hours" : `Past ${window} days`}</option>)}</select></label> : null}<span role="status">{completed < cards.length ? `Loading ${completed}/${cards.length} previews…` : "Live library previews"}<small>{study.feedback.reviewedCardCount(feedback)} of {cards.length} cards reviewed</small></span><button type="button" onClick={() => { setExportText(report()); exportDialog.current?.showModal(); }}>View report</button><button type="button" className="human-review-copy" onClick={() => void copy()}><Icon name="copy" />Copy feedback</button></div>
    <p className="human-review-status" role="status">{copyStatus || message || saveError || "Feedback saves in this browser as you type. Copy the report when you’re ready to discuss it."}</p>
    {study.theses.map((thesis, index) => <section className="human-review-thesis" id={`review-${thesis.id}`} key={thesis.id} aria-labelledby={`thesis-${thesis.id}`}>
      <div className="human-review-thesis-heading"><span>0{index + 1}</span><div><h2 id={`thesis-${thesis.id}`}>{thesis.title}</h2><p>{thesis.thesis}</p></div></div>
      <div className="human-review-grid">{thesis.cards.map(definition => {
        const item = cards.find(card => card.id === definition.id)!;
        const entry = feedback.cards[item.id];
        const error = errors[item.id] && (/group population exceeds host budget/.test(errors[item.id]) ? "This window contains too many groups to preview. Choose a shorter preview window." : errors[item.id]);
        return <article className="human-review-entry" key={item.id} id={`review-card-${item.id}`} data-card-definition={item.id}>
          <div className="human-review-card-label"><span>{String(cards.indexOf(item) + 1).padStart(2, "0")}</span><span>{study.cardLabel ?? "Revised"}</span>{collected.has(item.id) ? <span>Collected</span> : null}</div>
          <ReviewCardPreview item={item} days={item.window.days} preview={previews[item.id]} error={error} schema={schemas[item.dataset]} onOpen={onOpen} onKeep={onKeep} onReload={reloadPreview} />
          <p className="human-review-sample">{sampleSize(previews[item.id])}</p>
          {previews[item.id]?.study?.workspaceRows?.length ? <div className="human-review-workspaces"><strong>Within each workspace directory</strong><small>Five most active · context contributions / assessed contributions</small>{previews[item.id].study!.workspaceRows!.map(row => <a href={row.url} key={row.location} title={row.location} onClick={event => { if (!event.metaKey && !event.ctrlKey) { event.preventDefault(); onOpen(row.url); } }}><span>{row.label}</span><strong>{(row.value * 100).toLocaleString(undefined, { maximumFractionDigits: 1 })}% <small>{row.count}/{row.total}</small></strong></a>)}</div> : null}
          <p className="human-review-rationale">{definition.rationale}</p>
          <details><summary>How this is counted</summary><p>{definition.calculation}</p><p>{item.note}</p>{previews[item.id]?.study?.detail ? <p>{previews[item.id].study!.detail}</p> : null}<code>{item.id}</code></details>
          {error ? <div className="human-review-error" role="alert">{error}<button type="button" onClick={() => setGeneration(value => value + 1)}>Retry previews</button></div> : null}
          {previews[item.id]?.study?.evidence?.length ? <div className="human-review-evidence"><strong>Inspect the evidence</strong>{previews[item.id].study!.evidence!.map(evidence => <a key={evidence.id} href={evidence.url} onClick={event => { if (!event.metaKey && !event.ctrlKey) { event.preventDefault(); onOpen(evidence.url); } }}><small className="human-review-evidence-kind">{evidence.label}</small><q>{evidence.text}</q><span>Open message <Icon name="arrow-right" /></span></a>)}</div> : null}
          <div className="human-review-verdict" role="group" aria-label={`Verdict for ${item.title}`}>{([['keep', 'Keep'], ['change', 'Change'], ['drop', 'Drop']] as const).map(([value, label]) => <button type="button" key={value} aria-pressed={entry?.verdict === value} onClick={() => updateCard(item.id, { verdict: entry?.verdict === value ? undefined : value })}>{label}</button>)}</div>
          <label className="human-review-note">Feedback on this card<textarea aria-label={`Feedback on ${item.title}`} placeholder="What would make this useful to you?" value={entry?.note ?? ""} onChange={event => updateCard(item.id, { note: event.target.value })} /></label>
        </article>;
      })}</div>
      <label className="human-review-section-note">On this group <span>{thesis.question}</span><textarea aria-label={`Feedback on ${thesis.title}`} placeholder="Your thoughts on this group…" value={feedback.theses[thesis.id] ?? ""} onChange={event => setFeedback(current => ({ ...current, theses: { ...current.theses, [thesis.id]: event.target.value } }))} /></label>
    </section>)}
    <section className="human-review-overall" aria-labelledby="human-review-overall-title"><span className="human-review-eyebrow">THE SET AS A WHOLE</span><h2 id="human-review-overall-title">What belongs on your dashboard?</h2><p>Which cards would you use together? What feels repetitive, what is missing, and what would you want to see first?</p><label><span className="human-review-sr-only">Overall feedback</span><textarea aria-label="Overall feedback" placeholder="If I could keep only three of these…" value={feedback.overall} onChange={event => setFeedback(current => ({ ...current, overall: event.target.value }))} /></label><button type="button" className="human-review-copy" onClick={() => void copy()}><Icon name="copy" />Copy all feedback for chat</button></section>
    <dialog ref={exportDialog} className="human-review-export" aria-labelledby="human-review-export-title"><h2 id="human-review-export-title">Your feedback report</h2><p>Copy this text and paste it into the chat. Card IDs keep each comment attached to its definition.</p><textarea aria-label="Feedback report" readOnly value={exportText} onFocus={event => event.currentTarget.select()} /><button type="button" onClick={() => exportDialog.current?.close()}>Close</button></dialog>
  </section>;
}
