import { todayBounds } from "./day-window";
import { browserCardPreviewStorage, createCardPreviewCache } from "./card-preview-cache";
import { clearPreviewCache, loadQueryPreviews, type PreviewJob, type QueryPreview } from "./query-previews";
import { followStale, staleRetryDelays, type ResultPolicy } from "./result-cache";

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

// Each save writes every cached cover to the browser's store. A catalog
// loading in batches asks for one per batch, so they are combined: at most
// one write every few seconds, and one when the page is left.
let saveTimer: ReturnType<typeof setTimeout> | undefined, saving: ReturnType<typeof createCardPreviewCache> | undefined;
function saveNow() {
  clearTimeout(saveTimer); saveTimer = undefined;
  const store = saving; saving = undefined;
  if (store) void store.save();
}
function saveSoon(store: ReturnType<typeof createCardPreviewCache>) {
  if (saving && saving !== store) saveNow();
  saving = store;
  if (saveTimer !== undefined) return;
  saveTimer = setTimeout(saveNow, 4000);
  // Node's test runner must not wait on it.
  (saveTimer as { unref?: () => void }).unref?.();
}
if (typeof window !== "undefined") window.addEventListener("pagehide", saveNow);

// IDs belong to the caller (saved cards, suggestions and catalog use different
// IDs). Everything affecting the cover belongs to its key, including schema,
// time zone, search and template edits. Hashing keeps persisted keys small.
export async function cardPreviewKey(job: PreviewJob): Promise<string> {
  const { id: _id, priority: _priority, ...definition } = job;
  const serialized = JSON.stringify([definition, job.options?.window?.mode === "today" || job.options?.dashboard && new URL(job.options.dashboard, location.origin).searchParams.get("analyze_window") === "today" ? todayBounds()[0] : null, Intl.DateTimeFormat().resolvedOptions().timeZone, Intl.NumberFormat().resolvedOptions().locale], (_key, value) =>
    value && typeof value === "object" && !Array.isArray(value) ? Object.fromEntries(Object.entries(value).sort(([a], [b]) => a.localeCompare(b))) : value);
  if (!globalThis.crypto?.subtle) return serialized;
  const encoded = new TextEncoder().encode(serialized);
  const digest = await crypto.subtle.digest("SHA-256", encoded);
  return Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, "0")).join("");
}

// policy is how the server's result cache answers (result-cache.ts): a page
// showing its cards asks "cached", a person refreshing asks "fresh" or
// "live". A card answered with a stale result is published with its real
// age and marked refreshing, then asked for again in the background (unless
// follow is false: work that only prepares covers nobody is looking at).
export async function loadCardPreviews(jobs: PreviewJob[], publish: (id: string, preview?: QueryPreview, error?: unknown) => void, signal?: AbortSignal,
  options: { policy?: ResultPolicy; follow?: boolean } = {}) {
  if (signal?.aborted) return;
  const policy = options.policy ?? "cached";
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
    if (!entry?.fresh || policy === "live") pending.push(job);
  }
  const generation = store.generation();
  const stale = new Map<string, PreviewJob>(), byID = new Map(jobs.map(job => [job.id, job]));
  const receive = (final: boolean) => (id: string, preview?: QueryPreview, error?: unknown) => {
    if (signal?.aborted) return;
    if (preview) {
      const waiting = preview.served?.state === "stale", at = preview.served?.built ?? Date.now();
      if (waiting) stale.set(id, byID.get(id)!); else stale.delete(id);
      store.put(keys.get(id)!, preview, generation, at);
      const shown = { ...preview, updatedAt: at, refreshing: waiting && !final };
      previous.set(id, shown); publish(id, shown);
      // Keep completed work even if a large catalog load is interrupted.
      saveSoon(store);
    } else if (previous.has(id)) {
      publish(id, { ...previous.get(id)!, refreshing: false, refreshError: String(error) });
    } else publish(id, undefined, error);
  };
  await loadQueryPreviews(pending, receive(options.follow === false), signal, policy);
  // A finished load is written at once, so a reload straight after finds it.
  if (pending.length) { saveSoon(store); saveNow(); }
  if (!stale.size || options.follow === false) return;
  // Not awaited: the page's cards are all drawn; this only freshens them.
  void followStale(async attempt => {
    const last = attempt === staleRetryDelays.length - 1;
    await loadQueryPreviews([...stale.values()], receive(last), signal, "cached");
    saveSoon(store); saveNow();
    return stale.size > 0;
  }, signal);
}

// Covers arrive one at a time, each in its own task. A page holding many
// cards re-renders all of them per state update, so it applies the arrivals
// of a short interval together.
export type PreviewArrival = { id: string; preview?: QueryPreview; error?: unknown };
export function batchPreviewArrivals(apply: (arrivals: PreviewArrival[]) => void, delay = 80) {
  let waiting: PreviewArrival[] = [], timer: ReturnType<typeof setTimeout> | undefined;
  const flush = () => {
    clearTimeout(timer); timer = undefined;
    if (!waiting.length) return;
    const arrivals = waiting; waiting = [];
    apply(arrivals);
  };
  return {
    publish(id: string, preview?: QueryPreview, error?: unknown) { waiting.push({ id, preview, error }); timer ??= setTimeout(flush, delay); },
    flush,
    cancel() { clearTimeout(timer); timer = undefined; waiting = []; },
  };
}
