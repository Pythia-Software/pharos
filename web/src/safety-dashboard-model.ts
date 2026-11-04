import { optimizeSelectionPlan, type OptimizeSelectionGroup } from "./optimize-model";
export const safetyRanks = ["danger", "recent", "calls", "evidence"] as const;
export type SafetyRank = typeof safetyRanks[number];
export const safetyRankLabels: Record<SafetyRank, string> = { danger: "Danger", recent: "Recent", calls: "Calls", evidence: "New evidence" };

// How likely a check's notices are to need the user, set per check by Pharos.
// Only the two upper levels get a chip, so a chip always means something.
export type SafetyDanger = "high" | "medium" | "low";
export const safetyDangerLabels: Record<SafetyDanger, string> = { high: "Dangerous", medium: "Risky", low: "" };
const dangerOrder: Record<string, number> = { high: 3, medium: 2, low: 1, "": 0 };

// A check is Recent when one of its notices was seen in the last three days.
export const safetyRecentDays = 3;

type Notice = { id: string; state: string; target: string; target_label: string; last_seen_at: string; calls: number; material_change: boolean; danger?: string };
const seen = (notice: { last_seen_at: string }) => Date.parse(notice.last_seen_at) || 0;

export function safetyDashboardPlaces(notices: readonly Notice[]) {
  const places = new Map<string, { key: string; label: string; count: number }>();
  for (const notice of notices) {
    const place = places.get(notice.target) ?? { key: notice.target, label: notice.target_label, count: 0 };
    place.count++;
    places.set(notice.target, place);
  }
  return [...places.values()].sort((a, b) => b.count - a.count || a.label.localeCompare(b.label));
}

type CheckNotice = Notice & { rule: string; title: string; explanation?: string; subject?: string; first_seen_at?: string; cart?: unknown };
export type SafetyCheckSummary = {
  rule: string; title: string; explanation: string; danger: SafetyDanger | "";
  ids: string[]; count: number; locations: number;
  // Where: the busiest locations, with the rest counted.
  places: { label: string; count: number }[]; morePlaces: number;
  // What: a directory every selected path shares, and the most common
  // resources (relative to it), with the rest counted.
  under: string; resources: { name: string; count: number }[]; moreResources: number;
  // When and how much: the span of activity, calls, new evidence, and how
  // many already wait in a prompt.
  first_seen_at: string; last_seen_at: string; recent: boolean; calls: number; fresh: number; inPrompt: number;
};

// Paths read shorter from the home directory, and a Git subject names its
// repository before the operation; the location already says where.
export function safetyResourceName(subject: string): string {
  const name = subject.split("·").map(part => part.trim()).filter(Boolean).at(-1) ?? "";
  return name.replace(/^\/Users\/[^/]+(?=\/|$)/, "~").replace(/^(?:\$PWD|\$\{PWD\}|\.)\//, "").replace(/(.)\/+$/, "$1");
}

// A long resource keeps its start and its end, where paths say the most.
export function safetyShortName(name: string, max = 48): string {
  if (name.length <= max) return name;
  const tail = Math.ceil((max - 1) * 0.6);
  return `${name.slice(0, max - 1 - tail)}…${name.slice(-tail)}`;
}

// The deepest directory every path shares, when it says more than the home
// directory or the filesystem root.
export function safetyCommonDirectory(names: readonly string[]): string {
  if (names.length < 2 || !names.every(name => /^[~/]/.test(name))) return "";
  const parts = names.map(name => name.split("/"));
  const shared: string[] = [];
  for (let i = 0; parts.every(path => i < path.length - 1 && path[i] === parts[0]![i]); i++) shared.push(parts[0]![i]!);
  const under = shared.join("/");
  return shared.filter(Boolean).length >= 2 ? under : "";
}

const top = <T extends { count: number }>(items: T[], n: number) => ({ shown: items.slice(0, n), more: Math.max(0, items.length - n) });
const tally = (values: string[]) => {
  const counts = new Map<string, number>();
  for (const value of values) counts.set(value, (counts.get(value) ?? 0) + 1);
  return [...counts].map(([key, count]) => ({ key, count })).sort((a, b) => b.count - a.count || a.key.localeCompare(b.key));
};

// Notices in the current list and place, summarized by check: what the
// notices have in common (one location, one directory, a recurring resource),
// how many, when, and whether any are new or already in a prompt. Each check
// opens a table of every resource it selected there. By default the most
// dangerous checks lead, so a rare credential copy outranks a common process
// signal; other ranks order by latest activity, calls, or new evidence. Ties
// fall back to danger, then latest activity.
export function safetyDashboardChecks(notices: readonly CheckNotice[], state: string, place: string, rank: SafetyRank = "danger", now = Date.now()): SafetyCheckSummary[] {
  const groups = new Map<string, CheckNotice[]>();
  for (const notice of notices) {
    if (notice.state !== state || place && notice.target !== place) continue;
    groups.set(notice.rule, [...groups.get(notice.rule) ?? [], notice]);
  }
  return [...groups.values()].map(group => {
    const lead = group[0]!;
    const places = tally(group.map(notice => notice.target_label || notice.target));
    const names = group.map(notice => safetyResourceName(notice.subject ?? ""));
    const under = safetyCommonDirectory(names);
    const resources = tally(names.map(name => under ? name.slice(under.length).replace(/^\//, "") || name : name).filter(Boolean));
    const shownPlaces = top(places, 2), shownResources = top(resources, 3);
    const times = group.map(notice => notice.last_seen_at).concat(group.map(notice => notice.first_seen_at ?? notice.last_seen_at)).filter(Boolean).sort((a, b) => (Date.parse(a) || 0) - (Date.parse(b) || 0));
    return {
      rule: lead.rule, title: lead.title, explanation: lead.explanation ?? "", danger: (lead.danger ?? "") as SafetyDanger | "",
      ids: group.map(notice => notice.id), count: group.length, locations: new Set(group.map(notice => notice.target)).size,
      places: shownPlaces.shown.map(({ key, count }) => ({ label: key, count })), morePlaces: shownPlaces.more,
      under, resources: shownResources.shown.map(({ key, count }) => ({ name: key, count })), moreResources: shownResources.more,
      first_seen_at: times[0] ?? "", last_seen_at: times.at(-1) ?? "", recent: now - (Date.parse(times.at(-1) ?? "") || 0) < safetyRecentDays * 86_400_000,
      calls: group.reduce((sum, notice) => sum + notice.calls, 0), fresh: group.filter(notice => notice.material_change).length, inPrompt: group.filter(notice => notice.cart).length,
    };
  }).sort((a, b) => {
    const danger = dangerOrder[b.danger]! - dangerOrder[a.danger]!;
    const lead = rank === "recent" ? seen(b) - seen(a) : rank === "calls" ? b.calls - a.calls : rank === "evidence" ? b.fresh - a.fresh : danger;
    return lead || danger || seen(b) - seen(a) || b.count - a.count || a.title.localeCompare(b.title);
  });
}

// A prompt asks an agent working in one location, and holds up to 50 notices.
export const safetyPromptLimit = 50;
export type SafetySelectionGroup = OptimizeSelectionGroup;
// How a table selection maps onto prompts: one group per location, largest
// first, and why prompts are unavailable when they are.
export const safetySelectionPlan = <T extends Notice>(notices: readonly T[], ids: readonly string[]) => optimizeSelectionPlan(notices, ids);

export type SafetyCartCheck = { rule: string; title: string; danger?: SafetyDanger; ids: string[]; ticked: number };
// A location's prompt as checks rather than notices: on the dashboard a card
// is the unit of work, so its prompt lists each check once with how many of
// its notices it holds. Ticking or removing a check applies to all of them;
// the table's prompt cart keeps per-notice control.
export function safetyCartChecks(group: { items: readonly { id: string; title: string; ticked: boolean; rule?: string; danger?: SafetyDanger }[] }): SafetyCartCheck[] {
  const checks = new Map<string, SafetyCartCheck>();
  for (const item of group.items) {
    const key = item.rule ?? item.title;
    const check = checks.get(key) ?? { rule: key, title: item.title, danger: item.danger, ids: [], ticked: 0 };
    check.ids.push(item.id);
    if (item.ticked) check.ticked++;
    checks.set(key, check);
  }
  return [...checks.values()];
}
