import React, { useEffect, useRef, useState } from "react";
import type { FieldSchema } from "@pythia-software/query-table-core";
import { collectDurableQuery, migrateSavedQueries, moveBookmark, readBookmarks, removeBookmark, updateBookmark, writeBookmarks, type Bookmark } from "./bookmarks";
import { clearPreviewCache, loadQueryPreviews, queryRecipes, suggestionPool, type QueryPreview, type QuerySuggestion } from "./curiosity-data";
import { queryFromURL, rollingWindowDays, rollingWindowField, type QueryDataset } from "./query-links";
import { categoryForQuery } from "./query-categories";
import { QueryCard } from "./query-card";
import { QueryConveyor } from "./query-conveyor";
import { Icon } from "./icons";
import "./home.css";
import { HomeSearch } from "./search-composer";

const welcomes = ["What will you discover in your work?", "How will you be more efficient today?", "What could your past work teach you?", "Which pattern is worth a closer look?", "Where will curiosity take you today?"];
const isHome = () => ["/", "/home"].includes(location.pathname);
function navigate(url: string) { window.pharosNavigate?.(url); }

export function HomePage({ schemas }: { schemas: Record<QueryDataset, FieldSchema<Record<string, any>>> }) {
  const [active, setActive] = useState(isHome);
  const [collection, setCollection] = useState<Bookmark[]>(readBookmarks);
  const [pool, setPool] = useState<QuerySuggestion[]>(suggestionPool);
  const [previews, setPreviews] = useState<Record<string, QueryPreview>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true), [generation, setGeneration] = useState(0);
  const [welcome] = useState(() => welcomes[Math.floor(Math.random() * welcomes.length)]);
  const editor = useRef<HTMLDialogElement>(null);
  const [editing, setEditing] = useState<Bookmark | null>(null), [editTitle, setEditTitle] = useState(""), [editDescription, setEditDescription] = useState("");
  const [editDays, setEditDays] = useState("");
  const [editCover, setEditCover] = useState("");
  const [editError, setEditError] = useState("");
  const [message, setMessage] = useState(""), [undo, setUndo] = useState<{ item: Bookmark; index: number } | null>(null);
  const [drag, setDrag] = useState<{ saved?: string; suggestion?: QuerySuggestion } | null>(null), [insertBefore, setInsertBefore] = useState<string | undefined>();
  const [hidden, setHidden] = useState(document.hidden);

  useEffect(() => {
    migrateSavedQueries(); setCollection(readBookmarks());
    const bookmarks = () => setCollection(readBookmarks());
    const route = () => setActive(isHome());
    const visibility = () => setHidden(document.hidden);
    const refresh = () => { clearPreviewCache(); setGeneration(value => value + 1); };
    window.addEventListener("pharos:bookmarks", bookmarks); window.addEventListener("pharos:route", route);
    window.addEventListener("pharos:view", route);
    window.addEventListener("pharos:data-refresh", refresh); document.addEventListener("visibilitychange", visibility);
    return () => { window.removeEventListener("pharos:bookmarks", bookmarks); window.removeEventListener("pharos:route", route); window.removeEventListener("pharos:view", route); window.removeEventListener("pharos:data-refresh", refresh); document.removeEventListener("visibilitychange", visibility); };
  }, []);
  useEffect(() => { if (active) { setGeneration(value => value + 1); } }, [active]);

  useEffect(() => {
    if (!active) return;
    const controller = new AbortController();
    setLoading(true);
    const saved = new Set(collection.map(item => item.templateId).filter(Boolean));
    const jobs = [
      ...collection.map(item => ({ id: item.id, spec: queryFromURL(item.url, item.query), options: { recipe: queryRecipes.find(recipe => recipe.id === item.templateId?.split(":")[0]), url: item.url, coverMetricId: item.coverMetricId } })),
      ...pool.filter(item => ![...saved].some(id => id!.split(":")[0] === item.id)).map(item => ({ id: item.instanceId, spec: { dataset: item.dataset, query: item.query, window: item.window }, options: { recipe: item, url: item.url, suggested: true } })),
    ];
    void loadQueryPreviews(jobs.flatMap(job => job.spec ? [{ id: job.id, dataset: job.spec.dataset, query: job.spec.query, schema: schemas[job.spec.dataset], options: { ...job.options, window: job.spec.window } }] : []), (id, preview, error) => {
      if (controller.signal.aborted) return;
      if (preview) { setPreviews(current => ({ ...current, [id]: preview })); setErrors(current => { const next = { ...current }; delete next[id]; return next; }); }
      else { setErrors(current => ({ ...current, [id]: String(error) })); setPreviews(current => { const next = { ...current }; delete next[id]; return next; }); }
    }, controller.signal).then(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [active, collection, pool, generation, schemas]);

  const collectedTemplates = new Set(collection.map(item => item.templateId?.split(":")[0]));
  const poolById = new Map(pool.map(item => [item.instanceId, item]));
  // Keep completion order as the ready queue. The conveyor reveals the first
  // card in the middle, then stages pairs around it without shifting that anchor.
  const eligible = Object.keys(previews).flatMap(id => {
    const item = poolById.get(id);
    return item && previews[id]?.state === "ready" && !errors[id] && !collectedTemplates.has(item.id) ? [item] : [];
  });
  useEffect(() => {
    if (editing) { setEditTitle(editing.title); setEditDescription(editing.description ?? ""); setEditDays(String(queryFromURL(editing.url, editing.query)?.window?.days ?? "")); setEditCover(editing.coverMetricId ?? ""); setEditError(""); editor.current?.showModal(); }
  }, [editing]);

  async function keep(item: QuerySuggestion, before?: string) {
    try {
      const saved = await collectDurableQuery(item.dataset, item.query, item.title, { description: item.description, templateId: item.instanceId, window: item.window });
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
  const canEditWindow = editingSpec && Boolean(editingSpec.window?.field ?? rollingWindowField(editingSpec.dataset));
  return <div className="curiosity-home" data-feedback-label="Home dashboard">
    <div className="home-intro"><h1>{welcome}</h1><HomeSearch schema={schemas.library} /></div>
    <div className={`home-collection${drag ? " accepting" : ""}`} role="region" aria-label="Your collection" onDragOver={event => { if (drag) { event.preventDefault(); const target = (event.target as Element).closest("[data-card-id]"); setInsertBefore(target?.getAttribute("data-card-id") ?? undefined); } }} onDrop={event => drop(event)}>
      {collection.map(item => {
        const spec = queryFromURL(item.url, item.query), recipe = queryRecipes.find(recipe => recipe.id === item.templateId?.split(":")[0]);
        return <QueryCard key={item.id} id={item.id} title={item.title} description={item.description ?? "A saved view of your work."} url={item.url} windowLabel={spec?.window ? `${spec.window.days}d` : "Saved"} category={recipe?.category ?? categoryForQuery(spec?.dataset ?? item.dataset, spec?.query)}
          preview={spec ? previews[item.id] : { state: "ready", total: 0, summary: "Bookmarked page", excerpt: item.url.split("?")[0].slice(1) || "Home" }} error={errors[item.id]} before={insertBefore === item.id}
          onOpen={() => navigate(item.url)} onEdit={() => setEditing(item)} onRemove={() => remove(item)} onMove={direction => move(item.id, direction)}
          onDragStart={event => dragStart(event, { saved: item.id }, item.url)} onDragEnd={finishDrag} onDrop={event => drop(event, item.id)} />;
      })}
      {!collection.length ? <div className="home-empty"><Icon name="bookmark" /><span>Make this space yours.<small>Collect a different angle below. Your saved searches live here, too.</small></span></div> : null}
      {drag ? <span className="home-drop-label"><Icon name="arrow-up" />{drag.saved ? "Drop to reorder your collection" : "Drop to add to your collection"}</span> : null}
    </div>
    <div className="home-feedback" role="status">{message}{undo ? <button type="button" onClick={() => { const items = readBookmarks(); items.splice(Math.min(undo.index, items.length), 0, undo.item); writeBookmarks(items); setUndo(null); setMessage("Restored to your collection."); }}>Undo</button> : null}</div>
    <QueryConveyor items={eligible} previews={previews} loading={loading} hasErrors={Object.keys(errors).length > 0} active={active} suspended={hidden || !active || Boolean(drag) || Boolean(editing)}
      onRefresh={() => { clearPreviewCache(); setPreviews({}); setErrors({}); setPool(suggestionPool()); }} onOpen={navigate} onKeep={keep} onDragStart={(event, item) => dragStart(event, { suggestion: item }, item.url)} onDragEnd={finishDrag} />
    <dialog ref={editor} className="home-editor" aria-labelledby="homeEditorTitle" onCancel={() => setEditing(null)} onClose={() => setEditing(null)}><form onSubmit={event => {
      event.preventDefault(); if (!editing) return;
      try {
        const updated = updateBookmark(editing.id, editTitle, editDescription, editDays ? Number(editDays) : undefined, editCover);
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
      {Boolean(editingSpec?.query.aggregations?.length) ? <label>Cover metric<select aria-label="Cover metric" aria-describedby="homeCoverHelp" value={editCover} onChange={event => setEditCover(event.target.value)}>
        <option value="">First metric</option>
        {editingSpec!.query.aggregations!.map(metric => <option key={metric.id} value={metric.id}>{metric.label ?? metric.expression ?? `${metric.op} ${metric.field ?? "rows"}`}</option>)}
      </select><small id="homeCoverHelp">Choose the chart shown on this bookmark. All metrics stay in the saved view.</small></label> : null}
      {canEditWindow ? <label>Time window<select value={editDays} aria-describedby="homeWindowHelp" onChange={event => setEditDays(event.target.value)}>
        {!editingSpec?.window ? <option value="">Keep saved range</option> : null}
        {rollingWindowDays.map(days => <option key={days} value={days}>{days === 1 ? "Today" : `Last ${days} days`}</option>)}
      </select><small id="homeWindowHelp">{editDays ? "Includes today. Moves forward each day with your work." : "Keeps the dates and time filters already in this view."}</small></label>
        : <p className="home-editor-window-note">This bookmarked page uses its own time controls.</p>}
      {editError ? <p role="alert">{editError}</p> : null}
      <div className="home-editor-actions"><button type="button" onClick={() => editor.current?.close()}>Cancel</button><button type="submit">Save changes</button></div>
    </form></dialog>
  </div>;
}
