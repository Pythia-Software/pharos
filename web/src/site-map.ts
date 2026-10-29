import { supportsToday } from "./curiosity-data";
import { EMPTY_QUERY, decodeQueryStrict, encodeQuery, isFilterable, loadSchema, opsForField, type FieldSchema, type QueryState, type WhereTerm } from "@pythia-software/query-table-core";
import { analysisCatalogs } from "./analysis-catalogs";
import { analysisCycles, analysisDefaults, analysisReductions, analysisVersion, analysisViews, availableViews, canonicalState, compareModes, cycleUnavailable, noSplit, normalizeAnalysis, records, rollingOps, switchView, trendKind, validAnalysisState, viewUnavailable, type AnalysisCatalog, type AnalysisState, type AnalysisView } from "./dashboard-analysis-model";
import { dashboardDefaults } from "./dashboard-defaults";
import { chartPeriods } from "./chart-period";
import { chartDisplays } from "./chart-series";
import { chartScales } from "./chart-scale";
import { chartRanges, rangeDays, withTimeRange } from "./chart-range";
import { queryRecipes, instantiateRecipe, describeRecipeText, type QueryRecipe } from "./curiosity-data";
import { queryURL, resolveRollingQuery, rollingWindowDays, rollingWindowField, type QueryDataset } from "./query-links";
import { readUsageWorkspace, usageDatasets, usageDatasetTopics, usageTopicDatasets, usageTopics, usageWorkspaceURL, type UsageDataset, type UsageTopic } from "./usage-workspace-model";
import { parallelismRibbon } from "./card-destinations";
import { concurrencyWindowRange } from "./concurrency-settings";
import { cardDatasets, cardLibraryParams, cardSorts, cardTopicAll } from "./card-library-model";
import { cardKind } from "./card-cover";
import { queryCategories } from "./query-categories";
import { findingsLists, findingTabs } from "./findings-route";
import { delegationDirections, delegationModes, delegationRouteValues, delegationScopes, delegationTabs, defaultDelegationSort } from "./delegation-route";
import { emptyTextSearch, newRule, ruleLabels, scopes } from "./text-search-model";
import { freshSearch } from "./search-composer-state";
import { libraryHostParameters } from "./host-query-state";
import { recordKeywordParameter } from "./record-keyword-model";
import { appSections, findingsPages, tl1Pages } from "./app-navigation-model";
import { idSources, legacyRoutes, optionalSettingsPages, pageDescriptions, readerParams, siteSections } from "./site-routes";
import libraryDocument from "../../schemas/library.schema.json";
import usageDocument from "../../schemas/usage.schema.json";
import writingDocument from "../../schemas/writing.schema.json";
import conversationActivityDocument from "../../schemas/conversation_activity.schema.json";
import writingMessagesDocument from "../../schemas/writing_messages.schema.json";
import mcpCallsDocument from "../../schemas/mcp_calls.schema.json";
import toolsDocument from "../../schemas/tools.schema.json";
import toolCallsDocument from "../../schemas/tool_calls.schema.json";
import skillUsagesDocument from "../../schemas/skill_usages.schema.json";
import tl1AttemptsDocument from "../../schemas/tl1_attempts.schema.json";
import parallelismDocument from "../../schemas/parallelism.schema.json";
import findingsDocument from "../../schemas/findings.schema.json";

// The site map: every page the site serves, the options each one takes,
// and, for the Analyze dashboards, which combinations of options the page
// draws as given. web/generate-site-map.mjs writes it to
// internal/archive/assets/site-map.json, and a fixture of cases to
// testdata/site-map-cases.json, so Go can browse the site and build links
// with no TypeScript. docs/site-map.md describes the format.
//
// Every fact is read from the module that defines it. Where a page's rules
// decide validity (validAnalysisState, normalizeAnalysis), the generator runs
// them over every combination of a dashboard's options and records the
// result as tables; it checks that the tables reproduce the page's verdict
// exactly, and fails when they cannot.

export const siteMapVersion = 1;
const origin = "http://localhost";
const json = (value: unknown) => JSON.stringify(value);
const fail = (message: string): never => { throw new Error(`site map: ${message}`); };

// ----- Datasets ----------------------------------------------------------------

const schemaDocuments: Record<QueryDataset, unknown> = {
  library: libraryDocument, usage: usageDocument, writing: writingDocument, conversation_activity: conversationActivityDocument,
  writing_messages: writingMessagesDocument, mcp_calls: mcpCallsDocument, tools: toolsDocument, tool_calls: toolCallsDocument,
  skill_usages: skillUsagesDocument, tl1_attempts: tl1AttemptsDocument, parallelism: parallelismDocument, findings: findingsDocument,
};
export const siteSchemas = Object.fromEntries(Object.entries(schemaDocuments).map(([name, document]) => [name, loadSchema(document)])) as Record<QueryDataset, FieldSchema>;

type FieldValue = { value: string; label?: string };
type SiteField = { name: string; label: string; type: string; ops: string; values?: FieldValue[]; description?: string };
// Fields name their operators by a key into op_sets: the field types that
// share the operator list, such as "datetime|number".
function datasetFields(schema: FieldSchema, opSets: Map<string, { types: Set<string>; ops: string[] }>): SiteField[] {
  return schema.fields.filter(isFilterable).map(field => {
    const ops = opsForField(field), signature = json(ops);
    if (!opSets.has(signature)) opSets.set(signature, { types: new Set(), ops });
    opSets.get(signature)!.types.add(field.type);
    const source = field.filter?.values;
    const options = source && source.source === "static" ? source.options.map((option): FieldValue => typeof option === "string" ? { value: option } : { value: String(option.value), ...option.label ? { label: option.label } : {} }) : undefined;
    const description = (field as { description?: string }).description;
    return { name: field.name, label: field.label ?? field.name, type: field.type, ops: signature, ...options ? { values: options } : {}, ...description ? { description } : {} };
  });
}
function datasetLabel(name: QueryDataset) {
  const usage = (usageDatasets as Record<string, { label: string; description: string }>)[name];
  return usage ? { label: usage.label, description: usage.description } : { label: cardDatasets[name] ?? name };
}

// ----- Analyze dashboards ------------------------------------------------------

export type Verdict = "valid" | "invalid" | "coercible";
// How the dashboard page reads a link's analyze_chart (useUsageDashboardState):
// the chart over the topic's defaults, rejected unless validAnalysisState
// accepts it; a link before analysisVersion is migrated (readAnalysisLink),
// one in older vocabulary is rewritten (canonicalState); then the page draws
// normalizeAnalysis of it. A chart is valid when the page draws every
// setting of its view that it names as given, coercible when it opens but
// draws another. Keys the view does not draw (a Compare chart's period, as a
// view switch writes them) are checked for validity but draw nothing.
export function chartVerdict(topic: UsageTopic, chart: Record<string, unknown>): Verdict {
  const catalog = analysisCatalogs[topic] ?? fail(`no catalog for ${topic}`);
  const merged = { ...dashboardDefaults[topic], ...chart } as AnalysisState;
  if (!validAnalysisState(catalog, merged)) return "invalid";
  if (merged.v !== analysisVersion || json(canonicalState(catalog, merged)) !== json(merged)) return "coercible";
  const drawn = normalizeAnalysis(catalog, merged) as Record<string, unknown>;
  const keys = chartKeys[(merged.view ?? "trend") as AnalysisView];
  return Object.keys(chart).filter(key => keys.includes(key)).every(key => json(drawn[key]) === json(chart[key])) ? "valid" : "coercible";
}
// What Go writes for a chart: its older names read as current ones (as
// readChart reads them), then only the keys its view draws and a non-empty
// selection. The keys the merge adds are left to the page, which adds them
// again; writing them would turn a valid partial chart coercible where the
// page moves one. The generator checks that it draws as the whole chart does.
const legacyByTopic = new Map<UsageTopic, LegacyName[]>();
export function viewChart(topic: UsageTopic, chart: Record<string, unknown>): Record<string, unknown> {
  if (!legacyByTopic.has(topic)) legacyByTopic.set(topic, legacyNames(topic).legacy ?? []);
  const merged = { ...dashboardDefaults[topic], ...chart } as Record<string, unknown>;
  const current = Object.assign({}, chart, ...legacyByTopic.get(topic)!.filter(entry => json(merged[entry.key]) === json(entry.value)).map(entry => entry.set)) as Record<string, unknown>;
  const view = (current.view ?? dashboardDefaults[topic].view ?? "trend") as AnalysisView, selection = current.selection as { values?: unknown[] } | undefined;
  return Object.fromEntries(Object.entries(current).filter(([key]) => chartKeys[view].includes(key) || key === "selection" && Boolean(selection?.values?.length)));
}

// The keys a canonical analyze_chart holds for each view, in order: the
// settings that view draws. A non-empty legend selection follows them.
// Other keys are left out; the page fills them from its defaults.
export const chartKeys: Record<AnalysisView, string[]> = {
  trend: ["v", "view", "metric", "split", "period", "hours", "days", "rollingPeriods", "rollingOp", "display", "scale", "share", "completePeriods", "norm", "normPeriods", "seriesLimit"],
  compare: ["v", "view", "unit", "metric", "split", "reduce", "compareAs", "minSamples", "scale"],
  distribution: ["v", "view", "unit", "metric", "split", "minSamples", "scale"],
  scatter: ["v", "view", "unit", "metric", "metricY", "split", "scale", "scaleY"],
  cycle: ["v", "view", "unit", "metric", "split", "cycle", "reduce", "scale"],
};
// A chart's keys in canonical order, over the view's defaults.
export function canonicalChart(defaults: Record<string, unknown>, change: Record<string, unknown> = {}): Record<string, unknown> {
  const state = { ...defaults, ...change }, view = state.view as AnalysisView;
  const chart: Record<string, unknown> = Object.fromEntries(chartKeys[view].filter(key => state[key] !== undefined).map(key => [key, state[key]]));
  const selection = state.selection as { values: string[] } | undefined;
  if (selection?.values.length) chart.selection = selection;
  return chart;
}

// ----- How a dashboard draws a chart -------------------------------------------
//
// The page opens a link's chart over the topic's defaults (merge_defaults:
// dashboardDefaults) and draws normalizeAnalysis of that merge. The map holds
// how each option is drawn as a rule, applied in order:
// - its merged value stays when every constraint allows it, given other
//   options' values;
// - otherwise it becomes the fallback for its context;
// - an option the merge leaves out takes the rule's fill first, or goes
//   straight to the fallback when the fill is "(fallback)".
// A given option's value is its drawn value when its rule comes earlier, its
// merged value (or "(absent)") otherwise. multiple_periods is derived from the
// merge over fill_defaults: period hours with hours > 1, or days with
// days > 1; "self" is the option's own merged value.
// The generator enumerates every combination of option values, absent ones
// included, and checks that the rules reproduce the page's drawing and
// verdict on each.

export const absent = "(absent)", viaFallback = "(fallback)";
type Scalar = string | number | boolean;
type Rule = { option: string; fill?: Scalar; constraints: Array<{ given: string[]; allowed: unknown }>; fallback?: { given: string[]; values: unknown } };
type Shape = { option: string; constraints: string[][]; fallback: string[] };
const shape = (option: string, constraints: string[][], fallback: string[] = []): Shape => ({ option, constraints, fallback });
const drawShapes: Record<AnalysisView, Shape[]> = {
  trend: [shape("metric", [[]]), shape("split", [["metric"]], ["metric", "self"]), shape("share", [["metric"], ["split"]]), shape("display", [["metric"]]),
    shape("rollingOp", [["metric"]]), shape("period", [["metric", "multiple_periods"]], ["metric", "multiple_periods", "self"]), shape("norm", [["display"], ["share"], ["metric", "split"]])],
  compare: [shape("unit", [[]]), shape("metric", [[]]), shape("split", [["unit"]]), shape("reduce", [["unit", "metric"]], ["unit", "metric"]), shape("compareAs", [["split"], ["unit", "metric", "reduce"]])],
  distribution: [shape("unit", [["metric"]], ["metric"]), shape("metric", [["unit"]], ["unit"]), shape("split", [["unit"]])],
  scatter: [shape("unit", [[]]), shape("metric", [[]]), shape("metricY", [["metric"]], ["metric"]), shape("split", [["unit"]])],
  cycle: [shape("unit", [[]]), shape("split", [[]]), shape("metric", [[]]), shape("cycle", [[]], ["self"]), shape("reduce", [["metric"]], ["metric", "self"])],
};
// The defaults normalizeAnalysis fills multiple_periods' inputs with.
export const fillDefaults: Record<string, unknown> = { period: analysisDefaults.period, hours: analysisDefaults.hours, days: analysisDefaults.days };
const multiple = (merged: Record<string, unknown>) => {
  const state = { ...fillDefaults, ...merged };
  return state.period === "hours" && Number(state.hours) > 1 || state.period === "days" && Number(state.days) > 1;
};

function candidates(catalog: AnalysisCatalog, option: string): Scalar[] {
  const measures = Object.keys(catalog.measures);
  switch (option) {
    case "metric": case "metricY": return measures;
    case "split": return [noSplit, ...Object.keys(catalog.breakdowns), ...Object.keys(catalog.components ?? {})];
    case "unit": return [records, ...Object.keys(catalog.units)];
    case "reduce": return analysisReductions.map(([id]) => id);
    case "compareAs": return [...compareModes];
    case "cycle": return [...analysisCycles];
    case "period": return Object.keys(chartPeriods);
    case "display": return [...chartDisplays];
    case "rollingOp": return [...rollingOps];
    case "scale": case "scaleY": return [...chartScales];
    case "share": case "norm": case "completePeriods": return [false, true];
    default: return fail(`no candidates for ${option}`);
  }
}
// Values validAnalysisState accepts for one option, the rest as defaults.
function domainOf(topic: UsageTopic, base: Record<string, unknown>, option: string): Scalar[] {
  const catalog = analysisCatalogs[topic]!;
  return candidates(catalog, option).filter(value => validAnalysisState(catalog, { ...dashboardDefaults[topic], ...base, [option]: value } as AnalysisState));
}
const key = (value: unknown) => value === undefined ? absent : String(value);
const nest = (table: Record<string, unknown>, keys: string[], leaf: (node: unknown) => unknown) => {
  let node = table;
  keys.slice(0, -1).forEach(name => { node = (node[name] ??= {}) as Record<string, unknown>; });
  node[keys.at(-1)!] = leaf(node[keys.at(-1)!]);
};
const lookup = (table: unknown, keys: string[]) => keys.reduce<unknown>((node, name) => (node as Record<string, unknown> | undefined)?.[name], table);

// The verdict and drawing a Go reader reaches from the map alone: the
// reference for docs/site-map.md's rule.
export type ViewRules = { keys: string[]; domains: Record<string, Scalar[]>; numeric: Record<string, { min: number; max?: number; integer: boolean } | { max_values: number }>; rules: Rule[] };
export function drawChart(view: ViewRules, merged: Record<string, unknown>): Record<string, unknown> {
  const drawn: Record<string, unknown> = {};
  const value = (name: string, self: unknown) => name === "self" ? key(self) : name === "multiple_periods" ? key(multiple(merged)) : key(name in drawn ? drawn[name] : merged[name]);
  for (const rule of view.rules) {
    let own: unknown = merged[rule.option];
    const fallback = () => rule.fallback ? rule.fallback.given.length ? lookup(rule.fallback.values, rule.fallback.given.map(name => value(name, own))) : rule.fallback.values : undefined;
    if (own === undefined && rule.fill === viaFallback) { drawn[rule.option] = fallback(); continue; }
    if (own === undefined) own = rule.fill;
    const allowed = rule.constraints.every(constraint => (lookup(constraint.allowed, constraint.given.map(name => value(name, own))) as Scalar[] | undefined)?.includes(own as Scalar) ?? false);
    drawn[rule.option] = allowed ? own : fallback();
  }
  return drawn;
}
export type LegacyName = { key: string; value: unknown; set: Record<string, unknown> };
type ChartFacts = { merge_defaults: Record<string, unknown>; legacy?: LegacyName[]; views: Record<string, ViewRules> };
// A chart as the page reads it: over merge_defaults, with an older name read
// as the current one (canonicalState).
export function readChart(facts: ChartFacts, chart: Record<string, unknown>) {
  const original = { ...facts.merge_defaults, ...chart }, renamed = (facts.legacy ?? []).filter(entry => json(original[entry.key]) === json(entry.value));
  return { merged: Object.assign({}, original, ...renamed.map(entry => entry.set)) as Record<string, unknown>, renamed: renamed.length > 0 };
}
export function judgeChart(facts: ChartFacts, version: number, chart: Record<string, unknown>): Verdict {
  // A renamed chart opens, but not as it was written.
  const { merged, renamed } = readChart(facts, chart), view = facts.views[String(merged.view ?? "trend")];
  // A chart without v is an older link, which the page migrates.
  if (!view || merged.v !== undefined && merged.v !== version) return "invalid";
  for (const [name, value] of Object.entries(merged)) {
    if (name === "v" || name === "view") continue;
    const range = view.numeric[name];
    if (name === "selection") {
      const selection = value as { values?: unknown; excluded?: unknown };
      if (!selection || typeof selection.excluded !== "boolean" || !Array.isArray(selection.values) || selection.values.length > (range as { max_values: number }).max_values || selection.values.some(item => typeof item !== "string")) return "invalid";
    } else if (range && "min" in range) {
      if (typeof value !== "number" || value < range.min || range.max !== undefined && value > range.max || range.integer && !Number.isInteger(value)) return "invalid";
    } else if (view.domains[name]) {
      if (!view.domains[name].includes(value as Scalar)) return "invalid";
    }
    // Keys the view neither draws nor checks, the page ignores.
  }
  if (merged.v === undefined || renamed) return "coercible";
  const drawn = drawChart(view, merged);
  return Object.keys(chart).every(name => !view.keys.includes(name) || !(name in drawn) || drawn[name] === chart[name]) ? "valid" : "coercible";
}

// Every combination of a view's drawn options over its defaults, each
// option also left out where the topic's defaults do not set it.
export function enumerateView(topic: UsageTopic, view: AnalysisView, defaults: Record<string, unknown>, domains: Record<string, Scalar[]>) {
  const options = drawShapes[view].map(rule => rule.option), merge = dashboardDefaults[topic] as Record<string, unknown>;
  const base = Object.fromEntries(Object.entries(defaults).filter(([name]) => !options.includes(name)));
  const choices: Array<[string, Array<Scalar | undefined>]> = options.map(option => [option, [...domains[option], ...option in merge ? [] : [undefined]]]);
  if (view === "trend") choices.push(["multiple_periods", [false, true]]);
  const states: Array<{ chart: Record<string, unknown>; verdict: Verdict }> = [];
  const walk = (index: number, chart: Record<string, unknown>) => {
    if (index === choices.length) {
      const { multiple_periods: several, ...rest } = chart, state = { ...rest };
      if (several !== undefined) {
        const period = (state.period ?? merge.period ?? fillDefaults.period) as string;
        if (several && period !== "hours" && period !== "days") return;
        if (period === "hours") state.hours = several ? Math.max(2, Number(base.hours) || 2) : 1;
        if (period === "days") state.days = several ? Math.max(2, Number(base.days) || 2) : 1;
      }
      states.push({ chart: state, verdict: chartVerdict(topic, state) });
      return;
    }
    const [option, values] = choices[index];
    for (const value of values) walk(index + 1, value === undefined ? chart : { ...chart, [option]: value });
  };
  walk(0, base);
  return states;
}

type ViewFacts = ViewRules & { label: string; defaults: Record<string, unknown> };
function viewFacts(topic: UsageTopic, view: AnalysisView, label: string): ViewFacts {
  const catalog = analysisCatalogs[topic]!, merge = dashboardDefaults[topic] as Record<string, unknown>;
  const start = { ...merge, v: analysisVersion } as AnalysisState;
  const moved = normalizeAnalysis(catalog, switchView(catalog, normalizeAnalysis(catalog, start), view)) as Record<string, unknown>;
  // Outside the trend a missing scale draws linear, as normalizedDashboardChart reads it.
  const defaults = canonicalChart({ ...moved, v: analysisVersion, view, ...view === "trend" ? {} : { scale: moved.scale ?? "linear" } });
  if (chartVerdict(topic, defaults) !== "valid") fail(`${topic} ${view} defaults are not drawn as given: ${json(defaults)}`);
  const keys = chartKeys[view];
  // Every key validAnalysisState checks in this view, drawn or not: a value
  // it would accept for no key means the view does not check that key.
  const accepts = (name: string, value: unknown) => validAnalysisState(catalog, { ...merge, ...defaults, [name]: value } as AnalysisState);
  const checked = (name: string) => !accepts(name, numericKeys.includes(name) ? "not a number" : "(no such value)");
  const domains: Record<string, Scalar[]> = Object.fromEntries(allChartKeys.filter(name => !numericKeys.includes(name) && (keys.includes(name) || checked(name))).map(option => [option, domainOf(topic, defaults, option)]));
  const numeric = numericOptions(topic, view, defaults);
  const states = enumerateView(topic, view, defaults, domains).map(item => ({ ...item, merged: { ...merge, ...item.chart } as Record<string, unknown> }))
    .map(item => ({ ...item, drawn: item.verdict === "invalid" ? {} : normalizeAnalysis(catalog, item.merged as AnalysisState) as Record<string, unknown> }));
  const drawing = states.filter(item => item.verdict !== "invalid");
  // Each rule from the drawings: what stays, what replaces it, and the fill.
  const rules: Rule[] = [];
  for (const { option, constraints: givens, fallback: fallbackGiven } of drawShapes[view]) {
    const context = (item: typeof drawing[number], name: string) => name === "self" ? key(item.merged[option]) : name === "multiple_periods" ? key(multiple(item.merged)) : key(rules.some(rule => rule.option === name) ? item.drawn[name] : item.merged[name]);
    const allowed = givens.map(() => ({} as Record<string, unknown>)), fallbacks: Record<string, unknown> = {};
    let coerced = false;
    for (const item of drawing) {
      const own = item.merged[option], drawn = item.drawn[option];
      if (own === undefined) continue;
      if (drawn === own) givens.forEach((given, index) => nest(allowed[index], ["all", ...given.map(name => context(item, name))], leaf => (leaf as Scalar[] | undefined)?.includes(own as Scalar) ? leaf : [...(leaf as Scalar[] ?? []), own]));
      else {
        coerced = true;
        const path = ["all", ...fallbackGiven.map(name => context(item, name))], before = lookup(fallbacks, path);
        if (before !== undefined && before !== drawn) fail(`${topic} ${view}: ${option}'s fallback depends on more than ${fallbackGiven.join(", ")}`);
        nest(fallbacks, path, () => drawn);
      }
    }
    const rule: Rule = { option, constraints: givens.map((given, index) => ({ given, allowed: allowed[index].all ?? [] })), ...coerced ? { fallback: { given: fallbackGiven, values: fallbacks.all } } : {} };
    rules.push(rule);
    // Keep a constraint only where it removes something.
    rule.constraints = rule.constraints.filter(constraint => drawing.some(item => item.merged[option] !== undefined && !((lookup(constraint.allowed, constraint.given.map(name => context(item, name))) as Scalar[] | undefined)?.includes(item.merged[option] as Scalar) ?? false)));
    // The fill: a value that, put in place of a missing one, draws as the page does.
    const missing = drawing.filter(item => item.merged[option] === undefined);
    if (missing.length) {
      const draws = (fill: Scalar) => missing.every(item => json(drawChart({ keys, domains, numeric, rules: [...rules.slice(0, -1), { ...rule, fill }] }, item.merged)[option]) === json(item.drawn[option]));
      rule.fill = [...domains[option], viaFallback].find(draws) ?? fail(`${topic} ${view}: no fill draws a missing ${option} as the page does`);
    }
  }
  // A rule that never changes its option says nothing.
  const facts: ViewFacts = { label, keys, defaults, domains, numeric, rules: rules.filter(rule => rule.constraints.length || rule.fallback || rule.fill !== undefined) };
  for (const item of states) {
    const verdict = judgeChart({ merge_defaults: merge, views: { [view]: facts } }, analysisVersion, item.chart);
    if (verdict !== item.verdict) fail(`${topic} ${view}: the rules judge ${json(item.chart)} ${verdict}, the page ${item.verdict}`);
    if (item.verdict === "invalid") continue;
    const drawn = drawChart(facts, item.merged);
    for (const { option } of rules) {
      const value = option in drawn ? drawn[option] : item.merged[option];
      if (json(value) !== json(item.drawn[option])) fail(`${topic} ${view}: the rules draw ${option} of ${json(item.chart)} as ${json(value)}, the page as ${json(item.drawn[option])}`);
    }
  }
  return facts;
}

// The integer range validAnalysisState accepts for one setting.
// A probe stops at ceiling, which then stands for no maximum.
function integerRange(accepts: (value: number) => boolean, ceiling = Number.MAX_SAFE_INTEGER) {
  const max = ceiling;
  const min = [-1, 0, 1, 2].find(accepts);
  if (min === undefined) return fail("no accepted integer");
  let high = min;
  if (accepts(max)) high = max;
  else { let low = min, top = max; while (top - low > 1) { const middle = Math.floor((low + top) / 2); if (accepts(middle)) low = middle; else top = middle; } high = low; }
  if (!accepts(Math.floor((min + high) / 2)) || accepts(high + 1) && high < max) fail("an integer range is not contiguous");
  return { min, ...high === max ? {} : { max: high }, integer: !accepts(min + 0.5) };
}
const numericKeys = ["hours", "days", "rollingPeriods", "normPeriods", "seriesLimit", "minSamples"];
const allChartKeys = [...new Set(Object.values(chartKeys).flat())].filter(name => name !== "v" && name !== "view");
// The integer settings a view's chart holds, and the legend selection's size.
function numericOptions(topic: UsageTopic, view: AnalysisView, base: Record<string, unknown>) {
  const catalog = analysisCatalogs[topic]!;
  const accepts = (key: string) => (value: unknown) => validAnalysisState(catalog, { ...dashboardDefaults[topic], ...base, [key]: value } as AnalysisState);
  const options = Object.fromEntries(numericKeys.filter(key => chartKeys[view].includes(key) || !accepts(key)("not a number")).map(key => [key, integerRange(accepts(key))]));
  const selection = (count: number) => accepts("selection")({ values: Array.from({ length: count }, (_, index) => String(index)), excluded: false });
  return { ...options, selection: { max_values: integerRange(selection, 1_000_000).max ?? fail("a selection has no maximum") } };
}

// The older names a topic's catalog reads (its legacy mapping), probed with
// the values the site's own links still carry (cards' charts) and the list
// positions older metrics used. Other older vocabulary is outside the map.
function legacyNames(topic: UsageTopic): { legacy?: LegacyName[] } {
  const catalog = analysisCatalogs[topic]!, base = { ...dashboardDefaults[topic], v: analysisVersion } as Record<string, unknown>;
  if (!catalog.legacy) return {};
  const probes = new Map<string, unknown[]>();
  const add = (key: string, value: unknown) => { if (!probes.has(key)) probes.set(key, []); if (!probes.get(key)!.some(item => json(item) === json(value))) probes.get(key)!.push(value); };
  for (const recipe of queryRecipes) if (recipe.destination?.topic === topic) for (const [key, value] of Object.entries(recipe.destination.chart)) add(key, value);
  for (let position = 0; position < Object.keys(catalog.measures).length; position++) add("metric", position);
  const names: LegacyName[] = [];
  for (const [key, values] of probes) for (const value of values) {
    const state = { ...base, [key]: value }, read = catalog.legacy(state as AnalysisState) as Record<string, unknown>;
    const set = Object.fromEntries(Object.keys({ ...state, ...read }).filter(name => json(read[name]) !== json(state[name])).map(name => [name, read[name]]));
    if (Object.keys(set).length) names.push({ key, value, set });
  }
  return names.length ? { legacy: names } : {};
}
function dashboardFacts(topic: UsageTopic) {
  const catalog = analysisCatalogs[topic] ?? fail(`no catalog for ${topic}`);
  if (catalog.dataset !== usageTopicDatasets[topic]) fail(`${topic} draws ${catalog.dataset} but opens ${usageTopicDatasets[topic]}`);
  const views = Object.fromEntries(availableViews(catalog).map(([view, label]) => [view, viewFacts(topic, view, label)]));
  const unavailable = Object.fromEntries((["trend", "compare", "distribution", "scatter", "cycle"] as AnalysisView[]).flatMap(view => viewUnavailable(catalog, view) ? [[view, viewUnavailable(catalog, view)!]] : []));
  return {
    dataset: catalog.dataset, time: catalog.time, merge_defaults: dashboardDefaults[topic], ...legacyNames(topic),
    defaults: canonicalChart(views[(dashboardDefaults[topic].view ?? "trend")].defaults),
    views, ...Object.keys(unavailable).length ? { unavailable_views: unavailable } : {},
    measures: Object.fromEntries(Object.entries(catalog.measures).map(([id, measure]) => [id, { label: measure.label, unit: measure.unit, kind: trendKind(measure), additive: measure.additive, per_record_value: Boolean(measure.value), note: measure.note }])),
    breakdowns: Object.fromEntries(Object.entries(catalog.breakdowns).map(([id, breakdown]) => [id, { label: breakdown.label, fields: breakdown.fields, ...breakdown.note ? { note: breakdown.note } : {} }])),
    components: Object.fromEntries(Object.entries(catalog.components ?? {}).map(([id, components]) => [id, { label: components.label, measures: components.measures, fallback: components.fallback, parts: components.parts.map(part => part.label) }])),
    units: {
      [records]: { label: catalog.records.label, plural: catalog.records.plural, note: catalog.records.note },
      ...Object.fromEntries(Object.entries(catalog.units).map(([id, unit]) => [id, { label: unit.label, plural: unit.plural, note: unit.note, breakdowns: Object.keys(unit.breakdowns) }])),
    },
    cycles: Object.fromEntries(analysisCycles.map(cycle => [cycle, cycleUnavailable(catalog, cycle) ?? "available"])),
  };
}

// The chart's option labels where a module names them.
const optionLabels = {
  view: Object.fromEntries(analysisViews),
  reduce: Object.fromEntries(analysisReductions),
  window: Object.fromEntries(chartRanges.filter(([range]) => range !== "today").map(([range, label]) => [String(rangeDays(range)), range === "1d" ? "1 day" : label])),
};

// The dashboard window: a chart range preset, or all time. analyze_days
// carries the window only where readUsageWorkspace reads it back.
const presetFor = (days: number) => chartRanges.find(([range]) => range !== "today" && rangeDays(range) === days)?.[0] ?? fail(`no chart range of ${days} days`);
export const dashboardWindows = rollingWindowDays.filter(days => chartRanges.some(([range]) => rangeDays(range) === days));
export const analyzeDays = rollingWindowDays.filter(days => readUsageWorkspace(new URLSearchParams({ analyze_days: String(days) })).days === days);
export const windowTerms = (time: { first: string; last: string }, days: number | null) => withTimeRange([], time, undefined, undefined, days === null ? "all" : presetFor(days));
// The window's terms as templates: a dashboard's over its time fields
// ({first}, {last}), a query table's over its window field ({field}).
function windowTemplates() {
  const probe = dashboardWindows[0], time = { first: "{first}", last: "{last}" }, field = "{field}";
  const dashboard = templated(windowTerms(time, probe), probe) as WhereTerm[];
  const query = templated(resolveRollingQuery(EMPTY_QUERY, { days: probe, field, mode: "relative" }).where, probe) as WhereTerm[];
  for (const days of rollingWindowDays) {
    const fill = (terms: WhereTerm[], names: Record<string, string>) => JSON.parse(Object.entries(names).reduce((text, [name, value]) => text.replaceAll(name, value), json(terms).replaceAll(placeholder, String(days))));
    if (dashboardWindows.some(value => value === days) && json(fill(dashboard, { "{first}": "a", "{last}": "b" })) !== json(windowTerms({ first: "a", last: "b" }, days))) fail(`dashboard window terms at ${days} days differ from their template`);
    if (json(fill(query, { "{field}": "a" })) !== json(resolveRollingQuery(EMPTY_QUERY, { days, field: "a", mode: "relative" }).where)) fail(`query window terms at ${days} days differ from their template`);
  }
  return { dashboard_terms: dashboard, query_terms: query };
}
// Parallelism's ribbon parameter, as a template over the window.
function ribbonTemplate() {
  const template = { json: { ...parallelismRibbon(null), range: null }, lookup: { range: "parallelism_range" } };
  for (const days of [...rollingWindowDays, null]) if (json({ ...template.json, range: windowTables.parallelism_range[days === null ? "all" : String(days)] }) !== json(parallelismRibbon(days))) fail(`the ribbon at ${days} days differs from its template`);
  return template;
}

// A dashboard link as the page settles it: the topic's address
// (usageWorkspaceURL), the chart, and the filters with the window last.
// Parallelism's ribbon opens over the same window, as a card's does.
export function dashboardURL(topic: UsageTopic, chart: Record<string, unknown>, filters: WhereTerm[], days: number | null): string {
  const catalog = analysisCatalogs[topic]!;
  const base = usageWorkspaceURL({ mode: "dashboards", topic, dataset: usageTopicDatasets[topic], ...days !== null && analyzeDays.some(value => value === days) ? { days } : {} });
  const url = new URL(base, origin);
  url.searchParams.set("analyze_chart", json(chart));
  url.searchParams.set("analyze_filters", encodeQuery({ ...EMPTY_QUERY, where: [...filters, ...windowTerms(catalog.time, days)] }));
  if (topic === "parallelism") url.searchParams.set("analyze_parallelism", json(parallelismRibbon(days)));
  return url.pathname + url.search;
}
// A query table link with where terms only, and its rolling window.
export function datasetURL(dataset: QueryDataset, where: WhereTerm[], days: number | null): string {
  const field = rollingWindowField(dataset);
  return queryURL(dataset, { ...EMPTY_QUERY, where }, days !== null && field ? { days, field, mode: "relative" } : undefined);
}

// ----- Cards -------------------------------------------------------------------

// A card URL is rebuilt from an ordered list of parameters, each a literal,
// the window's days, a where-only or full query (encodeQuery) whose
// "-{window_days}d" values take the window, the card's chart or filters, or
// a JSON object whose keys look up a window table.
const placeholder = "{window_days}";
type ParamSpec = string | { days: true } | { query: unknown } | { card: "id" | "title" | "description" | "dataset" } | { card_chart: true } | { card_filters: true } | { json: Record<string, unknown>; lookup: Record<string, string> };
// Window tables are keyed by the window's days, or "all" for all time.
export const windowTables: Record<string, Record<string, string>> = { parallelism_range: { ...Object.fromEntries(rollingWindowDays.map(days => [String(days), concurrencyWindowRange(days)])), all: concurrencyWindowRange(null) } };
const templated = (value: unknown, days: number): unknown => JSON.parse(json(value).replaceAll(`"-${days}d"`, `"-${placeholder}d"`));
export const fillWindow = (value: unknown, days: number | null): unknown => days === null ? value : JSON.parse(json(value).replaceAll(placeholder, String(days)));
const decode = (token: string) => decodeQueryStrict(token) ?? fail(`unreadable token ${token}`);
// The same state as a template: members equal to EMPTY_QUERY's are left out.
export const compact = (query: QueryState): Record<string, unknown> => Object.fromEntries(Object.entries(query as unknown as Record<string, unknown>).filter(([key, value]) => json((EMPTY_QUERY as unknown as Record<string, unknown>)[key]) !== json(value))
  .map(([key, value]) => [key, key === "grouping" && value ? { ...value as object, source: compact((value as { source: QueryState }).source) } : value]));
const expand = (query: Record<string, unknown>): QueryState => ({ ...EMPTY_QUERY, ...query, ...query.grouping ? { grouping: { ...query.grouping as object, source: expand((query.grouping as { source: Record<string, unknown> }).source) } } : {} }) as QueryState;

type SiteCard = {
  id: string; title: string; description: string; category: string; dataset: string; kind: string; page: string;
  chart?: Record<string, unknown>; state?: Record<string, unknown>; filters?: WhereTerm[];
  window?: { today?: boolean; field: string; days: number[]; default_days: number[]; terms: WhereTerm[]; chart_days?: string[] };
  url: { path: string; params: Array<[string, ParamSpec]> };
};
export const recipeTextWindows = Object.fromEntries(rollingWindowDays.map(days => [String(days), Object.fromEntries(["{days}", "{period}", "{lastDays}"].map(token => [token, describeRecipeText(token, days)]))]));
const cardPage = (recipe: QueryRecipe) => recipe.destination ? `analyze/${recipe.destination.topic}` : recipe.dataset === "library" ? "library" : recipe.dataset === "findings" ? "findings/table" : recipe.dataset === "tl1_attempts" ? "tl1/runs" : `analyze/query/${recipe.dataset}`;

export function siteCard(recipe: QueryRecipe, clock: Date): SiteCard {
  const windows = recipe.period ? [] : [...rollingWindowDays];
  const probe = windows.at(-1) ?? null;
  const at = (days: number | null) => new URL(instantiateRecipe(recipe, days, clock).url, origin);
  const sample = at(probe), plain = recipe.period ? sample : at(null);
  if ([recipe.title, recipe.description, sample.search].some(text => decodeURIComponent(text).includes(placeholder))) fail(`${recipe.id} already holds ${placeholder}`);
  const card: SiteCard = { id: recipe.id, title: recipe.title, description: recipe.description, category: recipe.category, dataset: recipe.dataset, kind: cardKind(recipe), page: cardPage(recipe), url: { path: sample.pathname, params: [] } };
  if (recipe.destination) {
    const chart = JSON.parse(plain.searchParams.get("analyze_chart")!), topic = recipe.destination.topic;
    const drawn = normalizeAnalysis(analysisCatalogs[topic]!, canonicalState(analysisCatalogs[topic]!, { ...dashboardDefaults[topic], ...chart })) as Record<string, unknown>;
    card.chart = chart;
    card.state = canonicalChart({ ...drawn, v: analysisVersion, ...drawn.view === "trend" ? {} : { scale: drawn.scale ?? "linear" } });
    if (chartVerdict(topic, card.state) !== "valid") fail(`${recipe.id}'s dashboard state is not drawn as given: ${json(card.state)}`);
    card.filters = decode(plain.searchParams.get("analyze_filters")!).where;
  }
  if (probe !== null) {
    const where = (url: URL) => decode(url.searchParams.get("analyze_filters") ?? encodeQuery(EMPTY_QUERY)).where;
    const terms = where(sample).slice(card.filters?.length ?? 0);
    const chart = sample.searchParams.has("analyze_chart") ? JSON.parse(sample.searchParams.get("analyze_chart")!) : {};
    const chartDays = Object.keys(chart).filter(key => json(chart[key]) !== json(card.chart?.[key]));
    if (chartDays.some(key => chart[key] !== probe)) fail(`${recipe.id}'s chart changes with the window in another way`);
    card.window = { ...(supportsToday(recipe) ? { today: true } : {}), field: recipe.field, days: windows, default_days: recipe.days ?? [7, 30, 90], terms: templated(terms, probe) as WhereTerm[], ...chartDays.length ? { chart_days: chartDays } : {} };
  }
  for (const [key, value] of sample.searchParams) {
    let spec: ParamSpec = value;
    if (key.startsWith("q_")) spec = { query: probe === null ? compact(decode(value)) : templated(compact(decode(value)), probe) };
    else if (key === "analyze_filters" && card.filters) spec = { card_filters: true };
    else if (key === "analyze_chart" && card.chart) spec = { card_chart: true };
    else if (key === "analyze_parallelism") {
      const object = JSON.parse(value), table = windowTables.parallelism_range;
      spec = { json: { ...object, range: probe === null ? object.range : null }, lookup: probe === null ? {} : { range: "parallelism_range" } };
      if (probe !== null && object.range !== table[String(probe)]) fail(`${recipe.id}'s ribbon range is not the window's`);
    } else if (probe !== null && value === String(probe) && ["window_days", "analyze_days"].includes(key)) spec = { days: true };
    else if (key === "card_id" && value === card.id) spec = { card: "id" };
    else if (key === "card_query" && value === card.dataset) spec = { card: "dataset" };
    else if (key === "card_title" && value === card.title) spec = { card: "title" };
    else if (key === "card_description" && value === card.description) spec = { card: "description" };
    card.url.params.push([key, spec]);
  }
  for (const days of windows) for (const text of [recipe.title, recipe.description]) if (cardText(text, days) !== describeRecipeText(text, days)) fail(`${recipe.id}'s text at ${days} days differs from text_windows`);
  for (const days of windows.length ? windows : [null]) {
    const expected = at(days), built = new URL(cardURL(card, days), origin);
    if (expected.pathname !== built.pathname || json([...expected.searchParams.keys()]) !== json([...built.searchParams.keys()])) fail(`${recipe.id} at ${days}: parameters differ`);
    for (const [key, value] of expected.searchParams) {
      const other = built.searchParams.get(key)!;
      const same = key.startsWith("q_") || key === "analyze_filters" ? json(decode(value)) === json(decode(other)) : value === other;
      if (!same) fail(`${recipe.id} at ${days}: ${key} differs`);
    }
  }
  return card;
}

// A card's link at a window, rebuilt from its template as Go rebuilds it.
export function cardURL(card: SiteCard, days: number | null): string {
  const params = new URLSearchParams();
  for (const [key, spec] of card.url.params) {
    if (typeof spec === "string") params.append(key, spec);
    else if ("days" in spec) params.append(key, String(days));
    else if ("query" in spec) params.append(key, encodeQuery(expand(fillWindow(spec.query, days) as Record<string, unknown>)));
    else if ("card" in spec) params.append(key, card[spec.card]);
    else if ("card_filters" in spec) params.append(key, encodeQuery({ ...EMPTY_QUERY, where: [...card.filters!, ...days === null ? [] : fillWindow(card.window!.terms, days) as WhereTerm[]] }));
    else if ("card_chart" in spec) params.append(key, json({ ...card.chart, ...Object.fromEntries((days === null ? [] : card.window?.chart_days ?? []).map(name => [name, days])) }));
    else {
      const looked = Object.fromEntries(Object.entries(spec.lookup).map(([name, table]) => [name, windowTables[table][days === null ? "all" : String(days)]]));
      params.append(key, json(Object.fromEntries(Object.keys(spec.json).map(name => [name, name in looked ? looked[name] : spec.json[name]]))));
    }
  }
  return `${card.url.path}?${params}`;
}
// A card's title or description at a window, as describeRecipeText writes it.
export const cardText = (text: string, days: number) => Object.entries(recipeTextWindows[String(days)]).reduce((result, [token, value]) => result.replaceAll(token, value), text);

// ----- Settings pages ----------------------------------------------------------

const attribute = (tag: string, name: string) => new RegExp(`${name}="([^"]*)"`).exec(tag)?.[1];
const htmlText = (text: string) => text.replaceAll("&amp;", "&").replaceAll("&lt;", "<").replaceAll("&gt;", ">").replaceAll("&quot;", "\"");
// The settings panels in the page shell and the managed app's Libraries page.
export function settingsPages(...html: string[]) {
  return html.flatMap(source => [...source.matchAll(/<section\b[^>]*data-settings-route="[^"]*"[^>]*>/g)].map(([tag]) => ({
    route: attribute(tag, "data-settings-route")!, panel: attribute(tag, "id")!, title: htmlText(attribute(tag, "data-page-title") ?? ""), description: htmlText(attribute(tag, "data-page-description") ?? ""),
  })));
}

// ----- The tree ------------------------------------------------------------------

// requires: options this one needs, each given, or with values: holding (or
// defaulting to) one of them. conflicts: options a link cannot give with it.
type Requirement = { option: string; values?: Scalar[] };
type Option = { name: string; param: string; key?: string; end_param?: string; kind: string; values?: Array<{ value: Scalar; label?: string; description?: string }>; default?: unknown; requires?: Requirement[]; conflicts?: string[]; id?: string; description?: string; host_params?: string[] };
const needs = (option: string, ...values: Scalar[]): Requirement => ({ option, ...values.length ? { values } : {} });
type SiteNode = { ribbon?: unknown; path: string; title: string; description?: string; route: string; fixed?: Record<string, string>; options?: Option[]; children?: string[]; dataset?: string; dashboard?: string; cards?: boolean; settings?: string; builder?: string };
const values = (entries: ReadonlyArray<readonly [string, string]> | ReadonlyArray<string>, describe?: (value: string) => string | undefined) =>
  entries.map(entry => typeof entry === "string" ? { value: entry, ...describe?.(entry) ? { description: describe(entry) } : {} } : { value: entry[0], label: entry[1] });
const windowOption = (param: string, days: readonly number[], description: string): Option => ({ name: "days", param, kind: "window", values: days.map(day => ({ value: day, ...optionLabels.window[String(day)] ? { label: optionLabels.window[String(day)] } : {} })), description });

// The shell's page titles (VIEW_TITLES in ui.py): key: 'Title' pairs.
export function viewTitles(uiPy: string): Record<string, string> {
  const literal = /VIEW_TITLES=\{([^}]*)\}/.exec(uiPy)?.[1] ?? fail("no VIEW_TITLES in ui.py");
  return Object.fromEntries([...literal.matchAll(/(?:'([^']*)'|([\w]+)):'([^']*)'/g)].map(match => [match[1] ?? match[2], match[3]]));
}
export type SiteInputs = { settings: ReturnType<typeof settingsPages>; titles: Record<string, string>; clock?: Date };
export function buildSiteMap(inputs: SiteInputs) {
  const clock = inputs.clock ?? new Date("2026-01-01T12:00:00Z");
  const sections = appSections([], new URLSearchParams(), true);
  const sectionTitle = (id: string) => sections.find(section => section.id === id)?.label;
  const nodes: SiteNode[] = [];
  const add = (node: SiteNode) => { nodes.push(node); return node; };
  const opSets = new Map<string, { types: Set<string>; ops: string[] }>();
  const datasets = Object.fromEntries((Object.keys(schemaDocuments) as QueryDataset[]).map(name => [name, { ...datasetLabel(name), ...rollingWindowField(name) ? { window_field: rollingWindowField(name) } : {}, fields: datasetFields(siteSchemas[name], opSets) }]));
  const opKey = (signature: string) => [...opSets.get(signature)!.types].sort().join("|");
  for (const dataset of Object.values(datasets)) for (const field of dataset.fields) field.ops = opKey(field.ops);
  const cards = queryRecipes.filter(recipe => !recipe.retired).map(recipe => siteCard(recipe, clock));
  const topics = Object.keys(usageTopics) as UsageTopic[];
  const dashboards = Object.fromEntries(topics.map(topic => [topic, dashboardFacts(topic)]));
  // host_params: the parameters the page's table keeps as host query state
  // (host-query-state.ts). The app's query tokens carry that state, mirrored
  // in these parameters; a link's token may carry it only as they say it.
  const whereOption = (param: string, dataset: string, hostParams?: string[]): Option => ({ name: "filters", param, kind: "where", description: `Where terms on ${dataset}'s filterable fields (datasets.${dataset}.fields), encoded with encodeQuery as a where-only query. Browse <node>/filters/<field> for one field.`, ...(hostParams ? { host_params: hostParams } : {}) });

  for (const section of siteSections) {
    const title = inputs.titles[section.route === "/" ? "" : section.path] ?? (section.path === "work" ? "Work" : sectionTitle(section.path)) ?? fail(`no title for ${section.path}`);
    const node = add({ path: section.path, title, description: section.description, route: section.route, children: [] });
    const child = (page: SiteNode) => { node.children!.push(page.path); return add(page); };
    if (section.path === "cards") {
      node.cards = true;
      const used = (key: string) => cards.some(card => card.dataset === key);
      node.options = [
        { name: "search", param: "card_search", kind: "text", description: "Text matched against each card's id, title, description, data source and topic." },
        { name: "data", param: "card_data", kind: "enum", values: Object.entries(cardDatasets).filter(([key]) => used(key)).map(([value, label]) => ({ value, label })), default: "" },
        { name: "topic", param: "card_topic", kind: "enum", values: [{ value: cardTopicAll, label: "All" }, ...Object.entries(queryCategories).filter(([key]) => cards.some(card => card.category === key)).map(([value, item]) => ({ value, label: item.label }))], default: cardTopicAll },
        { name: "kind", param: "card_kind", kind: "enum", values: [...new Set(cards.map(card => card.kind))].sort().map(value => ({ value })), default: "" },
        { name: "sort", param: "card_sort", kind: "enum", values: cardSorts.map(([value, label]) => ({ value, label })), default: cardSorts[0][0], description: "The default sort is left out of the link." },
      ];
      node.builder = `Parameters in the order ${cardLibraryParams.join(", ")}; empty values and the default sort are left out.`;
    }
    if (section.path === "library") {
      const schema = siteSchemas.library;
      node.dataset = "library";
      node.builder = "searchURL: q_library (base_query with the where terms, offset 0), the window parameters, then text_builder for a text search, or kind, search and case for a file or URL search.";
      node.options = [
        { name: "text", param: "text_builder", kind: "text_search", requires: [needs("kind", "text")], description: "A text search: one rule per condition (rule_template), joined by all of them. Needs at least one word." },
        { name: "rule", param: "text_builder", key: "root.children[].kind", kind: "enum", requires: [needs("text")], values: Object.entries(ruleLabels).map(([value, label]) => ({ value, label })), default: newRule().kind, description: "near and pre also need other, the second phrase, within distance words." },
        { name: "scope", param: "text_builder", key: "scope", kind: "enum", requires: [needs("text")], values: scopes.map(scope => ({ value: scope.id, label: scope.label, description: scope.description })), default: scopes[0].id, description: "Set scope.surface to the scope's surface, scope.roles to its role (if any) and scope.kinds to [] (chooseScope); scope_surfaces lists them." },
        { name: "kind", param: "kind", kind: "enum", values: values(["text", "file", "url"]), default: "text", description: "file and url search file paths or URLs instead, with search holding the text and case=1 for case-sensitive." },
        { name: "search", param: "search", kind: "text", requires: [needs("kind", "file", "url")], description: "The file path or URL text, for kind file or url." },
        { name: "case", param: "case", kind: "boolean", values: [{ value: "1" }], requires: [needs("kind", "file", "url")], description: "Case-sensitive file or URL search." },
        { name: "view", param: "view", kind: "enum", values: values(["table", "conversation"]), default: "table", description: "The default, table, is left out of the link." },
        whereOption("q_library", "library", libraryHostParameters),
        windowOption("window_days", rollingWindowDays, "A rolling window on library.window_field, written as window_mode=relative, window_days, window_field and window_dataset=library."),
      ];
      const draft = emptyTextSearch(), rule = { ...newRule(), id: "rule-1" };
      Object.assign(node, {
        base_query: compact({ ...freshSearch(schema).query, offset: 0 }),
        text_builder_template: { ...draft, root: { ...draft.root, id: "group-1", children: [] } },
        rule_template: rule,
        scope_surfaces: Object.fromEntries(scopes.map(scope => [scope.id, { surface: scope.surface, roles: "role" in scope ? [scope.role] : [] }])),
      });
    }
    if (section.path === "analyze") {
      for (const topic of topics) child({ path: `analyze/${topic}`, title: usageTopics[topic].label, description: usageTopics[topic].description, route: "/analyze",
        fixed: { analyze_mode: "dashboards", analyze_topic: topic, analyze_data: usageTopicDatasets[topic] }, dataset: usageTopicDatasets[topic], dashboard: topic,
        ...topic === "carbon" ? { settings: "Estimates use the viewer's saved Carbon assumptions, which a link cannot carry." } : topic === "parallelism" ? { settings: "Records are built with the viewer's saved counting and turn gap, which a link cannot carry; the Provider and Model breakdowns need agent counting. The ribbon (analyze_parallelism) opens over the link's window.", ribbon: { param: "analyze_parallelism", ...ribbonTemplate() } } : {},
        options: [
          { name: "chart", param: "analyze_chart", kind: "dashboard_chart", description: `The chart: dashboards.${topic}.views lists each view's options and valid combinations.` },
          whereOption("analyze_filters", usageTopicDatasets[topic]),
          windowOption("analyze_filters", dashboardWindows, "The dashboard's range: window_terms appended to the filters, or no terms for all time. analyze_days repeats 7, 30 or 90."),
        ] });
      const query = child({ path: "analyze/query", title: "Query", description: pageDescriptions["analyze/query"], route: "/analyze", fixed: { analyze_mode: "query" }, children: [] });
      for (const dataset of Object.keys(usageDatasets) as UsageDataset[]) {
        const page = add({ path: `analyze/query/${dataset}`, title: usageDatasets[dataset].label, description: usageDatasets[dataset].description, route: "/analyze",
          fixed: { analyze_mode: "query", analyze_topic: usageDatasetTopics[dataset], analyze_data: dataset }, dataset, builder: "queryURL",
          options: [whereOption(`q_${dataset}`, dataset, [recordKeywordParameter(dataset)]), ...rollingWindowField(dataset) ? [windowOption("window_days", rollingWindowDays, `A rolling window on ${dataset}.window_field: its terms appended to the where terms, and window_mode=relative, window_days, window_field and window_dataset.`)] : []] });
        query.children!.push(page.path);
      }
    }
    if (section.path === "findings") {
      node.builder = "findingDetailURL for one finding; otherwise list and repo, each left out at its default.";
      node.options = [
        { name: "list", param: "list", kind: "enum", values: values(findingsLists), default: findingsLists[0], description: "The default, open, is left out of the link." },
        { name: "repo", param: "repo", kind: "text", id: "repository", description: "A repository name." },
        { name: "finding", param: "finding", kind: "id", id: "finding_id", description: "Open one finding." },
        { name: "tab", param: "tab", kind: "enum", values: values(findingTabs), default: findingTabs[0], requires: [needs("finding")], description: "A finding's tab; needs finding. The default, overview, is left out." },
      ];
      for (const page of findingsPages.slice(1)) {
        const view = new URL(page.href!, origin).searchParams.get("view")!;
        child({ path: `findings/${view}`, title: page.label, description: pageDescriptions[`findings/${view}`], route: "/findings", fixed: { view },
          ...view === "table" ? { dataset: "findings", builder: "queryURL", options: [whereOption("q_findings", "findings"), windowOption("window_days", rollingWindowDays, "A rolling window on findings.window_field, as for query tables.")] } : {} });
      }
    }
    if (section.path === "tl1") for (const page of tl1Pages.slice(1)) child({ path: page.path.slice(1), title: page.title, description: page.description, route: page.path,
      ...page.path === "/tl1/runs" ? { dataset: "tl1_attempts", builder: "queryURL", options: [whereOption("q_tl1_attempts", "tl1_attempts"), windowOption("window_days", rollingWindowDays, "A rolling window on tl1_attempts.window_field, as for query tables.")] } : {} });
    if (section.path === "tl1") node.description = `${section.description} ${tl1Pages[0].description}`;
    if (section.path === "work") {
      node.builder = "conversationRowURL, then the delegation explorer's parameters (delegationRouteValues, empty ones left out), then explorer or sessions.";
      node.options = [
        { name: "work_id", param: "{work_id}", kind: "id", id: "work_id", description: "The work item (workspace) in the path." },
        ...readerParams.map(param => ({ name: param.name, param: param.name, kind: param.id ? "id" : param.name === "at" ? "datetime" : "text", ...param.id ? { id: param.id } : {},
          ..."requires" in param ? { requires: param.requires.map(name => needs(name)) } : {}, ..."conflicts" in param ? { conflicts: [...param.conflicts] } : {}, description: param.description })),
        { name: "explorer", param: "explorer", kind: "enum", values: values(delegationTabs), default: delegationTabs[0], description: "How the delegation explorer shows sessions." },
        { name: "sessions", param: "sessions", kind: "enum", values: values(["hidden"]), conflicts: ["explorer"], description: "Hide the delegation explorer." },
        { name: "mode", param: "delegation_mode", kind: "enum", values: values(delegationModes), default: delegationModes[0], description: "Sessions as a hierarchy, or ranked by one metric." },
        { name: "sort", param: "delegation_sort", kind: "text", default: defaultDelegationSort, requires: [needs("mode", "rank")], description: "In rank mode, the metric id to rank by (the work's analysis lists them: tokens, cost, calls, errors …)." },
        { name: "scope", param: "delegation_scope", kind: "enum", values: values(delegationScopes), default: delegationScopes[0], requires: [needs("mode", "rank")], description: "In rank mode, the session's own metric (self) or with everything below it (all)." },
        { name: "direction", param: "delegation_direction", kind: "enum", values: values(delegationDirections), default: delegationDirections[0], requires: [needs("mode", "rank")], description: "In rank mode." },
        { name: "collapsed", param: "delegation_collapsed", kind: "text", description: "Comma-separated session ids to collapse, sorted." },
        { name: "session", param: "delegation_session", kind: "text", description: "The selected session id." },
        { name: "range", param: "delegation_start", end_param: "delegation_end", kind: "range", description: "A time range in epoch milliseconds: delegation_start and delegation_end, end after start." },
      ];
      const delegationParams = Object.keys(delegationRouteValues({ mode: "rank", sort: "", scope: "", direction: "", collapsed: [], selected: "", range: [0, 1] }));
      Object.assign(node, { delegation_params: delegationParams.filter(key => !["delegation_search", "delegation_matching"].includes(key)) });
    }
    if (section.path === "settings") {
      const root = inputs.settings.find(page => page.route === "/settings") ?? fail("no /settings panel");
      Object.assign(node, { title: `${title} · ${root.title}`, description: root.description });
      for (const page of inputs.settings.filter(item => item.route !== "/settings")) child({ path: page.route.slice(1), title: page.title, description: page.description, route: page.route,
        ...optionalSettingsPages[page.route] ? { settings: optionalSettingsPages[page.route] } : {} });
    }
    if (!node.children!.length) delete node.children;
  }
  return {
    version: siteMapVersion,
    generated_by: "web/generate-site-map.mjs from web/src/site-map.ts",
    paths: "Nodes are addressed by path. An option is <node>/<option name>; a filter field is <node>/filters/<field>, taken from the node's dataset. A card is cards/<id>.",
    nodes: Object.fromEntries(nodes.map(node => [node.path, node])),
    id_sources: idSources,
    excluded: { legacy_routes: legacyRoutes, params: ["ask_chat", "token", "fresh", "q_* snapshot references (@id)"] },
    windows: { days: rollingWindowDays, dashboard: dashboardWindows, analyze_days: analyzeDays, analyze_days_default: readUsageWorkspace(new URLSearchParams()).days, labels: optionLabels.window, tables: windowTables, ...windowTemplates() },
    analyze: {
      version: analysisVersion, fill_defaults: fillDefaults, absent, fallback: viaFallback, labels: { view: optionLabels.view, reduce: optionLabels.reduce },
      options: { period: Object.keys(chartPeriods), display: chartDisplays, scale: chartScales, rollingOp: rollingOps, compareAs: compareModes, cycle: analysisCycles, split_none: noSplit, unit_records: records },
      dashboards,
    },
    datasets, op_sets: Object.fromEntries([...opSets.keys()].map(signature => [opKey(signature), opSets.get(signature)!.ops])),
    empty_query: EMPTY_QUERY,
    cards: { text_windows: recipeTextWindows, items: cards },
  };
}
export type SiteMap = ReturnType<typeof buildSiteMap>;

// JSON with one line per member down to depth, so diffs stay readable.
export function formatJSON(value: unknown, depth: number, indent = ""): string {
  if (depth <= 0 || !value || typeof value !== "object" || !Object.keys(value).length) return json(value);
  const inner = `${indent} `, entries = Array.isArray(value) ? value.map(item => formatJSON(item, depth - 1, inner))
    : Object.entries(value).map(([key, item]) => `${json(key)}: ${formatJSON(item, depth - 1, inner)}`);
  const [open, close] = Array.isArray(value) ? ["[", "]"] : ["{", "}"];
  return `${open}\n${entries.map(entry => inner + entry).join(",\n")}\n${indent}${close}`;
}
