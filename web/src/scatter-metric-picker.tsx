import { scatterMetricOptions } from "./scatter-metrics";
import React, { useEffect, useId, useState } from "react";
import type { AnalysisCatalog } from "./dashboard-analysis-model";
import "./scatter-metric-picker.css";

// The search is an editing draft; only choosing a metric changes analyze_chart.
// Keep DOM focus in the input while navigating the grouped suggestions.
export function ScatterMetricPicker({ label, value, other, unit, catalog, onChange }: {
  label: string; value: string; other: string; unit: string; catalog: AnalysisCatalog; onChange: (id: string) => void;
}) {
  const id = useId(), [draft, setDraft] = useState<string | null>(null), [active, setActive] = useState(0);
  const matches = scatterMetricOptions(catalog, unit, other, draft ?? "");
  const groups = [...new Set(matches.map(([, item]) => item.group ?? "Metrics"))];
  const chosen = Math.min(active, Math.max(matches.length - 1, 0));
  useEffect(() => { if (draft !== null && matches[chosen]) document.getElementById(`${id}-${matches[chosen][0]}`)?.scrollIntoView({ block: "nearest" }); }, [active, draft, unit]);
  const select = (key: string) => { onChange(key); setDraft(null); setActive(0); };
  return <div className="usage-chart-field scatter-metric-picker">
    <label className="usage-preset-label" htmlFor={id}>{label}</label>
    <input id={id} role="combobox" aria-label={label} aria-autocomplete="list" aria-expanded={draft !== null} aria-controls={`${id}-list`}
      aria-activedescendant={draft !== null && matches.length ? `${id}-${matches[chosen][0]}` : undefined}
      value={draft ?? catalog.measures[value].label} placeholder="Search metrics…" autoComplete="off"
      onFocus={() => { setDraft(""); setActive(0); }} onBlur={() => setDraft(null)}
      onChange={event => { setDraft(event.target.value); setActive(0); }}
      onKeyDown={event => {
        if (event.key === "Escape") { event.preventDefault(); setDraft(null); }
        else if (event.key === "ArrowDown" || event.key === "ArrowUp") { event.preventDefault(); setDraft(draft ?? ""); setActive(current => Math.max(0, Math.min(matches.length - 1, current + (event.key === "ArrowDown" ? 1 : -1)))); }
        else if (event.key === "Enter" && draft !== null && matches.length) { event.preventDefault(); select(matches[chosen][0]); }
      }} />
    {draft !== null ? <div className="scatter-metric-options" id={`${id}-list`} role="listbox" aria-label={`${label} metrics`}>
      {!matches.length ? <p role="status">No matching metrics for this point type.</p> : groups.map(group => <div key={group} role="group" aria-label={group}>
        <div className="scatter-metric-group">{group}</div>
        {matches.filter(([, item]) => (item.group ?? "Metrics") === group).map(([key, item]) => <div id={`${id}-${key}`} key={key} role="option" aria-selected={matches[chosen]?.[0] === key}
          className={matches[chosen]?.[0] === key ? "active" : undefined} title={item.note}
          onMouseDown={event => event.preventDefault()} onClick={() => select(key)}>
          <strong>{item.label}</strong><small>{item.note}</small>
        </div>)}
      </div>)}
    </div> : null}
  </div>;
}
