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
  return fetch(input, { ...init, signal: requestSignal(caller, deadline) });
};

export async function queryRequest<T>(url: string, init: RequestInit = {}, timeoutMs = queryRequestTimeoutMs): Promise<T> {
  const deadline = AbortSignal.timeout(timeoutMs);
  const signal = requestSignal(init.signal, deadline);
  try {
    const response = await fetch(url, { ...init, signal });
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
