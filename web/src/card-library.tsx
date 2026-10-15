import React, { useEffect, useMemo, useRef, useState } from "react";
import type { FieldSchema, MetricDisplay } from "@pythia-software/query-table-core";
import { instantiateRecipe, loadQueryPreviews, type QueryPreview, type QueryRecipe, type QuerySuggestion } from "./curiosity-data";
import { availableChartKinds, cardDatasets, cardTemplates, coverChoices, decodeCover, saveCardTemplate } from "./card-library-model";
import { cardKind, defaultCardCover } from "./card-cover";
import { cardLoadingPresentation } from "./card-loading-model";
import { queryCategories } from "./query-categories";
import type { QueryDataset } from "./query-links";
import { QueryCard } from "./query-card";
import { Icon } from "./icons";
import "./card-library.css";

const chartNames: Partial<Record<MetricDisplay["kind"], string>> = { auto: "Automatic", list: "Ranked bars", table: "Table", "bar-horizontal": "Horizontal bars", "bar-vertical": "Columns", line: "Line", pie: "Pie", donut: "Donut", value: "Single value", histogram: "Histogram", box: "Box plot", scatter: "Scatter plot" };
export function CardLibrary({ schemas, onOpen, onKeep, collected, message }: { schemas: Record<QueryDataset, FieldSchema>; onOpen: (url: string) => void; onKeep: (item: QuerySuggestion) => void; collected: Set<string | undefined>; message: string }) {
  const initial = new URLSearchParams(location.search);
  const [search, setSearch] = useState(initial.get("card_search") ?? ""), [dataset, setDataset] = useState(initial.get("card_data") ?? ""), [topic, setTopic] = useState(initial.get("card_topic") ?? ""), [kind, setKind] = useState(initial.get("card_kind") ?? ""), [sort, setSort] = useState(initial.get("card_sort") ?? "topic");
  const [recipes, setRecipes] = useState(cardTemplates), [days, setDays] = useState(7);
  const [previews, setPreviews] = useState<Record<string, QueryPreview>>({}), [errors, setErrors] = useState<Record<string, string>>({}), [loading, setLoading] = useState(true);
  const [editing, setEditing] = useState<QueryRecipe | null>(null), [title, setTitle] = useState(""), [description, setDescription] = useState(""), [editDays, setEditDays] = useState(7), [metric, setMetric] = useState(""), [chart, setChart] = useState<MetricDisplay["kind"]>("auto"), [error, setError] = useState("");
  const [maxGroups, setMaxGroups] = useState(""), [showLegend, setShowLegend] = useState("auto"), [showOther, setShowOther] = useState("auto");
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const refresh = () => setRecipes(cardTemplates());
    window.addEventListener("pharos:card-templates", refresh); window.addEventListener("pharos:data-refresh", refresh);
    return () => { window.removeEventListener("pharos:card-templates", refresh); window.removeEventListener("pharos:data-refresh", refresh); };
  }, []);
  const visible = useMemo(() => recipes.filter(recipe => (!dataset || recipe.dataset === dataset) && (!topic || recipe.category === topic) && (!kind || cardKind(recipe) === kind) && `${recipe.id} ${recipe.idea} ${recipe.title} ${recipe.description} ${cardDatasets[recipe.dataset]} ${queryCategories[recipe.category].label}`.toLowerCase().includes(search.toLowerCase())).sort((a, b) => {
    const key = (recipe: QueryRecipe) => sort === "data" ? cardDatasets[recipe.dataset] : sort === "kind" ? cardKind(recipe) : sort === "title" ? "" : queryCategories[recipe.category].label;
    return key(a).localeCompare(key(b)) || a.title.localeCompare(b.title);
  }), [recipes, search, dataset, topic, kind, sort]);
  const cards = useMemo(() => visible.map(recipe => instantiateRecipe(recipe, days)), [visible, days]);
  useEffect(() => {
    const url = new URL(location.href);
    for (const [key, value] of Object.entries({ card_search: search, card_data: dataset, card_topic: topic, card_kind: kind, card_sort: sort })) { if (value) url.searchParams.set(key, value); else url.searchParams.delete(key); }
    history.replaceState(null, "", `${url.pathname}${url.search}`);
  }, [search, dataset, topic, kind, sort]);
  useEffect(() => {
    const controller = new AbortController(); setLoading(true); setPreviews({}); setErrors({});
    void loadQueryPreviews(cards.map(item => ({ id: item.id, dataset: item.dataset, query: item.query, schema: schemas[item.dataset], options: { recipe: item, cover: item.cover, window: item.window, url: item.url } })), (id, preview, error) => {
      if (controller.signal.aborted) return;
      if (preview) setPreviews(current => ({ ...current, [id]: preview }));
      if (error) setErrors(current => ({ ...current, [id]: String(error) }));
    }, controller.signal).then(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [cards, schemas]);
  useEffect(() => {
    if (editing) dialog.current?.showModal();
  }, [editing]);
  function openEditor(item: QueryRecipe) {
    const recipe = cardTemplates().find(recipe => recipe.id === item.id) ?? item;
    const cover = defaultCardCover(recipe);
    // Commit the complete form before showModal exposes it. Opening the modal
    // mid-initialization can flush focus updates while other settings are stale.
    setTitle(recipe.title); setDescription(recipe.description); setEditDays(recipe.days?.length === 1 ? recipe.days[0] : 7);
    setMetric(cover ? `${cover.mode}:${cover.metricId}` : ""); setChart(cover?.kind ?? "auto"); setError("");
    setMaxGroups(cover?.maxGroups === undefined ? "" : String(cover.maxGroups));
    setShowLegend(cover?.showLegend === undefined ? "auto" : String(cover.showLegend));
    setShowOther(cover?.showOther === undefined ? "auto" : String(cover.showOther));
    setEditing(recipe);
  }
  const completed = cards.filter(item => previews[item.id] || errors[item.id]).length;
  const chosenCover = decodeCover(metric);
  const editedCover = () => { const cover = decodeCover(metric, chart); return cover && { ...cover,
    ...(maxGroups.trim() ? { maxGroups: Number(maxGroups) } : {}),
    ...(showLegend !== "auto" ? { showLegend: showLegend === "true" } : {}),
    ...(showOther !== "auto" ? { showOther: showOther === "true" } : {}) }; };
  const destinationKeys = chosenCover?.mode === "source" ? editing?.measures.find(metric => metric.id === chosenCover.metricId)?.groupBy ?? editing?.exploration?.keys : editing?.exploration?.keys;
  return <section className="card-library" aria-label="Card library">
    <div className="card-library-heading"><div><h1>Card library</h1><p>Every question, chart and comparison. Find an angle to make your own.</p></div><button type="button" onClick={() => onOpen("/")}><Icon name="arrow-left" /> Your Home</button></div>
    <p><a className="home-card-library-link" href="/home?cards=human-review" onClick={event => { if (!event.metaKey && !event.ctrlKey) { event.preventDefault(); onOpen("/home?cards=human-review"); } }}>Review the human word cards <Icon name="arrow-right" /></a></p>
    <div className="card-library-controls">
      <label className="card-library-search">Find a card<input type="search" aria-label="Find a card" placeholder="Search questions, subjects…" value={search} onChange={event => setSearch(event.target.value)} /></label>
      <label>Data source<select aria-label="Data source" value={dataset} onChange={event => setDataset(event.target.value)}><option value="">All data</option>{Object.entries(cardDatasets).filter(([key]) => recipes.some(recipe => recipe.dataset === key)).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select></label>
      <label>Topic<select aria-label="Topic" value={topic} onChange={event => setTopic(event.target.value)}><option value="">All topics</option>{Object.entries(queryCategories).map(([key, value]) => <option key={key} value={key}>{value.label}</option>)}</select></label>
      <label>Card kind<select aria-label="Card kind" value={kind} onChange={event => setKind(event.target.value)}><option value="">All kinds</option>{[...new Set(recipes.map(cardKind))].sort().map(kind => <option key={kind}>{kind}</option>)}</select></label>
      <label>Sort by<select aria-label="Sort by" value={sort} onChange={event => setSort(event.target.value)}><option value="topic">Topic</option><option value="data">Data source</option><option value="kind">Card kind</option><option value="title">Title</option></select></label>
      <label>Preview window<select aria-label="Preview window" value={days} onChange={event => setDays(Number(event.target.value))}>{[1, 7, 30, 90].map(days => <option key={days} value={days}>Past {days === 1 ? "24 hours" : `${days} days`}</option>)}</select></label>
    </div>
    <p className="card-library-count" role="status">{cards.length} of {recipes.length} cards{loading ? ` · Loading charts… ${completed}/${cards.length}` : ""}<span>Empty views stay visible here.</span></p>
    <p className="card-library-feedback" role="status">{message}</p>
    <div className="card-library-grid">{cards.map(item => <div className="card-library-entry" id={`card-${item.id}`} data-card-definition={item.id} data-card-idea={item.idea} key={item.id}>
      <QueryCard id={item.id} title={item.title} description={item.description} actionLabel={item.action} url={item.url} windowLabel={`Last ${days}d`} category={item.category} loadingSpec={cardLoadingPresentation(item.query, { recipe: item, cover: item.cover, days })} preview={previews[item.id]} error={errors[item.id]} discovery onOpen={() => onOpen(item.url)} onKeep={() => onKeep(item)} />
      <div className="card-library-meta"><code className="card-library-id" title={`Card definition: ${item.id} · Wishlist: ${item.idea}`}>ID: {item.id}</code><span>{cardDatasets[item.dataset]}</span><span>{cardKind(item)} · {item.exploration ? "Grouped table" : "Records + metrics"}</span>{collected.has(item.id) ? <span className="card-library-collected"><Icon name="check" /> Collected</span> : null}
        <button type="button" onClick={() => openEditor(item)} aria-label={`Edit card ${item.title}`}><Icon name="edit" /> Edit card</button>
      </div>
    </div>)}</div>
    {!cards.length ? <p className="card-library-empty">No cards match these filters. <button type="button" onClick={() => { setSearch(""); setDataset(""); setTopic(""); setKind(""); }}>Clear filters</button></p> : null}
    <dialog className="home-editor card-template-editor" ref={dialog} onClose={() => setEditing(null)} onCancel={() => setEditing(null)} aria-labelledby="card-template-title"><form onSubmit={event => {
      event.preventDefault(); if (!editing) return;
      try { saveCardTemplate(editing.id, { title, description, days: editDays, cover: editedCover() }); dialog.current?.close(); } catch (error) { setError(String(error)); }
    }}><h2 id="card-template-title">Edit card template</h2><p>Saved in this library. Used for new suggestions and collections.</p>
      <label>Question<input autoFocus required maxLength={200} value={title} onChange={event => setTitle(event.target.value)} /></label>
      <label>Description<textarea maxLength={500} value={description} onChange={event => setDescription(event.target.value)} /></label>
      <label>Default lookback<select aria-label="Default lookback" value={editDays} onChange={event => setEditDays(Number(event.target.value))}>{[1, 7, 30, 90].map(days => <option key={days} value={days}>Last {days}d</option>)}</select></label>
      {editing && coverChoices(editing).length ? <><label>Cover metric<select aria-label="Cover metric" value={metric} onChange={event => { const next = decodeCover(event.target.value); setMetric(event.target.value); setChart(availableChartKinds(editing, next)[0]); }}>{coverChoices(editing).map(choice => <option key={choice.value} value={choice.value}>{choice.label}</option>)}</select></label>
        <label>Chart<select aria-label="Chart" value={chart} onChange={event => setChart(event.target.value as MetricDisplay["kind"])}>{availableChartKinds(editing, decodeCover(metric)).map(kind => <option key={kind} value={kind}>{chartNames[kind] ?? kind}</option>)}</select></label>
        <label>Visible rows / series<input type="number" aria-label="Visible rows / series" min={1} max={100} step={1} placeholder="Automatic" value={maxGroups} onChange={event => setMaxGroups(event.target.value)} /></label>
        <label>Chart key<select aria-label="Chart key" value={showLegend} onChange={event => setShowLegend(event.target.value)}><option value="auto">Automatic (multiple series only)</option><option value="true">Show</option><option value="false">Hide</option></select></label>
        <label>Other remainder<select aria-label="Other remainder" value={showOther} onChange={event => setShowOther(event.target.value)}><option value="auto">Automatic</option><option value="true">Show when exact and additive</option><option value="false">Hide</option></select></label></> : null}
      {editing ? <div className="card-template-destination"><strong>Opens {editing.exploration ? "a grouped table" : "source records"}</strong><p>{editing.exploration ? destinationKeys?.length ? `Group by ${destinationKeys.map(key => schemas[editing.dataset].fields.find(field => field.name === key)?.label ?? key).join(", ")}.` : "One summary row for the whole window." : "Matched observations and their metrics."} Window: Last {editDays}d.</p><small>{editing.note}</small><button type="button" className="card-template-query" onClick={() => { dialog.current?.close(); onOpen(instantiateRecipe({ ...editing, cover: editedCover() }, editDays).url); }}>Open query to edit grouping and filters <Icon name="arrow-right" /></button></div> : null}
      {error ? <p role="alert">{error}</p> : null}<div className="home-editor-actions"><button type="button" onClick={() => { if (editing) saveCardTemplate(editing.id); dialog.current?.close(); }}>Reset template</button><button type="button" onClick={() => dialog.current?.close()}>Cancel</button><button type="submit">Save template</button></div>
    </form></dialog>
  </section>;
}
