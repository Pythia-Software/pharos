// The Findings page's address: which list, finding, tab, repository and view
// it shows. The page and the site map (site-map.ts) both read it here.
export const findingsLists = ["open", "watching", "wins", "dismissed"] as const;
export const findingTabs = ["overview", "evidence", "prompt"] as const;
export const findingsViews = ["", "settings", "table"] as const;
export type FindingsList = typeof findingsLists[number];
export type FindingsRoute = { finding: string; tab: typeof findingTabs[number]; view: typeof findingsViews[number]; list: FindingsList; repo: string };
const oneOf = <T extends string>(options: readonly T[], value: string | null, fallback: T): T => options.includes(value as T) ? value as T : fallback;
export function readFindingsRoute(params: URLSearchParams): FindingsRoute {
  return {
    finding: params.get("finding") ?? "",
    tab: oneOf(findingTabs, params.get("tab"), "overview"),
    view: oneOf(findingsViews, params.get("view"), ""),
    list: oneOf(findingsLists, params.get("list"), "open"),
    repo: params.get("repo") ?? "",
  };
}
// One finding's page; the overview tab is the default and is left out.
export const findingDetailURL = (id: string, tab = "") => `/findings?${new URLSearchParams({ finding: id, ...(tab && tab !== "overview" ? { tab } : {}) })}`;
// Rank by: a linked findings_rank applies without saving, an unlinked view
// shows the saved default, and only a manual choice saves the default.
export function findingsRank<T extends string>(linked: T | "", saved: T | undefined, chosen?: T): { rank: T | "usd"; save?: { rank: T } } {
  return chosen ? { rank: chosen, save: { rank: chosen } } : { rank: linked || saved || "usd" };
}
