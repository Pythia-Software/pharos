import { scatterMetrics } from "./scatter-metrics";
import type { ResultPolicy } from "./result-cache";
const scatterSources = new Set(scatterMetrics.map(metric => metric.dataset));
// When a dashboard's results are stale: Refresh (pharos:usage-refresh), or
// a refresh of the dataset they read (pharos:data-refresh with the dataset),
// such as parallelism records rebuilt after saved Parallelism settings.
export const refreshesDataset = (event: Event, dataset: string) => event.type === "pharos:usage-refresh" || event.type === "pharos:data-refresh" && ((event as CustomEvent<unknown>).detail === dataset || dataset === "people_message_labels" && ["writing_messages","conversation_activity","rhythm","message_labels"].includes(String((event as CustomEvent<unknown>).detail)) || dataset === "scatter" && scatterSources.has(String((event as CustomEvent<unknown>).detail)));
// A person pressing a page's Refresh asks for current answers; every other
// refresh (a focused tab, a new day, rebuilt data) takes what is stored.
export const askedRefresh = () => new CustomEvent("pharos:usage-refresh", { detail: "fresh" });
export const refreshPolicy = (event: Event): ResultPolicy => event.type === "pharos:usage-refresh" && (event as CustomEvent<unknown>).detail === "fresh" ? "fresh" : "cached";
export function onDatasetRefresh(dataset: string, callback: (policy: ResultPolicy) => void, target: Pick<EventTarget, "addEventListener" | "removeEventListener"> = window): () => void {
  const listener = (event: Event) => { if (refreshesDataset(event, dataset)) callback(refreshPolicy(event)); };
  for (const type of ["pharos:usage-refresh", "pharos:data-refresh"]) target.addEventListener(type, listener);
  return () => { for (const type of ["pharos:usage-refresh", "pharos:data-refresh"]) target.removeEventListener(type, listener); };
}

export function onSafetyChange(callback: () => void, target: Pick<EventTarget, "addEventListener" | "removeEventListener"> = window): () => void {
  target.addEventListener("pharos:safety-changed", callback);
  return () => target.removeEventListener("pharos:safety-changed", callback);
}

type DiscoveryStatus = { built_at: string | null; running: boolean };
export function findingsDiscoveryChanged(previous: DiscoveryStatus | undefined, next: DiscoveryStatus): boolean {
  return Boolean(previous && (previous.built_at !== next.built_at || previous.running && !next.running));
}

// The datasets a saved preference rebuilds on the service: Carbon
// assumptions re-estimate usage, Parallelism settings rebuild its records.
const settingDatasets: Record<string, string> = { "pharos-carbon-v1": "usage", "pharos-concurrency-v1": "parallelism", "pharos-habits-v1": "rhythm" };
export const datasetsForSavedPreferences = (saved: { set?: Record<string, unknown>; delete?: string[] } | undefined) =>
  [...new Set([...Object.keys(saved?.set ?? {}), ...saved?.delete ?? []].flatMap(key => settingDatasets[key] ? [settingDatasets[key]] : []))];

// Fixes change on a triage action, a saved bound or a new grouping.
export function onFixesChange(callback: () => void, target: Pick<EventTarget, "addEventListener" | "removeEventListener"> = window): () => void {
  target.addEventListener("pharos:fixes-changed", callback);
  return () => target.removeEventListener("pharos:fixes-changed", callback);
}
