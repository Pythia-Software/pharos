import React, { useEffect, useRef, useState } from "react";
import type { FieldSchema } from "@pythia-software/query-table-core";
import { collectQuery, migrateSavedQueries, moveBookmark, readBookmarks, removeBookmark, updateBookmark, writeBookmarks, type Bookmark } from "./bookmarks";
import { clearPreviewCache, loadQueryPreview, queryRecipes, suggestionPool, type QueryPreview, type QuerySuggestion } from "./curiosity-data";
import { queryFromURL, rollingWindowDays, rollingWindowField, type QueryDataset } from "./query-links";
import { categoryForQuery } from "./query-categories";
import { QueryCard } from "./query-card";
import { QueryConveyor } from "./query-conveyor";
import { Icon } from "./icons";
import "./home.css";

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
  const [draft, setDraft] = useState(""), [kind, setKind] = useState("text");
  const [fuzzy, setFuzzy] = useState(true), [caseSensitive, setCaseSensitive] = useState(false), [separators, setSeparators] = useState(false);
  const input = useRef<HTMLInputElement>(null), editor = useRef<HTMLDialogElement>(null);
  const [editing, setEditing] = useState<Bookmark | null>(null), [editTitle, setEditTitle] = useState(""), [editDescription, setEditDescription] = useState("");
  const [editDays, setEditDays] = useState("");
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
  useEffect(() => { if (active) { input.current?.focus({ preventScroll: true }); setGeneration(value => value + 1); } }, [active]);

  useEffect(() => {
    if (!active) return;
    const controller = new AbortController();
    setLoading(true);
    const saved = new Set(collection.map(item => item.templateId).filter(Boolean));
    const jobs = [
      ...collection.map(item => ({ id: item.id, spec: queryFromURL(item.url), recipe: queryRecipes.find(recipe => recipe.id === item.templateId?.split(":")[0]), url: item.url })),
      ...pool.filter(item => ![...saved].some(id => id!.split(":")[0] === item.id)).map(item => ({ id: item.instanceId, spec: { dataset: item.dataset, query: item.query, window: item.window }, recipe: item, url: item.url })),
    ];
    let cursor = 0;
    async function work() {
      while (cursor < jobs.length && !controller.signal.aborted) {
        const job = jobs[cursor++];
        if (!job.spec) continue;
        try {
          const preview = await loadQueryPreview(job.spec.dataset, job.spec.query, schemas[job.spec.dataset], { recipe: job.recipe, window: job.spec.window, url: job.url }, controller.signal);
          if (!controller.signal.aborted) { setPreviews(current => ({ ...current, [job.id]: preview })); setErrors(current => { const next = { ...current }; delete next[job.id]; return next; }); }
        } catch (error) { if (!controller.signal.aborted) { setErrors(current => ({ ...current, [job.id]: String(error) })); setPreviews(current => { const next = { ...current }; delete next[job.id]; return next; }); } }
      }
    }
    // A mixed pool must never fan out into dozens of simultaneous scans.
    void Promise.all([work(), work(), work()]).then(() => { if (!controller.signal.aborted) setLoading(false); });
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
    if (editing) { setEditTitle(editing.title); setEditDescription(editing.description ?? ""); setEditDays(String(queryFromURL(editing.url)?.window?.days ?? "")); setEditError(""); editor.current?.showModal(); }
  }, [editing]);

  function keep(item: QuerySuggestion, before?: string) {
    try {
      const saved = collectQuery(item.dataset, item.query, item.title, { description: item.description, templateId: item.instanceId, window: item.window });
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
  const editingSpec = editing && queryFromURL(editing.url);
  const canEditWindow = editingSpec && Boolean(editingSpec.window?.field ?? rollingWindowField(editingSpec.dataset));
  return <div className="curiosity-home" data-feedback-label="Home dashboard">
    <div className="home-intro"><h1>{welcome}</h1><form role="search" onSubmit={event => {
      event.preventDefault(); const params = new URLSearchParams();
      if (draft.trim()) params.set("search", draft.trim()); if (kind !== "text") params.set("kind", kind);
      if (!fuzzy) params.set("fuzzy", "0"); if (caseSensitive) params.set("case", "1"); if (separators) params.set("separators", "1");
      navigate(`/library${params.size ? `?${params}` : ""}`);
    }}>
      <div className="home-search"><Icon name="search" /><input ref={input} type="search" value={draft} onChange={event => setDraft(event.target.value)} aria-label="Search your library" autoComplete="off" spellCheck={false}
        placeholder={kind === "file" ? "A file path or name…" : kind === "url" ? "A URL or host…" : "Search your conversations, ideas, and past work…"} /><button type="submit" aria-label="Search library"><Icon name="arrow-right" /></button></div>
      <div className="home-search-types library-view-toggle" role="group" aria-label="Search type"><span className="home-search-thumb" aria-hidden="true" style={{ transform: `translateX(${["text", "file", "url"].indexOf(kind) * 100}%)` }} />{[["text", "Conversation text", "search"], ["file", "Modified file", "read"], ["url", "Tool URL", "web"]].map(([value, label, icon]) => <button key={value} type="button" aria-pressed={kind === value} onClick={() => { setKind(value); input.current?.focus(); }}><Icon name={icon as "search"} /><span>{label}</span></button>)}</div>
      {kind === "text" ? <fieldset className="home-search-options" aria-label="Text search options"><label><input type="checkbox" checked={fuzzy} onChange={event => setFuzzy(event.target.checked)} /> Fuzzy words</label><label><input type="checkbox" checked={caseSensitive} onChange={event => setCaseSensitive(event.target.checked)} /> Case sensitive</label><label title="For quoted phrases, require punctuation and spaces exactly as typed"><input type="checkbox" checked={separators} onChange={event => setSeparators(event.target.checked)} /> Match separators</label></fieldset> : null}
    </form></div>
    <div className={`home-collection${drag ? " accepting" : ""}`} role="region" aria-label="Your collection" onDragOver={event => { if (drag) { event.preventDefault(); const target = (event.target as Element).closest("[data-card-id]"); setInsertBefore(target?.getAttribute("data-card-id") ?? undefined); } }} onDrop={event => drop(event)}>
      {collection.map(item => {
        const spec = queryFromURL(item.url), recipe = queryRecipes.find(recipe => recipe.id === item.templateId?.split(":")[0]);
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
        const updated = updateBookmark(editing.id, editTitle, editDescription, editDays ? Number(editDays) : undefined);
        if (updated.url !== editing.url) {
          setPreviews(current => { const next = { ...current }; delete next[editing.id]; return next; });
          setErrors(current => { const next = { ...current }; delete next[editing.id]; return next; });
        }
        editor.current?.close(); setMessage("Saved your changes.");
      } catch (error) { setEditError(String(error)); }
    }}>
      <h2 id="homeEditorTitle">Make this view yours</h2>
      <label>Title<input autoFocus value={editTitle} maxLength={200} required onChange={event => setEditTitle(event.target.value)} /></label>
      <label>Description<textarea value={editDescription} maxLength={500} onChange={event => setEditDescription(event.target.value)} /></label>
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
