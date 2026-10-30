import { normalizeConcurrencySettings } from "./concurrency-settings";
// Linked assumptions apply to every request made by that view, including
// dashboards, source tables, item details and computed metrics. Explicit
// request assumptions (for a card preview) retain precedence.
export function viewRequestURL(path: string, view = typeof location === "undefined" ? undefined : new URL(location.href)): string {
  if (!view) return path;
  const url=new URL(path,view.origin);
  if(url.origin!==view.origin)return path;
  if(url.pathname.startsWith("/api/query/usage")&&!url.searchParams.has("carbon_settings")&&view.searchParams.has("analyze_carbon"))url.searchParams.set("carbon_settings",view.searchParams.get("analyze_carbon")!);
  if(url.pathname.startsWith("/api/query/parallelism")&&!url.searchParams.has("parallelism_settings")&&view.searchParams.has("analyze_parallelism")) {
    let linked: unknown;try{linked=JSON.parse(view.searchParams.get("analyze_parallelism")!);}catch{linked=null;}
    const ribbon=normalizeConcurrencySettings(linked);
    url.searchParams.set("parallelism_settings",JSON.stringify({gap:ribbon.gap,counting:ribbon.counting}));
  }
  return /^https?:/.test(path)?url.href:url.pathname+url.search+url.hash;
}

// Query execution has a server budget, but a stalled connection (including a
// response body that never finishes) must also stop showing Loading in the UI.
export const queryRequestTimeoutMs = 45_000;

export class QueryRequestError extends Error {
  constructor(message: string, readonly status?: number) { super(message); }
}

// Only query rejection or the client deadline implicates the query itself.
// Offline, authentication and service failures say nothing about it.
export function shouldSkipQueryOnRestore(error: unknown): boolean {
  return error instanceof QueryRequestError && (error.status === undefined || [400, 422].includes(error.status));
}

function requestSignal(caller: AbortSignal | null | undefined, deadline: AbortSignal): AbortSignal {
  if (!caller) return deadline;
  // AbortSignal.any arrived in Safari 17.4; the app also supports 17.0–17.3.
  if (typeof AbortSignal.any === "function") return AbortSignal.any([caller, deadline]);
  const controller = new AbortController();
  const abort = (source: AbortSignal) => {
    caller.removeEventListener("abort", cancel);
    deadline.removeEventListener("abort", expire);
    controller.abort(source.reason);
  };
  const cancel = () => abort(caller), expire = () => abort(deadline);
  if (caller.aborted) cancel();
  else if (deadline.aborted) expire();
  else {
    caller.addEventListener("abort", cancel, { once: true });
    deadline.addEventListener("abort", expire, { once: true });
  }
  return controller.signal;
}

// Query Table's HTTP stores accept a host fetcher. Keep restoration and computed
// definitions bounded too; the signal remains attached while they read the body.
export const queryFetch: typeof fetch = (input, init = {}) => {
  const caller = init.signal ?? (input instanceof Request ? input.signal : undefined);
  const deadline = AbortSignal.timeout(queryRequestTimeoutMs);
  return fetch(typeof input === "string" ? viewRequestURL(input) : input, { ...init, signal: requestSignal(caller, deadline) });
};

export async function queryRequest<T>(url: string, init: RequestInit = {}, timeoutMs = queryRequestTimeoutMs): Promise<T> {
  const deadline = AbortSignal.timeout(timeoutMs);
  const signal = requestSignal(init.signal, deadline);
  try {
    const response = await fetch(viewRequestURL(url), { ...init, signal });
    const body = await response.json().catch(error => {
      signal.throwIfAborted();
      if (response.ok) throw error;
      return {};
    });
    signal.throwIfAborted();
    if (!response.ok) {
      const message = typeof body.error === "string" ? body.error : body.error?.message;
      throw new QueryRequestError(message || `Request failed with HTTP ${response.status}`, response.status);
    }
    return body as T;
  } catch (error) {
    if (init.signal?.aborted) throw init.signal.reason;
    if (deadline.aborted) throw new QueryRequestError("This query took too long. Try again or reset the page to start with default options.");
    throw error;
  }
}
