import { decodeQueryStrict, EMPTY_QUERY, type AggregationResultEntry, type WhereTerm } from "@pythia-software/query-table-core";
import { cardLoadingPresentation, fittedCoverHeight, type CardLoadingSpec } from "./card-loading-model";
import { foldSeries, type ChartBucket, type ChartSeries } from "./chart-series";
import type { QueryRecipe } from "./curiosity-data";
import type { QueryCategory } from "./query-categories";
import { thinkingCardRecipes } from "./thinking-card-recipes";
import { initialThinking, isThinkingTopic, orderThinkingGroups, thinkingFixedOrder, type ThinkingGroup, thinkingBreakdowns, thinkingCount, thinkingDuration, thinkingWhere, type ThinkingResponse, type ThinkingState } from "./thinking-model";
import { gridTicks, type GridCover } from "./grid-cover";
import { periodLabel as chartPeriodLabel } from "./chart-period";

// Waiting's and Thinking's charts as Home cards. The page's
// address (its thinking view and analyze_filters population) is the card
// definition, as for any dashboard card. Thinking's grids (time of day, the
// comparison heatmap, distributions over time) draw from the page's own
// /api/thinking response; its other charts from the Model Requests records
// with the matching Thinking card's measures.

export const thinkingCardTargets = ["thinking-waits", "thinking-compactions", "thinking-distributions"] as const;
export type ThinkingCardTarget = typeof thinkingCardTargets[number];
export const isThinkingCardTarget = (target: string): target is ThinkingCardTarget => (thinkingCardTargets as readonly string[]).includes(target);

// Waiting offers its wait distribution; legacy compaction cards remain readable.
export const thinkingTopicTargets = (topic: string): ThinkingCardTarget[] => topic === "thinking-overview" ? ["thinking-waits"] : ["thinking-distributions"];

export function isThinkingCardAddress(address: string): boolean {
  return isThinkingTopic(new URL(address, location.origin).searchParams.get("analyze_topic") ?? "");
}

// The population the page draws: its query filters and its controls.
export function readThinkingCard(address: string, now = new Date()): { topic: "thinking-overview" | "thinking-detail"; state: ThinkingState; where: WhereTerm[]; native: WhereTerm[]; filters: string } {
  const url = new URL(address, location.origin), params = url.searchParams, topic = params.get("analyze_topic") ?? "";
  if (url.origin !== location.origin || url.pathname !== "/analyze" || params.get("analyze_mode") !== "dashboards" || !isThinkingTopic(topic)) throw new Error("This dashboard card is unavailable.");
  const filters = params.get("analyze_filters"), native = filters ? decodeQueryStrict(filters) : EMPTY_QUERY;
  if (!native || native.grouping) throw new Error("This card's Thinking filters are invalid.");
  let state: ThinkingState;
  try { state = initialThinking(params); } catch { throw new Error("This card's Thinking view is invalid."); }
  return { topic, state, where: [...native.where, ...thinkingWhere(state, now.getTime())], native: native.where, filters: filters ?? "" };
}

const recipe = (id: string) => thinkingCardRecipes.find(item => item.id === id)!;
const breakdownField: Record<ThinkingState["groupBy"], string | undefined> = {
  none: undefined, model: "model", model_effort: "model_effort", speed: "speed", model_speed: "model_speed", model_effort_speed: "model_effort_speed", provider: "provider", effort: "effort", harness: "harness",
  project: "project_name", machine: "machine", preceding_tool: "preceding_tool", agent_depth: "agent_depth",
};
const breakdownLabel = (state: ThinkingState) => thinkingBreakdowns.find(([id]) => id === state.groupBy)![1].toLowerCase();
// The model and effort the page's controls narrowed to, which every chart shares.
const narrowed = (state: ThinkingState) => [state.model !== "all" ? state.model : "", state.effort !== "all" ? `${state.effort} effort` : ""].filter(Boolean).join(" · ");

// The Thinking card whose measures draw this chart, regrouped as the page is.
// Boxes share a log axis, as the page's do: waits span milliseconds to minutes.
export function thinkingCardRecipe(target: ThinkingCardTarget, state: ThinkingState): QueryRecipe {
  const card = thinkingCardMeasures(target, state);
  return { ...card, measures: card.measures.map(measure => measure.distribution?.kind === "box" ? { ...measure, display: { ...measure.display, kind: "box", xScale: { mode: "log" }, xLabel: card.unit } } : measure) };
}
function thinkingCardMeasures(target: ThinkingCardTarget, state: ThinkingState): QueryRecipe {
  // Waiting defaults to model; its card keeps the selected curve grouping.
  if (target === "thinking-waits") return waitBoxes([breakdownField[state.groupBy] ?? "model"], `Wait by ${state.groupBy === "none" ? "model" : breakdownLabel(state)}`);
  if (target === "thinking-compactions") return recipe("thinking-compactions");
  // Exact quantiles of a whole library exceed the service's budget, so
  // without a breakdown a card draws a box per model.
  const field = breakdownField[state.groupBy] ?? "model";
  if (state.view === "reasoning") {
    const base = recipe("thinking-reasoning-effort");
    return { ...base, cover: { mode: "source", metricId: "reasoning_efforts", kind: "box", maxGroups: 8, density: "compact" }, measures: base.measures.map(measure => ({ ...measure, groupBy: [field], groupLimit: 10000 })) };
  }
  if (field === "preceding_tool") return bySamples(recipe("thinking-preceding-tool"), state);
  // A histogram of every wait puts nearly all of them in its first bin; a box
  // on a log axis shows the typical wait and the slow tail.
  return bySamples(waitBoxes([field], `Wait by ${state.groupBy === "none" ? "model" : breakdownLabel(state)}`), state);
}

// A card shows the page's first groups in the page's order. Volume is each
// box's timed waits; any other order comes from the page's own response.
const bySamples = (card: QueryRecipe, state: ThinkingState): QueryRecipe => thinkingPageOrder("thinking-distributions", state) || state.groupBy === "none" ? card
  : { ...card, measures: card.measures.map(measure => ({ ...measure, sort: [{ key: "samples", dir: state.sortDirection }] })) };
export const thinkingPageOrder = (target: ThinkingCardTarget, state: ThinkingState) => target === "thinking-distributions" && !thinkingGridView(target, state) && state.groupBy !== "none"
  && (thinkingFixedOrder(state) || state.view === "reasoning" || state.sortBy !== "volume");

// Boxes in the order of the page's groups; any the page does not list follow.
export function orderedBoxes(result: AggregationResultEntry, groups: ThinkingGroup[]): AggregationResultEntry {
  const rank = new Map(groups.flatMap((group, i) => [[group.label, i], [group.key, i]] as [string, number][]));
  const at = (keys: unknown[]) => rank.get(String(keys[0] ?? "")) ?? groups.length;
  return { ...result, buckets: result.buckets.map((bucket, i) => ({ bucket, i })).sort((a, b) => at(a.bucket.keys) - at(b.bucket.keys) || a.i - b.i).map(({ bucket }) => bucket) };
}

function waitBoxes(groupBy: string[], label: string): QueryRecipe {
  const base = recipe("thinking-model-effort");
  return { ...base, cover: { mode: "source", metricId: "wait_efforts", kind: "box", maxGroups: 8, density: "compact" }, measures: base.measures.map(measure => ({ ...measure, groupBy, groupLimit: 10000, label })),
    note: "Individual post-tool gaps. Compact boxes use Tukey whiskers; Thinking uses p10–p90 whiskers and shows every group with its support counts." };
}

// Compactions draw as the page's compaction days do: a column per day,
// stacked by model, every day from the first to the last.
export function compactionColumns(result: AggregationResultEntry, limit = 6): { buckets: ChartBucket[]; series: ChartSeries[] } {
  // A compaction without a recorded day has no column to stand in.
  const rows = result.buckets.filter(bucket => typeof bucket.value === "number" && bucket.value > 0 && /^\d{4}-\d{2}-\d{2}$/.test(String(bucket.keys[0]))).map(bucket => [String(bucket.keys[0]), String(bucket.keys[1] ?? "Unspecified"), bucket.value as number] as [string, string, number]);
  const days = [...new Set(rows.map(([day]) => day))].sort(), buckets: ChartBucket[] = [];
  if (days.length) for (const date = new Date(`${days[0]}T12:00:00`), last = new Date(`${days.at(-1)}T12:00:00`); date <= last; date.setDate(date.getDate() + 1)) {
    const key = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
    buckets.push({ key, date: new Date(date), values: {} });
  }
  return { buckets, series: foldSeries(rows, new Map(buckets.map(bucket => [bucket.key, bucket])), name => name, undefined, [], limit, "model_family") };
}

export function thinkingCardTitle(target: ThinkingCardTarget, state: ThinkingState): string {
  let title: string;
  const by = state.groupBy === "none" ? "" : ` by ${breakdownLabel(state)}`;
  if (target === "thinking-waits") title = state.groupBy === "none" || state.groupBy === "model" ? "Model wait distribution" : `Wait by ${breakdownLabel(state)}`;
  else if (thinkingGridView(target, state)) title = state.view === "heatmap" ? `Wait heatmap${by}` : state.view === "time" ? `Wait distributions over time${by}`
    : state.calendarMetric === "ratio" ? "Wait against workload by time of day" : "Wait by time of day";
  else if (target === "thinking-compactions") title = "Context compactions";
  else if (state.view === "reasoning") title = state.groupBy === "none" ? "Reported reasoning by model" : `Reported reasoning by ${breakdownLabel(state)}`;
  else title = state.groupBy === "none" ? "Model wait distribution" : `Wait by ${breakdownLabel(state)}`;
  const only = narrowed(state);
  return only ? `${title}: ${only}` : title;
}

export function thinkingCardDescription(target: ThinkingCardTarget, state: ThinkingState): string {
  const sorted = state.groupBy !== "none" && ["histogram", "wait-box", "reasoning", "heatmap"].includes(state.view);
  const controls = target !== "thinking-distributions" ? [] : [
    sorted ? thinkingFixedOrder(state) ? "Effort ordered low to high, with Unreported last; models follow the shared model order"
      : `Groups ordered by ${state.sortBy === "volume" ? state.view === "reasoning" ? "report count" : "request count" : `${state.sortBy} ${state.view === "reasoning" ? "reported reasoning" : "wait"}`}, ${state.sortDirection === "asc" ? "ascending" : "descending"}` : "",
    state.view === "time" ? `Periods: ${chartPeriodLabel(state.period, state.period === "hours" ? state.hours : state.period === "days" ? state.days : 1)}` : "",
    state.view === "calendar" ? `Rows: ${state.calendarRows}; hours and dates in ${state.timezone}` : "",
    state.view === "histogram" || state.view === "wait-box" ? "Card shows wait distributions as box plots" : "",
  ];
  const range = state.range === "custom" ? `Range: ${state.from ?? "start of history"} to ${state.to ?? "now"}.` : "";
  return [range, ...controls, thinkingCardReading(target, state)].filter(Boolean).join(" · ");
}

function thinkingCardReading(target: ThinkingCardTarget, state: ThinkingState): string {
  if (thinkingGridView(target, state)) return state.view === "heatmap" ? `Each row is a group and each column a range of waits, shaded by ${state.normalization === "share" ? "its share of the group's waits" : "its requests"}.`
    : state.view === "time" ? `Each column is a time period and each row a range of waits, shaded by ${state.normalization === "share" ? "its share of that period's waits" : "its requests"}.`
    : state.calendarMetric === "ratio" ? "The median wait in each hour of each day against its workload estimate: warmer is slower than the workload suggests."
    : "The median wait in each hour of each day: darker is longer.";
  if (target === "thinking-compactions") return "Requests that follow a context compaction, day by day by model.";
  if (target === "thinking-distributions" && state.view === "reasoning") return "Positive reasoning reports, including requests without timing; zero cannot distinguish no reasoning from unreported reasoning.";
  if ((target === "thinking-distributions" || target === "thinking-waits") && state.groupBy !== "none") return `Observed post-tool waits by ${breakdownLabel(state)}. Missing timing stays missing.`;
  return "Post-tool request gaps: a coarse proxy for how long an agent waits before its next step. Missing timing stays missing.";
}

const rangeLabels: Record<ThinkingState["range"], string> = { all: "All time", today: "Today", "1d": "Past day", "7d": "Past 7 days", "30d": "Past 30 days", "90d": "Past 90 days", "6m": "Past 6 months", "1y": "Past year", custom: "Saved range" };
export const thinkingCardWindow = (state: ThinkingState): string => rangeLabels[state.range];

export function thinkingCardPresentation(target: ThinkingCardTarget, state: ThinkingState): { category: QueryCategory; loading: CardLoadingSpec } {
  if (thinkingGridView(target, state)) return { category: "thinking", loading: { kind: "columns", count: 24, height: 200 } };
  if (target === "thinking-compactions") return { category: "thinking", loading: { kind: "columns", count: 30, height: fittedCoverHeight("columns", 30) } };
  const card = thinkingCardRecipe(target, state);
  return { category: "thinking", loading: cardLoadingPresentation({ ...EMPTY_QUERY, where: card.where ?? [], aggregations: card.measures }, { recipe: card }) };
}

// Thinking's grid views draw as grids on their cards too.
export const thinkingGridView = (target: ThinkingCardTarget, state: ThinkingState) => target === "thinking-distributions" && ["calendar", "heatmap", "time"].includes(state.view);

// At most this many days, weeks or months: a card shows the most recent.
export const gridPeriods = 60;
const gridGroups = 12;

// The page's grid at card size, from the response the page draws. A view of
// one grid per group shows the busiest group, whose grid is the fullest.
// Wait ranges no request fell in are left off either end.
export function thinkingGrid(data: ThinkingResponse, state: ThinkingState): GridCover | undefined {
  const groups = orderThinkingGroups(data.groups, state);
  if (!groups.length) return;
  const first = groups.reduce((busiest, group) => group.stats.n > busiest.stats.n ? group : busiest);
  const bins = data.edges.slice(0, -1).map((edge, i) => `${thinkingDuration(edge)}–${thinkingDuration(data.edges[i + 1])}`);
  // Bins are rounded steps of a log scale; label the decades.
  const decades = data.edges.slice(0, -1).flatMap((edge, i) => Math.abs(Math.log10(edge) - Math.round(Math.log10(edge))) < 1e-6 ? [i] : []);
  const share = state.normalization === "share";
  const notes: string[] = [];
  if (groups.length > 1 && state.view !== "heatmap") notes.push(`${first.label}, the busiest of ${groups.length} groups`);
  if (state.view === "heatmap") {
    const shown = groups.slice(0, gridGroups);
    if (groups.length > shown.length) notes.push(`${shown.length} of ${groups.length} groups`);
    const used = usedBins(shown.map(group => group.counts), bins.length);
    const { cells, top } = contrast(shown.map(group => used.map(i => group.stats.n ? share ? group.counts[i] / group.stats.n : group.counts[i] : null)));
    return { rows: shown.map(group => group.label), columns: used.map(i => bins[i].split("–")[0]), tone: "sequential", rowTicks: shown.map((_, i) => i), columnTicks: used.flatMap((bin, i) => decades.includes(bin) ? [i] : []), cells,
      values: shown.map(group => used.map(i => `${bins[i]}: ${thinkingCount(group.counts[i])} of ${thinkingCount(group.stats.n)} (${((100 * group.counts[i]) / Math.max(1, group.stats.n)).toFixed(1)}%)`)),
      legend: share ? ["0%", `${percent(top)} of a group`] : ["0", `${thinkingCount(Math.round(top))}+ requests`], caption: notes.join(" · ") || undefined };
  }
  if (state.view === "time") {
    const periods = data.periods.slice(-gridPeriods), order = usedBins(periods.map(period => first.periods[period]?.counts ?? []), bins.length).reverse();
    if (data.periods.length > periods.length) notes.push(`last ${periods.length} of ${data.periods.length} periods`);
    const { cells, top } = contrast(order.map(bin => periods.map(period => { const cell = first.periods[period]; return cell ? share ? cell.counts[bin] / Math.max(1, cell.stats.n) : cell.counts[bin] : null; })));
    return { rows: order.map(i => thinkingDuration(data.edges[i])), columns: periods.map(periodLabel), tone: "sequential",
      rowTicks: order.flatMap((bin, row) => decades.includes(bin) ? [row] : []), columnTicks: gridTicks(periods.length, 4), cells,
      values: order.map(bin => periods.map(period => { const cell = first.periods[period]; return cell ? `${bins[bin]}: ${thinkingCount(cell.counts[bin])} of ${thinkingCount(cell.stats.n)} waits` : null; })),
      legend: share ? ["0%", `${percent(top)} of a period`] : ["0", `${thinkingCount(Math.round(top))}+ requests`], caption: notes.join(" · ") || undefined };
  }
  const periods = data.calendarPeriods.slice(-gridPeriods), hours = Array.from({ length: 24 }, (_, hour) => hour);
  if (data.calendarPeriods.length > periods.length) notes.push(`last ${periods.length} of ${data.calendarPeriods.length} ${state.calendarRows}s`);
  const cells = new Map(first.calendar.map(cell => [`${cell.day}|${cell.hour}`, cell])), ratio = state.calendarMetric === "ratio";
  const raw = contrast(periods.map(period => hours.map(hour => cells.get(`${period}|${hour}`)?.Raw ?? null)));
  return { rows: periods.map(periodLabel), columns: hours.map(hour => String(hour).padStart(2, "0")), tone: ratio ? "diverging" : "sequential",
    rowTicks: gridTicks(periods.length, 5), columnTicks: [0, 6, 12, 18],
    cells: ratio ? periods.map(period => hours.map(hour => { const value = cells.get(`${period}|${hour}`)?.Ratio;
      return value == null ? null : Math.sign(value - 1) * Math.min(1, Math.abs(value - 1) * 1.8); })) : raw.cells,
    values: periods.map(period => hours.map(hour => { const cell = cells.get(`${period}|${hour}`), value = ratio ? cell?.Ratio : cell?.Raw;
      return value == null ? null : `${ratio ? value.toFixed(2) + "×" : thinkingDuration(value)} · ${thinkingCount((ratio ? cell?.Adjusted : cell?.N) ?? 0)} requests · ${data.timezone}`; })),
    legend: ratio ? ["≤0.5×", "≥1.5×"] : ["0 s", `${thinkingDuration(raw.top)}+`], caption: notes.join(" · ") || undefined };
}
// The bins from the first to the last that any count fell in.
function usedBins(counts: number[][], length: number): number[] {
  const filled = Array.from({ length }, (_, i) => i).filter(i => counts.some(list => (list[i] ?? 0) > 0));
  if (!filled.length) return Array.from({ length }, (_, i) => i);
  return Array.from({ length: filled.at(-1)! - filled[0] + 1 }, (_, i) => filled[0] + i);
}

// A card is too small for a few extreme cells to set the scale: shade up to
// the 95th percentile of the drawn values, which the legend names.
function contrast(values: (number | null)[][]): { cells: (number | null)[][]; top: number } {
  const drawn = values.flat().filter((value): value is number => value !== null && value > 0).sort((a, b) => a - b);
  const top = drawn.length ? drawn[Math.min(drawn.length - 1, Math.floor(drawn.length * 0.95))] : 1;
  return { cells: values.map(row => row.map(value => value === null ? null : Math.min(1, value / top))), top };
}
const percent = (share: number) => `${Math.round(share * 100)}%+`;
const periodLabel = (period: string) => period.includes("T") ? new Date(period).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric" }) : period.length === 7 ? period : period.slice(5);
