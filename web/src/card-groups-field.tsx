import React from "react";
import type { CardGroups } from "./card-cover";

// Every card editor's group count: how many of the chart's leading groups the
// card shows. Empty keeps the card's own count.
export function CardGroupsField({ groups, value, onChange }: { groups: CardGroups; value: string; onChange: (value: string) => void }) {
  const label = `${groups.noun[0].toUpperCase()}${groups.noun.slice(1)} shown`;
  return <label>{label}<input type="number" aria-label={label} min={1} max={groups.max} step={1} placeholder={groups.fallback === undefined ? "Automatic" : `Automatic (${groups.fallback})`} value={value} onChange={event => onChange(event.target.value)} /></label>;
}
