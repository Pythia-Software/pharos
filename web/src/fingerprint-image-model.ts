export type FingerprintRequest = {entity: 'conversation' | 'workspace'; id: string; profile: 'wide' | 'card'; events: boolean};
export type FingerprintImage = Omit<FingerprintRequest, 'events'> & {state: 'ready' | 'pending' | 'retrying' | 'not_found'; images: Partial<Record<'light' | 'dark', string>>; href?: string; stale?: boolean; truncated?: boolean; pending?: boolean; events_pending?: number};
export type FingerprintSnapshot = {image?: FingerprintImage; error?: string};
type Entry = {request: FingerprintRequest; snapshot: FingerprintSnapshot; listeners: Set<() => void>; due: number; touched: number};
const keyOf = (r: FingerprintRequest) => JSON.stringify([r.entity, r.id, r.profile, r.events]);

// One batch/timer for a whole column, not one request/poller per row. Entries
// stop polling when unmounted; old replies cannot overwrite a refreshed page.
export function fingerprintImageCache(load: (items: FingerprintRequest[], events: boolean) => Promise<FingerprintImage[]>, options: {now?: () => number; limit?: number} = {}) {
 const now = options.now ?? Date.now, entries = new Map<string, Entry>(), pending = new Set<string>();
 let scheduled = false, fetching = false, epoch = 0, timer: ReturnType<typeof setTimeout> | undefined;
 const entry = (request: FingerprintRequest) => {
  const key = keyOf(request); let e = entries.get(key);
  if (!e) {e = {request, snapshot: {}, listeners: new Set(), due: 0, touched: now()}; entries.set(key, e);}
  e.touched = now(); return e;
 };
 function schedule() {
  if (timer) clearTimeout(timer); timer = undefined;
  let due = Infinity;
  for (const e of entries.values()) if (e.listeners.size) due = Math.min(due, e.due);
  if (due !== Infinity) timer = setTimeout(() => {
   timer = undefined;
   for (const [key, e] of entries) if (e.listeners.size && e.due <= now()) pending.add(key);
   queue();
  }, Math.max(50, due - now()));
 }
 function queue() {if (!scheduled) {scheduled = true; queueMicrotask(() => {scheduled = false; void flush();});}}
 function notify(e: Entry, snapshot: FingerprintSnapshot) {
  if (JSON.stringify(e.snapshot) === JSON.stringify(snapshot)) return;
  e.snapshot = snapshot; e.listeners.forEach(fn => fn());
 }
 async function flush() {
  if (fetching) return;
  const keys = [...pending].filter(key => entries.get(key)?.listeners.size);
  pending.clear();
  if (!keys.length) {schedule(); return;}
  fetching = true; const generation = epoch;
  try {
   for (const events of [true, false]) {
    const group = keys.filter(key => entries.get(key)?.request.events === events);
    for (let i = 0; i < group.length; i += 250) {
     const batch = group.slice(i, i + 250), requests = batch.map(key => entries.get(key)!.request);
     try {
      const images = await load(requests, events);
      if (generation !== epoch) continue;
      const byKey = new Map(images.map(image => [keyOf({...image, events}), image]));
      for (const key of batch) {const e = entries.get(key), image = byKey.get(key); if (!e) continue;
       if (!image) throw new Error('Fingerprint unavailable');
       notify(e, {image}); e.due = now() + (image.state === 'ready' ? 30000 : image.state === 'not_found' ? 60000 : 2000);
      }
     } catch {
      if (generation !== epoch) continue;
      for (const key of batch) {const e = entries.get(key); if (e) {notify(e, {...e.snapshot, error: 'Fingerprint unavailable'}); e.due = now() + 5000;}}
     }
    }
   }
  } finally {
   fetching = false;
   // Bound inactive history without evicting a mounted cell or active request.
   const inactive = [...entries].filter(([, e]) => !e.listeners.size).sort((a, b) => a[1].touched - b[1].touched);
   while (entries.size > (options.limit ?? 600) && inactive.length) entries.delete(inactive.shift()![0]);
   if (pending.size) queue(); else schedule();
  }
 }
 return {
  snapshot(request: FingerprintRequest) {return entry(request).snapshot;},
  subscribe(request: FingerprintRequest, listener: () => void) {
   const e = entry(request); e.listeners.add(listener);
   if (e.due <= now()) {pending.add(keyOf(request)); queue();} else schedule();
   return () => {e.listeners.delete(listener); schedule();};
  },
  invalidate() {epoch++; for (const [key, e] of entries) {if (e.listeners.size) {e.due = 0; pending.add(key);} else entries.delete(key);} queue();},
  failed(request: FingerprintRequest) {const e = entry(request); notify(e, {error: 'Fingerprint unavailable'}); e.due = now() + 5000; schedule();},
  dispose() {epoch++; entries.clear(); pending.clear(); if (timer) clearTimeout(timer); timer = undefined;},
 };
}
