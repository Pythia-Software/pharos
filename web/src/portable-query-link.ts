import { createQueryLinkService, decodeQueryStrict, type QuerySnapshotStore } from "@pythia-software/query-table-core";

// Automatic synchronization stays self-contained until the library has saved
// the definition. The dependency's default local @code is intentionally never
// used for the address bar. Explicit Share uses the same snapshot store.
export const portableLinkOptions = { maxUrlBytes: Number.MAX_SAFE_INTEGER };
export function portableQueryLinks(key: string, store?: QuerySnapshotStore) {
  const service = createQueryLinkService(key, store, portableLinkOptions);
  return async (parameter: string, current: () => string, replace: (url: string) => void): Promise<void> => {
    if (!store) return;
    const original = new URL(current());
    const token = original.searchParams.get(parameter);
    if (!token || token.startsWith("@") || new TextEncoder().encode(original.href).length <= 2000) return;
    // durable() resolves only after Save has succeeded. A failure leaves the
    // inline definition intact and portable; a newer query/navigation wins.
    const link = await service.durable(decodeQueryStrict(token), original.href, parameter);
    const latest = new URL(current());
    if (latest.pathname !== original.pathname || latest.searchParams.get(parameter) !== token) return;
    latest.searchParams.set(parameter, new URL(link.url).searchParams.get(parameter)!);
    replace(latest.href);
  };
}
