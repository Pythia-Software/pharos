import { todayBounds } from "./day-window";
import type { ChartPeriod } from "./chart-period";
import type { WhereTerm } from "@pythia-software/query-table-core";
import {
  decodeQueryStrict,
  EMPTY_QUERY,
} from "@pythia-software/query-table-core";
import { timeBounds } from "./chart-range";
import { chartSeriesStyle } from "./chart-colors";

export const thinkingTopics = ["thinking-overview", "thinking-detail"] as const;
export const isThinkingTopic = (
  topic: string,
): topic is (typeof thinkingTopics)[number] =>
  thinkingTopics.some((value) => value === topic);
export const thinkingViews = [
  ["time", "Distributions over time"],
  ["heatmap", "Heatmap · compare groups"],
  ["histogram", "Histograms"],
  ["reasoning", "Reported reasoning · box plots"],
  ["wait-box", "Wait distributions · box plots"],
  ["calendar", "Time of day"],
] as const;
export const thinkingBreakdowns = [
  ["none", "None · all requests"],
  ["model", "Model"],
  ["model_effort", "Model + effort"],
  ["provider", "Agent family"],
  ["effort", "Effort"],
  ["harness", "Agent / harness"],
  ["repository", "Repository"],
  ["machine", "Machine"],
  ["preceding_tool", "Preceding tool"],
  ["agent_depth", "Agent depth"],
] as const;
export type ThinkingState = {
  view: (typeof thinkingViews)[number][0];
  groupBy: (typeof thinkingBreakdowns)[number][0];
  model: string;
  effort: string;
  normalization: "share" | "count";
  minimum: number;
  period: ChartPeriod;
  hours: number;
  days: number;
  range: "all" | "today" | "1d" | "7d" | "30d" | "90d" | "6m" | "1y" | "custom";
  from?: string;
  to?: string;
  calendarRows: "day" | "week" | "month";
  calendarMetric: "raw" | "ratio";
  hourA: number;
  hourB: number;
  sortBy: "median" | "p95" | "volume";
  sortDirection: "asc" | "desc";
  hiddenPercentiles: string[];
  session: string;
  traceScale: "linear" | "log";
  timezone: string;
};
export const defaultThinking: ThinkingState = {
  view: "time",
  groupBy: "none",
  model: "all",
  effort: "all",
  normalization: "share",
  minimum: 0,
  period: "day",
  hours: 6,
  days: 3,
  range: "all",
  calendarRows: "day",
  calendarMetric: "raw",
  hourA: 18,
  hourB: 9,
  sortBy: "median",
  sortDirection: "desc",
  hiddenPercentiles: [],
  session: "",
  traceScale: "linear",
  timezone: "",
};
export function validateThinking(
  value: unknown,
): value is Partial<ThinkingState> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const v = value as Record<string, unknown>,
    enums: Record<string, readonly string[]> = {
      view: thinkingViews.map((v) => v[0]),
      groupBy: thinkingBreakdowns.map((v) => v[0]),
      normalization: ["share", "count"],
      period: ["hour", "hours", "day", "days", "week", "month"],
      range: ["all", "today", "1d", "7d", "30d", "90d", "6m", "1y", "custom"],
      calendarRows: ["day", "week", "month"],
      calendarMetric: ["raw", "ratio"],
      sortBy: ["median", "p95", "volume"],
      sortDirection: ["asc", "desc"],
      traceScale: ["linear", "log"],
    };
  for (const [k, item] of Object.entries(v)) {
    if (!Object.hasOwn(defaultThinking, k) && !["from", "to"].includes(k))
      return false;
    if (enums[k] && !enums[k].includes(String(item))) return false;
    if (
      ["model", "effort", "session", "timezone"].includes(k) &&
      (typeof item !== "string" ||
        item.length > (k === "model" ? 300 : k === "effort" ? 100 : 500))
    )
      return false;
    if (
      ["from", "to"].includes(k) &&
      (typeof item !== "string" ||
        !/^\d{4}-\d{2}-\d{2}T/.test(item) ||
        !Number.isFinite(Date.parse(item)))
    )
      return false;
    if (
      ["minimum", "hours", "days", "hourA", "hourB"].includes(k) &&
      (!Number.isInteger(item) ||
        Number(item) < (["hours", "days"].includes(k) ? 1 : 0) ||
        Number(item) >
          (k === "minimum"
            ? 1000000
            : ["hourA", "hourB"].includes(k)
              ? 21
              : 10000) ||
        (["hourA", "hourB"].includes(k) && Number(item) % 3 !== 0))
    )
      return false;
    if (
      k === "hiddenPercentiles" &&
      (!Array.isArray(item) ||
        item.some((k) => !["median", "p75", "p90", "p95"].includes(k)))
    )
      return false;
  }
  if (v.from && v.to && Date.parse(String(v.from)) >= Date.parse(String(v.to)))
    return false;
  if (v.timezone)
    try {
      new Intl.DateTimeFormat("en", { timeZone: String(v.timezone) });
    } catch {
      return false;
    }
  return true;
}
export function initialThinking(params: URLSearchParams): ThinkingState {
  let state = {
    ...defaultThinking,
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
  };
  // Card entry uses the card's native query population and moving time intent.
  // Once mounted, the shared thinking view field owns every applied control.
  if (params.has("thinking")) {
    const v = JSON.parse(params.get("thinking")!);
    if (!validateThinking(v)) throw Error("Invalid Thinking view.");
    state = { ...state, ...v };
  }
  if (
    !params.has("thinking") ||
    !Object.hasOwn(JSON.parse(params.get("thinking")!), "range")
  ) {
    const token = params.get("analyze_filters"),
      decoded = token ? decodeQueryStrict(token) : EMPTY_QUERY;
    if (!decoded || decoded.grouping)
      throw Error("Invalid Thinking population filters.");
    const q = decoded;
    const bounds = timeBounds(q.where, {
      first: "requested_at",
      last: "requested_at",
    });
    const relative = q.where.find(
      (t) =>
        "field" in t &&
        t.field === "requested_at" &&
        t.op === ">=" &&
        /^-\d+d$/.test(t.value),
    );
    if (relative && "field" in relative) {
      const days = Number(relative.value.slice(1, -1));
      state = {
        ...state,
        range:
          (
            {
              1: "1d",
              7: "7d",
              30: "30d",
              90: "90d",
              180: "6m",
              365: "1y",
            } as Record<number, ThinkingState["range"]>
          )[days] ?? "custom",
        ...(![1, 7, 30, 90, 180, 365].includes(days)
          ? { from: bounds.from?.toISOString(), to: bounds.to?.toISOString() }
          : {}),
      };
    } else if (bounds.from || bounds.to)
      state = {
        ...state,
        range: "custom",
        from: bounds.from?.toISOString(),
        to: bounds.to?.toISOString(),
      };
    if (params.get("analyze_window") === "today") state.range = "today";
  }
  // Keep old links readable, but these retired controls must not leave hidden
  // sample filters or a pinned zone behind. Absolute date bounds stay intact.
  return {
    ...state,
    minimum: 0,
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
  };
}
export function thinkingRequest(
  state: ThinkingState,
  overview = false,
  evidence = false,
  filters = "",
): string {
  const q = new URLSearchParams({
    view: state.view,
    groupBy: state.groupBy,
    model: state.model,
    effort: state.effort,
    range: state.range,
    period: state.period,
    hours: String(state.hours),
    days: String(state.days),
    rows: state.calendarRows,
    metric: state.calendarMetric,
    sort: state.sortBy,
    direction: state.sortDirection,
    minimum: String(state.minimum),
    hourA: String(state.hourA),
    hourB: String(state.hourB),
    timezone:
      state.timezone ||
      Intl.DateTimeFormat().resolvedOptions().timeZone ||
      "UTC",
  });
  if (state.range === "custom") {
    if (state.from) q.set("from", state.from);
    if (state.to) q.set("to", state.to);
  }
  if (overview) {
    q.set("overview", "1");
    q.set("groupBy", "model");
    q.set("view", "histogram");
    q.set("minimum", "0");
  }
  if (evidence) q.set("evidence", "1");
  if (filters) q.set("filters", filters);
  return "/api/thinking?" + q;
}
export function thinkingWhere(
  state: ThinkingState,
  now = Date.now(),
): WhereTerm[] {
  const terms: WhereTerm[] = [];
  if (state.model !== "all")
    terms.push({ field: "model", op: "=", value: state.model });
  if (state.effort !== "all")
    terms.push({ field: "effort", op: "=", value: state.effort });
  if (state.range === "custom") {
    if (state.from)
      terms.push({ field: "requested_at", op: ">=", value: state.from });
    if (state.to)
      terms.push({ field: "requested_at", op: "<", value: state.to });
  } else if (state.range === "today") {
    const [from, to] = todayBounds(new Date(now), state.timezone || undefined);
    terms.push(
      { field: "requested_at", op: ">=", value: from },
      { field: "requested_at", op: "<", value: to },
    );
  } else if (state.range !== "all")
    terms.push(
      {
        field: "requested_at",
        op: ">=",
        value:
          "-" +
          { "1d": 1, "7d": 7, "30d": 30, "90d": 90, "6m": 180, "1y": 365 }[
            state.range
          ] +
          "d",
      },
      { field: "requested_at", op: "<", value: "+0s" },
    );
  return terms;
}
export const thinkingFixedOrder = (state: ThinkingState) =>
  state.groupBy === "effort" || state.groupBy === "model_effort";
export const thinkingStyle = (group: ThinkingGroup, by: string) =>
  by === "none"
    ? { color: "var(--sea)", order: "" }
    : chartSeriesStyle(
        (
          {
            model: "model_family",
            repository: "repository_name",
            machine: "machine_id",
          } as Record<string, string>
        )[by] ?? by,
        by === "none"
          ? "Thinking"
          : by === "model_effort"
            ? group.model +
              " · " +
              (group.effort === "Unreported" ? "Unknown effort" : group.effort)
            : group.label,
      );
export function orderThinkingGroups(
  groups: ThinkingGroup[],
  state: ThinkingState,
): ThinkingGroup[] {
  if (!thinkingFixedOrder(state)) return groups;
  return [...groups].sort((a, b) =>
    state.groupBy === "model_effort"
      ? String(chartSeriesStyle("model_family", a.model).order).localeCompare(
          String(chartSeriesStyle("model_family", b.model).order),
          undefined,
          { numeric: true },
        ) ||
        a.model.localeCompare(b.model) ||
        String(thinkingStyle(a, state.groupBy).order).localeCompare(
          String(thinkingStyle(b, state.groupBy).order),
          undefined,
          { numeric: true },
        )
      : String(thinkingStyle(a, state.groupBy).order).localeCompare(
          String(thinkingStyle(b, state.groupBy).order),
          undefined,
          { numeric: true },
        ),
  );
}
export type ThinkingStats = {
  n: number;
  median: number | null;
  p75: number | null;
  p90: number | null;
  p95: number | null;
  q1: number | null;
  q3: number | null;
  low: number | null;
  high: number | null;
};
export type ThinkingCell = {
  stats: ThinkingStats;
  retained: number;
  conversations: number;
  counts: number[];
};
export type ThinkingCalendarCell = {
  day: string;
  hour: number;
  Raw: number | null;
  Ratio: number | null;
  N: number;
  Adjusted: number;
  Conversations: number;
  AdjustedConversations: number;
};
export type ThinkingBand = {
  hour: number;
  days: number;
  n: number;
  Ratio: number | null;
  Low: number | null;
  High: number | null;
};
export type ThinkingGroup = {
  key: string;
  label: string;
  model: string;
  effort: string;
  Retained: number;
  Timed: number;
  Adjusted: number;
  Conversations: number;
  RetainedConversations: number;
  stats: ThinkingStats;
  counts: number[];
  periods: Record<string, ThinkingCell>;
  calendar: ThinkingCalendarCell[];
  bands: ThinkingBand[];
  comparison: {
    days: number;
    Ratio: number | null;
    Low: number | null;
    High: number | null;
  };
  cdf?: [number, number][];
};
export type ThinkingFactor = {
  model: string;
  factor: string;
  Ratio: number;
  Low: number;
  High: number;
  N: number;
  Conversations: number;
};
export type ThinkingCompaction = {
  ID: string;
  Model: string;
  Effort: string;
  Conversation: string;
  Gap: number;
  Context: number;
  Output: number;
  Before: number;
  After: number;
};
export type ThinkingTrace = {
  ID: string;
  Model: string;
  Conversation: string;
  Workspace: string;
  Count: number;
  Points: {
    ID: string;
    At: string;
    Rank: number;
    Context: number;
    Cache: number;
    Output: number;
    Gap: number | null;
    Smooth: number | null;
    Compaction: boolean;
  }[];
};
export type ThinkingValidation = {
  pairs: number;
  correlation: number | null;
  medianRatio: number | null;
  points: [number, number][];
  counters: [number, number][];
  counterCount: number;
  counterOverlap: number;
};
export type ThinkingResponse = {
  periodBounds: Record<string, { From: string; To: string }>;
  calendarBounds: Record<string, { From: string; To: string }>;
  Retained: number;
  Timed: number;
  Adjusted: number;
  Conversations: number;
  Hidden: number;
  Models: string[];
  Efforts: string[];
  edges: number[];
  groups: ThinkingGroup[];
  summary: ThinkingStats;
  periods: string[];
  calendarPeriods: string[];
  From: string;
  To: string;
  timezone: string;
  coverage?: ThinkingGroup[];
  agentCoverage?: ThinkingGroup[];
  factors?: ThinkingFactor[];
  compactions?: ThinkingCompaction[];
  traces?: ThinkingTrace[];
  validation?: ThinkingValidation;
};
export const thinkingPercentiles = [
  { key: "median", label: "Median", color: "var(--sea)", minimum: 30 },
  { key: "p75", label: "P75", color: "#c49b09", minimum: 50 },
  { key: "p90", label: "P90", color: "#dc7c23", minimum: 100 },
  { key: "p95", label: "P95", color: "#c23f33", minimum: 200 },
] as const;
export const thinkingDuration = (n: number | null) =>
  n === null
    ? "—"
    : n < 1
      ? Math.round(n * 1000) + " ms"
      : n.toLocaleString(undefined, { maximumFractionDigits: n < 10 ? 2 : 1 }) +
        " s";
export const thinkingCount = (n: number) => n.toLocaleString();
export const thinkingWindow = (h: number) =>
  String(h).padStart(2, "0") + ":00–" + String(h + 3).padStart(2, "0") + ":00";

export function thinkingBrush(
  data: ThinkingResponse,
  state: ThinkingState,
  a: number,
  b: number,
  calendar = false,
): Partial<ThinkingState> | null {
  if (!Number.isFinite(a) || !Number.isFinite(b) || a === b) return null;
  const keys = calendar ? data.calendarPeriods : data.periods,
    bounds = calendar ? data.calendarBounds : data.periodBounds;
  const start = Math.max(
      0,
      Math.min(keys.length - 1, Math.floor(Math.min(a, b))),
    ),
    end = Math.max(0, Math.min(keys.length - 1, Math.floor(Math.max(a, b))));
  const from = bounds[keys[start]]?.From,
    to = bounds[keys[end]]?.To;
  return from && to && Date.parse(from) < Date.parse(to)
    ? { range: "custom", from, to }
    : null;
}
