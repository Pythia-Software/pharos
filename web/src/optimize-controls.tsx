import React, { useId } from "react";
import { Icon } from "./icons";

export type OptimizePlace = { key: string; label: string; count: number };

export function OptimizePlaceFilter({ places, value, onChange, label, viewField }: {
  places: readonly OptimizePlace[]; value: string; onChange: (value: string) => void; label: string; viewField?: string;
}) {
  if (places.length < 2) return null;
  return <label className="findings-filter" data-pharos-target={viewField ? `control:${viewField}` : undefined}>
    <span className="visually-hidden">Where</span>
    <select data-view-field={viewField} value={value} onChange={event => onChange(event.target.value)} aria-label={label}>
      <option value="">Everywhere</option>
      {places.map(place => <option key={place.key} value={place.key}>{place.label} ({place.count})</option>)}
    </select><Icon name="chevron-down" />
  </label>;
}

// explanations, when given, say how each option orders the cards. They are
// each option's tooltip, so no line of text under the row is needed.
export function OptimizeRank<T extends string>({ value, onChange, options, explanations, viewField }: {
  value: T; onChange: (value: T) => void; options: Record<T, string>; explanations?: Partial<Record<T, string>>; viewField: string;
}) {
  const id = useId();
  return <div className="findings-rank" data-view-field={viewField} data-pharos-target={`control:${viewField}`}>
    <span id={id}>Rank by</span>
    <div className="findings-segmented" role="group" aria-labelledby={id}>
      {(Object.keys(options) as T[]).map(option => <button key={option} type="button" aria-pressed={value === option} title={explanations?.[option]} onClick={() => onChange(option)}>{options[option]}</button>)}
    </div>
  </div>;
}
