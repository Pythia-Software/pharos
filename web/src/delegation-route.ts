// The delegation explorer's address on a work page: whether its sessions are
// shown and how (table or parallel lanes), hierarchy or a ranking by one
// metric, collapsed sessions, the selected session and a time range. The
// explorer and the site map (site-map.ts) both read it here.
export const delegationTabs = ["table", "parallel"] as const;
export const delegationModes = ["hierarchy", "rank"] as const;
// A ranking sorts by one of the metrics the work's analysis lists, counted
// for the session itself or with everything below it.
export const delegationScopes = ["all", "self"] as const;
export const delegationDirections = ["desc", "asc"] as const;
export const defaultDelegationSort = "tokens";
export type DelegationRoute = {
  open: boolean; tab: typeof delegationTabs[number]; mode: typeof delegationModes[number];
  sort: string; scope: string; direction: string; collapsed: string[]; selected: string; range: [number, number] | null;
};
export function readDelegationRoute(params: URLSearchParams): DelegationRoute {
  const from = Number(params.get("delegation_start")), to = Number(params.get("delegation_end"));
  return {
    open: params.get("sessions") !== "hidden",
    tab: params.get("explorer") === "parallel" ? "parallel" : "table",
    mode: params.get("delegation_mode") === "rank" ? "rank" : "hierarchy",
    sort: params.get("delegation_sort") || defaultDelegationSort,
    scope: params.get("delegation_scope") || delegationScopes[0],
    direction: params.get("delegation_direction") || delegationDirections[0],
    collapsed: (params.get("delegation_collapsed") || "").split(",").filter(Boolean),
    selected: params.get("delegation_session") || "",
    range: params.has("delegation_start") && to > from ? [from, to] : null,
  };
}
// The explorer's parameters for a route, as it writes them back: an empty
// value removes its parameter. A ranking's sort, scope and direction are
// written only in rank mode; the legacy search parameters are always removed.
export function delegationRouteValues(route: Pick<DelegationRoute, "mode" | "sort" | "scope" | "direction" | "collapsed" | "selected" | "range">): Record<string, string> {
  const rank = route.mode === "rank";
  return {
    delegation_mode: route.mode,
    delegation_sort: rank ? route.sort : "",
    delegation_scope: rank ? route.scope : "",
    delegation_direction: rank ? route.direction : "",
    delegation_search: "",
    delegation_collapsed: [...route.collapsed].sort().join(","),
    delegation_session: route.selected,
    delegation_matching: "",
    ...route.range ? { delegation_start: String(route.range[0]), delegation_end: String(route.range[1]) } : {},
  };
}
