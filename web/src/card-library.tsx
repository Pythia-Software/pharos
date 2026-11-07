import { useViewState } from "./view-state";
import { readWindowChoice, type WindowChoice } from "./day-window";
import React, { useEffect, useMemo, useRef, useState } from "react";
import type { FieldSchema } from "@pythia-software/query-table-core";
import { clearPreviewCache, instantiateRecipe, suggestionWindowLabel, type QueryPreview, type QueryRecipe, type QuerySuggestion } from "./curiosity-data";
import { cardDatasets, cardLibraryParams, cardSorts, cardTemplates, cardTopicAll } from "./card-library-model";
export { cardLibraryParams } from "./card-library-model";
import { cardKind } from "./card-cover";
import { cardLoadingPresentation } from "./card-loading-model";
import { queryCategories, type QueryCategory } from "./query-categories";
import type { QueryDataset } from "./query-links";
import { QueryCard } from "./query-card";
import { Icon } from "./icons";
import { preferences } from "./preferences";
import { invalidateCardPreviews, loadCardPreviews } from "./card-previews";
import { usePreviewReload } from "./use-preview-reload";
import { migrateSavedQueries, readBookmarks } from "./bookmarks";
import { collectCardTemplate } from "./dashboard-card-model";
import { PageLink } from "./nested-pages";
import { focusPageContent } from "./page-focus";
import "./card-library.css";

// Card Library has its own page. Legacy links retain their filter layout;
// tabs and filters otherwise follow the library preferences. Cards are made
// and changed from dashboards and Home, not here.
export const CARD_LIBRARY_VIEW_KEY = "pharos-home-card-library-v1";
export type CardLibraryView = { topic?: string; filters?: boolean };
// A lazy page can first mount after navigation, rather than during startup.
const openedOnCardLibrary = location.pathname === "/cards";
export const isCardLibraryLink = (search = location.search) => new URLSearchParams(search).get("cards") === "library";
export const hasCardLibraryParams = (search = location.search, keys = cardLibraryParams) => { const params = new URLSearchParams(search); return keys.some(key => params.get(key)); };
// card_topic=all marks the All tab, including on legacy links.
export const isCardLibraryURL = (search = location.search) => isCardLibraryLink(search) || hasCardLibraryParams(search);
export function cardLibraryView(): CardLibraryView {
  const value = preferences().get<unknown>(CARD_LIBRARY_VIEW_KEY, {});
  return value && typeof value === "object" && !Array.isArray(value) ? value as CardLibraryView : {};
}
export function saveCardLibraryView(change: CardLibraryView) {
  preferences().set(CARD_LIBRARY_VIEW_KEY, { ...cardLibraryView(), ...change });
}
export function CardLibraryPage({ schemas }: { schemas: Record<QueryDataset, FieldSchema> }) {
  const [route, setRoute] = useState(() => location.href);
  const active = new URL(route).pathname === "/cards";
  const [collection, setCollection] = useState(readBookmarks);
  const [generation, setGeneration] = useState(0), [message, setMessage] = useState("");
  const [hidden, setHidden] = useState(document.hidden);
  const wasActive = useRef(openedOnCardLibrary && active);
  useEffect(() => {
    migrateSavedQueries(); setCollection(readBookmarks());
    const route = () => setRoute(location.href);
    const bookmarks = () => setCollection(readBookmarks());
    let stale = false;
    const refresh = () => {
      if (document.hidden) { stale = true; return; }
      stale = false; clearPreviewCache(); invalidateCardPreviews(); setGeneration(value => value + 1);
    };
    const visibility = () => { setHidden(document.hidden); if (!document.hidden && stale) refresh(); };
    window.addEventListener("pharos:route", route); window.addEventListener("pharos:view", route);
    window.addEventListener("pharos:bookmarks", bookmarks);
    window.addEventListener("pharos:usage-refresh", refresh); window.addEventListener("pharos:data-refresh", refresh); document.addEventListener("visibilitychange", visibility);
    return () => {
      window.removeEventListener("pharos:route", route); window.removeEventListener("pharos:view", route);
      window.removeEventListener("pharos:bookmarks", bookmarks);
      window.removeEventListener("pharos:usage-refresh", refresh); window.removeEventListener("pharos:data-refresh", refresh); document.removeEventListener("visibilitychange", visibility);
    };
  }, []);
  useEffect(() => {
    if (active && !wasActive.current) focusPageContent();
    wasActive.current = active;
  }, [active]);
  async function keep(item: QuerySuggestion) {
    try {
      await collectCardTemplate(item);
      setMessage(`Collected “${item.title}”.`);
    } catch (error) { setMessage(String(error)); }
  }
  if (!active) return null;
  return <div className="card-library-page">
    <PageLink className="card-library-back" href="/"><Icon name="arrow-left" />Back to Home</PageLink>
    <CardLibrary key={route} schemas={schemas} active={active} prewarm={!hidden} generation={generation}
      collected={new Set(collection.map(item => item.templateId?.split(":")[0]))} message={message}
      onOpen={url => window.pharosNavigate?.(url)} onKeep={item => void keep(item)} />
  </div>;
}

const ALL = cardTopicAll;
// "Card defaults" honour a template that pins a single lookback.
const cardDays = (recipe: QueryRecipe, days: WindowChoice | "default") => days !== "default" ? days : recipe.days?.length === 1 ? recipe.days[0] : 7;

function CardLibrary({ schemas, active, prewarm, generation, collected, message, onOpen, onKeep }: {
  schemas: Record<QueryDataset, FieldSchema>; active: boolean; prewarm: boolean; generation: number; collected: Set<string | undefined>; message: string;
  onOpen: (url: string) => void; onKeep: (item: QuerySuggestion) => void;
}) {
  // The page bumps generation for data refreshes.
  const recipes = useMemo(cardTemplates, [generation]);
  const topics = useMemo(() => (Object.keys(queryCategories) as QueryCategory[]).filter(key => recipes.some(recipe => recipe.category === key)), [recipes]);
  const [initial] = useState(() => {
    const params = new URLSearchParams(location.search), linked = isCardLibraryURL(), saved = cardLibraryView();
    // A topic link alone keeps the saved layout; filter links show their filters.
    const filtered = isCardLibraryLink() || hasCardLibraryParams(location.search, cardLibraryParams.filter(key => key !== "card_topic"));
    const valid = (topic?: string | null) => topic === ALL || recipes.some(recipe => recipe.category === topic) ? topic! : undefined;
    return { params, linked, topic: valid(params.get("card_topic")) ?? (linked ? ALL : valid(saved.topic) ?? ALL), filters: filtered || saved.filters === true };
  });
  const [search, setSearch] = useState(initial.params.get("card_search") ?? ""), [dataset, setDataset] = useState(initial.params.get("card_data") ?? ""), [kind, setKind] = useState(initial.params.get("card_kind") ?? ""), [sort, setSort] = useState(initial.params.get("card_sort") ?? "topic");
  const [chosen, setChosen] = useState(initial.topic), [days, setDaysValue] = useViewState<string>("card_window", "default", active);
  const setDays = (value: WindowChoice | "default") => setDaysValue(String(value));
  const [filtersOpen, setFiltersOpen] = useViewState("card_filters", initial.filters, active);
  const [previews, setPreviews] = useState<Record<string, QueryPreview>>({}), [errors, setErrors] = useState<Record<string, string>>({}), [loading, setLoading] = useState(true);
  const tabs = useRef<HTMLDivElement>(null);
  const topic = chosen === ALL || topics.includes(chosen as QueryCategory) ? chosen : topics[0] ?? ALL;
  const matching = useMemo(() => recipes.filter(recipe => (!dataset || recipe.dataset === dataset) && (!kind || cardKind(recipe) === kind) && `${recipe.id} ${recipe.idea} ${recipe.title} ${recipe.description} ${cardDatasets[recipe.dataset]} ${queryCategories[recipe.category].label}`.toLowerCase().includes(search.toLowerCase())), [recipes, search, dataset, kind]);
  const visible = useMemo(() => matching.filter(recipe => topic === ALL || recipe.category === topic).sort((a, b) => {
    const key = (recipe: QueryRecipe) => sort === "data" ? cardDatasets[recipe.dataset] : sort === "kind" ? cardKind(recipe) : sort === "title" ? "" : queryCategories[recipe.category].label;
    return key(a).localeCompare(key(b)) || a.title.localeCompare(b.title);
  }), [matching, topic, sort]);
  const cards = useMemo(() => visible.map(recipe => instantiateRecipe(recipe, cardDays(recipe, days === "default" ? "default" : readWindowChoice(days)))), [visible, days, generation]);
  const reloadPreview = usePreviewReload(setPreviews, setErrors, [cards, schemas, active]);
  const nearby = useRef(new Set<string>());
  const previousJobs = useRef(new Map<string, string>());
  const job = (item: QuerySuggestion) => ({ id: item.id, priority: () => nearby.current.has(item.id) ? 0 : 1, dataset: item.dataset, query: item.query, schema: schemas[item.dataset], options: { recipe: item, cover: item.cover, window: item.window, url: item.url } });
  useEffect(() => {
    if (!active || location.pathname !== "/cards") return;
    const url = new URL(location.href);
    for (const [key, value] of Object.entries({ card_search: search, card_data: dataset, card_topic: topic, card_kind: kind, card_sort: sort === "topic" ? "" : sort })) { if (value) url.searchParams.set(key, value); else url.searchParams.delete(key); }
    history.replaceState(history.state, "", `${url.pathname}${url.search}${url.hash}`);
  }, [active, search, dataset, topic, kind, sort]);
  useEffect(() => {
    nearby.current.clear();
    if (!active) return;
    const observer = new IntersectionObserver(entries => {
      for (const entry of entries) {
        const id = (entry.target as HTMLElement).dataset.cardDefinition!;
        if (entry.isIntersecting) nearby.current.add(id); else nearby.current.delete(id);
      }
    }, { root: document.querySelector("main"), rootMargin: "600px 0px" });
    document.querySelectorAll(".card-library-entry").forEach(element => observer.observe(element));
    return () => observer.disconnect();
  }, [active, cards]);
  useEffect(() => {
    if (!active) { setLoading(false); return; }
    const controller = new AbortController(); setLoading(true); setErrors({});
    const jobs = cards.map(job), definitions = new Map(jobs.map(job => [job.id, JSON.stringify(job)]));
    const retained = new Set([...definitions].filter(([id, definition]) => previousJobs.current.get(id) === definition).map(([id]) => id));
    previousJobs.current = definitions;
    setPreviews(current => Object.fromEntries(Object.entries(current).filter(([id]) => retained.has(id))));
    void loadCardPreviews(jobs, (id, preview, error) => {
      if (controller.signal.aborted) return;
      if (preview) { setPreviews(current => ({ ...current, [id]: preview })); setErrors(current => { const next = { ...current }; delete next[id]; return next; }); }
      if (error) setErrors(current => ({ ...current, [id]: String(error) }));
    }, controller.signal).then(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [active, cards, schemas, generation]);
  useEffect(() => {
    if (!prewarm || loading) return;
    const controller = new AbortController();
    // Warm the rest of the catalog after foreground cards. Small batches yield
    // between topics and are cancelled when the view or data changes.
    const items = recipes.map(recipe => instantiateRecipe(recipe, cardDays(recipe, days === "default" ? "default" : readWindowChoice(days))));
    void (async () => {
      for (let at = 0; at < items.length && !controller.signal.aborted; at += 8) {
        await loadCardPreviews(items.slice(at, at + 8).map(job), () => {}, controller.signal);
      }
    })();
    return () => controller.abort();
  }, [prewarm, loading, active, cards, schemas, generation, recipes, days]);
  function filter<T>(set: (value: T) => void) { return (value: T) => { set(value); }; }
  function choose(key: string, focus = false) {
    setChosen(key); saveCardLibraryView({ topic: key });
    if (focus) requestAnimationFrame(() => tabs.current?.querySelector<HTMLButtonElement>(`[data-topic="${key}"]`)?.focus());
  }
  function clearFilters() { setSearch(""); setDataset(""); setKind(""); }
  const completed = cards.filter(item => previews[item.id] || errors[item.id]).length;
  const tabKeys = [ALL, ...topics], index = tabKeys.indexOf(topic);
  const activeFilters = [search, dataset, kind].filter(Boolean).length;
  return <section id="card-library" className="card-library" aria-labelledby="card-library-title">
    <div className="card-library-head">
      <h1 id="card-library-title"><Icon name="read" />Card Library</h1>
      <div className="card-library-actions">
        <button data-view-field="card_filters" type="button" aria-expanded={filtersOpen} aria-controls="card-library-controls" onClick={() => { const open = !filtersOpen; setFiltersOpen(open); saveCardLibraryView({ filters: open }); }}><Icon name="search" />Filters{activeFilters ? <span className="card-library-badge">{activeFilters}</span> : null}</button>
      </div>
    </div>
    {filtersOpen ? <div id="card-library-controls" className="card-library-controls">
      <label className="card-library-search">Find a card<input type="search" aria-label="Find a card" placeholder="Search questions, subjects…" value={search} onChange={event => {
        // A new search looks across every topic; pick a tab to narrow it.
        if (!search && event.target.value && topic !== ALL) setChosen(ALL);
        filter(setSearch)(event.target.value);
      }} /></label>
      <label>Data source<select aria-label="Data source" value={dataset} onChange={event => filter(setDataset)(event.target.value)}><option value="">All data</option>{Object.entries(cardDatasets).filter(([key]) => recipes.some(recipe => recipe.dataset === key)).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select></label>
      <label>Card kind<select aria-label="Card kind" value={kind} onChange={event => filter(setKind)(event.target.value)}><option value="">All kinds</option>{[...new Set(recipes.map(cardKind))].sort().map(kind => <option key={kind}>{kind}</option>)}</select></label>
      <label>Sort by<select aria-label="Sort by" value={sort} onChange={event => filter(setSort)(event.target.value)}>{cardSorts.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
      <label>Preview window<select data-view-field="card_window" aria-label="Preview window" value={days} onChange={event => setDays(event.target.value === "default" ? "default" : readWindowChoice(event.target.value))}><option value="default">Card defaults</option><option value="today">Today (eligible cards)</option>{[1, 7, 30, 90].map(days => <option key={days} value={days}>Past {days === 1 ? "24 hours" : `${days} days`}</option>)}</select></label>
    </div> : null}
    <div ref={tabs} className="card-library-tabs" role="tablist" aria-label="Card topics" onKeyDown={event => {
      const next = { ArrowLeft: index - 1, ArrowRight: index + 1, Home: 0, End: tabKeys.length - 1 }[event.key];
      if (next === undefined) return;
      event.preventDefault(); choose(tabKeys[(next + tabKeys.length) % tabKeys.length], true);
    }}>
      {tabKeys.map(key => {
        const count = key === ALL ? matching.length : matching.filter(recipe => recipe.category === key).length;
        return <button key={key} type="button" role="tab" id={`card-library-tab-${key}`} data-topic={key} data-empty={count ? undefined : ""} aria-selected={key === topic} aria-controls="card-library-panel" tabIndex={key === topic ? 0 : -1}
          style={key === ALL ? undefined : { "--topic-tone": queryCategories[key as QueryCategory].tone } as React.CSSProperties} onClick={() => choose(key)}>
          {key === ALL ? "All" : queryCategories[key as QueryCategory].label}<span className="card-library-tab-count">{count}</span>
        </button>;
      })}
    </div>
    {filtersOpen ? <p className="card-library-count" role="status">{cards.length} of {recipes.length} cards{loading ? ` · ${completed === cards.length ? "Refreshing charts…" : `Loading charts… ${completed}/${cards.length}`}` : ""}<span>Empty views stay visible here.</span></p> : null}
    <p className="card-library-feedback" role="status">{message}</p>
    <div id="card-library-panel" className="card-library-grid" role="tabpanel" aria-labelledby={`card-library-tab-${topic}`} aria-busy={loading}>{cards.map(item => <div className="card-library-entry" id={`card-${item.id}`} data-card-definition={item.id} data-card-idea={item.idea} key={item.id}>
      <QueryCard discovery id={item.id} title={item.title} description={item.description} actionLabel={item.action} url={item.url} windowLabel={suggestionWindowLabel(item)} category={item.category} loadingSpec={cardLoadingPresentation(item.query, { recipe: item, cover: item.cover, days: item.window?.days })} preview={previews[item.id]} error={errors[item.id]}
        readinessSource={{dataset:item.dataset,query:item.query}} onOpen={onOpen} onKeep={() => onKeep(item)} onReload={() => reloadPreview(job(item))} />
      {collected.has(item.id) ? <span className="card-library-collected"><Icon name="check" /> Added to your collection</span> : null}
    </div>)}</div>
    {!cards.length ? <p className="card-library-empty">No cards match these filters. <button type="button" onClick={clearFilters}>Clear filters</button></p> : null}
  </section>;
}
