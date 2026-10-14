import { encodeQuery, normalizeQueryState, type QueryState, type SavedQuery, createQueryLinkService, httpQuerySnapshotStore } from "@pythia-software/query-table-core";
import type { CardCover } from "./card-cover";
import { preferences } from "./preferences";
import { queryDatasets, queryFromURL, queryURL, resolveRollingQuery, rollingQueryMatches, updateQueryWindowURL, type QueryDataset, type RollingWindow } from "./query-links";

export const BOOKMARKS_KEY = "pharos-bookmarks-v1";
export type Bookmark = {
  id: string; title: string; url: string; description?: string; savedAt?: number;
  query?: QueryState; dataset?: QueryDataset; savedQueryId?: string; templateId?: string; coverMetricId?: string; cover?: CardCover;
};

export function readBookmarks(): Bookmark[] {
  const value = preferences().get<unknown>(BOOKMARKS_KEY, []);
  return Array.isArray(value) ? value.filter(item => item && typeof item.id === "string" && typeof item.title === "string" && typeof item.url === "string") : [];
}

export function writeBookmarks(items: Bookmark[]) {
  preferences().set(BOOKMARKS_KEY, items);
  window.dispatchEvent(new Event("pharos:bookmarks"));
}

// Upgrade existing QT saved searches into the existing URL bookmark list.
// Keep the old query IDs so default-view preferences keep working. The old
// arrays remain a migration backup; a marker prevents resurrecting deletions.
export function migrateSavedQueries() {
  let items = readBookmarks(), changed = false;
  items = items.map(item => {
    const spec = queryFromURL(item.url, item.query);
    if (spec?.window && spec.window.mode !== "relative" && rollingQueryMatches(spec.query, spec.window)) {
      const url = updateQueryWindowURL(item.url, spec.window.days, item.query);
      changed = true; return { ...item, url, query: queryFromURL(url)?.query };
    }
    try {
      const url = new URL(item.url, location.origin);
      if (url.origin === location.origin && url.pathname === "/" && (url.searchParams.has("q_library") || url.searchParams.has("search"))) {
        changed = true; return { ...item, url: `/library${url.search}` };
      }
    } catch { /* Preserve unrelated bookmarks. */ }
    return item;
  });
  for (const dataset of queryDatasets) {
    const key = `pharos_${dataset}`, marker = `query-table:bookmarks-migrated:${key}`;
    if (preferences().get(marker, false)) continue;
    const old = preferences().get<unknown>(`query-table:saved:${key}`, []);
    if (Array.isArray(old)) for (const saved of old.slice(0, 100)) {
      if (!saved || typeof saved.id !== "string" || typeof saved.name !== "string" || !saved.query || typeof saved.savedAt !== "number") continue;
      const url = queryURL(dataset, normalizeQueryState(saved.query));
      const found = items.find(item => item.url === url && (!item.dataset || item.dataset === dataset) && (!item.savedQueryId || item.savedQueryId === saved.id));
      if (found) { found.dataset = dataset; found.savedQueryId = saved.id; found.savedAt ??= saved.savedAt; }
      else items.push({ id: `query:${dataset}:${saved.id}`, title: saved.name, url, dataset, savedQueryId: saved.id, savedAt: saved.savedAt });
      changed = true;
    }
    preferences().set(marker, true);
  }
  if (changed) writeBookmarks(items);
}

export function savedQueries(dataset: QueryDataset): SavedQuery[] {
  return readBookmarks().flatMap(item => {
    const spec = queryFromURL(item.url, item.query);
    if (spec?.dataset !== dataset) return [];
    return [{ id: item.savedQueryId ?? item.id, name: item.title, savedAt: item.savedAt ?? 0, query: resolveRollingQuery(spec.query, spec.window) }];
  });
}

export function savedRollingWindow(dataset: QueryDataset, query: QueryState): RollingWindow | undefined {
  const token = encodeQuery(query);
  for (const item of readBookmarks()) {
    const spec = queryFromURL(item.url, item.query);
    if (spec?.dataset === dataset && spec.window && encodeQuery(resolveRollingQuery(spec.query, spec.window)) === token) return spec.window;
  }
}

export function collectQuery(dataset: QueryDataset, query: QueryState, title: string, options: { description?: string; templateId?: string; window?: RollingWindow; savedAt?: number; dedupe?: boolean; url?: string; cover?: CardCover } = {}): Bookmark {
  const items = readBookmarks(), url = options.url ?? queryURL(dataset, query, options.window);
  const existing = items.find(item => {
    if (JSON.stringify(item.cover) !== JSON.stringify(options.cover)) return false;
    if (item.url === url) return true;
    const spec = options.templateId && item.templateId === options.templateId ? queryFromURL(item.url, item.query) : undefined;
    return spec?.dataset === dataset && encodeQuery(spec.query) === encodeQuery(query);
  });
  if (existing && options.dedupe !== false) return existing;
  if (savedQueries(dataset).length >= 100) throw new Error("This table already has 100 saved queries.");
  const id = crypto.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`, item: Bookmark = {
    id, savedQueryId: id, dataset, query: normalizeQueryState(query), url, title: title.trim(), description: options.description,
    cover: options.cover, templateId: options.templateId, savedAt: options.savedAt ?? Date.now(),
  };
  writeBookmarks([...items, item]);
  return item;
}

export function updateBookmark(id: string, title: string, description: string, days?: number, coverMetricId?: string): Bookmark {
  if (!title.trim() || title.trim().length > 200 || description.length > 500) throw new Error("Use a title up to 200 characters and a description up to 500.");
  const items = readBookmarks(), current = items.find(item => item.id === id), spec = current && queryFromURL(current.url, current.query);
  if (!current) throw new Error("This saved view is no longer in your collection.");
  if (spec && items.some(item => item.id !== id && item.title === title.trim() && queryFromURL(item.url, item.query)?.dataset === spec.dataset)) throw new Error("Another saved query in this table has that name.");
  const updated = { ...current, title: title.trim(), description: description.trim() };
  if (coverMetricId !== undefined) {
    if (coverMetricId && !validCoverValue(spec?.query, coverMetricId)) throw new Error("Choose a metric from this saved view.");
    if (coverMetricId) {
      updated.coverMetricId = coverMetricId;
      updated.cover = { ...(current.cover?.metricId === coverMetricId ? current.cover : {}), mode: spec?.query.grouping?.summaries.some(column => column.id === coverMetricId) ? "result" : "source", metricId: coverMetricId };
      const grouping = spec?.query.grouping;
      const metric = updated.cover.mode === "source" ? grouping?.source.aggregations?.find(metric => metric.id === coverMetricId) : undefined;
      if (spec && grouping && metric && JSON.stringify(grouping.keys) !== JSON.stringify(metric.groupBy)) {
        const fields = new Set([...metric.groupBy, ...grouping.summaries.map(column => column.id)]);
        const query = { ...spec.query, grouping: { ...grouping, keys: metric.groupBy }, offset: 0,
          select: [...metric.groupBy, ...grouping.summaries.map(column => column.id)].map(field => ({ field })),
          orderBy: spec.query.orderBy.filter(term => fields.has(term.field)), aggregations: [],
        };
        const url = new URL(updated.url, location.origin); url.searchParams.set(`q_${spec.dataset}`, encodeQuery(query));
        updated.url = `${url.pathname}${url.search}${url.hash}`; updated.query = query;
      }
    } else { delete updated.coverMetricId; delete updated.cover; }
  }
  if (days !== undefined && days !== spec?.window?.days) {
    updated.url = updateQueryWindowURL(updated.url, days, updated.query ?? current.query);
    updated.query = queryFromURL(updated.url)?.query;
    if (current.templateId) updated.templateId = `${current.templateId.split(":")[0]}:${days}`;
  }
  writeBookmarks(items.map(item => item.id === id ? updated : item));
  return updated;
}

function validCoverValue(query: QueryState | undefined, id: string): boolean {
  return Boolean(query?.aggregations?.some(metric => metric.id === id) || query?.grouping?.source.aggregations?.some(metric => metric.id === id) || query?.grouping?.summaries.some(column => column.id === id && column.op !== "row"));
}

export function removeBookmark(id: string) {
  const items = readBookmarks(), item = items.find(item => item.id === id), spec = item && queryFromURL(item.url, item.query);
  if (spec && preferences().get(`query-table:default:pharos_${spec.dataset}`, null) === (item?.savedQueryId ?? id)) preferences().remove(`query-table:default:pharos_${spec.dataset}`);
  writeBookmarks(items.filter(item => item.id !== id));
}

export function moveBookmark(id: string, before?: string) {
  const items = readBookmarks(), from = items.findIndex(item => item.id === id);
  if (from < 0 || id === before) return;
  const [item] = items.splice(from, 1), to = before ? items.findIndex(item => item.id === before) : items.length;
  items.splice(to < 0 ? items.length : to, 0, item); writeBookmarks(items);
}

// Explicit Save/Bookmark stores the immutable link before publishing it in the
// collection. Keep its query alongside the link for synchronous cover editing.
export async function collectDurableQuery(dataset: QueryDataset, query: QueryState, title: string, options: Parameters<typeof collectQuery>[3] = {}): Promise<Bookmark> {
  const service = createQueryLinkService(`pharos_${dataset}`, httpQuerySnapshotStore("/api/query-snapshots"));
  const link = await service.durable(query, new URL(queryURL(dataset, query, options.window), location.origin).href, `q_${dataset}`);
  const url = new URL(link.url);
  return collectQuery(dataset, query, title, { ...options, url: `${url.pathname}${url.search}${url.hash}` });
}

export function replaceSavedQuery(dataset: QueryDataset, id: string, query: QueryState, savedAt: number, url: string): SavedQuery {
  const items = readBookmarks(), current = items.find(item => (item.dataset ?? queryFromURL(item.url, item.query)?.dataset) === dataset && (item.savedQueryId ?? item.id) === id);
  if (!current) throw new Error("This saved query no longer exists.");
  const updated = { ...current, dataset, query: normalizeQueryState(query), url, savedAt };
  if (updated.coverMetricId && !validCoverValue(query, updated.coverMetricId)) delete updated.coverMetricId;
  if (updated.cover && !validCoverValue(query, updated.cover.metricId)) delete updated.cover;
  writeBookmarks(items.map(item => item.id === current.id ? updated : item));
  return { id, name: current.title, savedAt, query: updated.query };
}
