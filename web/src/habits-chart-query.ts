import { EMPTY_QUERY, type WhereTerm } from "@pythia-software/query-table-core";
import { parseRangeInput } from "./chart-range";
import { shiftDay, type HabitsSummary, type PunchcardActor } from "./habits-model";
import { orderedUsageQuery } from "./usage-query-order";
import { usageWorkspaceURL } from "./usage-workspace-model";
import { withSlice } from "./chart-selection";

export type HabitsChartSelection = ({ day: string } | { weekday: number; hour: number }) & { automation?: { initiator: string; label: string } };

// These panels have their own ranges and populations, independent of the
// catalog chart's filters. Rhythm rows are local clock hours; a habit day can
// continue past midnight, while punchcard weekdays always follow the clock.
export function habitsChartQuery(data: HabitsSummary, selection: HabitsChartSelection, actors: PunchcardActor[]) {
  let where: WhereTerm[] = withSlice([], "actor", { values: actors, excluded: false });
  if (selection.automation) where.push({ field: "initiator", op: "=", value: selection.automation.initiator },
    { field: "automation", op: "=", value: selection.automation.label });
  if ("day" in selection) {
    const zone = data.timezone === "Local" ? undefined : data.timezone;
    const start = String(data.settings.dayStart).padStart(2, "0") + ":00";
    where.push(
      { field: "started_at", op: ">=", value: parseRangeInput(`${selection.day}T${start}`, zone).toISOString() },
      { field: "started_at", op: "<", value: parseRangeInput(`${shiftDay(selection.day, 1)}T${start}`, zone).toISOString() },
    );
  } else {
    const offset = selection.hour < data.settings.dayStart ? 1 : 0;
    const from = shiftDay(data.punchcard.from, offset), to = shiftDay(data.punchcard.to, offset);
    const dates: string[] = [];
    for (let day = from; day <= to; day = shiftDay(day, 1)) {
      if ((new Date(`${day}T00:00:00Z`).getUTCDay() + 6) % 7 === selection.weekday) dates.push(day);
    }
    where = withSlice(where, "day", { values: dates.length ? dates : [""], excluded: false });
    where.push({ field: "hour_of_day", op: "=", value: String(selection.hour).padStart(2, "0") });
  }
  return orderedUsageQuery("rhythm", { ...EMPTY_QUERY, where });
}

export function habitsChartURL(data: HabitsSummary, selection: HabitsChartSelection, actors: PunchcardActor[], current: URLSearchParams) {
  return usageWorkspaceURL({ mode: "query", dataset: "rhythm", topic: "habits", query: habitsChartQuery(data, selection, actors) }, current);
}
