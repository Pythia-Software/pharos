import type { ComputedColumnStore } from "@pythia-software/query-table-core";

// The hook publishes saved definitions locally without listing again. Publish
// the matching execution envelope before either catalogue operation resolves.
export function withComputedCapabilityRefresh(store: ComputedColumnStore, refresh: (signal?: AbortSignal) => Promise<void>): ComputedColumnStore {
  return {
    ...store,
    async list(dataset, signal) {
      const definitions = await store.list(dataset, signal);
      await refresh(signal);
      return definitions;
    },
    async save(dataset, column, expectedRevision) {
      const saved = await store.save(dataset, column, expectedRevision);
      await refresh();
      return saved;
    },
  };
}
