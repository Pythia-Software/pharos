import { localStorageAdapter, normalizeQueryState, type QueryState, type SavedQuery, type StorageAdapter, httpQuerySnapshotStore, createQueryLinkService } from "@pythia-software/query-table-core";
import { preferences } from "./preferences";
import { collectDurableQuery, replaceSavedQuery, migrateSavedQueries, readBookmarks, removeBookmark, savedQueries } from "./bookmarks";
import { queryDatasets, readRollingWindow, rollingQueryMatches, queryURL, type QueryDataset } from "./query-links";
import { queryFetch } from "./query-request";

// Saved queries and each table's default view are choices the user made, so
// they live in the library and follow it between Macs. The last-used query
// restores where this Mac left off, so it stays in this web view's storage.
// Keys match the package's own localStorage adapter, whose saved queries this
// replaces: ui.py moves an existing copy into the library once.
const DEFAULT_PREFIX = "query-table:default:";
const MAX_SAVED_QUERIES = 100;
const MAX_SAVED_NAME_LENGTH = 200;
function datasetOf(key: string): QueryDataset | undefined {
  return queryDatasets.find(dataset => key === `pharos_${dataset}`);
}
function readSaved(key: string): SavedQuery[] {
  migrateSavedQueries();
  const dataset = datasetOf(key);
  return dataset ? savedQueries(dataset) : [];
}

function readDefaultId(key: string): string | null {
  const id = preferences().get<unknown>(DEFAULT_PREFIX + key, null);
  return typeof id === "string" && id ? id : null;
}

function writeDefaultId(key: string, id: string | null) {
  if (id) preferences().set(DEFAULT_PREFIX + key, id);
  else preferences().remove(DEFAULT_PREFIX + key);
}

function savedQueryName(name: string, items: SavedQuery[]): string {
  const normalized = name.trim();
  if (!normalized || normalized.length > MAX_SAVED_NAME_LENGTH) throw new Error(`Saved query name must be between 1 and ${MAX_SAVED_NAME_LENGTH} characters.`);
  if (items.length >= MAX_SAVED_QUERIES) throw new Error(`At most ${MAX_SAVED_QUERIES} saved queries are allowed.`);
  if (items.some(saved => saved.name === normalized)) throw new Error(`Saved query "${normalized}" already exists.`);
  return normalized;
}

// Client tables have no server query dataset or bookmark destination. Their
// named views and defaults still belong to the library, keyed by schema name;
// only the last-used view stays in this Mac's local storage.
export function clientTableStorageAdapter(): StorageAdapter {
  const local = localStorageAdapter();
  const read = (key: string): SavedQuery[] => preferences().get<SavedQuery[]>(`query-table:saved:${key}`, [])
    .map(item => ({ ...item, query: normalizeQueryState(item.query) }));
  const write = (key: string, items: SavedQuery[]) => preferences().set(`query-table:saved:${key}`, items);
  return {
    storesHostState: true,
    loadLast: local.loadLast,
    saveLast: local.saveLast,
    async listSaved(key) { return read(key).sort((a, b) => b.savedAt - a.savedAt); },
    async loadDefaultSaved(key) {
      const id = readDefaultId(key);
      const found = id && read(key).find(item => item.id === id);
      if (id && !found) writeDefaultId(key, null);
      return found || null;
    },
    async setDefaultSaved(key, id) {
      if (id && !read(key).some(item => item.id === id)) throw new Error(`Saved query "${id}" does not exist.`);
      writeDefaultId(key, id);
    },
    async saveNamed(key, name, query, savedAt, snapshotCode) {
      const items = read(key), normalizedName = savedQueryName(name, items);
      const item: SavedQuery = { id: crypto.randomUUID?.() ?? `${savedAt}-${Math.random().toString(36).slice(2)}`, name: normalizedName, savedAt, query: normalizeQueryState(query), ...(snapshotCode ? { snapshotCode } : {}) };
      write(key, [...items, item]);
      return item;
    },
    async updateNamed(key, id, query, savedAt, snapshotCode) {
      const items = read(key), original = items.find(item => item.id === id);
      if (!original) throw new Error(`Saved query "${id}" does not exist.`);
      const item: SavedQuery = { id, name: original.name, savedAt, query: normalizeQueryState(query), ...(snapshotCode ? { snapshotCode } : {}) };
      write(key, items.map(entry => entry.id === id ? item : entry));
      return item;
    },
    async deleteSaved(key, id) {
      write(key, read(key).filter(item => item.id !== id));
      if (readDefaultId(key) === id) writeDefaultId(key, null);
    },
  };
}

// Saved queries, defaults and the last-used query keep the query's host state
// (QueryState.hostState): the local adapter and normalizeQueryState carry it,
// bookmarks store the whole query, and durable links snapshot it. Query Table
// writes the last-used query only once it has loaded (persistLast:
// "succeeded"), so a failing query is never restored on a bare reload. fresh
// (Reset page) bypasses the last-used and default queries.
export function libraryStorageAdapter(options: { fresh?: boolean } = {}): StorageAdapter {
  const local = localStorageAdapter();
  return {
    storesHostState: true,
    snapshots: httpQuerySnapshotStore("/api/query-snapshots", queryFetch),
    async loadLast(key) {
      return options.fresh ? null : local.loadLast(key);
    },
    async saveLast(key, query) {
      await local.saveLast(key, query);
    },
    async listSaved(key) {
      return readSaved(key).sort((a, b) => b.savedAt - a.savedAt);
    },
    async loadDefaultSaved(key) {
      if (options.fresh) return null;
      const id = readDefaultId(key);
      if (!id) return null;
      const found = readSaved(key).find(query => query.id === id);
      if (!found) writeDefaultId(key, null);
      return found ?? null;
    },
    async setDefaultSaved(key, id) {
      if (id && !readSaved(key).some(query => query.id === id)) throw new Error(`Saved query "${id}" does not exist.`);
      writeDefaultId(key, id);
    },
    async saveNamed(key, name, query, savedAt) {
      const items = readSaved(key), normalizedName = savedQueryName(name, items);
      const dataset = datasetOf(key);
      if (!dataset) throw new Error("Unknown saved-query dataset.");
      const candidate = readRollingWindow(new URLSearchParams(location.search), dataset);
      const window = candidate && rollingQueryMatches(query, candidate) ? candidate : undefined;
      const bookmark = await collectDurableQuery(dataset, normalizeQueryState(query), normalizedName, { savedAt, window, dedupe: false });
      return { id: bookmark.savedQueryId ?? bookmark.id, name: bookmark.title, savedAt, query: normalizeQueryState(query) };
    },
    async updateNamed(key, id, query, savedAt) {
      const dataset = datasetOf(key);
      if (!dataset) throw new Error("Unknown saved-query dataset.");
      const candidate = readRollingWindow(new URLSearchParams(location.search), dataset);
      const window = candidate && rollingQueryMatches(query, candidate) ? candidate : undefined;
      const service = createQueryLinkService(key, httpQuerySnapshotStore("/api/query-snapshots", queryFetch));
      const link = await service.durable(query, new URL(queryURL(dataset, query, window), location.origin).href, `q_${dataset}`);
      const url = new URL(link.url);
      return replaceSavedQuery(dataset, id, query, savedAt, `${url.pathname}${url.search}${url.hash}`);
    },
    async deleteSaved(key, id) {
      const dataset = datasetOf(key);
      const item = readBookmarks().find(item => item.dataset === dataset && (item.savedQueryId ?? item.id) === id || item.id === id);
      if (item) removeBookmark(item.id);
      if (readDefaultId(key) === id) writeDefaultId(key, null);
    },
  };
}
