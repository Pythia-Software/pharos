// Query execution has a server budget, but a stalled connection (including a
// response body that never finishes) must also stop showing Loading in the UI.
export const queryRequestTimeoutMs = 45_000;

export class QueryRequestError extends Error {
  constructor(message: string, readonly status?: number) { super(message); }
}

export function shouldSkipQueryOnRestore(error: unknown): boolean {
  return error instanceof QueryRequestError && (error.status === undefined || [400, 422].includes(error.status));
}

// Query Table's HTTP stores accept a host fetcher. Keep restoration and computed
// definitions bounded too; the signal remains attached while they read the body.
export const queryFetch: typeof fetch = (input, init = {}) => {
  const caller = init.signal ?? (input instanceof Request ? input.signal : undefined);
  const deadline = AbortSignal.timeout(queryRequestTimeoutMs);
  return fetch(input, { ...init, signal: caller ? AbortSignal.any([caller, deadline]) : deadline });
};

export async function queryRequest<T>(url: string, init: RequestInit = {}, timeoutMs = queryRequestTimeoutMs): Promise<T> {
  const deadline = AbortSignal.timeout(timeoutMs);
  const signal = init.signal ? AbortSignal.any([init.signal, deadline]) : deadline;
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
