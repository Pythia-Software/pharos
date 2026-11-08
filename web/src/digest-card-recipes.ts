import type { SummaryColumn, WhereClause, WhereTerm } from "@pythia-software/query-table-core";
import type { CardExploration } from "./card-recipes";
import type { QueryRecipe } from "./curiosity-data";
import { agentEventFloor, agentEventFriction, agentEventNotObstacle, agentEventsEmpty, agentEventsPerThousand, agentEventsRead } from "./agent-event-card-recipes";
import { agentEventWorkTurns } from "./agent-event-rates-catalog";

// The weekly digest: this week against the ones before, from what Pharos
// already keeps. New and returning problems are Fixes rows, safeguards got
// around are Risky actions notices, and friction by week is the Agent Events
// dashboard's rate. Each card opens that table or dashboard filtered to what
// it draws. Choices made without asking are the Agent Events card of that
// name, not a digest card of their own.

const sum = (id: string, field: string, label: string): SummaryColumn => ({ id, op: "sum", field, label });
const row = (id: string, field: string, label: string): SummaryColumn => ({ id, op: "row", field, label });
const equal = (field: string, value: string): WhereClause => ({ field, op: "=", value });

// ----- Fixes: one row per fix, ranked by the work items it came up in -------

const workItems = sum("dg_work_items", "work_items", "Work items");
const fixRows = (subjects: string, action: string): CardExploration => ({
  keys: ["id"], valueField: workItems.id, unit: "work items", subjects, action, labelFields: ["dg_title"],
  summaries: [workItems, row("dg_title", "title", "Problem"), sum("dg_events", "events", "Events"), sum("dg_conversations", "conversations", "Conversations"), row("dg_first_seen", "first_seen", "First seen"), row("dg_last_seen", "last_seen", "Last seen")],
  context: [{ field: "dg_events", label: "events" }, { field: "dg_conversations", label: "conversations" }],
  // One fix per group; its row columns read that fix.
  representative: { orderBy: [{ field: "last_seen_at", dir: "desc", nulls: "last" }] },
});
// What Open lists: open fixes your Fixes settings do not hide.
const openFixes = [equal("state", "open"), equal("shown", "shown")];
const fixes = { category: "digest" as const, dataset: "fixes" as const, unit: "work items", measures: [] };
const fixesNote = "Fixes record days, not times, so a problem counts from the start of the local day it was seen. Open fixes only, as Open lists them: fixes your Fixes settings hide (seen in too few work items, quiet too long, or covered by guidance) are left out. A work item is a task the source recorded, so one long task counts once however many conversations it took.";
const fixCover = (metricId: string) => ({ mode: "result" as const, metricId, kind: "bar-horizontal" as const, maxGroups: 5, showOther: false });

// ----- Risky actions: safeguard notices --------------------------------------

const safeguardRules = ["safeguard-test", "safeguard-restriction", "safeguard-gate", "safeguard-other"];
const notices = sum("dg_notices", "review_count", "Notices");

export const digestCardRecipes: QueryRecipe[] = [
  { ...fixes, id: "digest-new-fixes", idea: "learning-07", field: "first_seen_at", days: [7, 30], title: "New problems to fix",
    description: "Open Fixes first seen in the last {days} days, by the work items they came up in.", where: openFixes,
    exploration: fixRows("new problems", "Review new problems"), cover: fixCover(workItems.id), action: "Review new problems",
    emptyLabel: "No new problems to fix: agents reported nothing new that Fixes lists.",
    note: `Each is a problem agents reported for the first time in this window, such as an environment step that failed or a doc that was out of date; open one for what agents said and the change that would prevent it. ${fixesNote}` },
  // "Seen again in the window, first seen before it" needs the window start
  // in the where, so this card keeps one window: a relative date in a where
  // is fixed, while the card's window control moves only the rolling range.
  { ...fixes, id: "digest-returning-fixes", idea: "conversation-05", field: "last_seen_at", days: [7], title: "Problems that keep coming back",
    description: "Open Fixes agents reported again in the last {days} days that were first seen before them, by the work items they came up in.",
    where: [...openFixes, { field: "first_seen_at", op: "<", value: "-7d" }],
    exploration: fixRows("returning problems", "Review returning problems"), cover: fixCover(workItems.id), action: "Review returning problems",
    emptyLabel: "No older problems came back: agents reported none of them again.",
    note: `A problem that comes back has outlasted the work that met it: a change to docs, checks, environment or tools would stop it for every agent. Work items counts every work item it came up in, not only this week's. ${fixesNote}` },
  { id: "digest-safeguards", category: "digest", idea: "verification-06", dataset: "safety", field: "last_seen_at", days: [7, 30], unit: "notices", title: "Safeguards agents got around",
    description: "Risky actions notices from the last {days} days where an agent weakened a test or check, or got around a restriction, gate or instruction, by kind.",
    where: [{ any: safeguardRules.map(rule => equal("rule", rule)) }], measures: [],
    exploration: { keys: ["rule_label"], summaries: [notices, { id: "dg_locations", op: "count_distinct", field: "target_label", label: "Locations" }], valueField: notices.id, unit: "notices", subjects: "kinds of safeguard", action: "Review the notices", labelFields: ["rule_label"], context: [{ field: "dg_locations", label: "locations" }] },
    cover: { mode: "result", metricId: notices.id, kind: "bar-horizontal", maxGroups: 4, showOther: false }, action: "Review the notices",
    emptyLabel: "No agent got around a safeguard in this window.",
    note: "Each notice is a time an agent said it weakened a test or check, or got around a restriction, gate or instruction, in any Risky actions state. Most weeks have none. Read each one and decide whether the safeguard needs changing or the agent needs telling. Notices come from what agents wrote, as the events pass read it, so a workaround an agent did not mention is not here." },
  { id: "digest-friction-weeks", category: "digest", idea: "conversation-01", dataset: "agent_event_rates", field: "occurred_at", days: [30], unit: "events per 1,000 messages", title: "Friction week by week",
    description: "Environment problems, missing tools, out-of-date docs, failures already there and unclear instructions per 1,000 agent messages read, week by week over the last {days} days.",
    where: [agentEventFriction, agentEventNotObstacle, ...agentEventWorkTurns],
    measures: [{ id: "friction_by_week", op: "sum", expression: agentEventsPerThousand, groupBy: ["week"], groupLimit: 200, sort: [{ key: "group0", dir: "asc" }], minCount: agentEventFloor, label: "Friction events per 1,000 messages read",
      display: { kind: "bar-vertical", xLabel: "Week starting", yLabel: "Events per 1,000 messages", format: { kind: "number", decimals: 1 } } }],
    cover: { mode: "source", metricId: "friction_by_week", kind: "bar-vertical", maxGroups: 14, showOther: false, showLegend: false, showHeadline: false, height: 240 },
    action: "Compare weeks", emptyLabel: agentEventsEmpty,
    note: `Weeks start on Monday, in local time. The first and latest weeks are partial: the window starts mid-week, and this week is still under way, so its rate can move. Plain obstacles are left out, as on Where agents hit friction. A week under 200 messages read is hidden. ${agentEventsRead}` },
];
