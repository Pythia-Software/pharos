import type { SummaryColumn, WhereTerm } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";

const nonempty = (field: string): WhereTerm[] => [{ field, op: "is_not_null", value: "" }, { field, op: "!=", value: "" }];
const positive: WhereTerm[] = [{ field: "total_tokens", op: ">", value: "0" }];
const priced: WhereTerm[] = [...positive, { field: "price_status", op: "=", value: "priced" }, { field: "cost_usd", op: ">=", value: "0" }];
const workWhere = [...priced, ...nonempty("root_conversation_id")];
const cost: SummaryColumn = { id: "ac_cost", op: "sum", field: "cost_usd", label: "API equivalent (USD)" };
const tokens: SummaryColumn = { id: "ac_tokens", op: "sum", field: "total_tokens", label: "Recorded tokens" };
const work: SummaryColumn = { id: "ac_work", op: "count_distinct", field: "root_conversation_id", label: "Root tasks" };
const title: SummaryColumn = { id: "ac_title", op: "row", field: "root_title", label: "Root task" };
const latest = { orderBy: [{ field: "last_usage_at", dir: "desc" as const, nulls: "last" as const }, { field: "id", dir: "asc" as const, nulls: "last" as const }] };
const pricedNote = "API equivalent uses retained price schedules, not subscription charges, invoices, human effort, or measured value. Period cost cards include positive-token facts with price_status=priced and nonnegative known cost; assumed, partial, and unpriced facts are excluded. Costs follow usage-fact time placement, which can be request time or session-start placement. Zero-priced facts remain valid observations. Missing cost is unknown, not zero. Repository attribution follows each fact’s recorded repository, including delegated facts where they were recorded.";
const workNote = "One work observation is a resolved top-level conversation including all recorded delegated work, linked launches, and continuations. Child facts stay in their root task even across workspaces or repositories, attributed to the root repository. Multiple independent root conversations in one workspace remain separate. Sum fully priced usage within the selected period, including children of roots with no activity of their own in the period; older usage is excluded. Missing or cyclic lineage is excluded from these work comparisons. Missing root repositories remain Unspecified.";
function costCard(id: string, title: string, description: string, keys: string[], summaries: SummaryColumn[], action: string, extra: Partial<QueryRecipe> = {}): QueryRecipe {
  return { id, title, description, category: "cost", idea: "cost-02", dataset: "usage", field: "first_usage_at", days: [7, 30], unit: "USD", where: priced, measures: [], action, note: pricedNote,
    exploration: { keys, summaries, valueField: summaries[0].id, unit: "USD", subjects: "cost groups", action, labelFields: keys },
    cover: { mode: "result", metricId: summaries[0].id, kind: "bar-horizontal", maxGroups: 6, showOther: false }, ...extra };
}
const workKeys = ["root_repository_name", "root_conversation_id"];
const workSummaries = [cost, tokens, title];

export const apiCostRecipes: QueryRecipe[] = [
  costCard("api-cost-total", "API equivalent this period", "Total recorded API equivalent over the last {days} days, using fully priced positive-token usage facts only.", ["repository_name"], [cost, tokens], "Inspect priced cost totals", {
    idea: "cost-01", exploration: { keys: ["repository_name"], summaries: [cost, tokens], valueField: cost.id, unit: "USD", subjects: "repositories", action: "Inspect priced cost totals", labelFields: ["repository_name"],
      coverMetric: { id: cost.id, op: "sum", field: cost.id, groupBy: [], label: "API equivalent (USD)" } },
    cover: { mode: "result", metricId: cost.id, kind: "auto", height: 260 },
  }),
  costCard("repository-cost-share", "Repository cost share", "Fully priced API equivalent over the last {days} days, attributed to each usage fact’s recorded repository.", ["repository_name"], [cost, tokens, work], "Explore repository cost shares", {
    idea: "cost-01", note: `${pricedNote} Five leading repositories plus exact Other partition all matching priced cost. Missing repository labels stay Unspecified. Zero-cost groups contribute no pie area and remain in the destination.`,
    exploration: { keys: ["repository_name"], summaries: [cost, tokens, work], valueField: cost.id, unit: "USD", subjects: "repositories", action: "Explore repository cost shares", labelFields: ["repository_name"],
      coverMetric: { id: cost.id, op: "sum", field: cost.id, groupBy: ["repository_name"], groupLimit: 10000, label: "Repository API equivalent (USD)", display: { kind: "pie" } } },
    cover: { mode: "result", metricId: cost.id, kind: "pie", maxGroups: 5, showOther: true, showLegend: true, legendColumns: 2 },
  }),
  costCard("window-work-cost", "Work costs in period", "Root-task API equivalent including delegated work over the last {days} days, ranked by period cost.", workKeys, workSummaries, "Inspect costly work this period", {
    where: workWhere, note: `${pricedNote} ${workNote} Rank six root tasks by their summed period cost, retaining all pairs in the destination. The root workspace supplies the representative title.`,
    exploration: { keys: workKeys, summaries: workSummaries, valueField: cost.id, unit: "USD", subjects: "root tasks", action: "Inspect costly work this period", labelFields: ["ac_title", "root_repository_name"], representative: latest },
  }),
  costCard("work-cost-distribution", "Work cost ranges", "Root-task period costs including delegated work over the last {days} days; three tasks per repository required.", workKeys, workSummaries, "Explore work cost ranges", {
    where: workWhere, note: `${pricedNote} ${workNote} Require three root-task observations per repository before selecting six cohorts by observation count. Zero-cost work stays in the distribution and triggers a shared linear dollar axis; otherwise all boxes share one log-dollar axis. Sparse cohorts and large outliers remain inspectable in the destination.`,
    exploration: { keys: workKeys, summaries: workSummaries, valueField: cost.id, unit: "USD", subjects: "root tasks", action: "Explore work cost ranges", labelFields: ["ac_title", "root_repository_name"], representative: latest,
      coverMetric: { id: cost.id, op: "count", field: cost.id, groupBy: ["root_repository_name"], groupLimit: 10000, sort: [{ key: "samples", dir: "desc" }], distribution: { kind: "box", input: `[${cost.id}]`, whiskers: "tukey" }, label: "Period work costs (USD)", display: { kind: "box", xLabel: "API equivalent / root task (USD)", xScale: { mode: "log" } } } },
    cover: { mode: "result", metricId: cost.id, kind: "box", maxGroups: 6, minSamples: 3, sampleUnit: "root tasks", density: "compact" },
  }),
  costCard("work-volume-cost", "Work volume and cost", "Root-task counts versus period cost including delegation over the last {days} days, attributed to root repositories.", ["root_repository_name"], [work, cost, tokens], "Compare work volume with cost", {
    where: workWhere, note: `${pricedNote} ${workNote} One point per repository compares distinct active root_conversation_id count with summed priced cost for those same facts. This is an observed workload relationship, not controlled unit pricing or task complexity. Both axes are logarithmic; zero-cost points are omitted with a count and stay in the destination. Show up to 200 repositories.`,
    exploration: { keys: ["root_repository_name"], summaries: [work, cost, tokens], valueField: work.id, unit: "root tasks", subjects: "repositories", action: "Compare work volume with cost", labelFields: ["root_repository_name"],
      coverMetric: { id: work.id, op: "max", field: work.id, expression: `MAX([${work.id}])`, expressionY: `MAX([${cost.id}])`, groupBy: ["root_repository_name"], groupLimit: 200, label: "Repository work volume and API equivalent", display: { kind: "scatter", xLabel: "Root tasks", yLabel: "API equivalent (USD)", xScale: { mode: "log" }, yScale: { mode: "log" }, xFormat: { kind: "number", decimals: 0 }, yFormat: { kind: "number", decimals: 2 }, legendPosition: "none" } } },
    cover: { mode: "result", metricId: work.id, kind: "scatter", maxGroups: 200 },
  }),
];

export function refineApiCostRecipe(recipe: QueryRecipe): QueryRecipe {
  if (!["cost-repos", "expensive-work", "price-coverage", "price-comparison"].includes(recipe.id)) return recipe;
  const view = recipe.exploration!;
  if (recipe.id === "cost-repos") return { ...recipe, days: [7, 30], title: "Daily API equivalent", description: "Fully priced API equivalent by recorded day and repository over the last {days} days.", where: priced, action: "Explore daily repository costs", note: `${pricedNote} Daily buckets use the ledger’s recorded local day. Fetch complete daily repository totals before combining the remainder into Other; empty day buckets are shown as zero recorded priced cost.`,
    exploration: { ...view, action: "Explore daily repository costs", coverMetric: { ...view.coverMetric!, groupLimit: 10000 } }, cover: { mode: "result", metricId: view.valueField, kind: "auto", maxGroups: 5, showOther: true, showLegend: true, height: 300 } };
  if (recipe.id === "price-coverage") return { ...recipe, days: [7, 30], title: "Token pricing coverage", description: "Recorded token shares by pricing status over the last {days} days; assumed and missing prices stay visible.", where: positive, action: "Inspect token pricing coverage", note: "Partition positive recorded token volume by retained price_status: priced, assumed, partial, unpriced, and any unknown labels. Weight by tokens, not fact count, sessions, or dollars. Unknown cost is not zero spend. Daily usage facts can combine requests, so a partly priced fact is classified by its retained status; this is coverage at the fact grain. This card includes all price statuses, unlike the period cost comparisons. Time placement follows recorded usage facts.",
    exploration: { ...view, action: "Inspect token pricing coverage", coverMetric: { id: view.valueField, op: "sum", field: view.valueField, groupBy: ["price_status"], groupLimit: 10000, label: "Tokens by pricing status", display: { kind: "donut" } } }, cover: { mode: "result", metricId: view.valueField, kind: "donut", maxGroups: 4, showOther: true, showLegend: true, showHeadline: false } };
  if (recipe.id === "expensive-work") return { ...recipe, retired: true, days: [7, 30], title: "Recently active lifetime costs", description: "Lifetime API equivalent for fully priced recorded work active in the last {days} days; includes older usage.", where: [{ field: "token_count", op: ">", value: "0" }, { field: "price_status", op: "=", value: "priced" }, { field: "cost_usd", op: ">=", value: "0" }], action: "Inspect lifetime work costs", note: "Select retained Library work records by recent activity_at, then rank their entire retained lifetime API equivalent. Only fully priced work totals with positive tokens and known nonnegative costs qualify; records with mixed/partial/assumed/unpriced totals are excluded. Activity can be recent while much of the cost is older. This is a different population and period from usage-in-window cards. A Library ID is a retained work record, not a merged directory, whole delegated conversation, or complete task. Latest activity supplies the representative title and repository. Show six costly records; all qualifying work remains inspectable.",
    exploration: { ...view, action: "Inspect lifetime work costs" }, cover: { mode: "result", metricId: view.valueField, kind: "bar-horizontal", maxGroups: 6, showOther: false } };
  return { ...recipe, retired: true, days: [7, 30], title: "Recorded repricing comparison", description: "Historical API equivalent versus retained current-price estimates over the last {days} days; current-price completeness is unverified.", where: [...priced, { field: "cost_today_usd", op: ">=", value: "0" }], action: "Inspect retained price comparisons", note: "Compare matched usage facts with complete historical pricing and nonnegative historical/current-price cost fields, grouped by repository. Apply the retained current price schedule to the same historical token quantities; this is a scenario, not money paid, realizable savings, or a live market-price lookup. The existing cost_today_usd field does not expose a separate completeness/assumption status for today’s schedule, so its estimate may be partial or assumed even when historical price_status=priced. Do not interpret it as a fully verified current-price quote. Both axes are logarithmic; zero coordinates remain inspectable and are omitted with a count. Show up to 200 repository pairs.",
    exploration: { ...view, action: "Inspect retained price comparisons" },
    measures: recipe.measures.map((metric, index) => index === 0 ? { ...metric, groupLimit: 200, display: { ...metric.display, kind: "scatter", xScale: { mode: "log" }, yScale: { mode: "log" } } } : metric),
    cover: { mode: "source", metricId: recipe.measures[0].id, kind: "scatter", maxGroups: 200 } };
}
