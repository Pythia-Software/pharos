import type { QueryPreview } from "./query-previews";

// Completed covers are disposable, local to this browser and library. Persist
// only presentation data; query execution and canonical revisions stay on the
// server. A new page always revalidates its saved covers.
export type CardPreviewSnapshot = { key: string; at: number; preview: QueryPreview };
export type CardPreviewStorage = { read: () => Promise<CardPreviewSnapshot[]>; write: (entries: CardPreviewSnapshot[]) => Promise<void> };
const lifetime = 7 * 86400000;
const defaultLimits = { entries: 400, bytes: 16 * 1024 * 1024 };

export function createCardPreviewCache(storage?: CardPreviewStorage, limits = defaultLimits) {
  const entries = new Map<string, CardPreviewSnapshot & { generation: number }>();
  let generation = 1, writes = Promise.resolve();
  // A busy or blocked browser store must not hold up live queries.
  let readTimer: ReturnType<typeof setTimeout> | undefined;
  const loaded = storage ? Promise.race([
    storage.read(),
    new Promise<CardPreviewSnapshot[]>(resolve => { readTimer = setTimeout(() => resolve([]), 200); }),
  ]) : Promise.resolve([]);
  const ready = loaded.then(saved => {
    for (const entry of saved) {
      if (!entry || typeof entry.key !== "string" || !Number.isFinite(entry.at) || Date.now() - entry.at > lifetime || entry.at > Date.now()
        || !entry.preview || !["ready", "empty"].includes(entry.preview.state)) continue;
      if (!entries.has(entry.key)) entries.set(entry.key, { ...entry, generation: 0 });
    }
  }).catch(() => {}).finally(() => clearTimeout(readTimer));
  return {
    ready,
    get(key: string) {
      const entry = entries.get(key);
      if (!entry || Date.now() - entry.at > lifetime) return;
      return { ...entry, fresh: entry.generation === generation && Date.now() - entry.at < 60_000 };
    },
    put(key: string, preview: QueryPreview, expectedGeneration = generation) {
      // A refresh that started before invalidation cannot make an old result fresh.
      if ((entries.get(key)?.generation ?? -1) > expectedGeneration) return;
      const { refreshing: _refreshing, refreshError: _error, updatedAt: _at, ...cover } = preview;
      entries.delete(key);
      entries.set(key, { key, at: Date.now(), preview: cover, generation: expectedGeneration });
    },
    invalidate() { generation++; },
    generation: () => generation,
    async save() {
      await ready;
      let bytes = 0;
      const kept = [...entries.values()].reverse().sort((a, b) => b.at - a.at).filter(entry => {
        if (Date.now() - entry.at > lifetime) return false;
        const size = new TextEncoder().encode(JSON.stringify(entry)).length;
        if (bytes + size > limits.bytes) return false;
        bytes += size; return true;
      }).slice(0, limits.entries).map(({ key, at, preview }) => ({ key, at, preview }));
      const keys = new Set(kept.map(entry => entry.key));
      for (const key of entries.keys()) if (!keys.has(key)) entries.delete(key);
      // Serialize writes so an older batch cannot overwrite a newer snapshot.
      if (storage) writes = writes.then(() => storage.write(kept)).catch(() => {});
      await writes;
    },
  };
}

export function browserCardPreviewStorage(scope: string): CardPreviewStorage | undefined {
  if (!scope || typeof indexedDB === "undefined") return;
  const database = new Promise<IDBDatabase>((resolve, reject) => {
    const request = indexedDB.open("pharos-card-previews-v1", 1);
    request.onupgradeneeded = () => request.result.createObjectStore("libraries");
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
    request.onblocked = () => reject(new Error("Preview cache unavailable"));
  });
  // Storage may be unavailable in a private web view. Live previews still work.
  database.catch(() => {});
  return {
    async read() {
      const db = await database;
      return new Promise((resolve, reject) => {
        const request = db.transaction("libraries").objectStore("libraries").get(scope);
        request.onsuccess = () => resolve(Array.isArray(request.result) ? request.result : []);
        request.onerror = () => reject(request.error);
      });
    },
    async write(entries) {
      const db = await database;
      await new Promise<void>((resolve, reject) => {
        const tx = db.transaction("libraries", "readwrite");
        tx.objectStore("libraries").put(entries, scope);
        tx.oncomplete = () => resolve();
        tx.onerror = tx.onabort = () => reject(tx.error);
      });
    },
  };
}
