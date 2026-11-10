import { EMPTY_QUERY, encodeQuery } from "@pythia-software/query-table-core";
import { parseRangeInput } from "./chart-range";
import { dashboardQuerySettingsSnapshot } from "./dashboard-query-settings";
import { formatDay, shiftDay, type HabitsSummary } from "./habits-model";
import { applyQueryIdea, usageQueryIdeas } from "./usage-query-ideas-model";
import { usageWorkspaceURL } from "./usage-workspace-model";

// Keep the automation table's period and habit-day boundary even when the
// visible chart describes a different actor or range.
export function habitsAutomationsLink(data: HabitsSummary, current: URLSearchParams) {
  const idea = usageQueryIdeas.rhythm.find(item => item.id === "rhythm:automations")!;
  const query = applyQueryIdea(EMPTY_QUERY, idea);
  const zone = data.timezone === "Local" ? Intl.DateTimeFormat().resolvedOptions().timeZone : data.timezone;
  const start = `${String(data.settings.dayStart).padStart(2, "0")}:00`;
  const where = [
    { field: "actor", op: "=" as const, value: "automated" },
    { field: "started_at", op: ">=" as const, value: parseRangeInput(`${data.punchcard.from}T${start}`, zone).toISOString() },
    { field: "started_at", op: "<" as const, value: parseRangeInput(`${shiftDay(data.punchcard.to, 1)}T${start}`, zone).toISOString() },
  ];
  const grouped = { ...query, grouping: { ...query.grouping!, source: { ...query.grouping!.source, where } } };
  const params = new URLSearchParams(current);
  params.set("analyze_query_settings", dashboardQuerySettingsSnapshot({ habits_settings: JSON.stringify(data.settings) })!);
  const address = usageWorkspaceURL({ mode: "query", search: true, dataset: "rhythm", topic: "habits", query: grouped }, params);
  const destination = new URLSearchParams(address.slice(address.indexOf("?") + 1));
  if (!destination.has("analyze_opened")) destination.set("analyze_opened", encodeQuery({ ...EMPTY_QUERY, where }));
  return { target: "section:habits-automations", title: "Automatic agent runs", note: `Turns started and active hours by schedule, script, orchestrator or agent, ${formatDay(data.punchcard.from)} to ${formatDay(data.punchcard.to)}.`,
    href: `/analyze?${destination}` };
}
