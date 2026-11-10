import React, { useId, useMemo, useState } from "react";
import type { FieldSchema, WhereTerm } from "@pythia-software/query-table-core";
import { describePopulation, populationLabels, populationOf, populationSuffix, withPopulation, type PopulationTerm } from "./dashboard-population";
import type { AnalysisCatalog } from "./dashboard-analysis-model";
import type { FilterChange } from "./chart-range";
import { AddFieldCondition, filterIssue, SearchFilters } from "./search-filters";
import { isOriginDimension, originLabel, useOriginMetadata } from "./origin-dimensions";
import { Icon } from "./icons";

// The population as its controls and title read it: its terms in words, and
// the title's suffix, so a partial population is never drawn unlabelled.
export function usePopulation(catalog: AnalysisCatalog, schema: FieldSchema<any>, where: WhereTerm[]) {
  const originRevision = useOriginMetadata();
  return useMemo(() => {
    if (!catalog.population) return { terms: [] as PopulationTerm[], suffix: "" };
    const labels = populationLabels(catalog, schema.fields, (field, value) => isOriginDimension(field) ? originLabel(field, value) : undefined);
    const terms = describePopulation(catalog, schema.fields, populationOf(catalog, where), labels);
    return { terms, suffix: populationSuffix(terms) };
  }, [catalog, schema, JSON.stringify(where), originRevision]);
}

// Population sits beside Range in the controls: a chip per term, each
// removable, and an editor over the same Query Table terms as a query's
// filters. The legend's value menus add terms too.
export function PopulationControl({ catalog, schema, where, terms, subjects, onFilter }: {
  catalog: AnalysisCatalog; schema: FieldSchema<any>; where: WhereTerm[]; terms: PopulationTerm[]; subjects: string; onFilter: FilterChange;
}) {
  const [editing, setEditing] = useState(false);
  const id = useId();
  const population = populationOf(catalog, where);
  const set = (next: WhereTerm[]) => onFilter(current => withPopulation(catalog, current, next));
  // The range has its own control.
  const fields = schema.fields.filter(field => field.name !== catalog.time.first && field.name !== catalog.time.last);
  return <>
    <div className="usage-chart-field dashboard-population" role="group" aria-label="Population">
      <span className="usage-preset-label">Population</span>
      <div className="dashboard-population-terms" data-pharos-target="control:chart:population">
        {terms.length ? terms.map((item, index) => <span key={index} className="dashboard-population-chip" title={item.chip}>
          <span>{item.chip}</span><button type="button" aria-label={`Remove ${item.chip}`} onClick={() => set(population.filter(term => term !== item.term))}><Icon name="close" /></button>
        </span>) : <span className="dashboard-population-all">All {subjects}</span>}
        <button type="button" className="dashboard-population-edit" aria-expanded={editing} aria-controls={editing ? id : undefined} onClick={() => setEditing(open => !open)}>{terms.length ? "Edit" : "Filter"}</button>
      </div>
    </div>
    {editing ? <PopulationEditor id={id} fields={fields} population={population} onApply={next => { set(next); setEditing(false); }} onCancel={() => setEditing(false)} /> : null}
  </>;
}

// A draft of the population's terms, applied whole so a half-written
// condition never queries.
function PopulationEditor({ id, fields, population, onApply, onCancel }: { id: string; fields: FieldSchema<any>["fields"]; population: WhereTerm[]; onApply: (where: WhereTerm[]) => void; onCancel: () => void }) {
  const [draft, setDraft] = useState(population);
  const issue = filterIssue(fields, draft);
  return <div id={id} className="dashboard-population-editor" role="group" aria-label="Edit population">
    <p className="usage-dashboard-note">Every view, breakdown and panel counts only these records. A legend value's menu adds one too.</p>
    <SearchFilters fields={fields} where={draft} onChange={setDraft} lead="where" />
    <AddFieldCondition fields={fields} onAdd={clause => setDraft([...draft, clause])} />
    {issue ? <p className="query-table-error" role="alert">{issue}</p> : null}
    <p className="dashboard-population-actions"><button type="button" disabled={Boolean(issue)} onClick={() => onApply(draft)}>Apply</button> <button type="button" className="secondary" onClick={onCancel}>Cancel</button></p>
  </div>;
}
