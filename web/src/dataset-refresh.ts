// When a dashboard's results are stale: Refresh (pharos:usage-refresh), or
// a refresh of the dataset they read (pharos:data-refresh with the dataset),
// such as parallelism records rebuilt after saved Parallelism settings.
export const refreshesDataset = (event: Event, dataset: string) => event.type === "pharos:usage-refresh" || event.type === "pharos:data-refresh" && (event as CustomEvent<unknown>).detail === dataset;
export function onDatasetRefresh(dataset: string, callback: () => void, target: Pick<EventTarget, "addEventListener" | "removeEventListener"> = window): () => void {
  const listener = (event: Event) => { if (refreshesDataset(event, dataset)) callback(); };
  for (const type of ["pharos:usage-refresh", "pharos:data-refresh"]) target.addEventListener(type, listener);
  return () => { for (const type of ["pharos:usage-refresh", "pharos:data-refresh"]) target.removeEventListener(type, listener); };
}

// The datasets a saved preference rebuilds on the service: Carbon
// assumptions re-estimate usage, Parallelism settings rebuild its records.
const settingDatasets: Record<string, string> = { "pharos-carbon-v1": "usage", "pharos-concurrency-v1": "parallelism" };
export const datasetsForSavedPreferences = (saved: { set?: Record<string, unknown>; delete?: string[] } | undefined) =>
  [...new Set([...Object.keys(saved?.set ?? {}), ...saved?.delete ?? []].flatMap(key => settingDatasets[key] ? [settingDatasets[key]] : []))];
