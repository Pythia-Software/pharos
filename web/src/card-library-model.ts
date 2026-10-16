import type { MetricDisplay } from "@pythia-software/query-table-core";
import { queryRecipes, type QueryRecipe } from "./curiosity-data";
import { defaultCardCover, type CardCover } from "./card-cover";
import { preferences } from "./preferences";

export const CARD_TEMPLATES_KEY = "pharos-card-templates-v1";
export const cardDatasets: Record<string, string> = { conversation_activity: "Conversation input/output", usage: "Token usage", library: "Workspaces", tool_calls: "Tool calls", skill_usages: "Skills", writing_messages: "Human messages", tl1_attempts: "TL1 attempts", mcp_calls: "MCP responses" };
export type CardTemplateEdit = { title: string; description: string; days: number; cover?: CardCover };
function overrides(): Record<string, CardTemplateEdit> {
  const value = preferences().get<unknown>(CARD_TEMPLATES_KEY, {});
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, CardTemplateEdit> : {};
}
export function cardTemplates(): QueryRecipe[] {
  const saved = overrides();
  return queryRecipes.filter(recipe => !recipe.retired).map(recipe => {
    const edit = saved[recipe.id];
    return edit && typeof edit.title === "string" && typeof edit.description === "string" && [1, 7, 30, 90].includes(edit.days)
      ? { ...recipe, title: edit.title, description: edit.description, days: [edit.days], cover: edit.cover } : recipe;
  });
}
export function coverChoices(recipe: QueryRecipe) {
  return [
    ...(recipe.exploration?.summaries.filter(column => column.op !== "row").map(column => ({ value: `result:${column.id}`, label: `${column.label ?? column.id} · grouped rows` })) ?? []),
    ...recipe.measures.map(metric => ({ value: `source:${metric.id}`, label: `${metric.label ?? metric.id} · observations` })),
  ];
}
export function decodeCover(value: string, kind?: MetricDisplay["kind"]): CardCover | undefined {
  const colon = value.indexOf(":");
  const mode = value.slice(0, colon);
  return colon > 0 && ["source", "result"].includes(mode) ? { mode: mode as CardCover["mode"], metricId: value.slice(colon + 1), ...(kind ? { kind } : {}) } : undefined;
}
export function availableChartKinds(recipe: QueryRecipe, cover = defaultCardCover(recipe)): MetricDisplay["kind"][] {
  const groupedMetric = recipe.exploration?.coverMetric;
  const metric = cover?.mode === "source" ? recipe.measures.find(metric => metric.id === cover.metricId)
    : groupedMetric?.field === cover?.metricId ? groupedMetric : undefined;
  if (metric?.distribution) return [metric.distribution.kind];
  if (metric?.expressionY) return ["scatter"];
  const column = cover?.mode === "result" ? recipe.exploration?.summaries.find(column => column.id === cover.metricId) : undefined;
  const additive = metric && !metric.expression && ["sum", "count"].includes(metric.op) ? true : column ? ["sum", "count"].includes(column.op) : metric && !metric.expression && ["sum", "count"].includes(metric.op);
  return ["auto", "bar-horizontal", "bar-vertical", "line", ...(additive ? ["pie", "donut"] as const : []), "value", "list", "table"];
}
export function saveCardTemplate(id: string, edit?: CardTemplateEdit) {
  const recipe = queryRecipes.find(recipe => recipe.id === id);
  if (!recipe) throw new Error("This card is no longer available.");
  const next = { ...overrides() };
  if (edit) {
    if (!edit.title.trim() || edit.title.length > 200 || edit.description.length > 500 || ![1, 7, 30, 90].includes(edit.days)) throw new Error("Choose a title, description and supported lookback.");
    if (edit.cover && (!coverChoices(recipe).some(choice => choice.value === `${edit.cover!.mode}:${edit.cover!.metricId}`) || !availableChartKinds(recipe, edit.cover).includes(edit.cover.kind ?? "auto"))) throw new Error("Choose a compatible metric and chart.");
    if (edit.cover?.density !== undefined && edit.cover.density !== "compact") throw new Error("Choose a valid row density.");
    if (edit.cover?.stackBy !== undefined && !(["session_kind", "session_role"].includes(edit.cover.stackBy))) throw new Error("Choose a supported stack dimension.");
    if (edit.cover?.height !== undefined && (!Number.isInteger(edit.cover.height) || edit.cover.height < 170 || edit.cover.height > 600)) throw new Error("Choose a chart height between 170 and 600 pixels.");
    if (edit.cover?.maxGroups !== undefined && (!Number.isInteger(edit.cover.maxGroups) || edit.cover.maxGroups < 1 || edit.cover.maxGroups > 100)) throw new Error("Choose between 1 and 100 visible rows or series.");
    if (edit.cover?.legendColumns !== undefined && ![1, 2].includes(edit.cover.legendColumns)) throw new Error("Choose one or two legend columns.");
    if (edit.cover?.minSamples !== undefined && (!Number.isInteger(edit.cover.minSamples) || edit.cover.minSamples < 1 || edit.cover.minSamples > 10000)) throw new Error("Choose a valid minimum sample size.");
    if (edit.cover && [edit.cover.showOther, edit.cover.showLegend, edit.cover.showHeadline].some(value => value !== undefined && typeof value !== "boolean")) throw new Error("Choose valid key and remainder settings.");
    next[id] = { ...edit, title: edit.title.trim(), description: edit.description.trim() };
  } else delete next[id];
  preferences().set(CARD_TEMPLATES_KEY, next);
  window.dispatchEvent(new Event("pharos:card-templates"));
}
