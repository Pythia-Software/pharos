import { EMPTY_QUERY, groupKeyField, predicatesOf, type FieldSchema, type GroupKey, type QueryState, type SummaryColumn, type WhereTerm } from "@pythia-software/query-table-core";
import { aggregate, analysisQuery, heatmapSource, isTrendView, measureExpression, noSplit, normalizeAnalysis, partClause, partField, ratioPart, records, selectionWhere, splitValid, unique, type AnalysisCatalog, type AnalysisMeasure, type AnalysisState } from "./dashboard-analysis-model";
import { trendQuery } from "./dashboard-trend-model";
import { groupedDashboardQuery } from "./usage-dashboard-model";
import type { UsageDataset } from "./usage-workspace-model";

// Purposes, ordering and colours are shared by every dashboard. A purpose is a
// stable identity even when its question changes with the current controls.
export const dashboardQueryPurposes = [
  { id: "contributors", label: "Find contributors" },
  { id: "inspect", label: "Inspect this view" },
  { id: "compare", label: "Compare another breakdown" },
  { id: "perspective", label: "Change perspective" },
] as const;
export type DashboardQueryPurpose = typeof dashboardQueryPurposes[number]["id"];
export type DashboardQuerySuggestion = { purpose: DashboardQueryPurpose; title: string; query: QueryState; dataset: UsageDataset; note: string; blocked?: string };
export type DashboardQueryContext = { catalog: AnalysisCatalog; state: AnalysisState; where: WhereTerm[]; supporting?: QueryState; now?: number; zone?: string; note?: string };
type Candidate = Omit<DashboardQuerySuggestion, "purpose" | "dataset">;

const canonical = (value: unknown): string => Array.isArray(value) ? `[${value.map(canonical).join(",")}]`
  : value && typeof value === "object" ? `{${Object.entries(value).filter(([, value]) => value !== undefined).sort(([a], [b]) => a.localeCompare(b)).map(([key, value]) => `${key}:${canonical(value)}`).join(",")}}` : JSON.stringify(value);

// Presentation, labels, column order and sort order do not make a new question.
// Representative ordering does: it chooses the evidence shown for each group.
export function dashboardQueryMeaning(query: QueryState): string {
  const column = ({ op, field, expression }: SummaryColumn) => ({ op, field, expression });
  return canonical({ where: query.where, grouping: query.grouping && { source: dashboardQueryMeaning(query.grouping.source),
    keys: query.grouping.keys.map(canonical).sort(), summaries: query.grouping.summaries.map(column).map(canonical).sort(), representative: query.grouping.representative },
    metrics: query.aggregations?.length ? query.aggregations.map(({ id: _, label: __, display: ___, ...metric }) => metric) : undefined });
}

function summary(id: string, measure: AnalysisMeasure): SummaryColumn {
  const expression = measureExpression(measure);
  return { id, ...(expression ? { op: "formula" as const, expression } : aggregate(measure)), label: measure.label,
    ...["number", "percent", "duration"].includes(measure.format.kind) ? { display: measure.format as SummaryColumn["display"] } : {} };
}
function columns(measure: AnalysisMeasure): SummaryColumn[] {
  return [summary("explore_value", measure), ...measure.ratio?.map((part, index): SummaryColumn => {
    const clause = partClause(part), expression = clause.expression, label = ratioPart(part).label ?? ratioPart(part).field!.replaceAll("_", " ");
    return { id: index ? "explore_denominator" : "explore_numerator", ...clause, op: expression ? "formula" : clause.op as SummaryColumn["op"], label };
  }) ?? []];
}
export function explorationGroups(source: QueryState, keys: GroupKey[], summaries: SummaryColumn[], where: WhereTerm[] = []): QueryState {
  if (!keys.length) return { ...source, aggregations: summaries.map(column => ({ id: column.id, label: column.label, ...column.expression ? { op: "count" as const, expression: column.expression } : { op: column.op as "sum", field: column.field }, groupBy: [] })) };
  return { ...EMPTY_QUERY, limit: source.limit, grouping: { source: { ...source, offset: 0, aggregations: [] }, keys, summaries, representative: null },
    select: [...keys.map(groupKeyField), ...summaries.map(item => item.id)].map(field => ({ field })), where,
    orderBy: [{ field: summaries[0].id, dir: "desc", nulls: "last" }, ...keys.map(key => ({ field: groupKeyField(key), dir: "asc" as const }))] };
}
const table = (query: QueryState): QueryState => !query.grouping && query.aggregations?.length && query.aggregations.every(clause => !clause.groupBy.length)
  ? { ...query, offset: 0 } : { ...groupedDashboardQuery(query), aggregations: [], offset: 0 };

// A fixed equality in an AND (including a one-value OR) makes a dimension a
// poor comparison. Never infer that from a branch of a multi-field OR.
function fixedFields(where: WhereTerm[]): Set<string> {
  return new Set(where.flatMap(term => {
    const clauses = predicatesOf(term);
    return clauses.length && clauses.every(clause => !clause.negated && clause.op === "=" && clause.field === clauses[0].field && clause.value === clauses[0].value) ? [clauses[0].field] : [];
  }));
}

// Native table queries share the catalog's metric arithmetic, implied
// populations, unit mappings and executable time bounds with the chart. The
// caller passes its resolved catalog (including People labels and Carbon).
export function dashboardExplorations(context: DashboardQueryContext, schema: FieldSchema): DashboardQuerySuggestion[] {
  const { catalog, where, now = Date.now(), zone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC" } = context;
  const state = normalizeAnalysis(catalog, context.state), measure = catalog.measures[state.metric], unit = !isTrendView(state.view) && catalog.units[state.unit];
  const source = isTrendView(state.view) ? heatmapSource(catalog, state) : { catalog, state };
  const built = isTrendView(state.view) ? trendQuery(source.catalog, source.state, where, { now, zone }) : analysisQuery(catalog, state, where, { now, zone });
  const supported = context.supporting ?? built.supporting;
  const raw = supported.grouping?.source ?? { ...supported, aggregations: [] };
  const fields = new Map(schema.fields.map(field => [field.name, field]));
  const split = catalog.breakdowns[state.split], components = catalog.components?.[state.split];
  // Heatmaps retain the old legend controls but query every bin. Check only
  // the selection and fields the effective query actually uses.
  const selection = source.state.selection, active = Boolean(selection && (selection.values.length || selection.allHidden));
  const legendFields = unit ? unit.breakdowns[source.state.split] : source.catalog.breakdowns[source.state.split]?.fields;
  const cohort = active && legendFields?.length ? selectionWhere(legendFields, selection!) : [];
  const scope = [...raw.where, ...supported.grouping ? supported.where : []];
  const missingSelection = active && legendFields?.length && (!cohort || cohort.some(term => !scope.some(candidate => canonical(candidate) === canonical(term))));
  const blocked = missingSelection ? "This legend selection exceeds the query filter limit. Reduce the selection to open its queries."
    : "problem" in built && built.problem ? built.problem : undefined;
  const parts = components?.parts.filter(part => !active || (selection!.excluded ? !selection!.values.includes(part.key) : selection!.values.includes(part.key)));
  const inputs = parts?.flatMap(part => part.fields ?? (part.field ? [part.field] : []));
  const baseNote = [context.note, catalog.request?.note].filter(Boolean).join(" ");
  const currentNote = [baseNote,
    isTrendView(state.view) ? "Period inputs before smoothing, accumulation or share normalization." : state.view === "cycle" ? "Whole-period inputs behind the hour or weekday summary." : "Values underlying the selected view.",
    (state.share || state.compareAs === "share") ? "Chart shares use the full matching population, including other groups." : "",
  ].filter(Boolean).join(" ");
  let current = table(supported);
  if (!current.grouping) {
    const derived = components?.derived;
    const summaries = derived ? derived.inputs.fields.map((field, index): SummaryColumn => ({ id: `explore_input_${index}`, op: "sum", field, label: fields.get(field)?.label ?? field }))
      : components ? parts!.filter(part => part.field).map((part, index): SummaryColumn => ({ id: `explore_part_${index}`, op: "sum", field: part.field, label: part.label })) : columns(measure);
    current = explorationGroups(raw, derived?.inputs.groupBy ?? [], summaries.length ? summaries : columns(measure));
  }
  if (active && inputs?.length && current.grouping) {
    const summaries = current.grouping.summaries.filter(column => column.field && inputs.includes(column.field));
    current = { ...current, grouping: { ...current.grouping, summaries }, select: current.select.filter(column => current.grouping!.keys.some(key => groupKeyField(key) === column.field) || summaries.some(summary => summary.id === column.field)) };
  }
  const inputName = components && active && parts?.length ? `${parts.map(part => part.label).join(" + ")} inputs to ${measure.label.toLowerCase()}` : components?.derived ? `Recorded inputs to ${measure.label.toLowerCase()}` : measure.label;
  const keyNames = (current.grouping?.keys ?? []).map(key => typeof key === "string" ? fields.get(key)?.label ?? key.replaceAll("_", " ") : `${key.interval} of ${fields.get(key.field)?.label ?? key.field}`);
  const inspect: Candidate = { title: `${inputName}${keyNames.length ? ` by ${keyNames.join(" + ").toLowerCase()}` : measure.additive ? " totals" : " summary"}`, query: current, note: currentNote };

  // A unit or HAVING-filtered group cannot be flattened into records without
  // losing its population. Show a representative source record per group;
  // the native group Detail then opens the group's full evidence.
  const valueField = [...active ? inputs ?? [] : [], measure.value, measure.field, ...measure.ratio?.map(partField) ?? []].find(field => field && fields.has(field) && fields.get(field)!.type !== "text");
  const evidenceFields = [...new Set(["id", "title", catalog.timestamp ?? catalog.time.last, valueField, ...schema.defaultSelect?.map(column => column.field) ?? []])].filter((field): field is string => Boolean(field && fields.has(field))).slice(0, 7);
  const rank = valueField ?? catalog.timestamp ?? catalog.time.last;
  const constrained = Boolean(current.grouping && (unit || current.where.length));
  let contributors: Candidate;
  if (constrained) {
    const evidence = evidenceFields.filter(field => !current.grouping!.keys.some(key => groupKeyField(key) === field)).map((field, index): SummaryColumn => ({ id: `explore_evidence_${index}`, op: "row", field, label: `Example · ${fields.get(field)!.label}` }));
    const summaries = [...current.grouping!.summaries, ...evidence];
    contributors = { title: `Example ${catalog.records.plural.toLowerCase()} behind each group`, note: `${baseNote} Keeps the group filters. Each group includes one source example; open Detail for all its records.`,
      query: { ...current, grouping: { ...current.grouping!, summaries, representative: { orderBy: [{ field: rank, dir: "desc", nulls: "last" }] } }, select: [...current.select, ...evidence.map(column => ({ field: column.id }))] } };
  } else {
    contributors = { title: `${catalog.records.plural}${valueField ? ` ranked by ${fields.get(valueField)!.label.toLowerCase()}` : " in the selected range"}`, note: `${baseNote} Source records behind the selected measure.`,
      query: { ...raw, select: evidenceFields.map(field => ({ field })), where: unique([...raw.where, ...measure.drill ?? []]), orderBy: [{ field: rank, dir: "desc", nulls: "last" }], aggregations: [], offset: 0 } };
  }

  // Selection predicates on a unit's constant source fields can safely travel
  // to another breakdown. Summary predicates remain HAVING; they are never
  // reinterpreted as predicates on individual records.
  const constant = new Set(unit ? Object.values(unit.breakdowns).flat() : split?.fields ?? []);
  const selected = supported.grouping ? supported.where.filter(term => predicatesOf(term).every(clause => constant.has(clause.field))) : [];
  const scoped = unique([...raw.where, ...selected]);
  const fixed = fixedFields(scoped);
  const splits = Object.entries(catalog.breakdowns).filter(([id, item]) => id !== state.split && (!item.measures || item.measures.includes(state.metric))
    && (!item.views || item.views.includes("compare")) && (!unit || splitValid(catalog, state.unit, id))
    && item.fields.every(field => fields.has(field)) && !item.where?.length && item.fields.some(field => !fixed.has(field)))
    .sort(([a], [b]) => Number(b === catalog.trend.split) - Number(a === catalog.trend.split));
  const compareState = { ...state, unit: unit ? state.unit : records, view: "compare" as const, compareAs: "bars" as const, selection: undefined };
  function comparison(next: AnalysisState, note: string): Candidate {
    const resolved = normalizeAnalysis(catalog, next), result = analysisQuery(catalog, resolved, scoped, { now, zone });
    let query = table(result.supporting);
    if (!query.grouping && !query.aggregations?.length) query = explorationGroups(raw, [], columns(measure));
    // Per-unit value predicates (e.g. a positive logarithmic distribution)
    // retain their meaning when comparing the same units and metric.
    if (unit && resolved.unit === state.unit && query.grouping) query = { ...query, where: unique([...query.where, ...supported.where.filter(term => predicatesOf(term).every(clause => clause.field === "analysis_value"))]) };
    const name = catalog.breakdowns[resolved.split]?.label;
    const changedUnit = unit && resolved.unit === records;
    return { title: `${measure.label}${name ? ` by ${name.toLowerCase()}` : " across the selected range"}${changedUnit ? ` · ${catalog.records.plural.toLowerCase()}` : ""}`, query,
      note: [baseNote, note, changedUnit ? `Compares source ${catalog.records.plural.toLowerCase()} before per-${unit.label.toLowerCase()} value filters, rather than combining ${unit.plural.toLowerCase()}.` : "",
        components && active ? "Compares the full measure across all components; component selection does not filter records." : "", state.minSamples ? `Minimum ${state.minSamples} applies to each new group.` : ""].filter(Boolean).join(" ") };
  }
  const comparisons = splits.map(([split]) => comparison({ ...compareState, split }, "Compare the selected measure using another breakdown."));
  // Some units have no second constant dimension (a repository, for example).
  // Offer an explicitly named change of counting unit rather than pretending
  // a model is constant within one repository or splitting its unit values.
  if (unit) comparisons.push(...Object.entries(catalog.breakdowns).filter(([id, item]) => id !== state.split && !item.where?.length && item.fields.every(field => fields.has(field))
    && (!item.measures || item.measures.includes(state.metric)) && (!item.views || item.views.includes("compare")) && item.fields.some(field => !fixed.has(field)))
    .map(([split]) => comparison({ ...compareState, unit: records, split }, "Explore the selected measure at its source grain.")));
  const perspectives: Candidate[] = [];
  if (!isTrendView(state.view)) {
    // Retained trend controls may not apply to this metric. Only offer a
    // normalized trend that still measures the user's metric.
    const next = normalizeAnalysis(catalog, { ...state, view: "trend", selection: undefined, rollingPeriods: 0, display: "interval", share: false });
    if (isTrendView(next.view) && next.metric === state.metric) {
      const result = trendQuery(catalog, next, scoped, { now, zone });
      const derived = catalog.components?.[next.split]?.derived;
      perspectives.push({ title: `${derived ? "Recorded inputs to " : ""}${measure.label} over time`, query: table(result.supporting), note: `${baseNote} ${derived ? "Token inputs before the Carbon component estimates." : "Period values over the same range; one observation per period."}` });
    }
  } else perspectives.push(comparison({ ...compareState, split: split ? state.split : noSplit }, "Compare values across the selected range rather than period by period."));
  // A ratio's parts, a second valid dimension, and a chronological view are
  // substantive alternatives, not different sorts of the same query.
  if (measure.ratio) perspectives.push({ title: `What goes into ${measure.label.toLowerCase()}`, query: explorationGroups({ ...raw, where: scoped }, split?.fields ?? [], columns(measure)), note: `${baseNote} Pooled numerator and denominator alongside the rate; missing denominators remain missing.` });
  perspectives.push(...comparisons.slice(1));
  const time = catalog.timestamp ?? catalog.periods.day;
  if (time && fields.has(time)) perspectives.push({ title: `${measure.label} by day`, query: explorationGroups({ ...raw, where: scoped }, [fields.get(time)!.type === "datetime" ? { field: time, interval: "day", timeZone: zone } : time], columns(measure)), note: `${baseNote} Daily values over the selected population.` });
  const used = new Set<string>();
  const pick = (purpose: DashboardQueryPurpose, candidates: Candidate[]): DashboardQuerySuggestion => {
    const candidate = candidates.find(item => !used.has(dashboardQueryMeaning(item.query)));
    if (!candidate) return { ...inspect, purpose, dataset: catalog.dataset as UsageDataset, blocked: "The current controls leave no distinct exploration for this purpose. Choose another breakdown or widen the filters." };
    used.add(dashboardQueryMeaning(candidate.query));
    const unavailable = blocked ?? (components && active && !parts?.length ? "No chart components are selected. Show a component to explore its inputs." : undefined);
    return { ...candidate, purpose, dataset: catalog.dataset as UsageDataset, note: candidate.note.trim(), ...unavailable ? { blocked: unavailable } : {} };
  };
  const inspected = pick("inspect", [inspect]);
  return [pick("contributors", [contributors]), inspected, pick("compare", comparisons), pick("perspective", perspectives)];
}
