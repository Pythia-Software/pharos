import type { AggregationClause } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";

const calendar = "Each box compares complete local days of the same weekday, Monday through Sunday. The first partial day and today are excluded. Uses the device timezone captured in the query. Boxes show the middle 50%, median, Tukey whiskers and outliers; hover includes the mean and number of days. Values describe recorded activity; capture coverage may be incomplete.";
function weekday(id: string, title: string, description: string, unit: string, metric: AggregationClause, extra: Partial<QueryRecipe> = {}): QueryRecipe {
  const field = extra.field ?? "sent_at";
  return { id, title, description, dataset: "writing_messages", field, category: "human", idea: "rhythm-01", unit, days: [90], calendarTimezone: "local",
    where: [{ field: "typed_words", op: ">", value: "0" }],
    measures: [{ ...metric, groupBy: [field], groupLimit: 10000, sort: [{ key: "group0", dir: "asc" }], dateGrouping: { field, interval: "day", timeZone: "UTC", range: "observed", includeEmpty: true }, display: { kind: "bar-vertical", xLabel: "Local day", yLabel: unit } }],
    weekday: { metricId: metric.id, unit },
    cover: { mode: "source", metricId: metric.id, kind: "auto", maxGroups: 7, showHeadline: false, density: "compact" },
    action: "Inspect daily totals", note: calendar, ...extra };
}

const timing = "Follows the saved Parallelism counting and gap settings. Timing is estimated from transcript turns, with delegated work folded into its root unit. Daily placement uses hourly fact starts without prorating.";
export const weekdayRecipes: QueryRecipe[] = [
  weekday("human-words-weekday", "Words by day of the week", "Daily human-written word totals by weekday over the last {days} days, including idle days.", "words / day", { id: "weekday_words", op: "sum", field: "typed_words", groupBy: [], label: "Daily human-written words" },
    { note: `${calendar} Only typed or dictated words contribute; pasted and automated text are excluded. Days without recorded writing count as zero.` }),
  weekday("human-messages-weekday", "Messages by day of the week", "Daily counts of messages containing your words by weekday over the last {days} days, including idle days.", "messages / day", { id: "weekday_messages", op: "count", groupBy: [], label: "Daily messages with your words" },
    { note: `${calendar} Counts messages containing at least one typed or dictated word, matching Messages per day. Days without recorded writing count as zero.` }),
  weekday("parallel-active-weekday", "Active hours by day of the week", "Daily hours with any agent active by weekday over the last {days} days, including idle days.", "active hours / day", { id: "weekday_active", op: "sum", field: "elapsed_hours", groupBy: [], label: "Daily hours with any active" },
    { dataset: "parallelism", field: "started_at", category: "machines", idea: "rhythm-03", where: [], note: `${calendar} ${timing} Overlapping units count wall time once. Days without recorded activity count as zero.` }),
  weekday("parallel-weekday", "Parallelism by day of the week", "Daily time-weighted agents at once while active by weekday over the last {days} days; active days only.", "agents at once while active", { id: "weekday_parallelism", op: "sum", field: "unit_ms", expression: "SUM([unit_ms])/NULLIF(SUM([elapsed_ms]),0)", groupBy: [], label: "Daily agents at once while active" },
    { dataset: "parallelism", field: "started_at", category: "machines", idea: "rhythm-03", where: [], note: `${calendar} ${timing} Each daily observation is unit time ÷ wall time with any unit active. Idle days have no ratio and are excluded; each active day has equal weight in its weekday box.` }),
];
