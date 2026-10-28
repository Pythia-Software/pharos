import { decodeQueryStrict, type WhereClause, type WhereTerm } from "@pythia-software/query-table-core";
import { analysisCatalogs } from "./analysis-catalogs";
import { chartKeys, chartVerdict, canonicalChart, judgeChart, viewChart, cardURL, compact, dashboardURL, dashboardWindows, datasetURL, enumerateView, siteSchemas, type SiteMap, type Verdict } from "./site-map";
import { normalizeAnalysis, switchView, type AnalysisState, type AnalysisView } from "./dashboard-analysis-model";
import { usageDatasets, usageTopics, type UsageDataset, type UsageTopic } from "./usage-workspace-model";
import { rollingWindowDays, type QueryDataset } from "./query-links";
import { cardLibraryParams } from "./card-library-model";
import { emptyTextSearch, newRule, chooseScope } from "./text-search-model";
import { freshSearch, searchURL } from "./search-composer-state";
import { findingDetailURL } from "./findings-route";
import { conversationRowURL } from "./usage-row-model";
import { delegationRouteValues } from "./delegation-route";
export { buildSiteMap, formatJSON, settingsPages, viewTitles } from "./site-map";

// The cross-language cases for the site map (testdata/site-map-cases.json):
// a deterministic sample of Analyze dashboard charts with the page's verdict
// and, for valid ones, the link the page settles on; every card at every
// rolling window; a link per page; and the design's motivating example. A
// link's query tokens are decoded beside it, since deflate bytes differ
// between implementations.

const json = (value: unknown) => JSON.stringify(value);
const decoded = (url: string) => {
  const params = new URL(url, "http://localhost").searchParams, out: Record<string, unknown> = {};
  for (const [key, value] of params) if (key === "analyze_filters" || key.startsWith("q_")) { const query = decodeQueryStrict(value)!; out[key] = { where: query.where, ...compact(query) }; }
  return out;
};
const where = (field: string, op: string, value = "", negated = false): WhereTerm => ({ field, op, value, ...negated ? { negated } : {} } as WhereTerm);

// Filter terms for a dataset that use several operators: = on an enum, a
// text contains and a negated !=, a number comparison, a nullary test, and
// an OR of equalities.
function filterSets(dataset: QueryDataset): WhereTerm[][] {
  const fields = siteSchemas[dataset].fields;
  const enumField = fields.find(field => field.type === "enum" && field.filter?.values?.source === "static");
  const enumValue = (() => { const option = enumField?.filter?.values?.source === "static" ? enumField.filter.values.options[0] : undefined; return typeof option === "string" ? option : option ? String(option.value) : ""; })();
  const text = fields.find(field => field.type === "text" && /repository|model|tool|name/.test(field.name)) ?? fields.find(field => field.type === "text");
  const number = fields.find(field => field.type === "number");
  const sets: WhereTerm[][] = [[]];
  if (enumField) sets.push([where(enumField.name, "=", enumValue)]);
  if (text) sets.push([where(text.name, "contains", "pharos")], [where(text.name, "!=", "scratch", true), where(text.name, "is_not_null")]);
  if (number) sets.push([where(number.name, ">", "0")]);
  if (enumField && text) sets.push([{ any: [where(text.name, "=", "a"), where(text.name, "=", "b")] as WhereClause[] }, where(enumField.name, "!=", enumValue)]);
  return sets;
}

type ChartCase = { topic: UsageTopic; view: string; partial?: true; written?: string; chart: Record<string, unknown>; verdict: Verdict; filters?: WhereTerm[]; days?: number | null; url?: string; decoded?: Record<string, unknown> };
// Up to count items spread evenly over a list.
const spread = <T,>(items: T[], count: number) => items.length <= count ? items : Array.from({ length: count }, (_, index) => items[Math.floor(index * items.length / count)]);

function chartCases(map: SiteMap): ChartCase[] {
  const cases: ChartCase[] = [];
  let turn = 0;
  for (const topic of Object.keys(usageTopics) as UsageTopic[]) {
    const facts = map.analyze.dashboards[topic], sets = filterSets(facts.dataset as UsageDataset), windows = [...dashboardWindows, null];
    for (const [view, info] of Object.entries(facts.views)) {
      // A spread of every enumerated combination, valid and coercible.
      const judged = enumerateView(topic, view as AnalysisView, info.defaults, info.domains).filter(item => chartKeys[view as AnalysisView].every(name => name in item.chart));
      const linked = (item: ChartCase) => {
        const filters = sets[turn % sets.length], days = windows[turn % windows.length];
          turn++;
          item.filters = filters; item.days = days; item.url = dashboardURL(topic, item.chart, filters, days); item.decoded = decoded(item.url);
          return item;
      };
      for (const verdict of ["valid", "coercible"] as Verdict[]) for (const { chart } of spread(judged.filter(item => item.verdict === verdict), verdict === "valid" ? 2 : 2)) {
        const item: ChartCase = { topic, view, chart, verdict };
        cases.push(verdict === "valid" ? linked(item) : item);
      }
      // Partial charts, which the page merges over the topic's defaults: one
      // option at each of its values, and the view's defaults less one key.
      const partial: Record<string, unknown>[] = [
        ...Object.entries(info.domains).flatMap(([option, values]) => values.map(value => ({ v: map.analyze.version, view, [option]: value }))),
        ...Object.keys(info.numeric).filter(option => option !== "selection" && info.defaults[option] !== undefined).map(option => ({ v: map.analyze.version, view, [option]: info.defaults[option] })),
        // Without view, a chart takes the topic's; only that view's keys are in the map.
        ...Object.keys(info.defaults).filter(option => option !== "view" || (facts.merge_defaults.view ?? "trend") === view).map(option => Object.fromEntries(Object.entries(info.defaults).filter(([name]) => name !== option))),
      ];
      let partialLinks = 0;
      for (const chart of partial) {
        const item: ChartCase = { topic, view, partial: true, chart, verdict: chartVerdict(topic, chart) };
        cases.push(item.verdict === "valid" && "view" in chart && partialLinks++ < 1 ? linked(item) : item);
      }
      // Charts the page rejects: an unknown or ill-typed value per option.
      const invalid: Record<string, unknown>[] = [
        { v: 3 }, { metric: "no_such_measure" }, { split: "no_such_breakdown" }, { scale: "huge" }, { selection: { values: [1], excluded: false } },
        ...view === "trend" ? [{ period: "fortnight" }, { hours: 0 }, { rollingPeriods: -1 }, { normPeriods: 1001 }, { share: "yes" }]
          : [{ unit: "no_such_unit" }, { minSamples: -1 }, { reduce: "sum" }, { compareAs: "pie" }, { cycle: "month" }, { metricY: "no_such_measure" }],
      ];
      // Each as a one-key chart; the first also over the whole default chart.
      for (const [index, change] of invalid.entries()) {
        const chart = { v: map.analyze.version, view, ...change };
        cases.push({ topic, view, partial: true, chart, verdict: chartVerdict(topic, chart) });
        if (!index) cases.push({ topic, view, chart: { ...canonicalChart(info.defaults), ...change }, verdict: chartVerdict(topic, { ...canonicalChart(info.defaults), ...change }) });
      }
    }
    // Charts the page writes itself: after a view switch it saves the whole
    // switched state over the current one (switchView, then persist), with
    // keys the new view does not draw. And every card's own chart.
    const catalog = analysisCatalogs[topic]!, merge = facts.merge_defaults as Record<string, unknown>;
    for (const [from, info] of Object.entries(facts.views)) for (const to of Object.keys(facts.views)) {
      const current = { ...merge, ...info.defaults } as AnalysisState;
      const chart = { ...current, ...switchView(catalog, current, to as AnalysisView) } as Record<string, unknown>;
      cases.push({ topic, view: to, written: `switchView ${from} to ${to}`, chart, verdict: chartVerdict(topic, chart) });
    }
    for (const card of map.cards.items.filter(card => card.page === `analyze/${topic}`)) {
      const chart = card.chart as Record<string, unknown>;
      cases.push({ topic, view: String(chart.view ?? merge.view ?? "trend"), written: `card ${card.id}`, chart, verdict: chartVerdict(topic, chart) });
    }
    // A view the catalog cannot show.
    for (const view of Object.keys(facts.unavailable_views ?? {})) {
      const chart = { ...canonicalChart(Object.values(facts.views)[0].defaults), view };
      cases.push({ topic, view, chart, verdict: chartVerdict(topic, chart) });
    }
  }
  return cases;
}

// The design's example: tokens per day through Anthropic's Opus models, a
// 7-day trailing average, by model, over 30 days. Pharos records Anthropic
// usage under the provider claude (schemas/usage.schema.json); there is no
// provider anthropic.
export function motivatingExample(map: SiteMap) {
  const info = map.analyze.dashboards.tokens.views.trend;
  const chart = canonicalChart(info.defaults, { metric: "tokens", split: "model_family", rollingPeriods: 7 });
  const filters = [where("provider", "=", "claude"), where("model_family", "contains", "opus")];
  const url = dashboardURL("tokens", chart, filters, 30);
  return { name: "Anthropic Opus-class tokens per day, 7-day trailing average, by model", page: "analyze/tokens", topic: "tokens" as const, view: "trend", chart, filters, days: 30, verdict: chartVerdict("tokens", chart), url, decoded: decoded(url) };
}

// Every card at one window, and at every window the first card of each way
// a card's link takes its window: the page kind it opens, Parallelism's
// ribbon, a chart that follows the window, its own filters or none, window
// terms on another field than the card's, a cover, the local calendar, and
// a card without a window.
type Card = SiteMap["cards"]["items"][number];
export const windowPath = (card: Card) => json([
  card.page.startsWith("analyze/query") ? "analyze/query" : card.page.split("/")[0], card.page === "analyze/parallelism", Boolean(card.window), Boolean(card.window?.chart_days),
  Boolean(card.filters?.length), card.window?.terms.some(term => "field" in term && term.field !== card.window!.field) ?? false,
  card.url.params.some(([name]) => name === "card_cover"), json(card.url.params).includes("timeZone"),
]);
function cardCases(map: SiteMap) {
  const seen = new Set<string>();
  return map.cards.items.flatMap(card => {
    const path = windowPath(card), every = !seen.has(path);
    seen.add(path);
    const windows: Array<number | null> = card.window ? every ? card.window.days : [card.window.default_days[0]] : [null];
    return windows.map(days => {
      const url = cardURL(card, days), filters = decoded(url).analyze_filters;
      return { id: card.id, days, url, ...filters ? { decoded: { analyze_filters: filters } } : {} };
    });
  });
}

function nodeCases(map: SiteMap) {
  const cases: Array<{ path: string; url: string; state?: unknown; decoded?: Record<string, unknown> }> = [];
  const add = (path: string, url: string, state?: unknown) => cases.push({ path, url, ...state === undefined ? {} : { state }, ...Object.keys(decoded(url)).length ? { decoded: decoded(url) } : {} });
  add("home", "/");
  const cardsParams = (values: Record<string, string>) => `/cards?${new URLSearchParams(cardLibraryParams.filter(key => values[key]).map(key => [key, values[key]]))}`;
  add("cards", "/cards");
  add("cards", cardsParams({ card_search: "tokens", card_topic: "usage", card_sort: "title" }), { card_search: "tokens", card_topic: "usage", card_sort: "title" });
  add("cards", cardsParams({ card_data: "tool_calls", card_kind: "Ranking", card_topic: "all" }), { card_data: "tool_calls", card_kind: "Ranking", card_topic: "all" });
  // Library: a text search, a file search, and table filters with a window.
  const schema = siteSchemas.library, text = emptyTextSearch();
  // Rule ids are the editor's own; fixed ones keep the cases stable.
  const rule = (id: number, words: string, kind: "phrase" | "words") => ({ ...newRule(words), id: `rule-${id}`, kind });
  const rules = { ...text, scope: chooseScope(text.scope, "user"), root: { ...text.root, id: "group-1", children: [rule(1, "flaky test", "phrase"), rule(2, "retry", "words")] } };
  add("library", searchURL({ ...freshSearch(schema), text: rules }), { kind: "text", rules: rules.root.children.map(rule => ({ kind: rule.kind, text: rule.text })), surface: rules.scope.surface, roles: rules.scope.roles });
  add("library", searchURL({ ...freshSearch(schema), kind: "file", file: "internal/archive/api.go", fileCase: true }), { kind: "file", search: "internal/archive/api.go", case: true });
  const filtered = freshSearch(schema), window = { days: 30, field: map.datasets.library.window_field!, mode: "relative" as const };
  add("library", searchURL({ ...filtered, text: { ...text, root: { ...text.root, id: "group-1", children: [rule(1, "deploy", "words")] } }, window, query: { ...filtered.query, where: [where("repository_name", "contains", "pharos"), where(window.field, ">=", "-30d"), where(window.field, "<", "+0s")] } }), { kind: "text", window: 30 });
  // Query tables, with and without a window.
  let turn = 0;
  for (const dataset of [...Object.keys(usageDatasets), "findings", "tl1_attempts"] as QueryDataset[]) {
    const path = dataset === "findings" ? "findings/table" : dataset === "tl1_attempts" ? "tl1/runs" : `analyze/query/${dataset}`;
    for (const filters of filterSets(dataset).slice(1, 4)) {
      const days = map.datasets[dataset].window_field ? rollingWindowDays[turn++ % rollingWindowDays.length] : null;
      add(path, datasetURL(dataset, filters, days), { filters, days });
    }
  }
  add("findings", "/findings");
  add("findings", "/findings?list=wins&repo=pharos", { list: "wins", repo: "pharos" });
  add("findings", findingDetailURL("tool-failures:repository:abc123", "evidence"), { finding: "tool-failures:repository:abc123", tab: "evidence" });
  add("findings/settings", "/findings?view=settings", { view: "settings" });
  for (const path of Object.keys(map.nodes).filter(path => path.startsWith("tl1") || path.startsWith("settings"))) add(path, map.nodes[path].route);
  // The reader: a work, a conversation and message, and the delegation explorer.
  add("work", conversationRowURL({ workspace_id: "workspace-1" })!, { work_id: "workspace-1" });
  add("work", conversationRowURL({ workspace_id: "workspace-1", conversation_id: "conversation-2" }, "message-3", undefined, "call-4")!, { work_id: "workspace-1", conversation: "conversation-2", message: "message-3", call: "call-4" });
  add("work", conversationRowURL({ workspace_id: "workspace-1", conversation_id: "conversation-2" }, undefined, "2026-01-01T12:00:00Z")!, { work_id: "workspace-1", conversation: "conversation-2", at: "2026-01-01T12:00:00Z" });
  const route = { mode: "rank" as const, sort: "cost", scope: "self", direction: "asc", collapsed: ["session-b", "session-a"], selected: "session-c", range: [1767268800000, 1767272400000] as [number, number] };
  const reader = new URL(conversationRowURL({ workspace_id: "workspace-1", conversation_id: "conversation-2" })!, "http://localhost");
  for (const [key, value] of Object.entries(delegationRouteValues(route))) if (value) reader.searchParams.set(key, value);
  reader.searchParams.set("explorer", "parallel");
  add("work", reader.pathname + reader.search, { work_id: "workspace-1", conversation: "conversation-2", ...route, collapsed: [...route.collapsed].sort(), tab: "parallel" });
  return cases;
}

export function buildSiteMapCases(map: SiteMap) {
  for (const topic of Object.keys(usageTopics)) if (!analysisCatalogs[topic]) throw new Error(`site map cases: no catalog for ${topic}`);
  const charts = chartCases(map);
  for (const item of charts) {
    const facts = map.analyze.dashboards[item.topic];
    if (judgeChart(facts, map.analyze.version, item.chart) !== item.verdict) throw new Error(`site map cases: the rules misjudge ${item.topic} ${json(item.chart)}`);
    if (item.verdict === "invalid") continue;
    // Keeping only the view's keys changes neither the verdict nor the drawing.
    const kept = viewChart(item.topic, item.chart), view = chartKeys[(item.chart.view ?? facts.merge_defaults.view ?? "trend") as AnalysisView];
    const draw = (chart: Record<string, unknown>) => json(view.map(key => (normalizeAnalysis(analysisCatalogs[item.topic]!, { ...facts.merge_defaults, ...chart } as AnalysisState) as Record<string, unknown>)[key]));
    if (chartVerdict(item.topic, kept) !== item.verdict || draw(kept) !== draw(item.chart)) throw new Error(`site map cases: ${item.topic} ${json(item.chart)} draws otherwise with only its view's keys`);
  }
  return {
    version: map.version,
    about: "Cases for the site map, from web/src/site-map-cases.ts. analyze: by topic, charts with the page's verdict (valid; coercible: opens but draws other settings; invalid: rejected). Partial charts are merged over the topic's merge_defaults, as the page does. Some valid charts carry their link. cards: every card at one window, and some at every window. nodes: a link per page with the state its reader reads. decoded: each query token's state.",
    example: motivatingExample(map),
    // By topic; a chart names its view, or takes the topic's.
    analyze: Object.fromEntries(Object.keys(usageTopics).map(topic => [topic, charts.filter(item => item.topic === topic).map(({ topic: _topic, view: _view, partial: _partial, ...item }) => item)])),
    cards: cardCases(map),
    nodes: nodeCases(map),
  };
}
