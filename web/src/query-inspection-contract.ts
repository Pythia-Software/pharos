import { parseJsonInspectionState } from "@pythia-software/query-table-core";

// Documentation is validated by the installed public codec, not another host
// parser. Native itemDetails owns updates, eviction, links and persistence.
export const jsonInspectionTemplate = parseJsonInspectionState({
  version: 1, open: false, search: "", searchMode: "filter", raw: false,
  collapsedPaths: [], expandedBuckets: [], visibleEntryLimit: 200, searchOverrides: [],
});
