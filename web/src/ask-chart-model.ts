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
// retained chart. Other datasets and disposed chats must not wake it.
export function onAskChartRefresh(dataset: string, openings: EventTarget, refresh: () => void, data: EventTarget = window): () => void {
  openings.addEventListener("open", refresh);
  const stop = onDatasetRefresh(dataset, refresh, data);
  return () => { openings.removeEventListener("open", refresh); stop(); };
}
