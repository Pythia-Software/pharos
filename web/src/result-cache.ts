// The frontend's side of the server's result cache (docs/result-cache.md).
// A request names a policy; the response says what was served and when it
// was computed. This module is the whole rule for choosing between a stored
// answer and a live one:
//
//   - A page loading or revalidating its content asks "cached": whatever is
//     stored is shown at once, and the server refreshes stale entries when
//     it has capacity. A stale answer is shown with its real age and asked
//     for again (staleRetryDelays) until it is fresh.
//   - A person asking a page to refresh asks "fresh": stored answers are
//     used only while current, and the rest are computed now.
//   - A person asking for one thing again asks "live": always computed.
export type ResultPolicy = "cached" | "fresh" | "live";
export type ResultServed = { state: "fresh" | "stale" | "live"; built: number };

export const resultCacheHeader = "X-Pharos-Cache", resultBuiltHeader = "X-Pharos-Cache-Built";

// How long after a stale answer to ask again, in turn. Asking is cheap (the
// server reads one stored row); the waits give its refresher time to work.
export const staleRetryDelays = [5_000, 15_000, 45_000, 120_000];

export function resultPolicyHeaders(policy?: ResultPolicy): Record<string, string> {
  return policy ? { [resultCacheHeader]: policy } : {};
}

export function resultServed(headers: { get(name: string): string | null }, now = Date.now()): ResultServed | undefined {
  const state = headers.get(resultCacheHeader);
  if (state !== "fresh" && state !== "stale" && state !== "live") return;
  const built = Date.parse(headers.get(resultBuiltHeader) ?? "");
  return { state, built: Number.isFinite(built) ? built : now };
}

// What a view drawn from several answers was served: stale if any was, and
// as old as its oldest.
export function combineServed(...parts: (ResultServed | undefined)[]): ResultServed | undefined {
  const known = parts.filter((part): part is ResultServed => Boolean(part));
  if (!known.length) return;
  const state = known.some(part => part.state === "stale") ? "stale" : known.every(part => part.state === "live") ? "live" : "fresh";
  return { state, built: Math.min(...known.map(part => part.built)) };
}

const served = new WeakMap<object, ResultServed>();
export function noteServed<T>(body: T, info?: ResultServed): T {
  if (info && body && typeof body === "object") served.set(body, info);
  return body;
}
export function servedOf(body: unknown): ResultServed | undefined {
  return body && typeof body === "object" ? served.get(body) : undefined;
}

// A scope carries a policy to every query request made with its signal, and
// collects what they were served. Views built by code that only passes a
// signal down (a dashboard card's chart) join the cache this way, without
// each layer knowing about it. A layer that makes its own signal drops out
// of the scope, and its requests are simply computed as before.
type Scope = { policy: ResultPolicy; served: ResultServed[] };
const scopes = new WeakMap<AbortSignal, Scope>();
export function resultScope(parent: AbortSignal | undefined, policy: ResultPolicy) {
  const controller = new AbortController(), scope: Scope = { policy, served: [] };
  if (parent?.aborted) controller.abort(parent.reason);
  else parent?.addEventListener("abort", () => controller.abort(parent.reason), { once: true });
  scopes.set(controller.signal, scope);
  return { signal: controller.signal, served: () => combineServed(...scope.served) };
}

// scopedRequest names the scope's policy on a query request; its note
// records what the response says was served.
export function scopedRequest(url: string, init: RequestInit): { init: RequestInit; note: (headers: { get(name: string): string | null }) => void } {
  const scope = init.signal ? scopes.get(init.signal) : undefined;
  if (!scope || init.method !== "POST" || !url.split("?")[0].endsWith("/query-v3")) return { init, note: () => {} };
  const headers = new Headers(init.headers);
  headers.set(resultCacheHeader, scope.policy);
  return { init: { ...init, headers }, note: response => { const info = resultServed(response); if (info) scope.served.push(info); } };
}

// Calls ask(delay index) after each delay until it reports nothing is stale
// any more, the delays run out, or the signal aborts. A hidden page waits
// until it is shown: nobody is looking at what the answers would update.
export async function followStale(ask: (attempt: number) => Promise<boolean>, signal?: AbortSignal, delays = staleRetryDelays,
  wait: (ms: number, signal?: AbortSignal) => Promise<void> = pause, hidden: () => boolean = () => typeof document !== "undefined" && document.hidden): Promise<void> {
  for (let attempt = 0; attempt < delays.length; attempt++) {
    await wait(delays[attempt], signal);
    while (hidden() && !signal?.aborted) await wait(1_000, signal);
    if (signal?.aborted || !await ask(attempt)) return;
  }
}

// What a view says about the answer it shows: when it was known to be
// current, whether a newer one is on its way, and why none is coming.
export type ServedReadiness = { updatedAt: number; refreshing?: boolean; refreshError?: string };
export const stillStale = "These results have not been updated yet.";
// A current answer was checked against the data just now, whenever it was
// computed. A stale one is as old as its computation, and is either being
// followed (refreshing) or not: nothing will replace it until the view is
// loaded again, which is what refreshError tells the reader.
export function servedReadiness(served: ResultServed | undefined, following: boolean, failure?: unknown, now = Date.now()): ServedReadiness {
  if (served?.state !== "stale") return { updatedAt: now };
  return following ? { updatedAt: served.built, refreshing: true } : { updatedAt: served.built, refreshError: failure instanceof Error ? failure.message : failure ? String(failure) : stillStale };
}

// Runs load under a policy and publishes its value with its readiness.
// While the answer is stale it is loaded again (followStale) and published
// in place, so a view catches up without anyone asking. When following
// ends with the answer still stale (out of tries, or the follow-ups
// failed), the value is published once more as no longer refreshing. Only
// the first load's failure is the caller's.
export async function loadFollowing<T>(load: (signal: AbortSignal) => Promise<T>, publish: (value: T, readiness: ServedReadiness) => void, signal: AbortSignal, policy: ResultPolicy,
  follow: typeof followStale = followStale): Promise<void> {
  let shown: { value: T; served?: ResultServed } | undefined, failure: unknown;
  const once = async (asked: ResultPolicy) => {
    const scope = resultScope(signal, asked), value = await load(scope.signal), served = scope.served();
    if (signal.aborted) return false;
    shown = { value, served }; failure = undefined;
    publish(value, servedReadiness(served, true));
    return served?.state === "stale";
  };
  if (!await once(policy)) return;
  await follow(() => once("cached").catch(error => { failure = error; return !signal.aborted; }), signal);
  if (!signal.aborted && shown?.served?.state === "stale") publish(shown.value, servedReadiness(shown.served, false, failure));
}

function pause(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise(resolve => {
    const done = () => { clearTimeout(timer); signal?.removeEventListener("abort", done); resolve(); };
    const timer = setTimeout(done, ms);
    signal?.addEventListener("abort", done, { once: true });
  });
}
