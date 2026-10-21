// Filter suggestions share the server's one metadata scan slot. Keep only one
// network request active so background pickers cannot occupy every HTTP/1
// connection ahead of a foreground row/group request. Repeated picker renders
// share their in-flight request and a short-lived, stable result object.
export function createQueryMetadataLoader(now = Date.now, maxAge = 5000) {
  const cache = new Map<string, { value: Promise<unknown>; expires: number }>();
  let tail: Promise<unknown> = Promise.resolve();
  return function load<T>(key: string, read: () => Promise<T>, signal?: AbortSignal): Promise<T> {
    if (signal?.aborted) return Promise.reject(signal.reason ?? new DOMException("Aborted", "AbortError"));
    let entry = cache.get(key);
    if (!entry || entry.expires <= now()) {
      const next = { value: Promise.resolve() as Promise<unknown>, expires: Infinity };
      next.value = tail.catch(() => {}).then(read).then(value => {
        next.expires = now() + maxAge;
        return value;
      }, error => {
        if (cache.get(key) === next) cache.delete(key);
        throw error;
      });
      entry = next;
      cache.set(key, entry);
      tail = entry.value.catch(() => {});
      if (cache.size > 128) {
        for (const [name, item] of cache) if (item.expires <= now()) cache.delete(name);
      }
    }
    const result = entry.value as Promise<T>;
    if (!signal) return result;
    return new Promise<T>((resolve, reject) => {
      const abort = () => reject(signal.reason ?? new DOMException("Aborted", "AbortError"));
      signal.addEventListener("abort", abort, { once: true });
      result.then(resolve, reject).finally(() => signal.removeEventListener("abort", abort));
    });
  };
}
