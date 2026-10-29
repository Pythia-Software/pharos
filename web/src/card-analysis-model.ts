import { createQueryLinkService, httpQuerySnapshotStore, type FieldSchema, type QueryState } from "@pythia-software/query-table-core";
import { describeRecipeText, instantiateRecipe, queryRecipes } from "./curiosity-data";
import { cardSourceDataset } from "./card-exploration";
import { defaultCardCover, type CardCover } from "./card-cover";
import { queryFromURL, readRollingWindow, rollingWindowDays } from "./query-links";
import type { PreviewJob } from "./query-previews";
import { stampCardChart, usageWorkspaceURL } from "./usage-workspace-model";

// The card's source query and presentation are the analysis contract. Dashboard
// projections remain useful related context, but cannot replace this population,
// staged calculation, distribution or specialized study.
export function cardAnalysisJob(url: string, schema: FieldSchema, fallback?: QueryState): PreviewJob {
  const params = new URL(url, location.origin).searchParams, dataset = cardSourceDataset(params);
  const recipe = queryRecipes.find(recipe => recipe.id === params.get("card_id") && recipe.dataset === dataset);
  const spec = queryFromURL(url, fallback);
  if (!recipe || !dataset || !spec || schema.name !== `pharos_${dataset}`) throw new Error("This card's source query is unavailable.");
  let cover = defaultCardCover(recipe);
  if (params.has("card_cover")) {
    const value = JSON.parse(params.get("card_cover")!);
    if (!value || !["source", "result"].includes(value.mode) || typeof value.metricId !== "string") throw new Error("This card's chart settings are invalid.");
    cover = value as CardCover;
  }
  const window = readRollingWindow(params, dataset);
  const copy = (field: "title" | "description") => {
    const saved = params.get(`card_${field}`);
    // Collections may store already-rendered default copy. Restore its template
    // when changing range, while retaining wording the user customized.
    return saved === null || [null, "today" as const, ...rollingWindowDays].some(days => describeRecipeText(recipe[field], days) === saved) ? recipe[field] : saved;
  };
  const item = instantiateRecipe({ ...recipe,
    title: copy("title"), description: copy("description"),
  }, window?.mode === "today" ? "today" : window?.days ?? null);
  return { id: recipe.id, dataset, query: spec.query, schema,
    options: { recipe: item, window, cover, coverMetricId: params.get("card_metric") ?? undefined, url } };
}

export async function resolveCardAnalysisJob(url: string, schema: FieldSchema, signal?: AbortSignal): Promise<PreviewJob> {
  const params = new URL(url, location.origin).searchParams, dataset = cardSourceDataset(params);
  if (!dataset) throw new Error("This card's source query is unavailable.");
  const service = createQueryLinkService(schema.name, httpQuerySnapshotStore("/api/query-snapshots"));
  const query = await service.resolve(new URL(url, location.origin).href, `q_${dataset}`, signal);
  if (!query) throw new Error("This card's source query is unavailable.");
  return cardAnalysisJob(url, schema, query);
}

export function relatedCardDashboardURL(params: URLSearchParams): string {
  const related = new URLSearchParams(params);
  stampCardChart(related);
  for (const key of [...related.keys()]) if (key.startsWith("card_")) related.delete(key);
  return usageWorkspaceURL({ mode: "dashboards" }, related);
}
