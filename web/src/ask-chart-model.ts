import { EMPTY_QUERY, type FieldSchema } from "@pythia-software/query-table-core";
import { readDashboardCard } from "./dashboard-card-model";
import type { QueryDataset } from "./query-links";
import type { PreviewJob } from "./query-previews";
import { onDatasetRefresh } from "./dataset-refresh";

// Ask renders the addressed dashboard without collecting it or rewriting its URL.
// Unsupported and malformed destinations keep their ordinary link presentation.
export function askChartPreviewJob(address: string, schemas: Partial<Record<QueryDataset, FieldSchema>>): PreviewJob | undefined {
  try {
    const { dataset } = readDashboardCard(address);
    const schema = schemas[dataset];
    if (!schema || schema.name !== `pharos_${dataset}`) return;
    return { id: address, dataset, schema, query: EMPTY_QUERY, options: { dashboard: address } };
  } catch { return; }
}

// Reopening Ask and the dashboard's calendar/data refreshes revalidate the
// retained chart. Reopening reuses a preview still fresh in the cache, such
// as one loaded while Ask floated; data refreshes make it stale. Other
// datasets and disposed chats must not wake it.
export function onAskChartRefresh(dataset: string, openings: EventTarget, refresh: (stale: boolean) => void, data: EventTarget = window): () => void {
  const reopened = () => refresh(false);
  openings.addEventListener("open", reopened);
  const stop = onDatasetRefresh(dataset, () => refresh(true), data);
  return () => { openings.removeEventListener("open", reopened); stop(); };
}
