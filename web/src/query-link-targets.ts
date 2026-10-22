import type { LinkTargets, QueryState } from "@pythia-software/query-table-core";
import { queryDatasets, queryURL } from "./query-links";

// Group link columns compose the query; Pharos decides where it opens. Every
// dataset is a target, so a link can open matching records in any table.
export const linkTargets: LinkTargets = Object.fromEntries(queryDatasets.map(dataset => [dataset, (query: QueryState) => queryURL(dataset, { ...query, fitToScreen: true })]));

// Plain clicks stay in the app. Modified and middle clicks keep native link behavior.
export function navigateLink(href: string): boolean {
  if (!window.pharosNavigate) return false;
  const url = new URL(href, window.location.href);
  // Conversation-only links need the service's ID-to-work redirect before
  // the reader can open them. Leave these and external URLs to the browser.
  if (url.origin !== window.location.origin || url.pathname.startsWith("/conversation/")) return false;
  window.pharosNavigate(href);
  return true;
}
