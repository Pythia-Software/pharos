// The Fixes dashboard's logic: which items a list shows, in what order, which
// the settings' bounds take off the menu, and how a card describes an item.
// The page (fixes.tsx) only renders what these return.

export const fixesRanks = ["priority", "work", "recent", "time", "spread"] as const;
export type FixesRank = typeof fixesRanks[number];
export const fixesRankLabels: Record<FixesRank, string> = { priority: "Priority", work: "Work items", recent: "Recent", time: "Time lost", spread: "Repositories" };
export const fixesRankExplanations: Record<FixesRank, string> = {
  priority: "Work items, weighted down once a problem has been quiet longer than your setting, plus two per hour of agent time in the friction it started.",
  work: "How many separate work items ran into it.",
  recent: "Latest occurrence first.",
  time: "Agent time in friction episodes its events started.",
  spread: "How many repositories it spans.",
};
export const fixesKinds = ["env", "harness", "tool", "flaky", "knowledge", "stale", "obstacle"] as const;
export type FixesKind = typeof fixesKinds[number];
export const fixesKindLabels: Record<FixesKind, string> = {
  env: "Environment or setup", harness: "Harness and shell behaviour", tool: "Tool or integration", flaky: "Known failing check",
  knowledge: "Codebase knowledge", stale: "Out-of-date docs", obstacle: "Recurring obstacle",
};
export type FixesState = "open" | "handed_off" | "dismissed";

export type FixStatus = { state: string; label: string; detail: string; first?: string; last?: string; days_quiet: number };
export type FixGuidanceHit = { repository: string; file: string; line: number; term: string; text: string };
export type FixCount = { key: string; label?: string; count: number };
export type FixCard = {
  id: string; title: string; category: string; category_label: string; source: string;
  target: string; target_label: string; scope: string; scope_reason: string; destination_kind: string; destination: string;
  terms: string[]; files: string[]; work_items: number; events: number; conversations: number; repositories: FixCount[];
  status: FixStatus; hours: number; episodes: number; guidance_hit: FixGuidanceHit | null; groups: number; tentative: boolean; safeguards: number;
  state: FixesState; recurred: boolean; handed_at?: string | null; cart?: { target: string; target_label: string; ticked: boolean } | null;
  priority: number; hidden: "" | "stale" | "rare" | "intended"; deprioritized: boolean;
  // lead: the member that states the problem best, quoted under the title.
  lead?: string; check?: string | null; intended?: boolean; intended_reason?: string | null;
};
export type FixesBounds = { quiet_days: number; hide_days: number; min_work_items: number };

// Repositories are counted by name; events outside one count as "No repository".
const named = (item: FixCard) => item.repositories.filter(repository => repository.key && repository.key !== "No repository");
const repositoryCount = (item: FixCard) => named(item).length;
const rankKey: Record<FixesRank, (item: FixCard) => number | string> = {
  priority: item => item.priority, work: item => item.work_items, recent: item => item.status.last ?? "", time: item => item.hours, spread: repositoryCount,
};

export function fixesRanked(items: readonly FixCard[], rank: FixesRank): FixCard[] {
  const key = rankKey[rank] ?? rankKey.priority;
  return items.slice().sort((a, b) => {
    const x = key(a), y = key(b);
    return (x < y ? 1 : x > y ? -1 : 0) || b.priority - a.priority || a.title.localeCompare(b.title);
  });
}

export type FixesFilter = { list: FixesState; place: string; kind: string; rank: FixesRank; showHidden: boolean };
// What a list shows. Bounds apply to Open only: what you handed off or closed
// stays listed. Hidden items are counted, and shown on request, marked.
export function fixesDashboard(items: readonly FixCard[], filter: FixesFilter) {
  const inScope = items.filter(item => (!filter.place || item.target === filter.place) && (!filter.kind || item.category === filter.kind));
  const counts: Record<FixesState, number> = { open: 0, handed_off: 0, dismissed: 0 };
  const hidden = { stale: 0, rare: 0, intended: 0, total: 0 };
  for (const item of inScope) {
    if (item.state === "open" && item.hidden) { hidden[item.hidden]++; hidden.total++; continue; }
    counts[item.state]++;
  }
  const listed = fixesRanked(inScope.filter(item => item.state === filter.list && (filter.list !== "open" || !item.hidden || filter.showHidden)), filter.rank);
  return {
    counts, hidden: filter.list === "open" ? hidden : { stale: 0, rare: 0, intended: 0, total: 0 },
    main: listed.filter(item => !item.tentative), maybe: listed.filter(item => item.tentative),
  };
}

// Where prompts go: the shared setup first, then repositories by how many
// items each holds.
export function fixesPlaces(items: readonly FixCard[]) {
  const places = new Map<string, { key: string; label: string; count: number }>();
  for (const item of items) {
    const place = places.get(item.target) ?? { key: item.target, label: item.target_label, count: 0 };
    place.count++;
    places.set(item.target, place);
  }
  return [...places.values()].sort((a, b) => Number(b.key === "global") - Number(a.key === "global") || b.count - a.count || a.label.localeCompare(b.label));
}

export function fixesKindsPresent(items: readonly FixCard[]) {
  const counts = new Map<string, number>();
  for (const item of items) counts.set(item.category, (counts.get(item.category) ?? 0) + 1);
  return fixesKinds.filter(kind => counts.has(kind)).map(kind => ({ key: kind, label: fixesKindLabels[kind], count: counts.get(kind)! }));
}

const plural = (n: number, one: string, many = `${one}s`) => `${n.toLocaleString()} ${n === 1 ? one : many}`;
const shortDay = (day?: string) => day ? new Date(`${day}T12:00:00`).toLocaleDateString(undefined, { month: "short", day: "numeric" }) : "";
export function fixesRange(status: Pick<FixStatus, "first" | "last">): string {
  if (!status.first) return "No dates recorded";
  return status.first === status.last || !status.last ? shortDay(status.first) : `${shortDay(status.first)} – ${shortDay(status.last)}`;
}

// The facts line of a card: how often, where, when and what it cost.
export function fixesFacts(item: FixCard): string[] {
  const repositories = repositoryCount(item);
  return [
    plural(item.events, "event"),
    repositories > 1 ? plural(repositories, "repository", "repositories") : named(item)[0]?.key ?? "",
    fixesRange(item.status),
    item.hours >= 0.05 ? `${item.hours.toLocaleString(undefined, { maximumFractionDigits: 1 })} h of agent time` : "",
  ].filter(Boolean);
}

// Why an item is off the menu, in the words of the settings.
export function fixesHiddenReason(item: Pick<FixCard, "hidden" | "status" | "work_items">, bounds: FixesBounds): string {
  if (item.hidden === "intended") return "Guidance already covers this: agents cite the rule they are following, so it is working as intended.";
  if (item.hidden === "stale") return `Not seen for ${plural(item.status.days_quiet, "day")}; your settings hide problems quiet longer than ${plural(bounds.hide_days, "day")}.`;
  if (item.hidden === "rare") return `Seen in ${plural(item.work_items, "work item")}; your settings hide problems seen in fewer than ${bounds.min_work_items}.`;
  return "";
}

// What the bounds hide from Open, or "" when they hide nothing.
export function fixesHiddenNote(hidden: { stale: number; rare: number; intended?: number; total: number }, bounds: FixesBounds): string {
  if (!hidden.total) return "";
  const parts = [
    hidden.stale ? `${plural(hidden.stale, "problem")} not seen in the last ${plural(bounds.hide_days, "day")}` : "",
    hidden.rare ? `${plural(hidden.rare, "problem")} seen in fewer than ${plural(bounds.min_work_items, "work item")}` : "",
  ].filter(Boolean);
  const intended = hidden.intended ? `${plural(hidden.intended, "problem")} the guidance causes on purpose` : "";
  if (!parts.length) return `Left out: ${intended}.`;
  return `Your settings hide ${parts.join(" and ")}.${intended ? ` Also left out: ${intended}.` : ""}`;
}

// The settings page's preview: how many open items each bound would hide.
// Mirrors fixesHidden in Go so the counts move as the selects change.
export function fixesBoundsPreview(items: readonly Pick<FixCard, "state" | "status" | "work_items" | "intended">[], bounds: FixesBounds) {
  let stale = 0, rare = 0, shown = 0;
  for (const item of items) {
    if (item.state !== "open" || item.intended) continue;
    if (bounds.hide_days > 0 && item.status.state !== "unknown" && item.status.days_quiet > bounds.hide_days) stale++;
    else if (item.work_items < bounds.min_work_items) rare++;
    else shown++;
  }
  return { stale, rare, shown };
}

// fixesLeadQuote is the sentence quoted under a title, unless the title is
// that sentence already.
export function fixesLeadQuote(item: { title: string; lead?: string }): string {
  const plain = (text: string) => text.replace(/[\s.!?…]+$/, "").trim().toLowerCase();
  // A title cut from the lead ("Test status: X" → "X") says the same thing.
  return item.lead && !plain(item.lead).endsWith(plain(item.title)) ? item.lead : "";
}
