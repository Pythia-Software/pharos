import { createQueryLinkService, httpQuerySnapshotStore, type FieldSchema, type QueryState } from "@pythia-software/query-table-core";
import { describeRecipeText, instantiateRecipe, queryRecipes } from "./curiosity-data";
import { cardSourceDataset } from "./card-exploration";
import { defaultCardCover, type CardCover } from "./card-cover";
import { cardDestination } from "./card-destinations";
import { queryFromURL, readRollingWindow, resolveRollingQuery, rollingWindowDays } from "./query-links";
import type { PreviewJob } from "./query-previews";
import { stampCardChart, usageWorkspaceURL } from "./usage-workspace-model";

// Saved cards retain their source query and cover for Home previews and editing.
// Navigation opens the full dashboard; it does not execute this preview job.
export function savedCardPreviewJob(url: string, schema: FieldSchema, fallback?: QueryState): PreviewJob {
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

export async function resolveSavedCardPreviewJob(url: string, schema: FieldSchema, signal?: AbortSignal): Promise<PreviewJob> {
  const params = new URL(url, location.origin).searchParams, dataset = cardSourceDataset(params);
  if (!dataset) throw new Error("This card's source query is unavailable.");
  const service = createQueryLinkService(schema.name, httpQuerySnapshotStore("/api/query-snapshots"));
  const query = await service.resolve(new URL(url, location.origin).href, `q_${dataset}`, signal);
  if (!query) throw new Error("This card's source query is unavailable.");
  return savedCardPreviewJob(url, schema, query);
}

// Browser bookmarks and shared links can still contain the old card page's
// mapping. Rebuild the dashboard from its exact retained source, resolving
// durable links before discarding their source metadata. Never replace a
// missing source with a newly instantiated recipe.
export async function resolveCardDashboardURL(address: string, signal?: AbortSignal): Promise<string> {
  const url = new URL(address, location.origin), params = url.searchParams, dataset = cardSourceDataset(params);
  const recipe = queryRecipes.find(recipe => recipe.id === params.get("card_id") && recipe.dataset === dataset);
  if (url.origin !== location.origin || url.pathname !== "/analyze" || params.get("analyze_mode") !== "dashboards" || !dataset || !(recipe?.destination || recipe?.thinking)) throw new Error("This card's dashboard is unavailable.");
  const service = createQueryLinkService(`pharos_${dataset}`, httpQuerySnapshotStore("/api/query-snapshots"));
  const query = await service.resolve(url.href, `q_${dataset}`, signal);
  if (!query) throw new Error("This card's source query is unavailable.");
  const spec = queryFromURL(url.href, query);
  if (!spec) throw new Error("This card's source query is unavailable.");
  const destination = new URL(cardDestination(recipe, resolveRollingQuery(spec.query, spec.window), spec.window), location.origin);
  return usageWorkspaceURL({ mode: "dashboards" }, destination.searchParams) + url.hash;
}

export function cardDashboardURL(params: URLSearchParams): string {
  const related = new URLSearchParams(params);
  stampCardChart(related);
  for (const key of [...related.keys()]) if (key.startsWith("card_")) related.delete(key);
  return usageWorkspaceURL({ mode: "dashboards" }, related);
}
