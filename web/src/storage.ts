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
const REJECTED_PREFIX = "pharos-query-rejected:";
const queryFingerprint = (query: QueryState) => JSON.stringify(normalizeQueryState(query));

// Query Table saves the last-used query only once it loads (persistLast:
// "succeeded"), but restores a table's default saved query on every bare
// visit. A default that the service rejected or that ran out of time would
// fail again each time, so it is skipped until Retry, or until the same
// query succeeds (for example when loaded from Saved).
type Restoration = { defaults: QueryState; canonicalize: (query: QueryState) => QueryState };
// The table's default saved query, when query is its effective form: a
// default acquires schema details, host scope and host defaults during
// restoration.
function defaultFor(key: string, query: QueryState, restoration: Restoration): SavedQuery | undefined {
  const id = readDefaultId(key), saved = id ? readSaved(key).find(saved => saved.id === id) : undefined;
  if (!saved) return;
  const effective = restoration.canonicalize(normalizeQueryState(saved.query, restoration.defaults));
  if (!effective.hostState && restoration.defaults.hostState) effective.hostState = restoration.defaults.hostState;
  return queryFingerprint(effective) === queryFingerprint(query) ? saved : undefined;
}

export function rejectDefaultQuery(key: string, query: QueryState, restoration: Restoration) {
  const saved = defaultFor(key, query, restoration);
  if (!saved) return;
  notified.delete(key);
  try { localStorage.setItem(REJECTED_PREFIX + key, JSON.stringify([queryFingerprint(saved.query)])); } catch { /* Storage can be unavailable. */ }
}

/** A successful run of the default (from Saved, say) restores it on bare visits again. */
export function acceptDefaultQuery(key: string, query: QueryState, restoration: Restoration) {
  const saved = defaultFor(key, query, restoration);
  if (saved && rejected(key, saved.query)) retryDefaultQuery(key);
}

/** Stops skipping the default; returns its id, so Try again can load it. */
export function retryDefaultQuery(key: string): string | null {
  notified.delete(key);
  try { localStorage.removeItem(REJECTED_PREFIX + key); } catch { /* Storage can be unavailable. */ }
  const id = readDefaultId(key);
  return id && readSaved(key).some(saved => saved.id === id) ? id : null;
}

// The skip is reported once per page: the Saved menu's refreshes, after a
// save or deletion, ask for the default again and must not bring back a
// notice the person has dismissed with Reset table.
const notified = new Set<string>();

function rejected(key: string, query: QueryState) {
  try {
    const fingerprints: unknown = JSON.parse(localStorage.getItem(REJECTED_PREFIX + key) ?? "null");
    return Array.isArray(fingerprints) && fingerprints.includes(queryFingerprint(query));
  } catch { return false; }
}
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
// "succeeded"), so a failing query is never restored on a bare reload; a
// failing default is skipped (rejectDefaultQuery). fresh (Reset page)
// bypasses the last-used and default queries.
export function libraryStorageAdapter(options: { fresh?: boolean; onRecovery?: () => void } = {}): StorageAdapter {
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
      if (found && rejected(key, found.query)) {
        if (!notified.has(key)) { notified.add(key); options.onRecovery?.(); }
        return null;
      }
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
