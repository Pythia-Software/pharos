import type { AxisBins } from "./dashboard-analysis-model";

// Session shape's ordered values (docs/session-shape.md#bands): each band and
// the archetypes in their order, labelled as the schema labels them. The
// analysis catalog reads them, and chart-colors gives each step its colour.

export const bandBreakdowns = {
  length_band: { label: "Length", field: "messages", order: ["1-5", "6-15", "16-30", "31-60", "61-120", "121+"], values: { "1-5": "1–5 messages", "6-15": "6–15 messages", "16-30": "16–30 messages", "31-60": "31–60 messages", "61-120": "61–120 messages", "121+": "121+ messages" } },
  active_band: { label: "Active time", field: "active_seconds", order: ["<5m", "5-15m", "15-30m", "30-60m", "1-2h", "2h+"], values: { "<5m": "Under 5 min", "5-15m": "5–15 min", "15-30m": "15–30 min", "30-60m": "30–60 min", "1-2h": "1–2 h", "2h+": "2 h or more" } },
  tool_band: { label: "Tool calls", field: "tool_calls", order: ["0", "1-5", "6-20", "21-50", "51-100", "101+"], values: { "0": "No tool calls", "1-5": "1–5 calls", "6-20": "6–20 calls", "21-50": "21–50 calls", "51-100": "51–100 calls", "101+": "101+ calls" } },
  autonomy_band: { label: "Calls per typed message", field: "autonomy", order: ["<0.5", "0.5-1", "1-2", "2-5", "5-10", "10+"], values: { "<0.5": "Under 0.5", "0.5-1": "0.5–1", "1-2": "1–2", "2-5": "2–5", "5-10": "5–10", "10+": "10 or more" } },
  turn_cycle_band: { label: "Turn cycle", field: "turn_cycle_p50_seconds", order: ["<10s", "10-30s", "30-60s", "1-3m", "3-10m", "10m+"], values: { "<10s": "Under 10 s", "10-30s": "10–30 s", "30-60s": "30–60 s", "1-3m": "1–3 min", "3-10m": "3–10 min", "10m+": "10 min or more" } },
  context_band: { label: "Peak context", field: "context_peak_ratio", order: ["<10%", "10-25%", "25-50%", "50-75%", "75-90%", "90%+"], values: { "<10%": "Under 10%", "10-25%": "10–25%", "25-50%": "25–50%", "50-75%": "50–75%", "75-90%": "75–90%", "90%+": "90% or more" } },
} as const;
export type BandBreakdown = keyof typeof bandBreakdowns;

// Each band's value, finely binned (internal/archive/session_shape.go), so a
// heatmap draws it on a continuous axis with the band's edges as guides.
// edges are each band's lower edge, in the band's order.
const count = { kind: "number", decimals: 0 } as const, seconds = { kind: "duration", sourceUnit: "seconds" } as const;
export const bandBins: Record<BandBreakdown | "archetype", AxisBins> = {
  length_band: { field: "messages_bin", scheme: { scale: "count" }, label: "Messages per session", format: count, edges: [1, 6, 16, 31, 61, 121] },
  active_band: { field: "active_seconds_bin", scheme: { scale: "log" }, label: "Active time per session", format: seconds, edges: [0, 300, 900, 1800, 3600, 7200] },
  tool_band: { field: "tool_calls_bin", scheme: { scale: "count" }, label: "Tool calls per session", format: count, edges: [0, 1, 6, 21, 51, 101] },
  autonomy_band: { field: "autonomy_bin", scheme: { scale: "log" }, label: "Tool calls per typed message", format: { kind: "number" }, edges: [0, 0.5, 1, 2, 5, 10] },
  turn_cycle_band: { field: "turn_cycle_bin", scheme: { scale: "log" }, label: "Turn cycle (session median)", format: seconds, edges: [0, 10, 30, 60, 180, 600] },
  context_band: { field: "context_peak_bin", scheme: { scale: "linear", per: 40 }, label: "Peak context share", format: { kind: "percent" }, edges: [0, 0.1, 0.25, 0.5, 0.75, 0.9] },
  archetype: { field: "typed_messages_bin", scheme: { scale: "count" }, label: "Typed messages per session", format: count, edges: [0, 1, 6, 16, 51] },
};
export const archetypes = { order: ["automated", "quick", "standard", "deep", "marathon"], values: { automated: "Automated", quick: "Quick", standard: "Standard", deep: "Deep", marathon: "Marathon" } } as const;

// One step per value, lightest for the least and darkest for the most. Bands
// share a blue ramp; the archetypes put Automated in grey (no one typed in
// it), then a purple ramp from Quick to Marathon. Neither ramp is a verdict.
const bandRamp = ["#bfe0f2", "#8fc6e8", "#5fa6d8", "#3d86c4", "#2a67a6", "#1e4d86"];
const archetypeRamp = ["#9aa3ab", "#d4c2f0", "#a986de", "#7d58c2", "#583595"];
export const orderedValues: Record<string, { order: readonly string[]; labels: Record<string, string>; colors: readonly string[] }> = {
  ...Object.fromEntries(Object.entries(bandBreakdowns).map(([field, band]) => [field, { order: band.order, labels: band.values as Record<string, string>, colors: bandRamp }])),
  archetype: { order: archetypes.order, labels: archetypes.values, colors: archetypeRamp },
};

// The bins by the field that holds them, for card covers grouped by one.
export const binnedFields: Record<string, AxisBins> = Object.fromEntries(Object.values(bandBins).map(bins => [bins.field, bins]));
