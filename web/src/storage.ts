import { localStorageAdapter, normalizeQueryState, type SavedQuery, type StorageAdapter } from "@pythia-software/query-table-core";
import { preferences } from "./preferences";

// Saved queries and each table's default view are choices the user made, so
// they live in the library and follow it between Macs. The last-used query
// restores where this Mac left off, so it stays in this web view's storage.
// Keys match the package's own localStorage adapter, whose saved queries this
// replaces: ui.py moves an existing copy into the library once.
const SAVED_PREFIX = "query-table:saved:";
const DEFAULT_PREFIX = "query-table:default:";
const MAX_SAVED_QUERIES = 100;
const MAX_SAVED_NAME_LENGTH = 200;

function readSaved(key: string): SavedQuery[] {
  const stored = preferences().get<unknown>(SAVED_PREFIX + key, []);
  if (!Array.isArray(stored)) return [];
  const saved: SavedQuery[] = [];
  for (const item of stored.slice(0, MAX_SAVED_QUERIES)) {
    if (!item || typeof item !== "object" || typeof item.id !== "string" || !item.id || typeof item.name !== "string" || !item.name
      || typeof item.savedAt !== "number" || !Number.isFinite(item.savedAt) || !item.query || typeof item.query !== "object") continue;
    saved.push({ id: item.id, name: item.name, savedAt: item.savedAt, query: normalizeQueryState(item.query) });
  }
  return saved;
}

function readDefaultId(key: string): string | null {
  const id = preferences().get<unknown>(DEFAULT_PREFIX + key, null);
  return typeof id === "string" && id ? id : null;
}

function writeDefaultId(key: string, id: string | null) {
  if (id) preferences().set(DEFAULT_PREFIX + key, id);
  else preferences().remove(DEFAULT_PREFIX + key);
}

export function libraryStorageAdapter(): StorageAdapter {
  const local = localStorageAdapter();
  return {
    loadLast: key => local.loadLast(key),
    saveLast: (key, query) => local.saveLast(key, query),
    async listSaved(key) {
      return readSaved(key).sort((a, b) => b.savedAt - a.savedAt);
    },
    async loadDefaultSaved(key) {
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
      const normalizedName = name.trim();
      if (!normalizedName || normalizedName.length > MAX_SAVED_NAME_LENGTH) {
        throw new Error(`Saved query name must be between 1 and ${MAX_SAVED_NAME_LENGTH} characters.`);
      }
      const items = readSaved(key);
      if (items.length >= MAX_SAVED_QUERIES) throw new Error(`At most ${MAX_SAVED_QUERIES} saved queries are allowed.`);
      if (items.some(saved => saved.name === normalizedName)) throw new Error(`Saved query "${normalizedName}" already exists.`);
      const item: SavedQuery = {
        id: `${savedAt}-${Math.round((savedAt * 9301 + 49297) % 233280)}`,
        name: normalizedName,
        savedAt,
        query: normalizeQueryState(query),
      };
      preferences().set(SAVED_PREFIX + key, [...items, item]);
      return item;
    },
    async deleteSaved(key, id) {
      preferences().set(SAVED_PREFIX + key, readSaved(key).filter(query => query.id !== id));
      if (readDefaultId(key) === id) writeDefaultId(key, null);
    },
  };
}
