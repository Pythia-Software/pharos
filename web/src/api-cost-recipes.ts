import type { SummaryColumn, WhereTerm } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";

const nonempty = (field: string): WhereTerm[] => [{ field, op: "is_not_null", value: "" }, { field, op: "!=", value: "" }];
const positive: WhereTerm[] = [{ field: "total_tokens", op: ">", value: "0" }];
const priced: WhereTerm[] = [...positive, { field: "price_status", op: "=", value: "priced" }, { field: "cost_usd", op: ">=", value: "0" }];
const workWhere = [...priced, ...nonempty("root_conversation_id")];
const cost: SummaryColumn = { id: "ac_cost", op: "sum", field: "cost_usd", label: "API equivalent (USD)" };
const tokens: SummaryColumn = { id: "ac_tokens", op: "sum", field: "total_tokens", label: "Recorded tokens" };
const work: SummaryColumn = { id: "ac_work", op: "count_distinct", field: "root_conversation_id", label: "Top-level conversations" };
const title: SummaryColumn = { id: "ac_title", op: "row", field: "root_title", label: "Top-level conversation" };
const latest = { orderBy: [{ field: "last_usage_at", dir: "desc" as const, nulls: "last" as const }, { field: "id", dir: "asc" as const, nulls: "last" as const }] };
const pricedNote = "API equivalent uses retained price schedules, not subscription charges, invoices, human effort, or measured value. Period cost cards include positive-token facts with price_status=priced and nonnegative known cost; assumed, partial, and unpriced facts are excluded. Costs follow usage-record time placement, which can be request time or session-start placement. Zero-priced facts remain valid observations. Missing cost is unknown, not zero. Project attribution follows each fact’s recorded project, including delegated facts where they were recorded.";
const workNote = "One conversation observation is a resolved top-level conversation including all recorded delegated work, linked launches, and continuations. Child facts stay in their top-level conversation even across workspaces or projects, attributed to the top-level project. Multiple independent top-level conversations in one workspace remain separate. Sum fully priced usage within the selected period, including children of roots with no activity of their own in the period; older usage is excluded. Missing or cyclic parent links is excluded from these conversation comparisons. Missing top-level projects remain Unspecified.";
function costCard(id: string, title: string, description: string, keys: string[], summaries: SummaryColumn[], action: string, extra: Partial<QueryRecipe> = {}): QueryRecipe {
  return { id, title, description, category: "cost", idea: "cost-02", dataset: "usage", field: "first_usage_at", days: [7, 30], unit: "USD", where: priced, measures: [], action, note: pricedNote,
    exploration: { keys, summaries, valueField: summaries[0].id, unit: "USD", subjects: "cost groups", action, labelFields: keys },
    cover: { mode: "result", metricId: summaries[0].id, kind: "bar-horizontal", maxGroups: 6, showOther: false }, ...extra };
}
const workKeys = ["root_project_name", "root_conversation_id"];
const workSummaries = [cost, tokens, title];

export const apiCostRecipes: QueryRecipe[] = [
  {
    id: "provider-cost-share", title: "Provider cost share", description: "Provider share of total API-equivalent cost over the last {days} days, smoothed over 7 days.", category: "cost", idea: "cost-01", dataset: "usage", field: "first_usage_at", days: [90], unit: "USD", scope: "provider",
    where: [{ field: "cost_usd", op: ">=", value: "0" }],
    measures: [{ id: "provider-daily-cost", op: "sum", field: "cost_usd", groupBy: ["day", "provider"], groupLimit: 10000, label: "Daily provider API equivalent", display: { kind: "auto", yLabel: "Cost share (%) · 7d average" } }],
    cover: { mode: "result", metricId: "card_cost_usd", kind: "auto", maxGroups: 15, showOther: true, showLegend: true, showHeadline: false, height: 300, timeline: { rollingPeriods: 7, percent: true } },
    action: "Explore daily provider costs",
    note: "Matches Machine Tokens cost share: include all known nonnegative API-equivalent costs, including assumed and partial pricing. Missing costs are excluded. Sum by recorded local day and provider, then average costs over each trailing seven calendar buckets, including zero-activity days, before dividing each provider by the same window’s total cost. The first visible bucket includes the preceding six calendar days; earlier history is used only for smoothing, while totals and destination filters keep the selected window. A zero-cost window has no share. Provider attribution follows each usage record, including delegated usage. Missing providers remain Unspecified; exact Other preserves the denominator. API equivalent is not subscription spend or invoices.",
  },
  costCard("api-cost-total", "API equivalent this period", "Total recorded API equivalent over the last {days} days, using fully priced positive-token usage records only.", ["project_name"], [cost, tokens], "Inspect priced cost totals", {
    idea: "cost-01", exploration: { keys: ["project_name"], summaries: [cost, tokens], valueField: cost.id, unit: "USD", subjects: "projects", action: "Inspect priced cost totals", labelFields: ["project_name"],
      coverMetric: { id: cost.id, op: "sum", field: cost.id, groupBy: [], label: "API equivalent (USD)" } },
    cover: { mode: "result", metricId: cost.id, kind: "auto", height: 260 },
  }),
  costCard("repository-cost-share", "Project cost share", "Fully priced API equivalent over the last {days} days, attributed to each usage record’s recorded project.", ["project_name"], [cost, tokens, work], "Explore project cost shares", {
    idea: "cost-01", note: `${pricedNote} Five leading projects plus exact Other partition all matching priced cost. Missing project labels stay Unspecified. Zero-cost groups contribute no pie area and remain in the destination.`,
    exploration: { keys: ["project_name"], summaries: [cost, tokens, work], valueField: cost.id, unit: "USD", subjects: "projects", action: "Explore project cost shares", labelFields: ["project_name"],
      coverMetric: { id: cost.id, op: "sum", field: cost.id, groupBy: ["project_name"], groupLimit: 10000, label: "Project API equivalent (USD)", display: { kind: "pie" } } },
    cover: { mode: "result", metricId: cost.id, kind: "pie", maxGroups: 5, showOther: true, showLegend: true, legendColumns: 2 },
  }),
  costCard("window-work-cost", "Conversation costs in period", "Top-level conversation API equivalent including delegated work over the last {days} days, ranked by period cost.", workKeys, workSummaries, "Inspect costly conversations this period", {
    where: workWhere, note: `${pricedNote} ${workNote} Rank six top-level conversations by their summed period cost, retaining all pairs in the destination. The root workspace supplies the representative title.`,
    exploration: { keys: workKeys, summaries: workSummaries, valueField: cost.id, unit: "USD", subjects: "top-level conversations", action: "Inspect costly conversations this period", labelFields: ["ac_title", "root_project_name"], representative: latest },
  }),
  costCard("work-cost-distribution", "Conversation cost ranges", "Top-level conversation period costs including delegated work over the last {days} days; three conversations per project required.", workKeys, workSummaries, "Explore conversation cost ranges", {
    where: workWhere, note: `${pricedNote} ${workNote} Require three top-level conversation observations per project before selecting six groups by observation count. Zero-cost work stays in the distribution and triggers a shared linear dollar axis; otherwise all boxes share one log-dollar axis. Sparse groups and large outliers remain inspectable in the destination.`,
    exploration: { keys: workKeys, summaries: workSummaries, valueField: cost.id, unit: "USD", subjects: "top-level conversations", action: "Explore conversation cost ranges", labelFields: ["ac_title", "root_project_name"], representative: latest,
      coverMetric: { id: cost.id, op: "count", field: cost.id, groupBy: ["root_project_name"], groupLimit: 10000, sort: [{ key: "samples", dir: "desc" }], distribution: { kind: "box", input: `[${cost.id}]`, whiskers: "tukey" }, label: "Period conversation costs (USD)", display: { kind: "box", xLabel: "API equivalent / top-level conversation (USD)", xScale: { mode: "log" } } } },
    cover: { mode: "result", metricId: cost.id, kind: "box", maxGroups: 6, minSamples: 3, sampleUnit: "top-level conversations", density: "compact" },
  }),
  costCard("work-volume-cost", "Conversation count and cost", "Top-level conversation counts versus period cost including delegation over the last {days} days, attributed to top-level projects.", ["root_project_name"], [work, cost, tokens], "Compare conversation count with cost", {
    where: workWhere, note: `${pricedNote} ${workNote} One point per project compares distinct active root_conversation_id count with summed priced cost for those same facts. This is an observed workload relationship, not controlled unit pricing or task complexity. Both axes are logarithmic; zero-cost points are omitted with a count and stay in the destination. Show up to 200 projects.`,
    exploration: { keys: ["root_project_name"], summaries: [work, cost, tokens], valueField: work.id, unit: "top-level conversations", subjects: "projects", action: "Compare conversation count with cost", labelFields: ["root_project_name"],
      coverMetric: { id: work.id, op: "max", field: work.id, expression: `MAX([${work.id}])`, expressionY: `MAX([${cost.id}])`, groupBy: ["root_project_name"], groupLimit: 200, label: "Project conversation counts and API equivalent", display: { kind: "scatter", xLabel: "Top-level conversations", yLabel: "API equivalent (USD)", xScale: { mode: "log" }, yScale: { mode: "log" }, xFormat: { kind: "number", decimals: 0 }, yFormat: { kind: "number", decimals: 2 }, legendPosition: "none" } } },
    cover: { mode: "result", metricId: work.id, kind: "scatter", maxGroups: 200 },
  }),
];

export function refineApiCostRecipe(recipe: QueryRecipe): QueryRecipe {
  if (!["cost-repos", "expensive-work", "price-coverage"].includes(recipe.id)) return recipe;
  const view = recipe.exploration!;
  if (recipe.id === "cost-repos") return { ...recipe, days: [7, 30], title: "Daily API equivalent", description: "Fully priced API equivalent by recorded day and project over the last {days} days.", where: priced, action: "Explore daily project costs", note: `${pricedNote} Daily buckets use the ledger’s recorded local day. Fetch complete daily project totals before combining the remainder into Other; empty day buckets are shown as zero recorded priced cost.`,
    exploration: { ...view, action: "Explore daily project costs", coverMetric: { ...view.coverMetric!, groupLimit: 10000 } }, cover: { mode: "result", metricId: view.valueField, kind: "auto", maxGroups: 5, showOther: true, showLegend: true, height: 300 } };
  if (recipe.id === "price-coverage") return { ...recipe, days: [7, 30], title: "Token pricing coverage", description: "Recorded token shares by pricing status over the last {days} days; assumed and missing prices stay visible.", where: positive, action: "Inspect token pricing coverage", note: "Partition positive recorded token volume by retained price_status: priced, assumed, partial, unpriced, and any unknown labels. Weight by tokens, not fact count, sessions, or dollars. Unknown cost is not zero spend. Daily usage records can combine requests, so a partly priced fact is classified by its retained status; this is coverage at the row level. This card includes all price statuses, unlike the period cost comparisons. Time placement follows recorded usage records.",
    exploration: { ...view, action: "Inspect token pricing coverage", coverMetric: { id: view.valueField, op: "sum", field: view.valueField, groupBy: ["price_status"], groupLimit: 10000, label: "Tokens by pricing status", display: { kind: "donut" } } }, cover: { mode: "result", metricId: view.valueField, kind: "donut", maxGroups: 4, showOther: true, showLegend: true, showHeadline: false } };
  return { ...recipe, retired: true, days: [7, 30], title: "Recently active lifetime costs", description: "Lifetime API equivalent for fully priced recorded work active in the last {days} days; includes older usage.", where: [{ field: "token_count", op: ">", value: "0" }, { field: "price_status", op: "=", value: "priced" }, { field: "cost_usd", op: ">=", value: "0" }], action: "Inspect lifetime workspace costs", note: "Select retained Library work records by recent activity_at, then rank their entire retained lifetime API equivalent. Only fully priced work totals with positive tokens and known nonnegative costs qualify; records with mixed/partial/assumed/unpriced totals are excluded. Activity can be recent while much of the cost is older. This is a different population and period from usage-in-window cards. A Library ID is a retained work record, not a merged directory, whole delegated conversation, or complete conversation. Latest activity supplies the representative title and project. Show six costly records; all qualifying work remains inspectable.",
    exploration: { ...view, action: "Inspect lifetime workspace costs" }, cover: { mode: "result", metricId: view.valueField, kind: "bar-horizontal", maxGroups: 6, showOther: false } };
}
