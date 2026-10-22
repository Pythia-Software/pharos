import type { LinkTargets, QueryState } from "@pythia-software/query-table-core";
import { queryDatasets, queryURL } from "./query-links";
import { usageDatasets } from "./usage-workspace-model";

// Group link columns compose the query; Pharos decides where it opens. Every
// dataset is a target, so a link can open matching records in any table.
export const linkTargets: LinkTargets = Object.fromEntries(queryDatasets.map(dataset => [dataset, (query: QueryState) => queryURL(dataset, Object.hasOwn(usageDatasets, dataset) ? { ...query, fitToScreen: true } : query)]));

// Plain clicks stay in the app. Modified and middle clicks keep native link behavior.
export function navigateLink(href: string): boolean {
  if (!window.pharosNavigate) return false;
  window.pharosNavigate(href);
  return true;
}
