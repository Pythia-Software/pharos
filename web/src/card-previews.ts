import { todayBounds } from "./day-window";
import { browserCardPreviewStorage, createCardPreviewCache } from "./card-preview-cache";
import { clearPreviewCache, loadQueryPreviews, type PreviewJob, type QueryPreview } from "./query-previews";

declare global { interface Window { pharosPreviewScope?: string } }
let scope: string | undefined, cache: ReturnType<typeof createCardPreviewCache> | undefined;
function currentCache() {
  const next = typeof window === "undefined" ? "" : window.pharosPreviewScope ?? "";
  if (!cache || scope !== next) {
    if (cache) clearPreviewCache();
    scope = next; cache = createCardPreviewCache(browserCardPreviewStorage(next));
  }
  return cache;
}
export function invalidateCardPreviews() { cache?.invalidate(); }

// IDs belong to the caller (saved cards, suggestions and catalog use different
// IDs). Everything affecting the cover belongs to its key, including schema,
// time zone, search and template edits. Hashing keeps persisted keys small.
export async function cardPreviewKey(job: PreviewJob): Promise<string> {
  const { id: _id, priority: _priority, ...definition } = job;
  const serialized = JSON.stringify([definition, job.options?.window?.mode === "today" ? todayBounds()[0] : null, Intl.DateTimeFormat().resolvedOptions().timeZone, Intl.NumberFormat().resolvedOptions().locale], (_key, value) =>
    value && typeof value === "object" && !Array.isArray(value) ? Object.fromEntries(Object.entries(value).sort(([a], [b]) => a.localeCompare(b))) : value);
  if (!globalThis.crypto?.subtle) return serialized;
  const encoded = new TextEncoder().encode(serialized);
  const digest = await crypto.subtle.digest("SHA-256", encoded);
  return Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, "0")).join("");
}

export async function loadCardPreviews(jobs: PreviewJob[], publish: (id: string, preview?: QueryPreview, error?: unknown) => void, signal?: AbortSignal) {
  if (signal?.aborted) return;
  const store = currentCache();
  const keys = new Map(await Promise.all(jobs.map(async job => [job.id, await cardPreviewKey(job)] as const)));
  await store.ready;
  if (signal?.aborted) return;
  const pending: PreviewJob[] = [], previous = new Map<string, QueryPreview>();
  for (const job of jobs) {
    if (signal?.aborted) return;
    const entry = store.get(keys.get(job.id)!);
    if (entry) {
      const preview = { ...entry.preview, updatedAt: entry.at, refreshing: !entry.fresh };
      previous.set(job.id, preview); publish(job.id, preview);
    }
    if (!entry?.fresh) pending.push(job);
  }
  const generation = store.generation();
  let lastSave = Date.now();
  await loadQueryPreviews(pending, (id, preview, error) => {
    if (signal?.aborted) return;
    if (preview) {
      store.put(keys.get(id)!, preview, generation);
      publish(id, { ...preview, updatedAt: Date.now() });
      // Keep completed work even if a large catalog load is interrupted.
      if (Date.now() - lastSave > 1000) { lastSave = Date.now(); void store.save(); }
    } else if (previous.has(id)) {
      publish(id, { ...previous.get(id)!, refreshing: false, refreshError: String(error) });
    } else publish(id, undefined, error);
  }, signal);
  if (pending.length) void store.save();
}
