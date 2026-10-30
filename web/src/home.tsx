import { readWindowChoice } from "./day-window";
import React, { useEffect, useRef, useState } from "react";
import type { FieldSchema } from "@pythia-software/query-table-core";
import { collectDurableQuery, migrateSavedQueries, moveBookmark, readBookmarks, removeBookmark, updateBookmark, writeBookmarks, type Bookmark } from "./bookmarks";
import { clearPreviewCache, instantiateRecipe, queryRecipes, suggestionPool, suggestionWindowLabel, supportsToday, type QueryPreview, type QuerySuggestion } from "./curiosity-data";
import { queryFromURL, queryHasTimeFilter, rollingWindowDays, rollingWindowField, type QueryDataset } from "./query-links";
import { categoryForQuery } from "./query-categories";
import { QueryCard } from "./query-card";
import { QueryConveyor } from "./query-conveyor";
import { Icon } from "./icons";
import "./home.css";
import { AskPharosHome } from "./ask-pharos";
import { cardTemplates } from "./card-library-model";
import { defaultCardCover } from "./card-cover";
import { cardLoadingPresentation } from "./card-loading-model";
import { loadCardPreviews, invalidateCardPreviews } from "./card-previews";
import { usePreviewReload } from "./use-preview-reload";

const welcomes = ["What will you discover in your work?", "How will you be more efficient today?", "What could your past work teach you?", "Which pattern is worth a closer look?", "Where will curiosity take you today?"];
const isHome = () => ["/", "/home"].includes(location.pathname);
function navigate(url: string) { window.pharosNavigate?.(url); }
function timeWindowValue(spec: ReturnType<typeof queryFromURL>): string {
  if (spec?.window) return spec.window.mode === "today" ? "today" : String(spec.window.days);
  const field = spec && rollingWindowField(spec.dataset);
  return spec && field && !queryHasTimeFilter(spec.query, field) ? "all" : "";
}

export function HomePage({ schemas }: { schemas: Record<QueryDataset, FieldSchema<Record<string, any>>> }) {
  const [active, setActive] = useState(isHome);
  const [collection, setCollection] = useState<Bookmark[]>(readBookmarks);
  const [pool, setPool] = useState<QuerySuggestion[]>(() => suggestionPool(new Date(), cardTemplates()));
  const [previews, setPreviews] = useState<Record<string, QueryPreview>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true), [generation, setGeneration] = useState(0);
  const [welcome] = useState(() => welcomes[Math.floor(Math.random() * welcomes.length)]);
  const editor = useRef<HTMLDialogElement>(null);
  const [editing, setEditing] = useState<Bookmark | null>(null), [editTitle, setEditTitle] = useState(""), [editDescription, setEditDescription] = useState("");
  const [editDays, setEditDays] = useState("");
  const [editCover, setEditCover] = useState("");
  const [editError, setEditError] = useState("");
  const [feedback, setMessage] = useState(""), [undo, setUndo] = useState<{ item: Bookmark; index: number } | null>(null);
  const [drag, setDrag] = useState<{ saved?: string; suggestion?: QuerySuggestion } | null>(null), [insertBefore, setInsertBefore] = useState<string | undefined>();
  const [hidden, setHidden] = useState(document.hidden);
  const reloadPreview = usePreviewReload(setPreviews, setErrors, [active, collection, pool, generation, schemas]);

  useEffect(() => {
    migrateSavedQueries(); setCollection(readBookmarks());
    const bookmarks = () => setCollection(readBookmarks());
    const route = () => setActive(isHome());
    const templates = () => { setPool(suggestionPool(new Date(), cardTemplates())); setGeneration(value => value + 1); };
    window.addEventListener("pharos:card-templates", templates);
    // A hidden tab notes new data and reruns its cards once it is shown.
    let stale = false;
    const refresh = () => { if (document.hidden) { stale = true; return; } stale = false; clearPreviewCache(); invalidateCardPreviews(); setGeneration(value => value + 1); };
    const visibility = () => { setHidden(document.hidden); if (!document.hidden && stale) refresh(); };
    window.addEventListener("pharos:bookmarks", bookmarks); window.addEventListener("pharos:route", route);
    window.addEventListener("pharos:view", route);
    window.addEventListener("pharos:usage-refresh", refresh); window.addEventListener("pharos:data-refresh", refresh); document.addEventListener("visibilitychange", visibility);
    return () => { window.removeEventListener("pharos:card-templates", templates); window.removeEventListener("pharos:bookmarks", bookmarks); window.removeEventListener("pharos:route", route); window.removeEventListener("pharos:view", route); window.removeEventListener("pharos:usage-refresh", refresh); window.removeEventListener("pharos:data-refresh", refresh); document.removeEventListener("visibilitychange", visibility); };
  }, []);
  useEffect(() => { if (active) setGeneration(value => value + 1); }, [active]);
  useEffect(() => {
    if (!active) return;
    const controller = new AbortController();
    setLoading(true);
    const saved = new Set(collection.map(item => item.templateId).filter(Boolean));
    const jobs = [
      ...collection.map(item => ({ id: item.id, spec: queryFromURL(item.url, item.query), options: { recipe: queryRecipes.find(recipe => recipe.id === item.templateId?.split(":")[0]), url: item.url, coverMetricId: item.coverMetricId, cover: item.cover } })),
      ...pool.filter(item => ![...saved].some(id => id!.split(":")[0] === item.id)).map(item => ({ id: item.instanceId, spec: { dataset: item.dataset, query: item.query, window: item.window }, options: { recipe: item, cover: item.cover, url: item.url, suggested: true } })),
    ];
    void loadCardPreviews(jobs.flatMap(job => job.spec ? [{ id: job.id, dataset: job.spec.dataset, query: job.spec.query, schema: schemas[job.spec.dataset], options: { ...job.options, window: job.spec.window } }] : []), (id, preview, error) => {
      if (controller.signal.aborted) return;
      if (preview) { setPreviews(current => ({ ...current, [id]: preview })); setErrors(current => { const next = { ...current }; delete next[id]; return next; }); }
      else { setErrors(current => ({ ...current, [id]: String(error) })); setPreviews(current => { const next = { ...current }; delete next[id]; return next; }); }
    }, controller.signal).then(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [active, collection, pool, generation, schemas]);

  useEffect(() => {
    if (!active || hidden || loading) return;
    const controller = new AbortController();
    // Prepare default library covers before its page is opened, without
    // mounting hundreds of offscreen charts. Foreground work finishes first.
    const items = cardTemplates().map(recipe => instantiateRecipe(recipe, recipe.defaultWindow ?? (recipe.days?.length === 1 ? recipe.days[0] : 7)));
    void (async () => {
      for (let at = 0; at < items.length && !controller.signal.aborted; at += 8) {
        await loadCardPreviews(items.slice(at, at + 8).map(item => ({ id: item.id, dataset: item.dataset, query: item.query, schema: schemas[item.dataset], options: { recipe: item, cover: item.cover, window: item.window, url: item.url } })), () => {}, controller.signal);
      }
    })();
    return () => controller.abort();
  }, [active, hidden, loading, generation, schemas]);

  const collectedTemplates = new Set(collection.map(item => item.templateId?.split(":")[0]));
  const poolById = new Map(pool.map(item => [item.instanceId, item]));
  // Keep completion order as the ready queue. The conveyor reveals the first
  // card in the middle, then stages pairs around it without shifting that anchor.
  const eligible = Object.keys(previews).flatMap(id => {
    const item = poolById.get(id);
    return item && previews[id]?.state === "ready" && !errors[id] && !collectedTemplates.has(item.id) ? [item] : [];
  });
  useEffect(() => {
    if (editing) { setEditTitle(editing.title); setEditDescription(editing.description ?? ""); setEditDays(timeWindowValue(queryFromURL(editing.url, editing.query))); const metricId = editing.coverMetricId ?? editing.cover?.metricId ?? ""; setEditCover(coverChoices.some(choice => choice.id === metricId) ? metricId : ""); setEditError(""); editor.current?.showModal(); }
  }, [editing]);

  async function keep(item: QuerySuggestion, before?: string) {
    try {
      const saved = await collectDurableQuery(item.dataset, item.query, item.title, { description: item.description, templateId: item.instanceId, window: item.window, cover: item.cover ?? defaultCardCover(item), url: item.url });
      if (before) moveBookmark(saved.id, before);
      setMessage(`Collected “${item.title}”.`); setUndo(null);
      setPreviews(current => ({ ...current, [saved.id]: current[item.instanceId] }));
    } catch (error) { setMessage(String(error)); }
  }
  function drop(event: React.DragEvent, before?: string) {
    if (!drag) return;
    event.preventDefault(); event.stopPropagation();
    if (drag.saved) moveBookmark(drag.saved, before); else if (drag.suggestion) keep(drag.suggestion, before);
    setDrag(null); setInsertBefore(undefined);
  }
  function dragStart(event: React.DragEvent, value: typeof drag, url: string) {
    setDrag(value); event.dataTransfer.effectAllowed = value?.saved ? "move" : "copy"; event.dataTransfer.setData("text/plain", url);
  }
  function move(id: string, direction: -1 | 1) {
    const index = collection.findIndex(item => item.id === id), target = index + direction;
    if (target < 0 || target >= collection.length) return;
    moveBookmark(id, direction < 0 ? collection[target].id : collection[target + 1]?.id);
  }
  function remove(item: Bookmark) {
    setUndo({ item, index: collection.findIndex(other => other.id === item.id) }); removeBookmark(item.id); setMessage(`Removed “${item.title}”.`);
  }
  const finishDrag = () => { setDrag(null); setInsertBefore(undefined); };
  const editingSpec = editing && queryFromURL(editing.url, editing.query);
  const editingRecipe = queryRecipes.find(recipe => recipe.id === editing?.templateId?.split(":")[0]);
  // Current-state cards (findings) and all-time cards keep their population.
  const canEditWindow = editingSpec && !editingRecipe?.period && Boolean(editingSpec.window?.field ?? rollingWindowField(editingSpec.dataset));
  const canChooseToday = editingRecipe ? supportsToday(editingRecipe) : editingSpec && !["tl1_attempts", "findings"].includes(editingSpec.dataset);
  const coverChoices = editingSpec?.query.grouping
    ? [...editingSpec.query.grouping.summaries.filter(column => column.op !== "row").map(column => ({ id: column.id, label: `${column.label ?? column.id} · grouped rows` })), ...(editingSpec.query.grouping.source.aggregations ?? []).map(metric => ({ id: metric.id, label: `${metric.label ?? metric.id} · observations` }))]
    : editingSpec?.query.aggregations?.map(metric => ({ id: metric.id, label: metric.label ?? metric.id })) ?? [];
  return <div className="curiosity-home" data-feedback-label="Home dashboard">
    <div className="home-intro"><h1>{welcome}</h1><AskPharosHome /></div>
    <div className={`home-collection${drag ? " accepting" : ""}`} role="region" aria-label="Your collection" onDragOver={event => { if (drag) { event.preventDefault(); const target = (event.target as Element).closest("[data-card-id]"); setInsertBefore(target?.getAttribute("data-card-id") ?? undefined); } }} onDrop={event => drop(event)}>
      {collection.map(item => {
        const spec = queryFromURL(item.url, item.query), recipe = queryRecipes.find(recipe => recipe.id === item.templateId?.split(":")[0]);
        return <QueryCard key={item.id} id={item.id} title={item.title} description={item.description ?? "A saved view of your work."} actionLabel={recipe?.action} url={item.url} windowLabel={recipe?.period ? suggestionWindowLabel(recipe) : spec?.window ? suggestionWindowLabel({ window: spec.window, dataset: spec.dataset }) : timeWindowValue(spec) === "all" ? "All time" : "Saved"} category={recipe?.category ?? categoryForQuery(spec?.dataset ?? item.dataset, spec?.query)}
          loadingSpec={spec ? cardLoadingPresentation(spec.query, { recipe, cover: item.cover, metricId: item.coverMetricId, days: spec.window?.days }) : undefined}
          preview={spec ? previews[item.id] : { state: "ready", total: 0, summary: "Bookmarked page", excerpt: item.url.split("?")[0].slice(1) || "Home" }} error={errors[item.id]} before={insertBefore === item.id}
          onOpen={navigate} onEdit={() => setEditing(item)} onRemove={() => remove(item)} onMove={direction => move(item.id, direction)}
          onReload={spec ? () => reloadPreview({ id: item.id, dataset: spec.dataset, query: spec.query, schema: schemas[spec.dataset], options: { recipe, url: item.url, coverMetricId: item.coverMetricId, cover: item.cover, window: spec.window } }) : undefined}
          onDragStart={event => dragStart(event, { saved: item.id }, item.url)} onDragEnd={finishDrag} onDrop={event => drop(event, item.id)} />;
      })}
      {!collection.length ? <div className="home-empty"><Icon name="bookmark" /><span>Make this space yours.<small>Collect cards that help you see your work from different angles</small></span></div> : null}
      {drag ? <span className="home-drop-label"><Icon name="arrow-up" />{drag.saved ? "Drop to reorder your collection" : "Drop to add to your collection"}</span> : null}
    </div>
    <div className="home-feedback" role="status">{feedback}{undo ? <button type="button" onClick={() => { const items = readBookmarks(); items.splice(Math.min(undo.index, items.length), 0, undo.item); writeBookmarks(items); setUndo(null); setMessage("Restored to your collection."); }}>Undo</button> : null}</div>
    <QueryConveyor items={eligible} previews={previews} loading={loading} hasErrors={Object.keys(errors).length > 0} active={active} suspended={hidden || !active || Boolean(drag) || Boolean(editing)}
      onRefresh={() => { clearPreviewCache(); invalidateCardPreviews(); setGeneration(value => value + 1); setErrors({}); setPool(suggestionPool(new Date(), cardTemplates())); }} onOpen={navigate} onKeep={keep} onDragStart={(event, item) => dragStart(event, { suggestion: item }, item.url)} onDragEnd={finishDrag}
      onAllCards={() => navigate("/cards")} />
    <dialog ref={editor} className="home-editor" aria-labelledby="homeEditorTitle" onCancel={() => setEditing(null)} onClose={() => setEditing(null)}><form onSubmit={event => {
      event.preventDefault(); if (!editing) return;
      try {
        const updated = updateBookmark(editing.id, editTitle, editDescription, !canEditWindow ? undefined : editDays === "all" ? null : editDays ? readWindowChoice(editDays) : undefined, editCover);
        if (updated.url !== editing.url || updated.coverMetricId !== editing.coverMetricId) {
          setPreviews(current => { const next = { ...current }; delete next[editing.id]; return next; });
          setErrors(current => { const next = { ...current }; delete next[editing.id]; return next; });
        }
        editor.current?.close(); setMessage("Saved your changes.");
      } catch (error) { setEditError(String(error)); }
    }}>
      <h2 id="homeEditorTitle">Make this view yours</h2>
      <label>Title<input autoFocus value={editTitle} maxLength={200} required onChange={event => setEditTitle(event.target.value)} /></label>
      <label>Description<textarea value={editDescription} maxLength={500} onChange={event => setEditDescription(event.target.value)} /></label>
      {Boolean(coverChoices.length) ? <label>Cover metric<select aria-label="Cover metric" aria-describedby="homeCoverHelp" value={editCover} onChange={event => setEditCover(event.target.value)}>
        <option value="">First metric</option>
        {coverChoices.map(metric => <option key={metric.id} value={metric.id}>{metric.label}</option>)}
      </select><small id="homeCoverHelp">Choose the value shown on this bookmark. The full comparison stays in the saved view.</small></label> : null}
      {canEditWindow ? <label>Time window<select value={editDays} aria-describedby="homeWindowHelp" onChange={event => setEditDays(event.target.value)}>
        {!editingSpec?.window ? <option value="">Keep saved range</option> : null}
        {canChooseToday ? <option value="today">Today</option> : null}
        {rollingWindowDays.map(days => <option key={days} value={days}>{days === 1 ? "Past 24 hours" : days === 365 ? "Last year" : days === 180 ? "Last 180d" : `Past ${days} days`}</option>)}
        <option value="all">All time</option>
      </select><small id="homeWindowHelp">{editDays === "today" ? "Since local midnight, through now. Follows the current day when this query runs." : editDays === "all" ? "Removes the saved date range. Other query filters stay in place." : editDays === "365" ? "Last 365 days, through now. Evaluated when this query runs." : editDays ? "Last N days, through now. Evaluated when this query runs." : "Keeps the dates and time filters already in this view."}</small></label>
        : <p className="home-editor-window-note">This bookmarked page uses its own time controls.</p>}
      {editError ? <p role="alert">{editError}</p> : null}
      {editingRecipe?.exploration && !editingSpec?.query.grouping ? <div className="home-editor-upgrade"><button type="button" onClick={() => {
        const next = instantiateRecipe(editingRecipe, editDays === "today" ? "today" : Number(editDays) || 30);
        editor.current?.close(); navigate(next.url);
      }}>Try grouped comparison <Icon name="arrow-right" /></button><small>Open the latest version of this question. Save it from the table to keep it.</small></div> : null}
      <div className="home-editor-actions"><button type="button" onClick={() => editor.current?.close()}>Cancel</button><button type="submit">Save changes</button></div>
    </form></dialog>
  </div>;
}
