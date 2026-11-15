import { queryFromURL } from "./query-links";
import { dashboardCardSource } from "./dashboard-card-model";

// Read card populations through the same readers that execute their previews.
// Dashboard cards need no q_* token (including bespoke Habits and Thinking).
export type CardReadinessSource = {dataset:string;query:unknown};
export function cardReadinessSource(address:string,resolved?:CardReadinessSource):CardReadinessSource {
  // The owner has already resolved durable queries from saved definitions or
  // remote snapshots. A cache miss must not discard the population it displays.
  if(resolved)return resolved;
  const query=queryFromURL(address);
  if(query)return query;
  try { const source=dashboardCardSource(address);return {dataset:source.dataset,query:source}; }
  catch { return {dataset:"unknown",query:null}; }
}
