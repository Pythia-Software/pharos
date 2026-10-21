import type { QueryRecipe } from "./curiosity-data";

const tokens = { dataset: "usage", field: "first_usage_at", unit: "tokens", category: "usage", idea: "cost-01" } as const;
const daily = (id: string, field: string, split: string, label: string) => ({ id, op: "sum" as const, field, groupBy: ["day", split], groupLimit: 10000, label });
const cover = (metricId: string) => ({ mode: "source" as const, metricId, kind: "auto" as const, maxGroups: 15, showOther: true, showLegend: true, showHeadline: false, height: 300 });
const volume = "Recorded processing volume includes cached input, assumed, partial and unpriced usage. It is not unique text, effort or value. Dates use the retained usage fact’s placement; facts are not prorated.";

export const dashboardCardRecipes: QueryRecipe[] = [
  { ...tokens, id: "token-volume-trend", title: "Smoothed token volume", description: "Daily tokens by provider over the last {days} days, averaged over seven calendar days including idle days.", days: [30, 90], where: [{ field: "total_tokens", op: ">=", value: "0" }],
    measures: [daily("daily_tokens", "total_tokens", "provider", "Daily provider tokens")], cover: { ...cover("daily_tokens"), timeline: { rollingPeriods: 7 } }, action: "Explore smoothed token volume",
    note: `${volume} Six preceding days supply smoothing history without changing the selected-period totals.` },
  { ...tokens, id: "token-volume-log", title: "Token volume across scales", description: "Daily repository tokens over the last {days} days on a logarithmic scale, keeping smaller workloads visible.", days: [30, 90], where: [{ field: "total_tokens", op: ">=", value: "0" }],
    measures: [daily("daily_tokens_log", "total_tokens", "repository_name", "Daily repository tokens (log scale)")], cover: { ...cover("daily_tokens_log"), timeline: { scale: "logarithmic" } }, action: "Explore logarithmic token volume",
    note: `${volume} Log1p scaling keeps zero at zero; vertical distance does not represent an additive difference. Switch to linear to compare absolute amounts.` },
  { ...tokens, id: "cumulative-api-cost", title: "Accumulating API equivalent", description: "API-equivalent cost accumulated from the start of the last {days} days, split by provider; fully priced facts only.", category: "cost", unit: "USD", days: [30, 90],
    where: [{ field: "total_tokens", op: ">", value: "0" }, { field: "price_status", op: "=", value: "priced" }, { field: "cost_usd", op: ">=", value: "0" }],
    measures: [daily("daily_cost", "cost_usd", "provider", "Cumulative API equivalent (USD)")], cover: { ...cover("daily_cost"), timeline: { display: "cumulative" } }, action: "Explore cumulative API cost",
    note: "Each point adds recorded daily costs from the range start. Zero-priced facts remain included; missing, assumed and partial prices are excluded. API equivalent is not subscription spend or invoices. Grouped rows retain interval costs." },
  { id: "mcp-call-trend", title: "Pharos request activity", description: "Daily requests served by Pharos over the last {days} days, split by tool and averaged over seven days.", dataset: "mcp_calls", field: "called_at", category: "tools", idea: "skills-06", unit: "calls", days: [30, 90],
    measures: [daily("daily_mcp_calls", "call_count", "tool_name", "Daily Pharos requests")], cover: { ...cover("daily_mcp_calls"), maxGroups: 6, showOther: false, timeline: { rollingPeriods: 7 } }, action: "Explore Pharos request trends",
    note: "Includes successful and failed server requests. Tools are selected by request volume over the chart’s requested history. The server retains at most 5,000 requests, so long windows may be incomplete. These are Pharos server calls, not all captured external MCP activity." },
];
