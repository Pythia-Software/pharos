import { predicatesOf, type FieldDef, type WhereTerm } from "@pythia-software/query-table-core";
import { isSliceTerm, readSlice, withSlice, type SliceSelection, type SliceValue } from "./chart-selection";
import { withTimeRange } from "./chart-range";
import { selectionWhere, type AnalysisCatalog } from "./dashboard-analysis-model";
import { lowerLabel, sentenceText } from "./text-search-model";
import { describeWhere } from "./search-filters";

// A catalog dashboard's population: the analyze_filters terms other than its
// range, as ordinary Query Table where terms. Every view, breakdown and panel
// counts only these records; a measure's own coverage (its where) is not
// part of it. The legend's "Only this value" and "Leave this value out" write
// a term on the breakdown's field (docs/dashboard-analysis.md, "Population").

export const populationOf = (catalog: AnalysisCatalog, where: WhereTerm[]) => withTimeRange(where, catalog.time, undefined, undefined, "all");
// where with its population replaced, keeping its range.
export function withPopulation(catalog: AnalysisCatalog, where: WhereTerm[], population: WhereTerm[]): WhereTerm[] {
  const kept = new Set(populationOf(catalog, where));
  return [...population, ...where.filter(term => !kept.has(term))];
}

// A value as the legend names it: the breakdown's own label, the schema's
// option label, the catalog's labeller, or the value; an empty one is the
// breakdown's empty label.
export type ValueLabel = (field: string, value: SliceValue) => string;
export function populationLabels(catalog: AnalysisCatalog, fields: FieldDef[], origin?: (field: string, value: string) => string | undefined): ValueLabel {
  return (field, value) => {
    const breakdown = breakdownOf(catalog, field);
    if (value === null || value === "") return breakdown?.empty ?? "No value";
    const option = fields.find(item => item.name === field)?.filter?.values;
    const listed = option?.source === "static" ? option.options.find(item => typeof item !== "string" && item.value === value) : undefined;
    return breakdown?.values?.[value] ?? (listed && typeof listed !== "string" ? listed.label : undefined) ?? catalog.labels?.[field]?.(value) ?? origin?.(field, value) ?? value;
  };
}

// The breakdown whose legend could have written a term on field.
const breakdownOf = (catalog: AnalysisCatalog, field: string) => Object.values(catalog.breakdowns).find(item => item.fields.length === 1 && item.fields[0] === field);
const list = (values: string[]) => values.length < 3 ? values.join(" or ") : `${values.slice(0, -1).join(", ")} or ${values[values.length - 1]}`;

// One term in words, for its chip ("Harness is not Antigravity CLI") and the
// chart title's suffix ("excluding Antigravity CLI"). A term on one field
// that the legend could have written reads as that field's values; any
// other reads as the query editor reads it.
export type PopulationTerm = { term: WhereTerm; chip: string; short: string };
export function describePopulation(catalog: AnalysisCatalog, fields: FieldDef[], population: WhereTerm[], label: ValueLabel): PopulationTerm[] {
  return population.map(term => {
    const field = predicatesOf(term)[0]?.field, breakdown = field ? breakdownOf(catalog, field) : undefined;
    if (field && breakdown && isSliceTerm(term, field)) {
      const slice = readSlice([term], field), values = slice.values.map(value => label(field, value));
      const name = breakdown.label;
      // Leaving out the empty value reads as requiring one.
      if (slice.excluded && slice.values.every(value => value === null)) return { term, chip: `${name} is set`, short: `excluding ${values[0]}` };
      return { term, chip: `${name} ${slice.excluded ? values.length > 1 ? "is none of" : "is not" : values.length > 1 ? "is one of" : "is"} ${list(values)}`, short: `${slice.excluded ? "excluding" : "only"} ${list(values)}` };
    }
    const text = sentenceText(describeWhere(fields, [term])[0]);
    return { term, chip: text[0].toUpperCase() + text.slice(1), short: lowerLabel(text) };
  });
}
export const populationSuffix = (terms: PopulationTerm[]) => terms.length ? ` · ${terms.map(item => item.short).join("; ")}` : "";

// The legend's population actions on one group of the breakdown. Only this
// value replaces the field's slice; Leave out removes the value from an
// inclusion, or adds it to the exclusions. A combined group (provider and
// model) is a term of its own.
export type PopulationAction = "keep" | "drop";
export function withPopulationGroup(catalog: AnalysisCatalog, where: WhereTerm[], split: string, key: string, action: PopulationAction): WhereTerm[] {
  const fields = catalog.breakdowns[split]?.fields ?? [];
  if (!fields.length) return where;
  if (fields.length > 1) return [...where, ...selectionWhere(fields, { values: [key], excluded: action === "drop" }) ?? []];
  const field = fields[0], value: SliceValue = key || null, current = readSlice(populationOf(catalog, where), field);
  let next: SliceSelection<SliceValue>;
  if (action === "keep") next = { values: [value], excluded: false };
  else if (current.values.length && !current.excluded) {
    const values = current.values.filter(item => item !== value);
    next = values.length ? { values, excluded: false } : { values: [value], excluded: true };
  } else next = { values: [...new Set([...current.values, value])], excluded: true };
  return withSlice(where, field, next);
}

// A legend selection made the population: each selected group kept, or
// each excluded group left out.
export function selectionPopulation(catalog: AnalysisCatalog, where: WhereTerm[], split: string, selection: { values: string[]; excluded: boolean }): WhereTerm[] {
  const fields = catalog.breakdowns[split]?.fields ?? [];
  if (fields.length !== 1) return [...where, ...selectionWhere(fields, selection) ?? []];
  if (!selection.excluded) return withSlice(where, fields[0], { values: selection.values.map(value => value || null), excluded: false });
  return selection.values.reduce((next, value) => withPopulationGroup(catalog, next, split, value, "drop"), where);
}
